package ledger

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/deedglass/deedglass/internal/deed"
	"github.com/deedglass/deedglass/internal/httpx"
	"github.com/deedglass/deedglass/internal/ids"
)

// Scopes the Ledger routes need (contract §8; S08 confirmed them).
const (
	ScopeActionsRead = httpx.ScopeActionsRead
	ScopeDeedsRead   = httpx.ScopeDeedsRead
)

// Route timeouts beyond httpx's 30 s default.
const (
	exportTimeout = 10 * time.Minute
	checkTimeout  = CheckBudget + 15*time.Second
)

// Routes are the six Ledger routes (contract §9): GET /v1/actions, /v1/actions/{id}, /v1/deeds,
// /v1/deeds/{id}, /v1/deeds/export and /v1/ledger/check. Every one is SessionOrKey, viewer and
// up. Agent keys need actions:read or deeds:read and see only their own agent's rows; the export
// and the chain check cover the whole org, so they take a Console session or an org admin key
// (S08).
func Routes(l *Ledger) []httpx.Route {
	h := handlers{l}
	return []httpx.Route{
		{Method: "GET", Pattern: "/v1/actions", OperationID: "listActions", Auth: httpx.SessionOrKey, MinRole: httpx.RoleViewer,
			Scopes: []string{ScopeActionsRead}, Handler: h.listActions},
		{Method: "GET", Pattern: "/v1/actions/{id}", OperationID: "getAction", Auth: httpx.SessionOrKey, MinRole: httpx.RoleViewer,
			Scopes: []string{ScopeActionsRead}, Handler: h.getAction},
		{Method: "GET", Pattern: "/v1/deeds", OperationID: "listDeeds", Auth: httpx.SessionOrKey, MinRole: httpx.RoleViewer,
			Scopes: []string{ScopeDeedsRead}, Handler: h.listDeeds},
		{Method: "GET", Pattern: "/v1/deeds/{id}", OperationID: "getDeed", Auth: httpx.SessionOrKey, MinRole: httpx.RoleViewer,
			Scopes: []string{ScopeDeedsRead}, Handler: h.getDeed},
		{Method: "GET", Pattern: "/v1/deeds/export", OperationID: "exportDeeds", Auth: httpx.SessionOrKey, MinRole: httpx.RoleViewer,
			Scopes: []string{httpx.ScopeAdmin}, Timeout: exportTimeout, Handler: h.exportDeeds},
		{Method: "GET", Pattern: "/v1/ledger/check", OperationID: "checkLedger", Auth: httpx.SessionOrKey, MinRole: httpx.RoleViewer,
			Scopes: []string{httpx.ScopeAdmin}, Timeout: checkTimeout, Handler: h.checkLedger},
	}
}

type handlers struct{ l *Ledger }

// org is the principal's org; every Ledger read is scoped to it.
func org(r *http.Request) (ids.OrgID, error) {
	p, ok := httpx.PrincipalFrom(r.Context())
	if !ok || p.OrgID.IsZero() {
		return ids.OrgID{}, httpx.E(httpx.CodeAuthenticationRequired)
	}
	httpx.Annotate(r.Context(), p.OrgID.String(), "", "")
	return p.OrgID, nil
}

// ownAgent is the agent an agent key is limited to, or "" for people and admin keys.
func ownAgent(r *http.Request) string {
	if p, ok := httpx.PrincipalFrom(r.Context()); ok && p.Kind == httpx.KindAgentKey {
		return p.AgentID.String()
	}
	return ""
}

// apiError maps Ledger errors onto the envelope; anything else stays an internal error.
func apiError(err error) error {
	var pe *ParamError
	if errors.As(err, &pe) {
		return httpx.ErrValidation(pe.Param, pe.Message)
	}
	return err
}

// Not-found copy (S06 §5): the same sentence whether the id doesn't exist or belongs to another org.
func notFoundAction(id string) *httpx.Error {
	return httpx.E(httpx.CodeNotFound, httpx.Msgf("No action %s in this org. Check the id, or list recent actions: GET /v1/actions.", clipID(id)))
}

