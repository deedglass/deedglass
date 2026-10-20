// Package fetch asks the TSAs and the transparency service an operator configured for evidence
// about a chain head (TY-03, ADR-112). It is the only part of the timestamp code that touches the
// network, and it does so only through the *http.Client the caller passes (cmd/deedglass builds it
// from the one egress door). With nothing configured it makes no call at all.
package fetch

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/deedglass/deedglass/internal/deed"
	"github.com/deedglass/deedglass/internal/deed/stamp"
)

// Config is what an operator configured (deedglass.yaml timestamps.*).
type Config struct {
	TSAs       []string      // RFC 3161 endpoints, https (http on loopback in dev)
	MinTSA     int           // tokens a head needs (default 2 when TSAs are set)
	Service    string        // a transparency service's base URL; "" for none
	ServiceKey *stamp.JWK    // the service's key (needed when Service is set)
	Signer     deed.Signer   // the issuer's Ed25519 seal key, for the Signed Statement
	Issuer     string        // the issuer's base URL (the statement's iss)
	Timeout    time.Duration // per request (default 10 s)
	Client     *http.Client  // from the egress door
	Now        func() time.Time
}

// Enabled reports whether anything is configured. Nothing configured means no outbound call.
func (c *Config) Enabled() bool { return c != nil && (len(c.TSAs) > 0 || c.Service != "") }

// MaxResponse bounds a TSA's or service's answer.
const MaxResponse = 256 << 10

// Error is a stamp that could not be made, with each source's reason.
type Error struct {
	Reasons []string
}

func (e *Error) Error() string { return "stamp: " + strings.Join(e.Reasons, "; ") }

// Stamp gets evidence for head: a token from every configured TSA (at least MinTSA must succeed)
// and, when a service is configured, a registration with it. Each token is checked over the head
// (and its nonce) before it is kept, and the inclusion proof with the configured service key.
func Stamp(ctx context.Context, c *Config, head stamp.Head) (stamp.Entry, error) {
	e := stamp.Entry{Head: head, RFC3161: []stamp.Token{}, SCITT: []stamp.Registration{}}
	if !c.Enabled() {
		return e, errors.New("stamp: no TSA or transparency service is configured")
	}
	if c.Client == nil {
		return e, errors.New("stamp: no HTTP client")
	}
	imprint, err := head.Imprint()
	if err != nil {
		return e, err
	}
	var (
		mu      sync.Mutex
		reasons []string
		wg      sync.WaitGroup
		tokens  = make([]*stamp.Token, len(c.TSAs))
	)
	for i, u := range c.TSAs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tok, err := c.token(ctx, u, imprint)
			if err != nil {
				mu.Lock()
				reasons = append(reasons, fmt.Sprintf("%s: %v", u, err))
				mu.Unlock()
				return
			}
			tokens[i] = &stamp.Token{TSA: u, Token: tok}
		}()
	}
	wg.Wait()
	for _, t := range tokens { // configured order, whatever finished first
		if t != nil {
			e.RFC3161 = append(e.RFC3161, *t)
		}
	}
	min := c.MinTSA
	if min <= 0 {
		min = 2
	}
	if min > len(c.TSAs) {
		min = len(c.TSAs)
	}
	if len(e.RFC3161) < min {
		reasons = append(reasons, fmt.Sprintf("%d of %d TSAs answered; %d needed", len(e.RFC3161), len(c.TSAs), min))
		return e, &Error{Reasons: reasons}
	}
	if c.Service != "" {
		reg, err := c.register(ctx, head)
		if err != nil {
			return e, &Error{Reasons: append(reasons, fmt.Sprintf("%s: %v", c.Service, err))}
		}
		e.SCITT = append(e.SCITT, reg)
	}
	return e, nil
}

func (c *Config) timeout() time.Duration {
	if c.Timeout > 0 {
		return c.Timeout
	}
	return 10 * time.Second
}

// token asks one TSA and checks its token over imprint.
func (c *Config) token(ctx context.Context, u string, imprint []byte) (string, error) {
	nonce, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 63))
	if err != nil {
		return "", err
	}
	req, err := stamp.NewRequest(imprint, nonce)
	if err != nil {
		return "", err
	}
	body, err := c.post(ctx, u, "application/timestamp-query", "application/timestamp-reply", req, http.StatusOK)
	if err != nil {
		return "", err
	}
	tok, err := stamp.ParseResponse(body)
	if err != nil {
		return "", err
	}
	info, err := stamp.VerifyToken(tok, imprint, nil)
	if err != nil {
		return "", err
	}
	if info.Nonce == nil || info.Nonce.Cmp(nonce) != 0 {
		return "", errors.New("the token's nonce is not the one sent")
	}
	return stamp.EncodeB64(tok), nil
}

// register sends the Signed Statement to the service (POST <service>/entries, application/cose)
// and checks the inclusion proof it answers with (201).
func (c *Config) register(ctx context.Context, head stamp.Head) (stamp.Registration, error) {
	var reg stamp.Registration
	if c.ServiceKey == nil {
		return reg, errors.New("no service key is configured (timestamps.scitt_key_file)")
	}
	if c.Signer == nil {
		return reg, errors.New("no seal key to sign the statement")
	}
	now := time.Now
	if c.Now != nil {
		now = c.Now
	}
	stmt, err := stamp.SignStatement(head, c.Signer, c.Issuer, now())
	if err != nil {
		return reg, err
	}
	incl, err := c.post(ctx, strings.TrimSuffix(c.Service, "/")+"/entries", "application/cose", "application/cose", stmt, http.StatusCreated)
	if err != nil {
		return reg, err
	}
	if _, err := stamp.VerifyInclusion(incl, stmt, *c.ServiceKey); err != nil {
		return reg, err
	}
	return stamp.Registration{Service: c.Service, Statement: stamp.EncodeB64(stmt), Inclusion: stamp.EncodeB64(incl), ServiceKey: *c.ServiceKey}, nil
}

func (c *Config) post(ctx context.Context, u, ctype, accept string, body []byte, want int) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return nil, errors.New("not a usable URL")
	}
	req.Header.Set("Content-Type", ctype)
	req.Header.Set("Accept", accept)
	res, err := c.Client.Do(req)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, fmt.Errorf("no answer within %s", c.timeout())
		}
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return nil, fmt.Errorf("could not connect (%v)", err)
	}
	defer res.Body.Close()
	b, err := io.ReadAll(io.LimitReader(res.Body, MaxResponse+1))
	if err != nil {
		return nil, errors.New("the answer was cut off")
	}
	if len(b) > MaxResponse {
		return nil, errors.New("the answer is larger than 256 KiB")
	}
	if res.StatusCode != want {
		return nil, fmt.Errorf("HTTP %d", res.StatusCode)
	}
	return b, nil
}
