package httpx

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"path"
	"regexp"
	"runtime/debug"
	"strings"
	"sync/atomic"
	"time"

	"github.com/deedglass/deedglass/internal/buildinfo"
	"github.com/deedglass/deedglass/internal/ids"
)

// Options configure a Server. The zero value serves public routes only: every other route answers
// 401 authentication_required until S07/S08 plug in their authenticators (fail closed).
type Options struct {
	Logger         *slog.Logger // wrap it with NewRedactingHandler; default slog.Default()
	Prod           bool         // DEEDGLASS_ENV=prod
	BaseURL        *url.URL     // HSTS is sent only in prod with an https base URL
	TrustedProxies []netip.Prefix
	Keys           KeyAuthenticator     // S08
	Sessions       SessionAuthenticator // S07
	Idempotency    IdemStore            // default: an in-memory store (tests and dev)
	Metrics        *Metrics             // default: a new registry
	Now            func() time.Time     // default time.Now
}

// Server is the HTTP pipeline: the global stages, the router and every mounted route.
type Server struct {
	o        Options
	log      *slog.Logger
	mux      *http.ServeMux
	routes   []Route
	literal  []string // mounted literal paths, for did-you-mean
	contract map[string]ContractRoute
	metrics  *Metrics
	draining atomic.Bool
	inFlight atomic.Int64
}

// New returns a server with no routes mounted.
func New(o Options) *Server {
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.Idempotency == nil {
		o.Idempotency = NewMemoryStore(o.Now)
	}
	if o.Metrics == nil {
		o.Metrics = NewMetrics()
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	s := &Server{o: o, log: o.Logger, mux: http.NewServeMux(), contract: contractByKey, metrics: o.Metrics}
	s.mux.Handle("/", http.HandlerFunc(s.unmatched))
	return s
}

// Mount adds routes. It panics on a declaration error (no auth, a POST without an idempotency rule,
// a route missing from the contract catalog, a duplicate): a bad route table never starts.
func (s *Server) Mount(routes ...Route) {
	for _, rt := range routes {
		rt = rt.normalize(s.contract)
		for _, m := range s.routes {
			if m.Key() == rt.Key() {
				panic("httpx: route " + rt.Key() + " is mounted twice")
			}
		}
		s.mux.Handle(rt.Key(), s.routeHandler(rt))
		s.routes = append(s.routes, rt)
		if !strings.Contains(rt.Pattern, "{") {
			s.literal = append(s.literal, rt.Pattern)
		}
	}
}

// Routes returns the mounted routes, with defaults applied.
func (s *Server) Routes() []Route { return append([]Route(nil), s.routes...) }

// Metrics returns the server's metrics registry.
func (s *Server) Metrics() *Metrics { return s.metrics }

// StartDraining switches health to draining (the first SIGTERM). Requests are still served.
func (s *Server) StartDraining() { s.draining.Store(true) }

// Draining reports whether StartDraining was called.
func (s *Server) Draining() bool { return s.draining.Load() }

// InFlight is the number of requests being served now.
func (s *Server) InFlight() int64 { return s.inFlight.Load() }

// ServeHTTP runs the global stages, outermost first: requestID, recoverer, realIP,
// accessLog+metrics, securityHeaders, then the router.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := s.o.Now()
	rid := ids.New[ids.RequestKind]().String()
	h := w.Header()
	h.Set("Deedglass-Request-Id", rid) // before anything can fail

	ri := &reqInfo{requestID: rid, route: "unmatched", apiVersion: preVersion(r)}
	r = r.WithContext(context.WithValue(r.Context(), ctxKey{}, ri))
	rw := &respWriter{ResponseWriter: w}

	s.inFlight.Add(1)
	s.metrics.inFlight.Add(1)
	defer func() {
		s.inFlight.Add(-1)
		s.metrics.inFlight.Add(-1)
		s.finish(r, rw, ri, s.o.Now().Sub(start))
	}()
	defer func() {
		p := recover()
		if p == nil {
			return
		}
		if p == http.ErrAbortHandler { //nolint:errorlint // the sentinel is compared by identity
			rw.status = 499
			panic(p)
		}
		s.log.ErrorContext(r.Context(), "panic serving request", "request_id", rid, "route", ri.route,
			"panic", fmt.Sprint(p), "stack", string(debug.Stack()))
		if !rw.wrote {
			writeError(rw, rid, E(CodeInternalError))
		}
	}()

	ri.ip = s.clientIP(r)
	if v := r.Header.Get("X-Request-Id"); v != "" && len(v) <= 64 && printable(v) {
		ri.clientRequestID = v
	}
	s.securityHeaders(h)
	setVersion(h, ri.apiVersion)

	if p := r.URL.Path; p == "" || p[0] != '/' || cleanPath(p) != p {
		// The mux would redirect an unclean path; an API answers exactly or not at all.
		writeError(rw, rid, errEndpointNotFound(r.Method, p, ""))
		return
	}
	s.mux.ServeHTTP(rw, r)
}

