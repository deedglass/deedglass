package connector

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"runtime/debug"
	"unicode/utf8"

	"github.com/deedglass/deedglass/internal/vault"
)

// L1Finding is one fact a native read-back compared (S29): what the call implies, and what the
// service shows. Long values are digests ("sha256:…", FindingValue); short ones are kept as is.
type L1Finding struct {
	Field    string // "title", "body", "marker", "blob_sha", "metadata.action_id"
	Expected any
	Observed any
	Match    bool
	// Code names the Witness catalog reason this finding becomes when it isn't the plain field
	// comparison (HC-P02: fhir_conditional_matched, fhir_no_new_version). The catalog, not the
	// connector, gives it its class. Empty for a plain comparison.
	Code string
}

// NativeReadBacker is a connector type that reads its own writes back and compares them in code
// (S29: GitHub's blob SHA and the issue marker, Slack's message metadata), where S19's expect
// templates and CEL can't. Only tools whose ToolSpec.L1 is set are read back this way.
type NativeReadBacker interface {
	// NativeReadBack reads call's write back. It returns the observation (Target names what was
	// read, like "issue #42 on GitHub") and the findings. Errors: a KindNotFound *Error is the
	// gap readback_not_found (the Witness tries again), *ErrReadBackSkipped the note
	// readback_skipped, any other error the gap readback_failed (a rate limit included: never a
	// mismatch). A read-back never writes.
	NativeReadBack(ctx context.Context, inst *Instance, call Call, res Result) (Observation, []L1Finding, error)
}

// NativeReadBacking is an Executor whose connector reads its writes back natively.
type NativeReadBacking interface {
	CanNativeReadBack(t ToolSpec) bool
	NativeReadBack(ctx context.Context, call Call, res Result) (Observation, []L1Finding, error)
}

// UnknownReadBacker is a NativeReadBacker that also looks for a write after an unknown outcome
// (HC-P02: a FHIR server's history since the call, for the action's token).
type UnknownReadBacker interface {
	CanReadBackUnknown(t ToolSpec) bool
}

// CanNativeReadBackUnknown reports whether the bound type looks for t's write after an unknown
// outcome.
func (b *bound) CanNativeReadBackUnknown(t ToolSpec) bool {
	u, ok := b.impl.(UnknownReadBacker)
	return ok && b.CanNativeReadBack(t) && u.CanReadBackUnknown(t)
}

// CanNativeReadBack reports whether the bound type reads t back natively.
func (b *bound) CanNativeReadBack(t ToolSpec) bool {
	_, ok := b.impl.(NativeReadBacker)
	return ok && t.L1 != "" && t.SideEffect != SideEffectRead
}

// NativeReadBack runs the type's native read-back the way ReadBack runs a read tool: the per-call
// redactor covers the credential, a panic is recovered, and every string is redacted.
func (b *bound) NativeReadBack(ctx context.Context, call Call, res Result) (obs Observation, fs []L1Finding, err error) {
	nr, ok := b.impl.(NativeReadBacker)
	if !ok {
		return Observation{}, nil, &ErrReadBackSkipped{Why: "the connector type " + b.impl.Type() + " can't read back"}
	}
	if !call.Deadline.IsZero() {
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(ctx, call.Deadline)
		defer cancel()
	}
	red := vault.NewRedactor()
	ctx = vault.WithRedactor(ctx, red)
	red = vault.RedactorFrom(ctx)
	defer func() {
		if p := recover(); p != nil {
			b.r.log.ErrorContext(ctx, "connector: a native read-back panicked", "connector", b.inst.Name, "type", b.impl.Type(),
				"action_id", call.ActionID.String(), "panic", fmt.Sprint(p), "stack", string(debug.Stack()))
			obs, fs, err = Observation{}, nil, &Error{Kind: KindUpstream, Code: CodeUpstreamError, AgentMessage: "The read-back stopped unexpectedly."}
		}
		clean := func(v any) any {
			if s, ok := v.(string); ok {
				return vault.Scrub(red.Redact(s))
			}
			return v
		}
		for i := range fs {
			fs[i].Expected, fs[i].Observed = clean(fs[i].Expected), clean(fs[i].Observed)
		}
		obs.Target = vault.Scrub(red.Redact(obs.Target))
		obs.Body = nil // a native read-back keeps findings, never the service's body
		if ce := AsError(err); ce != nil {
			ce.AgentMessage = vault.Scrub(red.Redact(ce.AgentMessage))
			ce.AdminDetail = vault.Scrub(red.Redact(ce.AdminDetail))
		}
	}()
	return nr.NativeReadBack(ctx, b.inst, call, res)
}

// MaxFindingRunes is the longest string a finding keeps as is; longer ones become digests.
const MaxFindingRunes = 256

// FindingValue is how a finding shows a string: as is up to MaxFindingRunes characters, else
// "sha256:<hex>" of its UTF-8 bytes (so a mismatch of two long bodies shows two digests).
func FindingValue(s string) any {
	if utf8.RuneCountInString(s) <= MaxFindingRunes {
		return s
	}
	sum := sha256.Sum256([]byte(s))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// Compare is a finding for one field: equal when both sides marshal to the same JSON.
func Compare(field string, expected, observed any) L1Finding {
	if s, ok := expected.(string); ok {
		expected = FindingValue(s)
	}
	if s, ok := observed.(string); ok {
		observed = FindingValue(s)
	}
	eb, _ := json.Marshal(expected)
	ob, _ := json.Marshal(observed)
	return L1Finding{Field: field, Expected: expected, Observed: observed, Match: string(eb) == string(ob)}
}

// SummaryStyle is how one part of a summary sentence is set.
type SummaryStyle string

const (
	SummaryPlain  SummaryStyle = ""
	SummaryStrong SummaryStyle = "strong" // a name: the agent, a repository, a channel
	SummaryCode   SummaryStyle = "code"   // an identifier: a branch, a path, a tool
)

// SummaryPart is one run of a summary sentence.
type SummaryPart struct {
	Text  string
	Style SummaryStyle
}

// Summary is one sentence Deedglass writes about a call from the tool and its enrich facts, never
// from free agent text: "support-bot wants to open a pull request in arjun/deedglass-sandbox from
// agent/fix-typo into main." Renderers escape every part (Slack mrkdwn, HTML, plain text).
type Summary []SummaryPart

// Plain is the sentence without styling.
func (s Summary) Plain() string {
	out := ""
	for _, p := range s {
		out += p.Text
	}
	return out
}

// SummaryInput is what a Summarizer sees: the agent's name, the tool, the arguments as the Gate
// normalized them, and the enrich facts the policy saw (request.github, request.slack).
type SummaryInput struct {
	Agent     string
	Qualified string // "github.create_pr"
	Tool      ToolSpec
	Args      json.RawMessage
	Facts     map[string]any
}

// Summarizer is a connector type that can say in one sentence what a call will do (S29: the
// Slack approval card; S22's email and S25's Console may reuse it). A nil or empty answer falls
// back to "<agent> wants to run <tool>."
type Summarizer interface {
	Summarize(in SummaryInput) Summary
}

// FallbackSummary is "<agent> wants to run <tool>.".
func FallbackSummary(agent, qualified string) Summary {
	return Summary{{Text: agent, Style: SummaryStrong}, {Text: " wants to run "}, {Text: qualified, Style: SummaryCode}, {Text: "."}}
}
