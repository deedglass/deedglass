package httpx

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/deedglass/deedglass/internal/deed"
)

// KeySetSource returns every seal key ever registered, as published (cmd/deedglass builds it from
// the store). httpx never imports the store.
type KeySetSource func(ctx context.Context) (*deed.KeySet, error)

// keySetTTL is how long one key set answers every request. `deedglass keys rotate` runs in another
// process, so a new key appears within this long (docs/SEAL_KEYS.md).
const keySetTTL = 60 * time.Second

// msgSealKeysUnavailable is the 503 message for GET /.well-known/deedglass/keys.json.
const msgSealKeysUnavailable = "Seal keys are temporarily unavailable. Retry in a few seconds."

type keySetCache struct {
	src  KeySetSource
	now  func() time.Time
	mu   sync.Mutex
	body []byte
	etag string
	at   time.Time
}

// WellKnownRoutes returns GET /.well-known/deedglass/keys.json: public, not versioned, cacheable,
// readable cross-origin (the browser verifier and the site fetch it), and never a partial list.
func (s *Server) WellKnownRoutes(src KeySetSource, now func() time.Time) []Route {
	if now == nil {
		now = time.Now
	}
	c := &keySetCache{src: src, now: now}
	return []Route{{Method: "GET", Pattern: "/.well-known/deedglass/keys.json", OperationID: "getSealKeys", Auth: Public,
		Timeout: 2 * time.Second, Since: "2026-10-01", Handler: c.serve}}
}

// get returns the cached body and ETag, or fetches them. One fetch at a time: concurrent misses
// wait for it rather than all reaching the database.
func (c *keySetCache) get(ctx context.Context) ([]byte, string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.body != nil && c.now().Sub(c.at) < keySetTTL {
		return c.body, c.etag, nil
	}
	ks, err := c.src(ctx)
	if err != nil {
		return nil, "", err
	}
	b, err := MarshalJSON(ks)
	if err != nil {
		return nil, "", err
	}
	sum := sha256.Sum256(b)
	c.body, c.etag, c.at = b, `"sha256:`+hex.EncodeToString(sum[:])+`"`, c.now()
	return c.body, c.etag, nil
}

func (c *keySetCache) serve(w http.ResponseWriter, r *http.Request) error {
	body, etag, err := c.get(r.Context())
	if err != nil {
		return E(CodeServiceUnavailable, Msg(msgSealKeysUnavailable), WithHeader("Retry-After", "5"), Cause(err))
	}
	h := w.Header()
	h.Set("Cache-Control", "public, max-age=300")
	h["ETag"] = []string{etag} // the exact spelling; Set would write "Etag"
	h.Set("Access-Control-Allow-Origin", "*")
	h.Set("Cross-Origin-Resource-Policy", "cross-origin")
	if etagMatch(r.Header.Get("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return nil
	}
	h["Content-Type"] = hvJSON
	h.Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodHead {
		return nil
	}
	_, err = w.Write(body)
	return err
}

// etagMatch applies If-None-Match (RFC 9110 §13.1.2): weak comparison, a list, or "*".
func etagMatch(header, etag string) bool {
	if header == "" {
		return false
	}
	for _, t := range strings.Split(header, ",") {
		t = strings.TrimPrefix(strings.TrimSpace(t), "W/")
		if t == "*" || t == etag {
			return true
		}
	}
	return false
}