// unmatched answers every request no pattern matched: 405 with Allow when another method matches
// the path, else 404 with did-you-mean.
func (s *Server) unmatched(w http.ResponseWriter, r *http.Request) {
	rid := RequestID(r.Context())
	var allowed []string
	probe := *r
	for _, m := range validMethods {
		if m == r.Method {
			continue
		}
		probe.Method = m
		if _, pat := s.mux.Handler(&probe); pat != "/" && pat != "" {
			allowed = append(allowed, m)
		}
	}
	if len(allowed) > 0 {
		writeError(w, rid, errMethodNotAllowed(r.Method, r.URL.Path, allowed))
		return
	}
	writeError(w, rid, errEndpointNotFound(r.Method, r.URL.Path, s.suggest(r.URL.Path)))
}

// suggest returns the mounted literal path within edit distance 2 of p, or "".
func (s *Server) suggest(p string) string {
	if len(p) > 200 {
		return ""
	}
	best, bestD := "", 3
	for _, c := range s.literal {
		if d := levenshtein(p, c); d < bestD && d > 0 {
			best, bestD = c, d
		}
	}
	return best
}

// routeHandler builds the per-route stages: timeout, bodyLimit, contentType, auth, version,
// idempotency, then the handler.
func (s *Server) routeHandler(rt Route) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ri := info(r.Context())
		ri.route = rt.Pattern
		ctx, cancel := context.WithTimeout(r.Context(), rt.Timeout)
		defer cancel()
		_ = http.NewResponseController(w).SetWriteDeadline(s.o.Now().Add(rt.Timeout + 5*time.Second))
		r = r.WithContext(ctx)

		if rt.Guard != nil && !rt.Guard.Admit(w, r) {
			return
		}
		if err := s.prepare(rt, w, r, ri); err != nil {
			s.render(w, r, rt, err)
			return
		}
		if s.idempotent(rt, r, ri) {
			s.serveIdempotent(rt, w, r, ri)
			return
		}
		s.invoke(rt, w, r)
	})
}

// prepare runs the stages that can refuse a request before its handler: nothing they refuse is
// stored for idempotent replay.
func (s *Server) prepare(rt Route, w http.ResponseWriter, r *http.Request, ri *reqInfo) error {
	// bodyLimit.
	if rt.MaxBody == NoBody {
		if r.ContentLength > 0 || (r.ContentLength < 0 && r.Body != nil && r.Body != http.NoBody) {
			return E(CodeRequestTooLarge, Msg("This endpoint doesn't take a request body. Send the request without one."))
		}
		r.Body = http.NoBody
	} else {
		if r.ContentLength > rt.MaxBody {
			return errTooLarge(rt.MaxBody)
		}
		if r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, rt.MaxBody)
		}
	}
	// contentType: required when a body-carrying method sends a body.
	if hasBody(r.Method) && r.ContentLength != 0 && rt.MaxBody != NoBody {
		if err := checkContentType(r.Header.Get("Content-Type"), rt.BodyType); err != nil {
			return err
		}
	}
	// auth.
	if err := s.authenticate(rt, r, ri); err != nil {
		return err
	}
	// version.
	return resolveVersion(w, r, ri)
}

