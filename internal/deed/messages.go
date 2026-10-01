package deed

import (
	"strconv"
	"strings"
)

// MessageTemplate is the copy for one verification code: one calm sentence and one next step, with
// {placeholders} (see RenderMessage). The table below is the only place this copy is written:
// `go run ./internal/deed/cmd/gencopy` writes it to testdata/deeds/messages.json and
// web/verify/src/messages.gen.ts, so the CLI and the browser verifier say exactly the same thing.
type MessageTemplate struct {
	Sentence string `json:"sentence"`
	Next     string `json:"next"`
}

// Messages maps a message key to its template. The key is the code, or "<code>.<variant>" when one
// code needs two sentences (MessageKey picks it).
//
// Placeholders: {Deed} "Deed 812" or "The deed" (seq unknown) · {deed} the same in lower case ·
// {seq} {prev} {want} {v} numbers · {field} {key} {issued_at} {org} {want_org} ("—" when empty) ·
// {detail} ("unreadable" when empty; for bundle_invalid a whole sentence) · {prev_anchor} seq − 1 ·
// {code} · {keys_url} (warnings only).
var Messages = map[string]MessageTemplate{
	string(CodeMalformed): {"{Deed} is not valid strict JSON ({detail}).",
		"Export it again from the issuer; do not edit deeds by hand."},
	string(CodeUnsupportedVersion): {"{Deed} uses deed format v{v}. This verifier reads v1.", "Update deedglass."},
	string(CodeSchemaInvalid):      {"{Deed} has an invalid field: {field}.", "Export it again from the issuer."},
	string(CodeIDMismatch):         {"{Deed}'s id does not match its body.", "Export it again from the issuer."},
	string(CodeBodyHashMismatch): {"{Deed} was changed after it was sealed.",
		"Get a fresh export from the issuer and compare {deed}."},
	string(CodeSealKeyMismatch): {"{Deed} names two different seal keys.", "Treat this deed as altered."},
	string(CodeSealKeyUnknown): {"{Deed} names seal key {key}, which is not in the key set.",
		"Fetch the issuer's keys: --keys <issuer>/.well-known/deedglass/keys.json"},
	string(CodeSealKeyRejected): {"The key set contains an unsafe key ({key}).",
		"Do not use this key set. Fetch it again from the issuer."},
	string(CodeSealKeyNotValidAt): {"{Deed} was sealed at {issued_at}, outside seal key {key}'s active window.",
		"Treat this deed as altered, or check you have the issuer's full key set."},
	string(CodeSealKeyRevoked): {"{Deed} was sealed by revoked key {key}.",
		"Ask the issuer which deeds they re-sealed after the revocation."},
	string(CodeSealInvalid): {"{Deed}'s seal does not match its contents.", "Treat this deed as altered."},
	string(CodeNonCanonical): {"{Deed} is not in canonical form (--strict).",
		"Re-export it; canonical bytes come straight from the Ledger."},
	string(CodeKeysMissing): {"Cannot check {deed}: no seal keys were given.",
		"Add --keys <file or https URL>. Issuers publish them at /.well-known/deedglass/keys.json"},
	string(CodeBundleInvalid): {"{detail} Nothing was extracted.",
		"Do not use this file. Ask the issuer for a new export."},
	string(CodeBundleSealInvalid): {"The bundle manifest's seal does not match the manifest.",
		"Treat this bundle as altered. Ask the issuer for a new export."},
	string(CodeChainGenesisMismatch): {"Deed 1 does not start this org's chain.",
		"Check the org id; the export may belong to another org."},
	string(CodeChainSeqGap): {"Deed {want} is missing. The chain jumps from {prev} to {seq}.",
		"A deed was removed or the export is incomplete. Ask the issuer for seq {prev}–{seq}."},
	string(CodeChainSeqGap) + ".start": {"The deeds start at deed {seq}, not deed 1.",
		"Verify the full export, or anchor it to the deed before: --anchor {prev_anchor}:<body_hash of deed {prev_anchor}>"},
	string(CodeChainSeqOutOfOrder): {"{Deed} appears out of order (after {prev}).", "Sort by seq, or export again."},
	string(CodeChainPrevHashMismatch): {"{Deed} does not link to deed {prev}.",
		"A deed before it was changed or replaced. Get a fresh export."},
	string(CodeChainOrgMismatch): {"{Deed} belongs to {org}, not {want_org}.", "Verify each org's deeds separately."},
	string(CodeChainTimeRegression): {"{Deed} was sealed before deed {prev}.",
		"Treat the chain as altered from {deed}."},
	"unknown": {"{Deed} failed verification ({code}).", "Export it again from the issuer."},

	// Warnings: the report is still valid, but the reader should know.
	"warning." + WarnTestKeys: {"These deeds were sealed by Deedglass's published test keys. They mean nothing outside tests.",
		"Verify deeds from a real issuer against its published keys."},
	"warning." + WarnKeysNotPinned: {"Seal keys came from the bundle itself. This shows nothing changed since export, not who sealed it.",
		"Pin the issuer's keys: --keys {keys_url}"},
	"warning." + WarnManifestUnsealed: {"The bundle manifest is not sealed.",
		"A sealed manifest is what shows no deeds were cut from the end. Ask the issuer for a sealed export."},
}

