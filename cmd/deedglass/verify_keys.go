package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/deedglass/deedglass/internal/deed"
)

// Seal keys for deedglass verify (S05 §3.3). Network is used only here, only for a --keys value that
// is an https URL (or http on a loopback host), with a 10 s budget, a 1 MiB cap, at most three
// same-host redirects, no cookies, no proxy and no retries.

const (
	keysFetchMax     = 1 << 20
	keysMaxRedirects = 3
)

// keysFetchTimeout is the whole budget for one --keys URL (a var so tests can shorten it).
var keysFetchTimeout = 10 * time.Second

// verifyTransport is replaced in tests (an httptest TLS server's transport).
var verifyTransport http.RoundTripper = &http.Transport{
	Proxy:                 nil, // never send the operator's proxy credentials anywhere
	DialContext:           (&net.Dialer{Timeout: keysFetchTimeout}).DialContext,
	TLSHandshakeTimeout:   keysFetchTimeout,
	ResponseHeaderTimeout: keysFetchTimeout,
	MaxIdleConns:          1,
	DisableKeepAlives:     true,
}

// keySource is one parsed key set and where it came from.
type keySource struct {
	ks     *deed.KeySet
	source string // the file path or URL, or "bundle"
}

var rePin = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// loadKeyArg reads one --keys value: a file, or an https URL.
func loadKeyArg(ctx context.Context, arg string) (keySource, error) {
	if strings.HasPrefix(arg, "https://") || strings.HasPrefix(arg, "http://") {
		ks, err := fetchKeys(ctx, arg)
		return keySource{ks, arg}, err
	}
	raw, err := readCapped(arg, keysFetchMax)
	if err != nil {
		return keySource{}, inputErr("keys_unreadable", fmt.Errorf("Can't read seal keys from %s: %s", clean(arg), err.Error()),
			"Pass a keys.json file or the issuer's https URL to --keys.")
	}
	ks, err := parseKeys(raw, arg)
	return keySource{ks, arg}, err
}

// parseKeys reads a key set and turns its errors into the copy from messages.go.
func parseKeys(raw []byte, from string) (*deed.KeySet, error) {
	ks, err := deed.ParseKeySet(bytes.TrimSpace(raw))
	if err == nil {
		return ks, nil
	}
	var ve *deed.VerifyError
	if errors.As(err, &ve) && ve.Code == deed.CodeSealKeyRejected {
		return nil, unsafeKeysErr(ve)
	}
	return nil, &ExitError{Code: ExitUsage, ErrCode: "keys_invalid",
		Err:  fmt.Errorf("%s is not a seal key set (%s)", clean(from), clean(strings.TrimPrefix(err.Error(), "deed: "))),
		Next: "Pass the issuer's keys.json: --keys <issuer>/.well-known/deedglass/keys.json"}
}

// unsafeKeysErr is an unsafe key set: a failure (exit 1), never a key to use.
func unsafeKeysErr(ve *deed.VerifyError) error {
	msg, next := deed.Message(ve)
	return &ExitError{Code: ExitFailure, ErrCode: string(ve.Code), Err: errors.New(clean(strings.TrimSuffix(msg, "."))), Next: next}
}

// fetchErr is the ○ "could not fetch" state: exit 2, nothing was checked. (Plain marks ○ for
// verify's renderer.)
func fetchErr(host, reason string) error {
	return &ExitError{Code: ExitUsage, ErrCode: "keys_unreachable", Plain: true,
		Err:  fmt.Errorf("Could not fetch seal keys from %s (%s)", clean(host), reason),
		Next: "Check the URL, or download keys.json and pass it as a file: --keys keys.json"}
}