func checkContentType(ct, want string) error {
	fail := E(CodeUnsupportedMediaType)
	if want != BodyJSON {
		fail = E(CodeUnsupportedMediaType, Msgf("Send the body as %s, with Content-Type: %s.", want, want))
	}
	if ct == "" {
		return fail
	}
	mt, params, err := mime.ParseMediaType(ct)
	if err != nil || mt != want {
		return fail
	}
	for k, v := range params {
		if k != "charset" || !strings.EqualFold(v, "utf-8") {
			return fail
		}
	}
	return nil
}

// authenticate applies route.Auth. A route whose authenticator isn't configured answers 401
// authentication_required: fail closed.
//
// API keys follow S08 §3.5, first failure wins: a bearer is present (1); the key authenticator
// applies the failed-auth limiter (2), the strict parse, the prefix lookup, the mode and the
// constant-time hash (3, one message for all of them), then revoked (4) and expired (5). The
// pipeline then refuses a stopped org (6), a stopped agent (7) and a suspended agent (8), unless the
// route sets SkipStopChecks, and finally checks the route's scopes (9).
func (s *Server) authenticate(rt Route, r *http.Request, ri *reqInfo) error {
	if rt.Auth == Public {
		return nil
	}
	bearer, hasBearer, badScheme := bearerToken(r)
	keyOK := rt.Auth == AgentKey || rt.Auth == SessionOrKey
	sessOK := rt.Auth == Session || rt.Auth == SessionOrKey
	required := func() error {
		if keyOK {
			return E(CodeAuthenticationRequired, WithHeader("WWW-Authenticate", `Bearer realm="deedglass"`))
		}
		return E(CodeAuthenticationRequired)
	}
	var (
		p   Principal
		err error
	)
	switch {
	case keyOK && (hasBearer || badScheme), rt.Auth == Session && hasBearer && s.o.Keys != nil:
		if badScheme {
			return withChallenge(E(CodeInvalidAPIKey, Msg("The Authorization header must be Bearer followed by an API key (dg_live_… or dg_test_…).")))
		}
		if s.o.Keys == nil {
			return required()
		}
		p, err = s.o.Keys.AuthenticateKey(r.Context(), bearer)
		if err != nil {
			if e := AsError(err); e != nil && e.Status == http.StatusUnauthorized {
				return withChallenge(e)
			}
			return err
		}
		if !p.IsKey() || p.OrgID.IsZero() {
			return s.internal(r, errors.New("httpx: the key authenticator returned a principal that isn't a key"))
		}
		if !keyOK {
			// A route only a signed-in person may call (Start all; minting admin keys).
			return ErrKeyOnSessionRoute(p, rt.MinRole, rt.Purpose)
		}
		if err := keyAllowed(rt, p); err != nil {
			return err
		}
	case sessOK:
		if s.o.Sessions == nil {
			return required()
		}
		p, err = s.o.Sessions.AuthenticateSession(r.Context(), r)
		if err != nil {
			return err
		}
		if p.Kind != KindUser {
			return s.internal(r, errors.New("httpx: the session authenticator returned a principal that isn't a person"))
		}
		// Roles: people are checked against MinRole on every request, with the live membership the
		// session authenticator just read (S07).
		if rt.MinRole != "" && !p.Role.AtLeast(rt.MinRole) {
			return ErrInsufficientRole(p.OrgName, p.Role, rt.MinRole, rt.Purpose)
		}
	default:
		return required()
	}
	ri.principal = &p
	ri.orgID = p.OrgID.String()
	if !p.AgentID.IsZero() {
		ri.agentID = p.AgentID.String()
	}
	if p.IsKey() {
		if h, ok := s.o.Keys.(KeyRequestHook); ok {
			return h.AfterKey(r, p)
		}
	}
	return nil
}

