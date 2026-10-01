package httpx

import (
	"context"
	"net"
	"net/http"
	"net/netip"

	"github.com/deedglass/deedglass/internal/ids"
)

// DevOrgHeader names the org of the dev principal.
const DevOrgHeader = "Deedglass-Dev-Org"

// DevPrincipal is the only principal until real sign-in (S07) and API keys (S08/S09) exist, and
// they delete it. It accepts a request only when all of these hold: the server is not in prod, the
// peer and the resolved client address are loopback, and Deedglass-Dev-Org names an org id. The
// principal is a read-only viewer of that org (scopes actions:read and deeds:read). Anything else
// is 401 authentication_required, as if no authenticator were configured: fail closed.
type DevPrincipal struct {
	Prod bool // DEEDGLASS_ENV=prod: refuse everything
}

// DevScopes are the dev principal's scopes.
var DevScopes = []string{"actions:read", "deeds:read"}

// AuthenticateSession implements SessionAuthenticator.
func (d DevPrincipal) AuthenticateSession(ctx context.Context, r *http.Request) (Principal, error) {
	h := r.Header.Values(DevOrgHeader)
	if d.Prod || len(h) != 1 || !loopbackPeer(r.RemoteAddr) || !loopbackAddr(ClientIP(ctx)) {
		return Principal{}, E(CodeAuthenticationRequired)
	}
	org, err := ids.Parse[ids.OrgKind](h[0])
	if err != nil {
		return Principal{}, E(CodeAuthenticationRequired, Param(DevOrgHeader),
			Msg("Deedglass-Dev-Org must be an org id like org_01j9z8m2k4p6r8t0v2x4z6b8d0. Run deedglass dev seed to print the demo org's id."))
	}
	return Principal{Kind: "dev", OrgID: org, Role: RoleViewer, Scopes: append([]string(nil), DevScopes...)}, nil
}

func loopbackPeer(remote string) bool {
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		host = remote
	}
	return loopbackAddr(host)
}

func loopbackAddr(s string) bool {
	a, err := netip.ParseAddr(s)
	return err == nil && a.Unmap().IsLoopback()
}
