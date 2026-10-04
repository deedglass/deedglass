package gmail

import (
	"encoding/json"
	"fmt"

	"github.com/deedglass/deedglass/internal/connector"
)

// Tool names (unqualified). None of them sends.
const (
	ToolCreateDraft = "create_draft"
	ToolUpdateDraft = "update_draft"
	ToolListDrafts  = "list_drafts"
	ToolGetDraft    = "get_draft"
	ToolGetThread   = "get_thread"
)

// L1 sentences.
const (
	L1CreateDraft = "The saved draft has the same recipients, subject and body."
	L1UpdateDraft = "The updated draft has the same recipients, subject and body."
)

const patDraftID = `^[A-Za-z0-9_-]{1,128}$`

func toolSpecs(maxRecipients int) []connector.ToolSpec {
	addrs := func(desc string, min int) string {
		if min > 0 {
			desc += fmt.Sprintf(" At least %d.", min)
		}
		return fmt.Sprintf(`{"type":"array","items":{"type":"string","maxLength":320},"description":%s}`, js(desc+fmt.Sprintf(" At most %d recipients in all.", maxRecipients)))
	}
	draftProps := `"to":` + addrs("Recipients, like \"Priya <priya@acme.com>\". Each entry is one address.", 1) +
		`,"cc":` + addrs("Cc recipients.", 0) +
		`,"bcc":` + addrs("Bcc recipients (refused unless the connector allows Bcc).", 0) +
		`,"subject":{"type":"string","maxLength":250,"description":"One line, no line breaks."}` +
		`,"body_text":{"type":"string","maxLength":262144,"description":"The plain-text body."}` +
		`,"body_html":{"type":"string","maxLength":262144,"description":"An optional HTML body. Stored as is; Deedglass never renders it."}` +
		`,"thread_id":{"type":"string","pattern":` + js(patDraftID) + `,"description":"Put the draft in this thread."}` +
		`,"in_reply_to":{"type":"string","maxLength":998,"description":"The Message-ID this replies to, like <abc@mail.gmail.com>."}`
	id := `"draft_id":{"type":"string","pattern":` + js(patDraftID) + `}`
	return []connector.ToolSpec{
		{Name: ToolCreateDraft, Description: "Save a draft in the mailbox. It doesn't send: a person reviews and sends it from Gmail.",
			InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["to","subject","body_text"],"properties":{` + draftProps + `}}`),
			SideEffect:  connector.SideEffectWrite, L1: L1CreateDraft},
		{Name: ToolUpdateDraft, Description: "Replace a draft's recipients, subject and body. It doesn't send.",
			InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["draft_id","to","subject","body_text"],"properties":{` + id + `,` + draftProps + `}}`),
			SideEffect:  connector.SideEffectWrite, Idempotent: true, L1: L1UpdateDraft},
		{Name: ToolListDrafts, Description: "List the mailbox's draft ids (at most 25).",
			InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{"limit":{"type":"integer","minimum":1,"maximum":25}}}`),
			SideEffect:  connector.SideEffectRead, Idempotent: true},
		{Name: ToolGetDraft, Description: "Read a draft: to, cc, subject, the plain-text body (up to 64 KiB) and the thread id.",
			InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["draft_id"],"properties":{` + id + `}}`),
			SideEffect:  connector.SideEffectRead, Idempotent: true},
		{Name: ToolGetThread, Description: "Read a thread: up to 20 messages, each body up to 16 KiB. Needs the gmail.readonly scope.",
			InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["thread_id"],"properties":{"thread_id":{"type":"string","pattern":` + js(patDraftID) + `}}}`),
			SideEffect:  connector.SideEffectRead, Idempotent: true, RequiresScope: "gmail.readonly"},
	}
}

func js(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