// keyAllowed is §3.5 steps 6–9 for an authenticated key. A RecordStopped route (POST /v1/actions)
// lets an agent key of a stopped or suspended agent, or of a stopped org, through to the handler,
// which records the refusal as a sealed deed (S13); admin keys still get the stop checks.
func keyAllowed(rt Route, p Principal) error {
	if !rt.SkipStopChecks && !(rt.RecordStopped && p.Kind == KindAgentKey) {
		switch {
		// During Stop all, admin keys keep read-only access (GET, HEAD) so incident response can
		// look around; anything that changes state is refused like every agent call (DL-022).
		case p.OrgStopped && p.Kind == KindAdminKey && !readOnly(rt.Method):
			return ErrOrgStoppedAdminWrite(p.OrgName)
		case p.OrgStopped && p.Kind != KindAdminKey:
			return ErrOrgStopped(p.OrgName)
		case p.Kind == KindAgentKey && p.AgentStatus == "stopped":
			return ErrAgentStopped(p.AgentName)
		case p.Kind == KindAgentKey && p.AgentStatus == "suspended":
			return ErrAgentSuspended(p.AgentName)
		case p.Kind == KindAgentKey && p.AgentStatus != "active":
			return ErrAgentStopped(p.AgentName) // an unknown status fails closed
		}
	}
	if p.Kind == KindAdminKey {
		// Admin keys act as the admin role, never as owner (S08 §3.4).
		switch {
		case rt.MinRole == RoleOwner:
			return ErrKeyOnSessionRoute(p, RoleOwner, rt.Purpose)
		case len(rt.Scopes) == 0, contains(rt.Scopes, ScopeAdmin):
			return nil
		case rt.Auth == SessionOrKey && rt.MinRole.Rank() > 0 && rt.MinRole.Rank() <= RoleAdmin.Rank():
			return nil // a route an admin may use in the Console (reading deeds, the Ledger)
		}
		return ErrInsufficientScope(rt.Scopes[0])
	}
	for _, sc := range rt.Scopes {
		if !contains(p.Scopes, sc) {
			if sc == ScopeAdmin {
				e := ErrAgentKeyForAdmin()
				if rt.AgentKeyMessage != "" {
					e.Message = rt.AgentKeyMessage
				}
				return e
			}
			return ErrInsufficientScope(sc)
		}
	}
	return nil
}

// readOnly reports whether a method can't change state.
func readOnly(method string) bool { return method == http.MethodGet || method == http.MethodHead }

// withChallenge adds RFC 6750's challenge to a 401 about a bearer key:
// Bearer realm="deedglass", error="invalid_token", error_description="<message>".
func withChallenge(e *Error) *Error {
	if e.Header.Get("WWW-Authenticate") == "" {
		WithHeader("WWW-Authenticate", `Bearer realm="deedglass", error="invalid_token", error_description="`+challengeText(e.Message)+`"`)(e)
	}
	return e
}