func notFoundDeed(id string) *httpx.Error {
	return httpx.E(httpx.CodeNotFound, httpx.Msgf("No deed %s in this org. Check the id, or list deeds: GET /v1/deeds.", clipID(id)))
}

func clipID(s string) string {
	if len(s) > 64 || strings.ContainsFunc(s, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return "with that id"
	}
	return s
}

func (h handlers) listActions(w http.ResponseWriter, r *http.Request) error {
	o, err := org(r)
	if err != nil {
		return err
	}
	f, p, err := ParseActionQuery(r.URL.Query())
	if err != nil {
		return apiError(err)
	}
	if own := ownAgent(r); own != "" {
		if f.Agent != "" && f.Agent != own {
			return httpx.ErrValidation("agent", "An agent key reads only its own agent's actions. Leave agent out, or pass "+own+".")
		}
		f.Agent = own
	}
	list, err := h.l.ListActions(r.Context(), o, f, p)
	if err != nil {
		return apiError(err)
	}
	return httpx.WriteJSON(w, http.StatusOK, list)
}

func (h handlers) getAction(w http.ResponseWriter, r *http.Request) error {
	o, err := org(r)
	if err != nil {
		return err
	}
	if len(r.URL.Query()) > 0 {
		return httpx.ErrValidation(firstKey(r), "This endpoint takes no query parameters.")
	}
	id := r.PathValue("id")
	d, err := h.l.GetAction(r.Context(), o, id)
	if errors.Is(err, ErrNotFound) || err == nil && ownAgent(r) != "" && d.Agent.ID != ownAgent(r) {
		return notFoundAction(id)
	}
	if err != nil {
		return err
	}
	httpx.Annotate(r.Context(), "", d.Agent.ID, d.ID)
	return httpx.WriteJSON(w, http.StatusOK, d)
}

func (h handlers) listDeeds(w http.ResponseWriter, r *http.Request) error {
	o, err := org(r)
	if err != nil {
		return err
	}
	f, p, err := ParseDeedQuery(r.URL.Query())
	if err != nil {
		return apiError(err)
	}
	f.OwnAgent = ownAgent(r)
	list, err := h.l.ListDeeds(r.Context(), o, f, p)
	if err != nil {
		return apiError(err)
	}
	return httpx.WriteJSON(w, http.StatusOK, list)
}

// COSE media type for GET /v1/deeds/{id} with Accept: application/cose.
const coseType = `application/cose; cose-type="cose-sign1"`

func (h handlers) getDeed(w http.ResponseWriter, r *http.Request) error {
	o, err := org(r)
	if err != nil {
		return err
	}
	if len(r.URL.Query()) > 0 {
		return httpx.ErrValidation(firstKey(r), "This endpoint takes no query parameters.")
	}
	id := r.PathValue("id")
	if ids.Check(ids.Deed, id) != nil {
		return notFoundDeed(id) // by id only; seqs are for the CLI
	}
	d, err := h.l.GetDeed(r.Context(), o, id)
	if errors.Is(err, ErrNotFound) || err == nil && ownAgent(r) != "" && (d.Agent != ownAgent(r) || d.Kind == "admin") {
		return notFoundDeed(id)
	}
	if err != nil {
		return err
	}
	hd := w.Header()
	etag := `"` + d.BodyHash + `"`
	hd.Set("ETag", etag)
	hd.Set("Cache-Control", "private, max-age=31536000, immutable")
	hd.Set("Vary", "Accept")
	if strings.Contains(r.Header.Get("Accept"), "application/cose") {
		if h.l.signer == nil {
			return httpx.E(httpx.CodeValidationFailed, httpx.Param("Accept"),
				httpx.Msg("COSE output is not enabled on this server. Request application/json."), withStatus(http.StatusNotAcceptable))
		}
		env, err := deed.ParseEnvelope(d.Envelope)
		if err != nil {
			return err
		}
		// iat is whole seconds: round up, so a key activated earlier in this second is valid at iat.
		at := h.l.now()
		if t := at.Truncate(time.Second); !t.Equal(at) {
			at = t.Add(time.Second)
		}
		b, err := deed.EncodeCOSE(env, h.l.signer, h.l.issuer, at)
		if err != nil {
			return err
		}
		hd.Set("Content-Type", coseType)
		hd.Set("Content-Length", strconv.Itoa(len(b)))
		w.WriteHeader(http.StatusOK)
		_, err = w.Write(b)
		return err
	}
	if inm := r.Header.Get("If-None-Match"); inm != "" && inm == etag {
		w.WriteHeader(http.StatusNotModified)
		return nil
	}
	hd.Set("Content-Type", httpx.ContentTypeJSON)
	hd.Set("Content-Length", strconv.Itoa(len(d.Envelope)))
	w.WriteHeader(http.StatusOK)
	_, err = w.Write(d.Envelope)
	return err
}

