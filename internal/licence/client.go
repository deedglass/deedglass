package licence

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Dodo's licence routes need no API key (docs.dodopayments.com/features/license-keys, checked
// 2026-10-11). Test and live keys live on different hosts.
const (
	DodoTestBase    = "https://test.dodopayments.com"
	DodoLiveBase    = "https://live.dodopayments.com"
	DefaultServer   = "https://deedglass.com"
	RequestTimeout  = 8 * time.Second
	maxResponseBody = 64 << 10
)

// Errors a person can act on. Each maps to one line in cmd/deedglass's licence copy.
var (
	ErrKeyUnknown     = errors.New("licence: Dodo doesn't know this key")             // 404
	ErrKeyInactive    = errors.New("licence: the key is no longer active")            // 403, or valid:false
	ErrKeyLimit       = errors.New("licence: the key is at its activation limit")     // 422
	ErrUnreachable    = errors.New("licence: couldn't reach the licence service")     // network, timeout, 5xx
	ErrNetworkOff     = errors.New("licence: network is off (DEEDGLASS_NO_NETWORK)")  // never calls out
	ErrServerRefused  = errors.New("licence: the licence server refused the request") // other 4xx from our route
	ErrBadTokenAnswer = errors.New("licence: the licence server's answer isn't a valid token")
)

// Client talks to Dodo (activate, validate, deactivate) and to our token route. HTTP must be a
// client from connector/egress (FirstParty): the person at the terminal named both destinations.
type Client struct {
	HTTP      *http.Client
	DodoBase  string // DodoTestBase or DodoLiveBase
	Server    string // https://deedglass.com, or a local site in development
	NoNetwork bool
}

func (c *Client) post(ctx context.Context, url string, in any, out any) (int, error) {
	if c.NoNetwork {
		return 0, ErrNetworkOff
	}
	body, err := json.Marshal(in)
	if err != nil {
		return 0, err
	}
	ctx, cancel := context.WithTimeout(ctx, RequestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return 0, fmt.Errorf("%w: %v", ErrUnreachable, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBody+1))
	if err != nil || len(raw) > maxResponseBody {
		return resp.StatusCode, ErrUnreachable
	}
	if resp.StatusCode >= 500 {
		return resp.StatusCode, ErrUnreachable
	}
	if resp.StatusCode/100 == 2 && out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			return resp.StatusCode, fmt.Errorf("%w: the answer isn't JSON", ErrUnreachable)
		}
	}
	return resp.StatusCode, nil
}

func dodoError(status int) error {
	switch status {
	case http.StatusNotFound:
		return ErrKeyUnknown
	case http.StatusForbidden:
		return ErrKeyInactive
	case http.StatusUnprocessableEntity:
		return ErrKeyLimit
	}
	return fmt.Errorf("%w: Dodo answered %d", ErrUnreachable, status)
}

// InstanceName is the name Dodo records for this machine: the first 8 hex characters of
// sha256(hostname). The hostname itself never leaves the machine.
func InstanceName(hostname string) string {
	s := sha256.Sum256([]byte(hostname))
	return hex.EncodeToString(s[:])[:8]
}

// Activate registers this machine with Dodo and returns the lki_ instance id.
func (c *Client) Activate(ctx context.Context, key, name string) (string, error) {
	var out struct {
		ID string `json:"id"`
	}
	st, err := c.post(ctx, c.DodoBase+"/licenses/activate", map[string]string{"license_key": key, "name": name}, &out)
	if err != nil {
		return "", err
	}
	if st/100 != 2 {
		return "", dodoError(st)
	}
	if !reInst.MatchString(out.ID) {
		return "", fmt.Errorf("%w: Dodo returned no instance id", ErrUnreachable)
	}
	return out.ID, nil
}

// Validate asks Dodo whether the key (and this instance) is still active.
func (c *Client) Validate(ctx context.Context, key, instance string) error {
	var out struct {
		Valid bool `json:"valid"`
	}
	in := map[string]string{"license_key": key}
	if instance != "" {
		in["license_key_instance_id"] = instance
	}
	st, err := c.post(ctx, c.DodoBase+"/licenses/validate", in, &out)
	if err != nil {
		return err
	}
	if st/100 != 2 {
		return dodoError(st)
	}
	if !out.Valid {
		return ErrKeyInactive
	}
	return nil
}

// Deactivate frees this machine's activation.
func (c *Client) Deactivate(ctx context.Context, key, instance string) error {
	st, err := c.post(ctx, c.DodoBase+"/licenses/deactivate", map[string]string{"license_key": key, "license_key_instance_id": instance}, nil)
	if err != nil {
		return err
	}
	if st/100 != 2 && st != http.StatusNotFound {
		return dodoError(st)
	}
	return nil
}

// tokenRequest is POST /api/licence/token's body. features_known (S50 §3.8) lists the features
// this build understands; the site signs only those (a request without it gets the v1 list).
type tokenRequest struct {
	LicenseKey    string   `json:"license_key"`
	Instance      string   `json:"instance"`
	FeaturesKnown []string `json:"features_known"`
}

// FetchToken asks our site to re-validate the key with Dodo and sign a licence token.
func (c *Client) FetchToken(ctx context.Context, key, instance string) (Token, error) {
	var raw json.RawMessage
	st, err := c.post(ctx, strings.TrimRight(c.Server, "/")+"/api/licence/token", tokenRequest{LicenseKey: key, Instance: instance, FeaturesKnown: KnownFeatures()}, &raw)
	if err != nil {
		return Token{}, err
	}
	switch {
	case st == http.StatusForbidden:
		return Token{}, ErrKeyInactive
	case st == http.StatusNotFound:
		return Token{}, ErrKeyUnknown
	case st/100 != 2:
		return Token{}, fmt.Errorf("%w (%d)", ErrServerRefused, st)
	}
	t, err := Parse(raw)
	if err != nil {
		return Token{}, fmt.Errorf("%w: %v", ErrBadTokenAnswer, err)
	}
	return t, nil
}
