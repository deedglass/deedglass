package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/deedglass/deedglass/internal/cli/ui"
	"github.com/deedglass/deedglass/internal/localapi"
	"github.com/deedglass/deedglass/internal/setup"
)

// deedglass app connect: the Connect screen from the terminal (DL-143 CLI parity). With no
// argument it lists the tools and their status; with one it starts connecting that tool (the
// same POST /v1/connect/{id}/start the screen sends) and prints how to finish.
func runAppConnect(ctx context.Context, e *Env, args []string) error {
	if len(args) > 1 {
		return usageErr(fmt.Errorf("deedglass app connect takes at most one tool (got %d)", len(args)), "For example: deedglass app connect cursor")
	}
	if len(args) == 1 && !slices.Contains(localapi.Clients, args[0]) {
		return usageErr(fmt.Errorf("deedglass app connect takes %s (got %q)", strings.Join(localapi.Clients, ", "), args[0]), "Run deedglass app connect to see them.")
	}
	// With no app running and no server or key named, say so plainly: the admin-key error that
	// apiClient would give is for a server elsewhere, not for someone who hasn't started the app.
	if t := resolveServer(e); t.From == fromDefault && t.keyFor(e, setup.AdminKeyFile) == "" {
		inv := invocation(e)
		return &ExitError{Code: ExitNotAuthed, ErrCode: "authentication_required",
			Err:  errors.New("Deedglass isn't running on this computer"),
			Next: "Run " + inv + " app to start it, then run " + inv + " app connect again. For a server elsewhere, pass --server and --api-key."}
	}
	c, err := apiClient(e)
	if err != nil {
		return err
	}
	var raw json.RawMessage
	if len(args) == 0 {
		_, err = c.Do(ctx, "GET", "/v1/connect", nil, &raw)
	} else {
		_, err = c.Do(ctx, "POST", "/v1/connect/"+args[0]+"/start", map[string]any{}, &raw)
	}
	if err != nil {
		return apiErr(e, c, err)
	}
	if e.Globals.JSON {
		_, err := e.Stdout.Write(append(raw, '\n'))
		return err
	}
	s := e.Style
	word := map[string]string{localapi.NotConnected: "not connected", localapi.Waiting: "waiting for its first action", localapi.Connected: "connected"}
	if len(args) == 0 {
		var l struct {
			Data []localapi.Item `json:"data"`
		}
		if err := json.Unmarshal(raw, &l); err != nil {
			return err
		}
		rows := make([]ui.Row, 0, len(l.Data))
		for _, it := range l.Data {
			g := ui.Bullet
			switch it.Status {
			case localapi.Connected:
				g = ui.OK
			case localapi.Waiting:
				g = ui.Arrow
			}
			rows = append(rows, ui.Row{Glyph: g, Key: it.Name, Value: word[it.Status] + "\n" + invocation(e) + " app connect " + it.ID})
		}
		ui.Lines(e.Stdout, s.KV(ui.Indent, e.Width, rows...)...)
		return nil
	}
	var it localapi.Item
	if err := json.Unmarshal(raw, &it); err != nil {
		return err
	}
	fmt.Fprintln(e.Stdout, s.Bold(it.Name)+"  "+s.Dim(word[it.Status]))
	show := func(label string, v *string) {
		if v != nil && *v != "" {
			fmt.Fprintln(e.Stdout, "\n"+label)
			fmt.Fprintln(e.Stdout, "  "+strings.ReplaceAll(strings.TrimRight(*v, "\n"), "\n", "\n  "))
		}
	}
	show("Open this link", it.Link)
	show("Or this page", it.WebLink)
	show("Or run", it.Command)
	show("Or paste this config", it.Config)
	return nil
}
