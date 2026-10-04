package learn

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/deedglass/deedglass/internal/httpx"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/policy"
)

// Accept, reject and the sweeper (S28 §3.6). Accepting a suggestion creates a policy version
// (origin suggestion) and runs it in shadow, next to the enforced one. Nothing is enforced: the
// PolicyWriter this package is given can add a version and start a shadow, and nothing else.
//
// Accept is a saga of idempotent steps, so a crash between any two is repaired by running it again
// (the sweeper does, as the person who started it):
//
//	1. CAS open → accepting                    (suggestion_decided otherwise)
//	2. the enforced version moved on           → superseded, suggestion_stale
//	3. a shadow runs and replace_shadow is off → back to open, shadow_slot_taken
//	4. AddSuggestedVersion (identical source → the existing version)
//	5. ActivateShadow                          (S12 seals policy.activated, mode shadow)
//	6. one org transaction: accepting → accepted + suggestion.accepted

// AcceptRequest is POST /v1/suggestions/{id}/accept's body.
type AcceptRequest struct {
	Reason        string `json:"reason"`
	ReplaceShadow bool   `json:"replace_shadow"`
}

// RejectRequest is POST /v1/suggestions/{id}/reject's body.
type RejectRequest struct {
	Reason string `json:"reason"`
}

// AcceptDone is what step 6 records.
type AcceptDone struct {
	By                      Actor
	VersionID               string
	Version                 int
	VersionHash             string
	ReplacedShadowVersionID string
	Reason                  string
	RequestID               string
	At                      time.Time
}

// StuckAfter is how long a suggestion may sit in accepting before the sweeper finishes it.
const StuckAfter = 2 * time.Minute

// MaxAcceptReason is the longest accept reason.
const MaxAcceptReason = 280

func suggestionNotFound(id string) error {
	return httpx.E(httpx.CodeNotFound, httpx.Msgf("No suggestion %s in this org. List them: GET /v1/suggestions.", clipID(id)))
}

// List is GET /v1/suggestions.
func (s *SuggestionService) List(ctx context.Context, org ids.OrgID, f SuggestionFilter, limit int, cursor string) (*List[Suggestion], error) {
	data, next, err := s.Store.ListSuggestions(ctx, org, f, limit, cursor)
	if err != nil {
		return nil, err
	}
	l := &List[Suggestion]{Object: "list", Data: data, HasMore: next != ""}
	if l.Data == nil {
		l.Data = []Suggestion{}
	}
	if next != "" {
		l.NextCursor = &next
	}
	return l, nil
}

// List is a cursor page.
type List[T any] struct {
	Object     string  `json:"object"`
	Data       []T     `json:"data"`
	HasMore    bool    `json:"has_more"`
	NextCursor *string `json:"next_cursor"`
}

// Get is GET /v1/suggestions/{id}.
func (s *SuggestionService) Get(ctx context.Context, org ids.OrgID, id string) (*Suggestion, error) {
	if _, err := ids.Parse[ids.SuggestionKind](id); err != nil {
		return nil, suggestionNotFound(id)
	}
	r, err := s.Store.GetSuggestion(ctx, org, id)
	if errors.Is(err, ErrNotFound) {
		return nil, suggestionNotFound(id)
	}
	if err != nil {
		return nil, err
	}
	return &r.Suggestion, nil
}

// Generate is POST /v1/suggestions/generate.
func (s *SuggestionService) Generate(ctx context.Context, org ids.OrgID, by Actor) (*LearnRun, error) {
	// A manual pass gets its own learn_runs row: the time and a random suffix (≤ 32 characters).
	period := "m-" + s.now().Format("20060102T150405.000000Z") + "-" + randomSuffix()
	if ok, err := s.Store.ClaimRun(ctx, org, "suggest", period, s.now()); err != nil {
		return nil, err
	} else if !ok {
		return nil, httpx.E(httpx.CodeConflict, httpx.Msg(MsgSuggestNotNow))
	}
	run, err := s.Suggester.Generate(ctx, org, by)
	if errors.Is(err, ErrSuggestBusy) {
		_ = s.Store.FinishRun(ctx, org, "suggest", period, false, nil, "busy", s.now())
		return nil, httpx.E(httpx.CodeConflict, httpx.Msg(MsgSuggestNotNow))
	}
	if err != nil {
		_ = s.Store.FinishRun(context.WithoutCancel(ctx), org, "suggest", period, false, nil, err.Error(), s.now())
		return nil, err
	}
	run.Period = period
	_ = s.Store.FinishRun(ctx, org, "suggest", period, true, runStats(run), "", s.now())
	return run, nil
}

