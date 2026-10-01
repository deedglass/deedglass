package identity

import (
	"context"
	"net/http"

	"github.com/deedglass/deedglass/internal/httpx"
	"github.com/deedglass/deedglass/internal/intent"
)

// On-behalf-of (S09 §3.9). An agent says which person it is serving with Deedglass-On-Behalf-Of
// and which conversation or run with Deedglass-Session. Both are opaque references: Deedglass
// records them as the agent sent them (or as it signed them), and can't know the person is real
// until an identity provider vouches for them (v2, docs/IDENTITY.md).

// The OBO headers.
const (
	HeaderOnBehalfOf = "Deedglass-On-Behalf-Of"
	HeaderSession    = "Deedglass-Session"
	HeaderIntent     = "Deedglass-Intent"
)

// OBO is the person and session an agent acts for. Empty fields were not sent.
type OBO struct {
	OnBehalfOf string `json:"on_behalf_of,omitempty"`
	Session    string `json:"session,omitempty"`
}

// IsZero reports whether neither reference was sent.
func (o OBO) IsZero() bool { return o.OnBehalfOf == "" && o.Session == "" }

// ParseOBO reads Deedglass-On-Behalf-Of and Deedglass-Session. Each may appear at most once and
// must match ^[A-Za-z0-9._~:@/+=-]{1,256}$; anything else is 422 validation_failed with the
// header as param. Go's HTTP server already refuses CR and LF in header values.
func ParseOBO(h http.Header) (OBO, error) {
	var o OBO
	for _, f := range []struct {
		name string
		dst  *string
	}{{HeaderOnBehalfOf, &o.OnBehalfOf}, {HeaderSession, &o.Session}} {
		vs := h.Values(f.name)
		switch {
		case len(vs) == 0:
			continue
		case len(vs) > 1:
			return OBO{}, httpx.ErrOBOInvalid(f.name, true)
		case !intent.ValidRef(vs[0]):
			return OBO{}, httpx.ErrOBOInvalid(f.name, false)
		}
		*f.dst = vs[0]
	}
	return o, nil
}

type oboKey struct{}

// OBOFrom returns the request's OBO headers as the key pipeline parsed them. They are the agent's
// unsigned claim; when a signed intent covers them, IntentResult.OBO holds the effective values.
func OBOFrom(ctx context.Context) OBO {
	if v, ok := ctx.Value(oboKey{}).(OBO); ok {
		return v
	}
	if v, ok := httpx.RequestValue(ctx, oboKey{}); ok {
		o, _ := v.(OBO)
		return o
	}
	return OBO{}
}

// WithOBO returns ctx carrying o, for code outside an HTTP request (workers, tests).
func WithOBO(ctx context.Context, o OBO) context.Context { return context.WithValue(ctx, oboKey{}, o) }

// AfterKey implements httpx.KeyRequestHook: it parses the OBO headers on every request an API key
// authenticated, and keeps them for OBOFrom.
func (k *KeyAuth) AfterKey(r *http.Request, _ Principal) error {
	o, err := ParseOBO(r.Header)
	if err != nil {
		return err
	}
	if !o.IsZero() {
		httpx.SetRequestValue(r.Context(), oboKey{}, o)
	}
	return nil
}