// fetchKeys GETs a keys.json URL under the §3.3 rules.
func fetchKeys(ctx context.Context, raw string) (*deed.KeySet, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || (u.Scheme != "https" && u.Scheme != "http") {
		return nil, usageErr(fmt.Errorf("--keys %s is not a usable URL", clean(raw)), "Use an https URL, for example --keys https://api.deedglass.com/.well-known/deedglass/keys.json")
	}
	if u.Scheme == "http" && !loopback(u.Hostname()) {
		return nil, usageErr(fmt.Errorf("--keys %s uses http to a host that is not this machine", clean(raw)),
			"Use https. Plain http is allowed only for localhost, 127.0.0.1 and [::1].")
	}
	host := u.Host
	ctx, cancel := context.WithTimeout(ctx, keysFetchTimeout)
	defer cancel()
	client := &http.Client{Transport: verifyTransport, Timeout: keysFetchTimeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > keysMaxRedirects {
				return errors.New("more than 3 redirects")
			}
			if req.URL.Host != host || req.URL.Scheme != u.Scheme {
				return fmt.Errorf("it redirected to %s", req.URL.Host)
			}
			return nil
		}}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "deedglass/"+buildInfo().Version)
	res, err := client.Do(req)
	if err != nil {
		var ue *url.Error
		switch {
		case errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ue) && ue.Timeout()):
			return nil, fetchErr(host, "timed out after "+strings.TrimSuffix(fmt.Sprintf("%g", keysFetchTimeout.Seconds()), ".0")+" s")
		case errors.As(err, &ue) && ue.Err != nil && (strings.Contains(ue.Err.Error(), "redirect")):
			return nil, fetchErr(host, ue.Err.Error())
		}
		return nil, fetchErr(host, lastPart(err))
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fetchErr(host, fmt.Sprintf("it answered %d", res.StatusCode))
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, keysFetchMax+1))
	if err != nil {
		return nil, fetchErr(host, lastPart(err))
	}
	if len(body) > keysFetchMax {
		return nil, fetchErr(host, "the answer is larger than 1 MiB")
	}
	return parseKeys(body, raw)
}

func loopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// mergeKeys combines key sets by key id. The same id with different content refuses everything:
// a verifier must never pick one.
func mergeKeys(sets []keySource) (*deed.KeySet, map[string]string, error) {
	type entry struct {
		key    deed.SealKey
		source string
	}
	byID := map[string]entry{}
	var ids []string
	issuer, updated := "", ""
	for _, s := range sets {
		if issuer == "" {
			issuer = s.ks.Issuer
		}
		if s.ks.UpdatedAt > updated {
			updated = s.ks.UpdatedAt
		}
		for _, k := range s.ks.Keys {
			if prev, ok := byID[k.ID]; ok {
				a, _ := json.Marshal(prev.key)
				b, _ := json.Marshal(k)
				if !bytes.Equal(a, b) {
					return nil, nil, &ExitError{Code: ExitFailure, ErrCode: "keys_conflict",
						Err:  fmt.Errorf("Seal key %s differs between %s and %s", k.ID, clean(prev.source), clean(s.source)),
						Next: "Use one key set from the issuer. Two different keys with one id is a sign of tampering."}
				}
				continue
			}
			byID[k.ID] = entry{k, s.source}
			ids = append(ids, k.ID)
		}
	}
	keys := make([]deed.SealKey, 0, len(ids))
	src := map[string]string{}
	for _, id := range ids {
		keys = append(keys, byID[id].key)
		src[id] = byID[id].source
	}
	raw, err := json.Marshal(deed.KeySet{Object: "seal_key_set", Issuer: issuer, UpdatedAt: updated, Keys: keys})
	if err != nil {
		return nil, nil, err
	}
	ks, err := parseKeys(raw, "the merged key set")
	return ks, src, err
}

// pinKeys keeps only the keys whose thumbprint is pinned.
func pinKeys(ks *deed.KeySet, pins []string) (*deed.KeySet, error) {
	keep := map[string]bool{}
	for _, p := range pins {
		keep[p] = true
	}
	out := *ks
	out.Keys = nil
	for _, k := range ks.Keys {
		if keep[k.Thumbprint] {
			out.Keys = append(out.Keys, k)
		}
	}
	raw, err := json.Marshal(out)
	if err != nil {
		return nil, err
	}
	return parseKeys(raw, "the pinned key set")
}

// readCapped reads a regular file of at most max bytes.
func readCapped(path string, max int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, errors.New(reason(err))
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, errors.New(reason(err))
	}
	if !fi.Mode().IsRegular() {
		return nil, errors.New("not a regular file")
	}
	b, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, errors.New(reason(err))
	}
	if int64(len(b)) > max {
		return nil, fmt.Errorf("larger than %s", humanBytes(max))
	}
	return b, nil
}

// reason is the short cause of a file error: "no such file", "permission denied".
func reason(err error) string {
	switch {
	case errors.Is(err, os.ErrNotExist):
		return "no such file"
	case errors.Is(err, os.ErrPermission):
		return "permission denied"
	}
	return lastPart(err)
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<30 && n%(1<<30) == 0:
		return deed.FormatCount(n>>30) + " GiB"
	case n >= 1<<20 && n%(1<<20) == 0:
		return deed.FormatCount(n>>20) + " MiB"
	case n >= 1<<10 && n%(1<<10) == 0:
		return deed.FormatCount(n>>10) + " KiB"
	}
	return deed.FormatCount(n) + " bytes"
}

func inputErr(code string, err error, next string) error {
	return &ExitError{Code: ExitUsage, ErrCode: code, Err: err, Next: next}
}