// WarningMessage renders a warning's copy.
func WarningMessage(code string, vars map[string]string) (sentence, next string) {
	t := Messages["warning."+code]
	return RenderMessage(t.Sentence, vars), RenderMessage(t.Next, vars)
}

// MessageKey picks the template for e: the code, a variant of it, or "unknown".
func MessageKey(e *VerifyError) string {
	if e.Code == CodeChainSeqGap && e.Prev == 0 && e.Want == 1 {
		return string(CodeChainSeqGap) + ".start"
	}
	if _, ok := Messages[string(e.Code)]; ok {
		return string(e.Code)
	}
	return "unknown"
}

// MessageVars are the placeholder values for e.
func MessageVars(e *VerifyError) map[string]string {
	ref := "The deed"
	if e.Seq > 0 {
		ref = "Deed " + strconv.FormatInt(e.Seq, 10)
	}
	detail := e.Detail
	switch {
	case detail != "":
	case e.Code == CodeBundleInvalid:
		detail = "This file is not a valid deed bundle."
	default:
		detail = "unreadable"
	}
	return map[string]string{
		"Deed": ref, "deed": strings.ToLower(ref[:1]) + ref[1:],
		"seq": strconv.FormatInt(e.Seq, 10), "prev": strconv.FormatInt(e.Prev, 10), "want": strconv.FormatInt(e.Want, 10),
		"prev_anchor": strconv.FormatInt(max(e.Seq-1, 0), 10), "v": strconv.FormatInt(e.Version, 10),
		"field": orDash(e.Field), "key": orDash(e.Key), "issued_at": orDash(e.IssuedAt),
		"org": orDash(e.Org), "want_org": orDash(e.WantOrg), "detail": detail, "code": string(e.Code),
		"keys_url": "<issuer>/.well-known/deedglass/keys.json",
	}
}

// RenderMessage replaces every {name} in tmpl with vars[name]. An unknown name is left as it is
// (a test keeps every template's names known).
func RenderMessage(tmpl string, vars map[string]string) string {
	var b strings.Builder
	for {
		i := strings.IndexByte(tmpl, '{')
		if i < 0 {
			b.WriteString(tmpl)
			return b.String()
		}
		j := strings.IndexByte(tmpl[i:], '}')
		if j < 0 {
			b.WriteString(tmpl)
			return b.String()
		}
		name := tmpl[i+1 : i+j]
		b.WriteString(tmpl[:i])
		if v, ok := vars[name]; ok {
			b.WriteString(v)
		} else {
			b.WriteString(tmpl[i : i+j+1])
		}
		tmpl = tmpl[i+j+1:]
	}
}

// Message returns the one sentence and the one next step a person sees for e. The CLI and the
// browser verifier show exactly these (S04 §5); a test keeps every Code covered and the copy calm
// (no exclamation marks, sentence case, a full stop at the end).
func Message(e *VerifyError) (sentence, next string) {
	t := Messages[MessageKey(e)]
	v := MessageVars(e)
	return RenderMessage(t.Sentence, v), RenderMessage(t.Next, v)
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}