func randomSuffix() string {
	const alphabet = "0123456789abcdefghjkmnpqrstvwxyz"
	var b [5]byte
	_, _ = rand.Read(b[:])
	for i := range b {
		b[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return string(b[:])
}

func runStats(r *LearnRun) map[string]any {
	reasons := map[string]int{}
	for _, d := range r.Dropped {
		reasons[d.Generator]++
	}
	return map[string]any{"created": len(r.Created), "superseded": r.Superseded, "unchanged": r.Unchanged, "waiting": r.Waiting,
		"dropped": len(r.Dropped), "dropped_by_generator": reasons, "actions": r.Actions}
}

// Accept runs the saga for a person or an org admin key.
func (s *SuggestionService) Accept(ctx context.Context, org ids.OrgID, by Actor, id string, req AcceptRequest, requestID string) (*Suggestion, error) {
	if s.Writer == nil {
		return nil, httpx.E(httpx.CodeServiceUnavailable)
	}
	if _, err := ids.Parse[ids.SuggestionKind](id); err != nil {
		return nil, suggestionNotFound(id)
	}
	reason := trimReason(req.Reason)
	if utf8.RuneCountInString(reason) > MaxAcceptReason || strings.ContainsAny(reason, "\n\r") {
		return nil, httpx.ErrValidation("reason", MsgAcceptReasonLong)
	}
	cur, err := s.Store.GetSuggestion(ctx, org, id)
	if errors.Is(err, ErrNotFound) {
		return nil, suggestionNotFound(id)
	}
	if err != nil {
		return nil, err
	}
	if cur.Kind == KindAdvisory {
		return nil, httpx.E(httpx.CodeConflict, httpx.Msg(MsgAcceptAdvisory))
	}
	// 1. CAS open → accepting.
	row, err := s.Store.BeginAccept(ctx, org, id, by, reason, req.ReplaceShadow, requestID)
	switch {
	case errors.Is(err, ErrNotFound):
		return nil, suggestionNotFound(id)
	case errors.Is(err, ErrStateConflict):
		return nil, s.decided(ctx, org, id)
	case err != nil:
		return nil, err
	}
	// 2. The base must still be the enforced version.
	enf, _, err := s.Store.EnforcedSource(ctx, org)
	if err != nil {
		return nil, s.reopenAfter(ctx, org, id, err)
	}
	if enf == nil || enf.VersionID != row.Policy.BaseVersionID {
		if err := s.Store.SupersedeOne(ctx, org, id); err != nil {
			return nil, err
		}
		now := 0
		if enf != nil {
			now = enf.Version
		}
		return nil, httpx.E(httpx.CodeSuggestionStale, httpx.Msg(SuggestionStaleMessage(row.Policy.Name, now, row.Policy.BaseVersion)))
	}
	// 3. One shadow slot per org.
	sh, _, err := s.Store.LiveShadowActivation(ctx, org)
	if err != nil {
		return nil, s.reopenAfter(ctx, org, id, err)
	}
	if sh != nil && !req.ReplaceShadow {
		if err := s.Store.ReopenSuggestion(ctx, org, id); err != nil {
			return nil, err
		}
		return nil, httpx.E(httpx.CodeShadowSlotTaken, httpx.Param("replace_shadow"), httpx.Msg(ShadowSlotTakenMessage(sh.Name, sh.Version)))
	}
	out, err := s.finish(ctx, org, row)
	if err != nil {
		var pe *policy.Error
		if errors.As(err, &pe) {
			// The policy service refused (a tampered stored version, a conflict): nothing was
			// accepted, so the suggestion goes back to open with the reason.
			return nil, s.reopenAfter(ctx, org, id, err)
		}
		return nil, err // left in accepting: the sweeper finishes it
	}
	return out, nil
}

// reopenAfter puts an accepting suggestion back to open and returns err.
func (s *SuggestionService) reopenAfter(ctx context.Context, org ids.OrgID, id string, err error) error {
	if rerr := s.Store.ReopenSuggestion(context.WithoutCancel(ctx), org, id); rerr != nil {
		return errors.Join(err, rerr)
	}
	return err
}

// finish runs steps 4–6 for a suggestion in accepting, as the person who started it.
func (s *SuggestionService) finish(ctx context.Context, org ids.OrgID, row *SuggestionRow) (*Suggestion, error) {
	by := Actor{Type: row.DecidedByType, ID: row.DecidedByID}
	p := Principal{Org: org, Actor: by, RequestID: row.RequestID}
	// 4. The version (identical content gives back the existing one).
	v, created, err := s.Writer.AddSuggestedVersion(ctx, p, row.Policy.ID, row.CandidateSource, row.ID, "Created from Deedglass Insights suggestion "+row.ID+".")
	if err != nil {
		return nil, err
	}
	// 5. The shadow, unless a previous attempt already started it.
	sh, shDeed, err := s.Store.LiveShadowActivation(ctx, org)
	if err != nil {
		return nil, err
	}
	var deeds []string
	var replaced *int
	replacedID := ""
	if sh != nil && sh.VersionID == v.ID {
		if shDeed != "" {
			deeds = append(deeds, shDeed)
		}
	} else {
		if sh != nil {
			n := sh.Version
			replaced, replacedID = &n, sh.VersionID
		}
		a, err := s.Writer.ActivateShadow(ctx, p, row.Policy.ID, v.Version, activationReason(row.ID, row.AcceptReason))
		if err != nil {
			return nil, err
		}
		deeds = append(deeds, a.Deeds...)
	}
	// 6. Accepted, with its own deed.
	d, err := s.Store.CompleteAccept(ctx, org, row.ID, AcceptDone{By: by, VersionID: v.ID, Version: v.Version, VersionHash: v.VersionHash,
		ReplacedShadowVersionID: replacedID, Reason: row.AcceptReason, RequestID: row.RequestID, At: s.now()})
	if errors.Is(err, ErrStateConflict) {
		return nil, s.decided(ctx, org, row.ID) // the sweeper finished it meanwhile
	}
	if err != nil {
		return nil, err
	}
	deeds = append(deeds, d)
	out, err := s.Store.GetSuggestion(ctx, org, row.ID)
	if err != nil {
		return nil, err
	}
	sug := out.Suggestion
	sug.Deeds, sug.ReplacedShadow = deeds, replaced
	if !created {
		n := fmt.Sprintf("This change already existed as %s v%d, so that version runs in shadow; no new version was created.", row.Policy.Name, v.Version)
		sug.Note = &n
	}
	return &sug, nil
}

// activationReason is S12's reason (3–280 characters): the suggestion and the person's reason.
func activationReason(id, reason string) string {
	r := "Accepted suggestion " + id
	if reason == "" {
		return r + "."
	}
	r += ": " + reason
	if utf8.RuneCountInString(r) > 280 {
		r = string([]rune(r)[:279]) + "…"
	}
	return r
}

// decided is suggestion_decided with who and when.
func (s *SuggestionService) decided(ctx context.Context, org ids.OrgID, id string) error {
	r, err := s.Store.GetSuggestion(ctx, org, id)
	if err != nil {
		return httpx.E(httpx.CodeSuggestionDecided)
	}
	who := ""
	if r.DecidedBy != nil {
		who, _ = s.Store.PersonName(ctx, org, *r.DecidedBy)
	}
	return httpx.E(httpx.CodeSuggestionDecided, httpx.Msg(SuggestionDecidedMessage(&r.Suggestion, who)))
}

// Reject closes an open suggestion with a reason and seals suggestion.rejected.
func (s *SuggestionService) Reject(ctx context.Context, org ids.OrgID, by Actor, id string, req RejectRequest, requestID string) (*Suggestion, error) {
	if err := checkReason(req.Reason, MsgRejectReason); err != nil {
		return nil, err
	}
	if _, err := ids.Parse[ids.SuggestionKind](id); err != nil {
		return nil, suggestionNotFound(id)
	}
	d, err := s.Store.RejectSuggestion(ctx, org, id, by, trimReason(req.Reason), requestID, s.now())
	switch {
	case errors.Is(err, ErrNotFound):
		return nil, suggestionNotFound(id)
	case errors.Is(err, ErrStateConflict):
		return nil, s.decided(ctx, org, id)
	case err != nil:
		return nil, err
	}
	r, err := s.Store.GetSuggestion(ctx, org, id)
	if err != nil {
		return nil, err
	}
	sug := r.Suggestion
	sug.Deeds = []string{d}
	return &sug, nil
}

// Sweep finishes every suggestion of org stuck in accepting for longer than StuckAfter (a crash
// between the saga's steps). Each step is idempotent, so a finished one never seals twice.
func (s *SuggestionService) Sweep(ctx context.Context, org ids.OrgID) (int, error) {
	if s.Writer == nil {
		return 0, nil
	}
	stuck, err := s.Store.StuckAccepting(ctx, org, s.now().Add(-StuckAfter))
	if err != nil {
		return 0, err
	}
	n := 0
	for _, id := range stuck {
		row, err := s.Store.GetSuggestion(ctx, org, id)
		if err != nil {
			return n, err
		}
		if row.Status != SugAccepting {
			continue
		}
		enf, _, err := s.Store.EnforcedSource(ctx, org)
		if err != nil {
			return n, err
		}
		if enf == nil || enf.VersionID != row.Policy.BaseVersionID {
			if err := s.Store.SupersedeOne(ctx, org, id); err != nil {
				return n, err
			}
			continue
		}
		if _, err := s.finish(ctx, org, row); err != nil {
			var he *httpx.Error
			if errors.As(err, &he) && he.Code == httpx.CodeSuggestionDecided {
				continue
			}
			var pe *policy.Error
			if errors.As(err, &pe) {
				_ = s.Store.ReopenSuggestion(ctx, org, id)
				continue
			}
			return n, err
		}
		n++
	}
	return n, nil
}
