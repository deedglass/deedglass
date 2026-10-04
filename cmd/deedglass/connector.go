package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/deedglass/deedglass/cmd/deedglass/internal/ui"
	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/connector/stripe"
	"github.com/deedglass/deedglass/internal/connectorapi"
	"github.com/deedglass/deedglass/internal/httpx"
	"github.com/deedglass/deedglass/internal/vault"
	"github.com/deedglass/deedglass/pkg/deedglass"
)

// The connector commands (S16) talk to a Deedglass server with an org admin key, like the agent
// commands. Credentials go in only from the environment, stdin or a file: there is no value flag,
// because flag values land in shell history and in ps.

// --- create ---

var connectorCreateValues struct{ typ, name, config, description string }

func connectorCreateFlags(fs *flag.FlagSet) {
	v := &connectorCreateValues
	*v = struct{ typ, name, config, description string }{}
	fs.StringVar(&v.typ, "type", "", "the connector type (rest)")
	fs.StringVar(&v.name, "name", "", "the connector name, used as the tool prefix: orders → orders.get")
	fs.StringVar(&v.config, "config", "", "a YAML or JSON file: the config, or a document with type, name and config")
	fs.StringVar(&v.description, "description", "", "what the connector is for, up to 500 characters")
}

// connectorFile is examples/connectors/orders.yaml's shape; a bare config also works.
type connectorFile struct {
	Type        string `json:"type"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Config      any    `json:"config"`
}

// readConnectorFile reads a YAML or JSON file into its type, name, description and config.
func readConnectorFile(path string) (connectorFile, error) {
	var f connectorFile
	b, err := os.ReadFile(path)
	if err != nil {
		return f, usageErr(fmt.Errorf("Can't read %s: %s", path, lastPart(err)), "Pass the path of a YAML or JSON file, like examples/connectors/orders.yaml.")
	}
	if len(b) > connectorapi.MaxCreateBody {
		return f, usageErr(fmt.Errorf("%s is larger than %d KiB", path, connectorapi.MaxCreateBody>>10), "A connector's config is at most 64 KiB.")
	}
	var doc any
	if err := yaml.Unmarshal(b, &doc); err != nil {
		return f, usageErr(fmt.Errorf("%s isn't valid YAML: %s", path, strings.TrimPrefix(err.Error(), "yaml: ")), "Check the file's indentation.")
	}
	m, ok := doc.(map[string]any)
	if !ok {
		return f, usageErr(fmt.Errorf("%s must hold a mapping", path), "Start the file with type:, name: and config:, or with the config's own keys.")
	}
	if c, has := m["config"]; has {
		f.Config = c
		f.Type, _ = m["type"].(string)
		f.Name, _ = m["name"].(string)
		f.Description, _ = m["description"].(string)
		return f, nil
	}
	f.Config = m
	return f, nil
}

func runConnectorCreate(ctx context.Context, e *Env, args []string) error {
	if len(args) > 0 {
		return usageErr(fmt.Errorf("deedglass connector create takes flags only (got %q)", args[0]),
			"deedglass connector create --type rest --name orders --config examples/connectors/orders.yaml")
	}
	v := connectorCreateValues
	if v.config == "" {
		return usageErr(errors.New("deedglass connector create needs --config"), "deedglass connector create --config examples/connectors/orders.yaml")
	}
	f, err := readConnectorFile(v.config)
	if err != nil {
		return err
	}
	in := struct {
		Type        string `json:"type"`
		Name        string `json:"name"`
		Description string `json:"description,omitempty"`
		Config      any    `json:"config"`
	}{Type: firstNonEmpty(v.typ, f.Type), Name: firstNonEmpty(v.name, f.Name), Description: firstNonEmpty(v.description, f.Description), Config: f.Config}
	if in.Type == "" || in.Name == "" {
		return usageErr(errors.New("deedglass connector create needs a type and a name"), "Pass --type rest --name orders, or put type: and name: in the file.")
	}
	c, err := apiClient(e)
	if err != nil {
		return err
	}
	var res connectorapi.Connector
	if _, err := c.Do(ctx, "POST", "/v1/connectors", in, &res); err != nil {
		return apiErr(e, c, err)
	}
	if e.Globals.JSON {
		return writeJSON(e, res)
	}
	s := e.Style
	fmt.Fprintln(e.Stdout, s.OKLine(fmt.Sprintf("Connector created %s %s %s %s", ui.Sep, res.Name, ui.Sep, res.ID)))
	fmt.Fprintf(e.Stdout, "  %s\n", toolsLine(res.Tools))
	if !res.Credentials.Set && configAuth(res.Config) != "none" {
		fmt.Fprintf(e.Stdout, "  %s No credentials yet. Next: deedglass connector credentials set %s --from-env %s\n",
			s.Amber(ui.Unknown), res.Name, connector.EnvVarFor(res.Name))
	}
	return nil
}

// toolsLine is "2 tools · orders.get (read) · orders.update_status (write)".
func toolsLine(ts []connectorapi.Tool) string {
	parts := []string{fmt.Sprintf("%d %s", len(ts), plural(len(ts), "tool", "tools"))}
	for i, t := range ts {
		if i == 6 {
			parts = append(parts, fmt.Sprintf("%d more", len(ts)-6))
			break
		}
		parts = append(parts, fmt.Sprintf("%s (%s)", t.Name, t.SideEffect))
	}
	return strings.Join(parts, " "+ui.Sep+" ")
}

func configAuth(cfg json.RawMessage) string {
	var c struct {
		Auth struct {
			Kind string `json:"kind"`
		} `json:"auth"`
	}
	_ = json.Unmarshal(cfg, &c)
	if c.Auth.Kind == "" {
		return "none"
	}
	return c.Auth.Kind
}

// --- list, show, types ---

func runConnectorList(ctx context.Context, e *Env, args []string) error {
	if len(args) > 0 {
		return usageErr(fmt.Errorf("deedglass connector list takes no arguments (got %q)", args[0]), "deedglass connector list [--json]")
	}
	c, err := apiClient(e)
	if err != nil {
		return err
	}
	var all []connectorapi.Connector
	q := url.Values{"limit": {"100"}}
	for {
		var l httpx.List[connectorapi.Connector]
		if _, err := c.Do(ctx, "GET", "/v1/connectors?"+q.Encode(), nil, &l); err != nil {
			return apiErr(e, c, err)
		}
		all = append(all, l.Data...)
		if !l.HasMore || l.NextCursor == nil {
			break
		}
		q.Set("cursor", *l.NextCursor)
	}
	if e.Globals.JSON {
		return writeJSON(e, httpx.NewList(all, false, nil))
	}
	if len(all) == 0 {
		fmt.Fprintln(e.Stdout, "No connectors yet. Add your first API in about two minutes: a base URL, one tool, one credential.")
		fmt.Fprintln(e.Stdout, "  Next: deedglass connector create --config examples/connectors/orders.yaml")
		return nil
	}
	rows := [][]string{{"NAME", "TYPE", "STATUS", "TOOLS", "CREDENTIALS", "HEALTH", "ID"}}
	for _, x := range all {
		rows = append(rows, []string{x.Name, x.Type, x.Status, fmt.Sprint(len(x.Tools)), credLine(x.Credentials, x.Config), x.Health.Status, x.ID})
	}
	printTable(e, rows, headerDim(e))
	return nil
}

// credLine is "set · bearer · …9f2c", "not set" or "none".
func credLine(m vault.Meta, cfg json.RawMessage) string {
	switch {
	case m.Set:
		parts := []string{"set", m.Kind}
		if m.Hint != "" {
			parts = append(parts, m.Hint)
		}
		return strings.Join(parts, " "+ui.Sep+" ")
	case configAuth(cfg) == "none":
		return "none needed"
	}
	return "not set"
}

// headerDim dims a table's header row.
func headerDim(e *Env) func(row, col int, cell string) string {
	return func(row, _ int, cell string) string {
		if row == 0 {
			return e.Style.Dim(cell)
		}
		return cell
	}
}

func getConnector(ctx context.Context, e *Env, c *deedglass.Client, ref string) (connectorapi.Connector, error) {
	var res connectorapi.Connector
	if _, err := c.Do(ctx, "GET", "/v1/connectors/"+url.PathEscape(ref), nil, &res); err != nil {
		return res, apiErr(e, c, err)
	}
	return res, nil
}

func runConnectorShow(ctx context.Context, e *Env, args []string) error {
	ref, err := oneArg("deedglass connector show", "a connector name or id", "deedglass connector show orders", args)
	if err != nil {
		return err
	}
	c, err := apiClient(e)
	if err != nil {
		return err
	}
	x, err := getConnector(ctx, e, c, ref)
	if err != nil {
		return err
	}
	if e.Globals.JSON {
		return writeJSON(e, x)
	}
	s := e.Style
	fmt.Fprintf(e.Stdout, "%s %s %s %s %s\n", s.Bold(x.Name), ui.Sep, x.Type, ui.Sep, x.ID)
	row := func(k, v string) { fmt.Fprintf(e.Stdout, "  %s %s\n", ui.Pad(k, 12), v) }
	row("status", x.Status)
	if x.Description != "" {
		row("about", x.Description)
	}
	row("config", x.ConfigHash)
	row("credentials", credLine(x.Credentials, x.Config))
	if x.Credentials.Set {
		row("", fmt.Sprintf("version %d %s encrypted with %s %s updated by %s", x.Credentials.Version, ui.Sep, x.Credentials.KEKID, ui.Sep, x.Credentials.UpdatedBy))
	}
	h := x.Health.Status
	if x.Health.TestedAt != nil {
		h += " " + ui.Sep + " tested " + x.Health.TestedAt.UTC().Format("2006-01-02 15:04 UTC")
	}
	row("health", h)
	for i, t := range x.Tools {
		k := ""
		if i == 0 {
			k = "tools"
		}
		row(k, fmt.Sprintf("%s (%s)", t.Name, t.SideEffect))
	}
	return nil
}

// --- test ---

func runConnectorTest(ctx context.Context, e *Env, args []string) error {
	ref, err := oneArg("deedglass connector test", "a connector name or id", "deedglass connector test orders", args)
	if err != nil {
		return err
	}
	c, err := apiClient(e)
	if err != nil {
		return err
	}
	var res connectorapi.TestResult
	if _, err := c.Do(ctx, "POST", "/v1/connectors/"+url.PathEscape(ref)+"/test", struct{}{}, &res); err != nil {
		return apiErr(e, c, err)
	}
	name := ref
	if strings.HasPrefix(ref, "con_") {
		if x, err := getConnector(ctx, e, c, ref); err == nil {
			name = x.Name
		}
	}
	if e.Globals.JSON {
		if err := writeJSON(e, res); err != nil {
			return err
		}
		if res.Status == string(connector.CheckFail) {
			return &ExitError{Code: ExitFailure, ErrCode: "connector_test_failed", Err: errors.New("connector test failed"), printed: true}
		}
		return nil
	}
	printTestReport(e, name, res)
	if res.Status == string(connector.CheckFail) {
		return &ExitError{Code: ExitFailure, ErrCode: "connector_test_failed", Err: errors.New("connector test failed"), printed: true}
	}
	return nil
}

// printTestReport is S16 §5's checklist.
func printTestReport(e *Env, name string, res connectorapi.TestResult) {
	s := e.Style
	sym := func(st string) string {
		switch st {
		case "pass":
			return s.Teal(ui.OK)
		case "fail":
			return s.Red(ui.Fail)
		case "warn":
			return s.Amber(ui.Unknown)
		}
		return s.Dim("·")
	}
	passed, skipped := 0, 0
	var firstFail *connector.Check
	for i, c := range res.Checks {
		switch c.Status {
		case "pass":
			passed++
		case "skip":
			skipped++
		}
		if c.Status == "fail" && firstFail == nil {
			firstFail = &res.Checks[i]
		}
	}
	switch res.Status {
	case "fail":
		what := "failed"
		if firstFail != nil {
			what = strings.ReplaceAll(firstFail.ID, "_", " ") + " failed"
			if firstFail.ID == "destination" && firstFail.DocURL != "" {
				what = "destination blocked"
			}
		}
		fmt.Fprintf(e.Stdout, "%s %s %s %s %s %s\n", s.Red(ui.Fail), name, ui.Sep, what, ui.Sep, durText(res.DurationMS))
	case "warn":
		fmt.Fprintf(e.Stdout, "%s %s %s %d of %d checks passed %s %s\n", s.Amber(ui.Unknown), name, ui.Sep, passed, len(res.Checks), ui.Sep, durText(res.DurationMS))
	default:
		skip := ""
		if skipped > 0 {
			skip = fmt.Sprintf(" %s %d skipped", ui.Sep, skipped)
		}
		fmt.Fprintf(e.Stdout, "%s %s %s %d checks passed%s %s %s\n", s.Teal(ui.OK), name, ui.Sep, passed, skip, ui.Sep, durText(res.DurationMS))
	}
	for _, c := range res.Checks {
		fmt.Fprintf(e.Stdout, "  %s %s %s\n", sym(string(c.Status)), ui.Pad(c.ID, 13), c.Detail)
		if c.Status == "fail" || c.Status == "warn" {
			if c.Fix != "" {
				fmt.Fprintf(e.Stdout, "    %s\n", c.Fix)
			}
			if c.DocURL != "" {
				fmt.Fprintf(e.Stdout, "    %s\n", c.DocURL)
			}
		}
	}
}

// durText is "184 ms", or "< 1 ms".
func durText(ms int64) string {
	if ms < 1 {
		return "< 1 ms"
	}
	return fmt.Sprintf("%d ms", ms)
}

// --- credentials ---

var credValues struct {
	fromEnv, kind string
	stdin         bool
	fields        listFlag
}

func credSetFlags(fs *flag.FlagSet) {
	v := &credValues
	v.fromEnv, v.kind, v.stdin, v.fields = "", "", false, nil
	fs.StringVar(&v.fromEnv, "from-env", "", "read the value from this environment variable")
	fs.BoolVar(&v.stdin, "stdin", false, "read the value from stdin (one line)")
	fs.Var(&v.fields, "field", "one field as KEY=env:VAR, KEY=file:PATH or KEY=stdin (repeatable)")
	fs.StringVar(&v.kind, "kind", "", "bearer, header, basic, query, dsn or fields (default: the config's auth.kind)")
}

// emptyEnvMessage is S16 §5's sentence for an empty --from-env.
func emptyEnvMessage(name string) string {
	return name + " is empty or unset. Export it in this shell and run the command again. Deedglass never reads secrets from flags."
}

// singleField is the field a kind's one value goes in.
func singleField(kind string) string {
	switch kind {
	case vault.KindBearer:
		return "token"
	case vault.KindDSN:
		return "dsn"
	}
	return "value"
}

func runConnectorCredentialsSet(ctx context.Context, e *Env, args []string) error {
	pos, err := mixedArgs("deedglass connector credentials set", credSetFlags, args)
	if err != nil {
		return err
	}
	ref, err := oneArg("deedglass connector credentials set", "a connector name or id",
		"deedglass connector credentials set orders --from-env ORDERS_TOKEN", pos)
	if err != nil {
		return err
	}
	v := credValues
	sources := 0
	for _, b := range []bool{v.fromEnv != "", v.stdin, len(v.fields) > 0} {
		if b {
			sources++
		}
	}
	if sources != 1 {
		return usageErr(errors.New("pass exactly one of --from-env, --stdin or --field"),
			"deedglass connector credentials set "+ref+" --from-env "+connector.EnvVarFor(ref)+". Deedglass never reads secrets from flags.")
	}
	if v.fromEnv != "" || len(v.fields) > 0 {
		// S30: a live Stripe key is refused before Deedglass talks to any server, whatever the
		// connector, so it never leaves this machine.
		vals := []string{e.Getenv(v.fromEnv)}
		for _, f := range v.fields {
			if _, src, ok := strings.Cut(f, "="); ok && strings.HasPrefix(src, "env:") {
				vals = append(vals, e.Getenv(strings.TrimPrefix(src, "env:")))
			}
		}
		for _, val := range vals {
			if code, _ := stripe.CheckKey(val); code == connector.CodeStripeLiveKeyRefused {
				return stripeKeyRefusal(ref, val)
			}
		}
	}
	c, err := apiClient(e)
	if err != nil {
		return err
	}
	x, err := getConnector(ctx, e, c, ref)
	if err != nil {
		return err
	}
	kind := firstNonEmpty(v.kind, configAuth(x.Config))
	if kind == "none" {
		kind = vault.KindBearer
		if len(v.fields) > 0 {
			// S29: several named fields with no auth.kind in the config (Slack's bot_token and
			// signing_secret, a GitHub App's private_key) are a fields credential.
			kind = vault.KindFields
		}
	}
	secret := map[string]string{}
	defer func() {
		for k := range secret {
			secret[k] = ""
		}
	}()
	switch {
	case v.fromEnv != "":
		val := e.Getenv(v.fromEnv)
		if strings.TrimSpace(val) == "" {
			return &ExitError{Code: ExitUsage, ErrCode: "validation_failed", Err: errors.New(strings.TrimSuffix(emptyEnvMessage(v.fromEnv), "."))}
		}
		if kind == vault.KindBasic || kind == vault.KindFields {
			return usageErr(fmt.Errorf("a %s credential has several fields", kind), "Pass each with --field, like --field username=env:ORDERS_USER --field password=env:ORDERS_PASSWORD.")
		}
		secret[singleField(kind)] = val
	case v.stdin:
		line, err := readSecretLine(e.stdin())
		if err != nil || line == "" {
			return &ExitError{Code: ExitUsage, ErrCode: "validation_failed", Err: errors.New("Nothing arrived on stdin"),
				Next: "Pipe the value in, for example: op read op://vault/orders/token | deedglass connector credentials set " + x.Name + " --stdin"}
		}
		if kind == vault.KindBasic || kind == vault.KindFields {
			return usageErr(fmt.Errorf("a %s credential has several fields", kind), "Pass each with --field KEY=env:VAR, KEY=file:PATH or KEY=stdin.")
		}
		secret[singleField(kind)] = line
	default:
		usedStdin := false
		for _, f := range v.fields {
			k, src, ok := strings.Cut(f, "=")
			if !ok || k == "" {
				return usageErr(fmt.Errorf("--field %q isn't KEY=SOURCE", clipFlag(f)), "Use KEY=env:VAR, KEY=file:PATH or KEY=stdin. Deedglass never reads secrets from flags.")
			}
			var val string
			switch {
			case strings.HasPrefix(src, "env:"):
				name := strings.TrimPrefix(src, "env:")
				val = e.Getenv(name)
				if strings.TrimSpace(val) == "" {
					return &ExitError{Code: ExitUsage, ErrCode: "validation_failed", Err: errors.New(strings.TrimSuffix(emptyEnvMessage(name), "."))}
				}
			case strings.HasPrefix(src, "file:"):
				path := strings.TrimPrefix(src, "file:")
				b, err := os.ReadFile(path)
				if err != nil {
					return usageErr(fmt.Errorf("Can't read %s: %s", path, lastPart(err)), "Check the path.")
				}
				if fi, err := os.Stat(path); err == nil && fi.Mode().Perm()&0o077 != 0 {
					// S29: a key file others can read. Still stored; the warning says how to fix it.
					fmt.Fprintf(e.Stderr, "%s %s can be read by other users (mode %04o). Run chmod 600 %s.\n",
						e.Style.Amber(ui.Unknown), path, fi.Mode().Perm(), path)
				}
				val = strings.TrimRight(string(b), "\r\n")
			case src == "stdin":
				if usedStdin {
					return usageErr(errors.New("only one field can come from stdin"), "Read the others from env: or file:.")
				}
				usedStdin = true
				line, err := readSecretLine(e.stdin())
				if err != nil || line == "" {
					return &ExitError{Code: ExitUsage, ErrCode: "validation_failed", Err: errors.New("Nothing arrived on stdin")}
				}
				val = line
			default:
				return usageErr(fmt.Errorf("--field %s takes env:, file: or stdin, never a value", k), "Deedglass never reads secrets from flags.")
			}
			secret[k] = val
		}
	}
	if x.Type == stripe.Type {
		// S30: a key that isn't a Stripe sandbox key never leaves this machine.
		if err := stripeKeyRefusal(x.Name, secret[stripe.FieldToken]); err != nil {
			return err
		}
	}
	var m vault.Meta
	body := map[string]any{"kind": kind, "secret": secret}
	if _, err := c.Do(ctx, "PUT", "/v1/connectors/"+url.PathEscape(x.ID)+"/credentials", body, &m); err != nil {
		return apiErr(e, c, err)
	}
	if e.Globals.JSON {
		return writeJSON(e, m)
	}
	s := e.Style
	parts := []string{x.Name, m.Kind}
	if m.Hint != "" {
		parts = append(parts, m.Hint)
	}
	fmt.Fprintln(e.Stdout, s.OKLine(fmt.Sprintf("Credentials stored %s %s %s encrypted with %s", ui.Sep, strings.Join(parts, " "+ui.Sep+" "), ui.Sep, m.KEKID)))
	fmt.Fprintln(e.Stdout, "  Agents never see this value. Deedglass adds it only when the Gate runs a call.")
	return nil
}

func clipFlag(s string) string {
	k, _, _ := strings.Cut(s, "=")
	return k + "=…"
}

// readSecretLine reads one line (at most 16 KiB) and trims its line ending.
func readSecretLine(r io.Reader) (string, error) {
	br := bufio.NewReaderSize(io.LimitReader(r, 16<<10+2), 16<<10+2)
	line, err := br.ReadString('\n')
	if err != nil && err != io.EOF {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

func runConnectorCredentialsRemove(ctx context.Context, e *Env, args []string) error {
	ref, err := oneArg("deedglass connector credentials remove", "a connector name or id", "deedglass connector credentials remove orders", args)
	if err != nil {
		return err
	}
	c, err := apiClient(e)
	if err != nil {
		return err
	}
	x, err := getConnector(ctx, e, c, ref)
	if err != nil {
		return err
	}
	var m vault.Meta
	if _, err := c.Do(ctx, "DELETE", "/v1/connectors/"+url.PathEscape(x.ID)+"/credentials", nil, &m); err != nil {
		return apiErr(e, c, err)
	}
	if e.Globals.JSON {
		return writeJSON(e, m)
	}
	if m.Deed == nil {
		fmt.Fprintln(e.Stdout, e.Style.OKLine(fmt.Sprintf("%s had no credentials %s nothing changed", x.Name, ui.Sep)))
		return nil
	}
	fmt.Fprintln(e.Stdout, e.Style.OKLine(fmt.Sprintf("Credentials removed %s %s %s the data key is destroyed", ui.Sep, x.Name, ui.Sep)))
	fmt.Fprintf(e.Stdout, "  Calls to %s.* now fail as credentials_missing until you store new ones.\n", x.Name)
	return nil
}

// sortedKeys is a map's keys in order.
func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