func withStatus(code int) httpx.Opt { return func(e *httpx.Error) { e.Status = code } }

// exportWriter sets the download headers once Export knows the range, before the first byte.
type exportWriter struct {
	w http.ResponseWriter
}

func (e exportWriter) ExportStarting(org string, first, last, _ int64) {
	h := e.w.Header()
	h.Set("Content-Type", "application/gzip")
	h.Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, ExportName(org, first, last)))
	h.Set("Cache-Control", "no-store")
}

func (e exportWriter) Write(p []byte) (int, error) { return e.w.Write(p) }

func (h handlers) exportDeeds(w http.ResponseWriter, r *http.Request) error {
	o, err := org(r)
	if err != nil {
		return err
	}
	rg, err := ParseRangeQuery(r.URL.Query())
	if err != nil {
		return apiError(err)
	}
	p, _ := httpx.PrincipalFrom(r.Context())
	h.l.log.InfoContext(r.Context(), "export requested", "org_id", o.String(), "request_id", httpx.RequestID(r.Context()),
		"principal", p.Ref(), "from_seq", rg.FromSeq, "to_seq", rg.ToSeq)
	_, err = h.l.Export(r.Context(), o, rg, exportWriter{w})
	var empty *EmptyRangeError
	if errors.As(err, &empty) {
		return httpx.E(httpx.CodeNotFound, httpx.Msg(emptyRangeMessage(empty)))
	}
	return apiError(err)
}

// emptyRangeMessage says where the chain ends, as a next step.
func emptyRangeMessage(e *EmptyRangeError) string {
	if e.Head == 0 {
		return "This org has no deeds yet. Deeds appear once actions are sealed."
	}
	return fmt.Sprintf("No deeds in that range. The chain ends at %s; ask for from_seq between 1 and %s.",
		deed.FormatCount(e.Head), deed.FormatCount(e.Head))
}

func (h handlers) checkLedger(w http.ResponseWriter, r *http.Request) error {
	o, err := org(r)
	if err != nil {
		return err
	}
	rg, err := ParseRangeQuery(r.URL.Query())
	if err != nil {
		return apiError(err)
	}
	res, err := h.l.CheckChain(r.Context(), o, rg, CheckOptions{})
	var empty *EmptyRangeError
	switch {
	case errors.As(err, &empty), errors.Is(err, ErrEmptyRange):
		head, _ := h.l.HeadSeq(r.Context(), o)
		return httpx.E(httpx.CodeNotFound, httpx.Msg(emptyRangeMessage(&EmptyRangeError{Head: head})))
	case errors.Is(err, ErrNotFound):
		return httpx.E(httpx.CodeNotFound, httpx.Msg("The deed before this range is missing, so the range can't be anchored. Check the whole chain: GET /v1/ledger/check."))
	case err != nil:
		return apiError(err)
	}
	return httpx.WriteJSON(w, http.StatusOK, res)
}

func firstKey(r *http.Request) string {
	for k := range r.URL.Query() {
		return k
	}
	return ""
}