// challengeText makes a message safe for a quoted-string in WWW-Authenticate (RFC 6750 §3:
// %x20-21 / %x23-5B / %x5D-7E): › becomes >, quotes and backslashes become ', the rest is dropped.
func challengeText(m string) string {
	var b strings.Builder
	for _, r := range m {
		switch {
		case r == '›':
			b.WriteByte('>')
		case r == '"' || r == '\\':
			b.WriteByte('\'')
		case r == '…':
			b.WriteString("...")
		case r >= 0x20 && r <= 0x7e:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// bearerToken reads "Authorization: Bearer <token>". badScheme is true for any other scheme.
func bearerToken(r *http.Request) (token string, ok, badScheme bool) {
	v := r.Header.Get("Authorization")
	if v == "" {
		return "", false, false
	}
	scheme, tok, found := strings.Cut(v, " ")
	if !found || !strings.EqualFold(scheme, "Bearer") || strings.TrimSpace(tok) == "" {
		return "", false, true
	}
	return strings.TrimSpace(tok), true, false
}

var versionRE = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

func latestVersion() string { return buildinfo.LatestAPIVersion }

// preVersion is the version the global stage echoes before auth: a valid requested version, or
// the latest. The per-route version stage refines it.
func preVersion(r *http.Request) string {
	if v := r.Header.Get("Deedglass-Version"); v != "" && buildinfo.SupportsAPIVersion(v) {
		return v
	}
	return latestVersion()
}

// resolveVersion validates Deedglass-Version, or picks the principal's pinned version (else the
// latest), and echoes it.
func resolveVersion(w http.ResponseWriter, r *http.Request, ri *reqInfo) error {
	vs := r.Header.Values("Deedglass-Version")
	switch {
	case len(vs) > 1:
		return E(CodeAPIVersionInvalid, Param("Deedglass-Version"), Msg("Send Deedglass-Version once. Use "+latestVersion()+", or leave it out to use your org's pinned version."))
	case len(vs) == 1:
		v := vs[0]
		if !versionRE.MatchString(v) || !buildinfo.SupportsAPIVersion(v) {
			return E(CodeAPIVersionInvalid, Param("Deedglass-Version"))
		}
		ri.apiVersion = v
	default:
		ri.apiVersion = latestVersion()
		if ri.principal != nil && buildinfo.SupportsAPIVersion(ri.principal.APIVersion) {
			ri.apiVersion = ri.principal.APIVersion
		}
	}
	setVersion(w.Header(), ri.apiVersion)
	return nil
}

// invoke runs the handler and renders a returned error, if nothing was written yet.
func (s *Server) invoke(rt Route, w http.ResponseWriter, r *http.Request) {
	if err := rt.Handler(w, r); err != nil {
		s.render(w, r, rt, err)
	}
}

// render writes err as the envelope. Causes are logged here, never rendered.
func (s *Server) render(w http.ResponseWriter, r *http.Request, rt Route, err error) {
	rid := RequestID(r.Context())
	if t, ok := w.(interface{ written() bool }); ok && t.written() {
		s.log.WarnContext(r.Context(), "handler returned an error after writing the response",
			"request_id", rid, "route", rt.Pattern, "error", err.Error())
		return
	}
	e := AsError(err)
	if e != nil && rt.Guard != nil {
		if v := e.Header.Get("WWW-Authenticate"); v != "" {
			e.Header.Set("WWW-Authenticate", rt.Guard.Challenge(r, v))
		}
	}
	switch {
	case e != nil:
		if e.cause != nil {
			lvl := slog.LevelDebug
			if e.Status >= 500 {
				lvl = slog.LevelError
			}
			s.log.Log(r.Context(), lvl, "request failed", "request_id", rid, "route", rt.Pattern, "code", string(e.Code), "cause", e.cause.Error())
		}
	case errors.Is(err, context.DeadlineExceeded) && errors.Is(r.Context().Err(), context.DeadlineExceeded):
		e = errTimeout(rt.Timeout.Seconds())
		s.log.WarnContext(r.Context(), "request timed out", "request_id", rid, "route", rt.Pattern, "timeout_ms", rt.Timeout.Milliseconds())
	default:
		e = s.internal(r, err)
	}
	writeError(w, rid, e)
}

func (s *Server) internal(r *http.Request, err error) *Error {
	s.log.ErrorContext(r.Context(), "internal error", "request_id", RequestID(r.Context()), "error", err.Error())
	return E(CodeInternalError, Cause(err))
}

// securityHeaders are set on every response. No CORS headers at all: the API is server to server,
// and the Console is same-origin (public metadata routes add their own, S04/S09).
func (s *Server) securityHeaders(h http.Header) {
	// Shared, read-only value slices (len == cap, so an Add copies): no allocation per request.
	h["X-Content-Type-Options"] = hvNosniff
	h["Referrer-Policy"] = hvNoReferrer
	h["X-Frame-Options"] = hvDeny
	h["Content-Security-Policy"] = hvCSP
	h["Cross-Origin-Resource-Policy"] = hvCORP
	h["Cache-Control"] = hvNoStore // a route may replace it
	if s.o.Prod && s.o.BaseURL != nil && s.o.BaseURL.Scheme == "https" {
		h["Strict-Transport-Security"] = hvHSTS
	}
}

var (
	hvNosniff    = []string{"nosniff"}
	hvNoReferrer = []string{"no-referrer"}
	hvDeny       = []string{"DENY"}
	hvCSP        = []string{"default-src 'none'; frame-ancestors 'none'"}
	hvCORP       = []string{"same-origin"}
	hvNoStore    = []string{"no-store"}
	hvHSTS       = []string{"max-age=63072000; includeSubDomains"}
	hvJSON       = []string{ContentTypeJSON}
	hvVersions   = func() map[string][]string {
		m := map[string][]string{}
		for _, v := range buildinfo.APIVersions {
			m[v] = []string{v}
		}
		return m
	}()
)

// setVersion echoes the resolved Deedglass-Version.
func setVersion(h http.Header, v string) {
	if s, ok := hvVersions[v]; ok {
		h["Deedglass-Version"] = s
		return
	}
	h.Set("Deedglass-Version", v)
}

// clientIP honours X-Forwarded-For only when the peer is a trusted proxy, walking the list right to
// left and skipping trusted hops.
func (s *Server) clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	peer, err := netip.ParseAddr(host)
	if err != nil {
		return host
	}
	peer = peer.Unmap()
	if !s.trusted(peer) {
		return peer.String()
	}
	hops := strings.Split(strings.Join(r.Header.Values("X-Forwarded-For"), ","), ",")
	client := peer
	for i := len(hops) - 1; i >= 0; i-- {
		a, err := netip.ParseAddr(strings.TrimSpace(hops[i]))
		if err != nil {
			break // a malformed hop: stop at the last address we could read
		}
		client = a.Unmap()
		if !s.trusted(client) {
			return client.String()
		}
	}
	return client.String()
}

func (s *Server) trusted(a netip.Addr) bool {
	for _, p := range s.o.TrustedProxies {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// finish writes the access-log line and the metrics for one request. Never bodies, never the query.
func (s *Server) finish(r *http.Request, rw *respWriter, ri *reqInfo, d time.Duration) {
	status := rw.status
	if status == 0 {
		status = http.StatusOK
	}
	s.metrics.observe(ri.route, r.Method, status, d)
	ctx := r.Context()
	if !s.log.Enabled(ctx, slog.LevelInfo) {
		return
	}
	ua := r.UserAgent()
	if len(ua) > 128 {
		ua = ua[:128]
	}
	s.log.LogAttrs(ctx, slog.LevelInfo, "request",
		slog.String("request_id", ri.requestID),
		slog.String("method", r.Method),
		slog.String("route", ri.route),
		slog.String("path", clip(r.URL.Path, 200)),
		slog.Int("status", status),
		slog.Int64("bytes", rw.bytes),
		slog.Float64("duration_ms", float64(d.Microseconds())/1000),
		slog.String("ip", ri.ip),
		slog.String("org_id", ri.orgID),
		slog.String("agent_id", ri.agentID),
		slog.String("action_id", ri.actionID),
		slog.String("api_version", ri.apiVersion),
		slog.Bool("idempotent_replay", ri.replayed),
		slog.String("client_request_id", ri.clientRequestID),
		slog.String("user_agent", ua),
	)
}

// respWriter records the status and byte count, and whether anything was written.
type respWriter struct {
	http.ResponseWriter
	status int
	bytes  int64
	wrote  bool
}

func (w *respWriter) WriteHeader(code int) {
	if w.wrote {
		return
	}
	if code >= 100 && code < 200 && code != http.StatusSwitchingProtocols {
		w.ResponseWriter.WriteHeader(code) // informational: not final
		return
	}
	w.status, w.wrote = code, true
	w.ResponseWriter.WriteHeader(code)
}

func (w *respWriter) Write(b []byte) (int, error) {
	if !w.wrote {
		w.WriteHeader(http.StatusOK)
	}
	n, err := w.ResponseWriter.Write(b)
	w.bytes += int64(n)
	return n, err
}

func (w *respWriter) written() bool { return w.wrote }

// Unwrap lets http.ResponseController reach Flush and the deadlines.
func (w *respWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// cleanPath is path.Clean that keeps a trailing slash.
func cleanPath(p string) string {
	c := path.Clean(p)
	if strings.HasSuffix(p, "/") && c != "/" {
		c += "/"
	}
	return c
}

func printable(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 || s[i] > 0x7e {
			return false
		}
	}
	return true
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

func levenshtein(a, b string) int {
	if a == b {
		return 0
	}
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}
