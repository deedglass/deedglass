package setup

import (
	"context"
	"time"

	"github.com/deedglass/deedglass/pkg/deedglass"
)

// ProgressSteps is how many steps the quickstart has (PRD-06): init, doctor, init --trigger, the
// undo of what it caught, and a person countersigning that undo.
const ProgressSteps = 5

// Facts are what the quickstart step line reads. Each comes from this directory's files or its local
// Ledger, never from counting commands: there are no points.
type Facts struct {
	SetUp    bool   // 1 · .deedglass/state.json names an org and an agent, and stack.json exists
	Checked  bool   // 2 · doctor just passed every check (only doctor knows; a later step implies it)
	Mismatch string // 3 · the newest action checked Doesn't match (init --trigger), "" when none
	Undo     bool   // 4 · an undo was asked for (deedglass undo run)
	Undone   bool   // 5 · an undo ran: a person countersigned it
	Server   string // the local server init started
}

// Progress is the step to do now, 1 to ProgressSteps, and its command: the step after the furthest
// one done. Step 0 means all five are done.
func (f Facts) Progress() (step int, next string) {
	switch {
	case f.Undone:
		return 0, ""
	case f.Undo:
		return 5, "countersign the undo at " + f.Server + "/app/approvals (the sign-in link is in .deedglass/serve.log)"
	case f.Mismatch != "":
		return 4, "deedglass undo preview " + f.Mismatch + " (it prints the undo run line)"
	case f.Checked:
		return 3, "deedglass init --trigger"
	case f.SetUp:
		return 2, "deedglass doctor"
	}
	return 1, "deedglass init"
}

// ReadFacts reads dir's facts: init's files, then the local Ledger through the admin key init saved
// (two reads from the local server, 3 s at most). What can't be read stays unset.
func ReadFacts(ctx context.Context, dir string) Facts {
	var f Facts
	st, _ := ReadState(dir)
	stack, _ := ReadStack(dir)
	if st.Org == "" || st.Agent == "" || stack == nil || stack.Listen == "" {
		return f
	}
	f.SetUp, f.Server = true, stack.Server()
	key, _ := ReadSecretFile(dir, AdminKeyFile)
	if key == "" {
		return f
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	c := &deedglass.Client{BaseURL: f.Server, APIKey: key, UserAgent: "deedglass-init"}
	var page struct {
		Data []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"data"`
	}
	if _, err := c.Do(ctx, "GET", "/v1/actions?limit=1&verdict=MISMATCH", nil, &page); err == nil && len(page.Data) > 0 {
		f.Mismatch = page.Data[0].ID
	}
	page.Data = nil
	if _, err := c.Do(ctx, "GET", "/v1/actions?limit=20&tool=%2A.undo", nil, &page); err == nil {
		for _, a := range page.Data {
			f.Undo = true
			f.Undone = f.Undone || a.Status == "executed"
		}
	}
	return f
}
