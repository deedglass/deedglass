package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/url"
	"strings"

	"github.com/deedglass/deedglass/cmd/deedglass/internal/ui"
	"github.com/deedglass/deedglass/internal/notify/webhook"
)

// deedglass webhooks create|list|test|delete (S22 §5.1), through the API with an org admin key.

var webhookCreateValues struct{ url, events, description string }

func webhookCreateFlags(fs *flag.FlagSet) {
	webhookCreateValues = struct{ url, events, description string }{}
	fs.StringVar(&webhookCreateValues.url, "url", "", "the endpoint (https; http://localhost in dev)")
	fs.StringVar(&webhookCreateValues.events, "events", "", "comma-separated event types, for example approval.requested,approval.decided")
	fs.StringVar(&webhookCreateValues.description, "description", "", "a note for people, at most 200 characters")
}

func runWebhooksCreate(ctx context.Context, e *Env, args []string) error {
	if len(args) > 0 {
		return usageErr(fmt.Errorf("deedglass webhooks create takes flags only (got %q)", args[0]),
			"deedglass webhooks create --url https://hooks.acme.test/deedglass --events approval.requested,approval.decided")
	}
	v := webhookCreateValues
	if strings.TrimSpace(v.url) == "" || strings.TrimSpace(v.events) == "" {
		return usageErr(fmt.Errorf("--url and --events are required"),
			"deedglass webhooks create --url https://hooks.acme.test/deedglass --events approval.requested,approval.decided")
	}
	var events []string
	for _, ev := range strings.Split(v.events, ",") {
		if ev = strings.TrimSpace(ev); ev != "" {
			events = append(events, ev)
		}
	}
	c, err := apiClient(e)
	if err != nil {
		return err
	}
	var w webhook.Webhook
	if _, err := c.Do(ctx, "POST", "/v1/webhooks", webhook.CreateInput{URL: v.url, Events: events, Description: v.description}, &w); err != nil {
		return apiErr(e, c, err)
	}
	if e.Globals.JSON {
		return writeJSON(e, w)
	}
	s := e.Style
	fmt.Fprintln(e.Stdout, s.OKLine(fmt.Sprintf("Webhook created %s %s %s %d %s", ui.Sep, shortID(w.ID), ui.Sep, len(w.Events), plural(len(w.Events), "event", "events"))))
	if w.Secret != nil {
		fmt.Fprintf(e.Stdout, "  Signing secret (shown once): %s\n", *w.Secret)
		fmt.Fprintln(e.Stdout, "  Store it now. Deedglass keeps only an encrypted copy and can't show it again.")
	}
	if w.Deed != nil {
		fmt.Fprintln(e.Stdout, s.OKLine(fmt.Sprintf("Deed sealed %s webhook.created %s %s", ui.Sep, ui.Sep, shortID(*w.Deed))))
	}
	return nil
}

func runWebhooksList(ctx context.Context, e *Env, args []string) error {
	if len(args) > 0 {
		return usageErr(fmt.Errorf("deedglass webhooks list takes no arguments (got %q)", args[0]), "deedglass webhooks list")
	}
	c, err := apiClient(e)
	if err != nil {
		return err
	}
	var raw json.RawMessage
	if _, err := c.Do(ctx, "GET", "/v1/webhooks", nil, &raw); err != nil {
		return apiErr(e, c, err)
	}
	if e.Globals.JSON {
		_, err := e.Stdout.Write(append(raw, '\n'))
		return err
	}
	var l struct{ Data []webhook.Webhook }
	if err := json.Unmarshal(raw, &l); err != nil {
		return err
	}
	if len(l.Data) == 0 {
		fmt.Fprintln(e.Stdout, "No webhook endpoints yet. Add one with deedglass webhooks create --url … --events approval.decided.")
		return nil
	}
	rows := make([][]string, 0, len(l.Data))
	for _, w := range l.Data {
		sym := e.Style.Teal(ui.OK)
		if w.Status != "active" {
			sym = e.Style.Amber(ui.Unknown)
		}
		health := w.Status
		if w.FailingSince != nil {
			health = "failing"
		}
		rows = append(rows, []string{sym, shortID(w.ID), health, w.URL, strings.Join(w.Events, ",")})
	}
	printColumns(e.Stdout, rows)
	return nil
}

func runWebhooksTest(ctx context.Context, e *Env, args []string) error {
	id, err := oneArg("deedglass webhooks test", "a webhook id", "deedglass webhooks test whk_01j9zw6q3m4n5p6q7r8s9t0v1w", args)
	if err != nil {
		return err
	}
	c, err := apiClient(e)
	if err != nil {
		return err
	}
	var r webhook.TestResult
	if _, err := c.Do(ctx, "POST", "/v1/webhooks/"+url.PathEscape(id)+"/test", nil, &r); err != nil {
		return apiErr(e, c, err)
	}
	if e.Globals.JSON {
		return writeJSON(e, r)
	}
	if r.Delivered {
		fmt.Fprintln(e.Stdout, e.Style.OKLine(fmt.Sprintf("Delivered webhook.test %s %d in %d ms", ui.Sep, r.ResponseStatus, r.ResponseMS)))
		return nil
	}
	why := "no answer"
	if r.Error != nil {
		why = *r.Error
	}
	return failNext(e, ExitFailure, "webhook_failed", fmt.Sprintf("webhook.test wasn't delivered: %s.", why),
		"Check that the endpoint answers 2xx within 10 s. Deedglass retries on its schedule.")
}

func runWebhooksDelete(ctx context.Context, e *Env, args []string) error {
	id, err := oneArg("deedglass webhooks delete", "a webhook id", "deedglass webhooks delete whk_01j9zw6q3m4n5p6q7r8s9t0v1w", args)
	if err != nil {
		return err
	}
	c, err := apiClient(e)
	if err != nil {
		return err
	}
	var d webhook.Deleted
	if _, err := c.Do(ctx, "DELETE", "/v1/webhooks/"+url.PathEscape(id), nil, &d); err != nil {
		return apiErr(e, c, err)
	}
	if e.Globals.JSON {
		return writeJSON(e, d)
	}
	fmt.Fprintln(e.Stdout, e.Style.OKLine(fmt.Sprintf("Webhook deleted %s %s", ui.Sep, shortID(d.ID))))
	fmt.Fprintln(e.Stdout, e.Style.OKLine(fmt.Sprintf("Deed sealed %s webhook.deleted %s %s", ui.Sep, ui.Sep, shortID(d.Deed))))
	return nil
}
