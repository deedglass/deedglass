// Package localapi is the local app's routes (DL-143): GET and PATCH /v1/local (what the first-run
// screens remember) and the Connect screen's GET /v1/connect and POST /v1/connect/{id}/start. They
// answer only inside `deedglass app`, the app on this computer; on any other serve they answer 404,
// which is how the Console knows it isn't the local app.
//
// No response ever holds a key. Connecting a client makes an agent for it (once) and keeps the
// agent's key in a 0600 file in the app's data directory; the client's config runs
// `deedglass mcp serve --client <id>`, which reads that file and sends the key only to this app.
package localapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/internal/httpx"
	"github.com/deedglass/deedglass/internal/identity"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/installlinks"
	"github.com/deedglass/deedglass/internal/mcpinstall"
	"github.com/deedglass/deedglass/internal/store"
)

// File names in the app's data directory.
const (
	StateFile = "local.json" // State
	AgentsDir = "agents"     // <id>.key, one agent key per connected client (0600)
)

// MsgNotLocal is not_found's message on a serve that isn't the local app.
const MsgNotLocal = "This server isn't the Deedglass app on this computer. Run deedglass app to open it."

// Paths are the first-run choices: who is setting it up.
var Paths = []string{"clinic", "build", "it"}

// State is local.json.
type State struct {
	Object   string               `json:"object"` // "local"
	FirstRun bool                 `json:"first_run"`
	Path     *string              `json:"path"`
	Org      string               `json:"org,omitempty"`
	Address  string               `json:"address,omitempty"` // the app's address at its last start
	Started  map[string]time.Time `json:"started,omitempty"` // Connect items started, by id
}

// ReadState reads local.json; a missing or unreadable file is a first run.
func ReadState(dir string) State {
	s := State{Object: "local", FirstRun: true}
	if b, err := os.ReadFile(filepath.Join(dir, StateFile)); err == nil {
		_ = json.Unmarshal(b, &s)
	}
	s.Object = "local"
	return s
}

// WriteState writes local.json (0600, through a temporary file).
func WriteState(dir string, s State) error {
	s.Object = "local"
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return writeSecret(filepath.Join(dir, StateFile), append(b, '\n'))
}

// AgentKeyPath is where the app keeps a connected client's agent key.
func AgentKeyPath(dir, id string) string { return filepath.Join(dir, AgentsDir, id+".key") }

func writeSecret(path string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// Runner is how an MCP client starts deedglass on this computer: npx -y deedglass, or the binary's
// own path when it is installed.
type Runner struct {
	Command string
	Args    []string
}

// API is the routes' dependencies. A nil API (any serve but deedglass app) answers 404.
type API struct {
	Dir     string // the app's data directory
	Address string // the app's address, http://127.0.0.1:4180
	Home    string // the person's home directory: where the AI tools keep their settings ("" turns Connect's one-click button off)
	GOOS    string // runtime.GOOS when empty (tests)
	AppData string // Windows: %APPDATA%
	People  *identity.Service
	DB      *store.DB
	Run     Runner
	// Detect lists the coding agents found on this computer (protect's names); Protect adds the
	// shell guard to them. Either may be nil.
	Detect  func() []string
	Protect func(ctx context.Context) error
	Now     func() time.Time

	mu sync.Mutex // local.json and the agent key files
}

// Routes are the six routes.
func Routes(a *API) []httpx.Route {
	return []httpx.Route{
		{Method: "GET", Pattern: "/v1/local", OperationID: "getLocal", Auth: httpx.SessionOrKey, MinRole: httpx.RoleViewer,
			Purpose: "reading the local app's state", Scopes: []string{httpx.ScopeAdmin}, Handler: a.getLocal},
		{Method: "PATCH", Pattern: "/v1/local", OperationID: "updateLocal", Auth: httpx.SessionOrKey, MinRole: httpx.RoleAdmin,
			Purpose: "changing the local app's first-run state", Scopes: []string{httpx.ScopeAdmin}, MaxBody: 1 << 10, Handler: a.patchLocal},
		{Method: "GET", Pattern: "/v1/connect", OperationID: "listConnect", Auth: httpx.SessionOrKey, MinRole: httpx.RoleViewer,
			Purpose: "reading what is connected", Scopes: []string{httpx.ScopeAdmin}, Timeout: 30 * time.Second, Handler: a.listConnect},
		{Method: "POST", Pattern: "/v1/connect/{id}/start", OperationID: "startConnect", Auth: httpx.SessionOrKey, MinRole: httpx.RoleAdmin,
			Purpose: "connecting a tool", Scopes: []string{httpx.ScopeAdmin}, Idempotency: httpx.IdemNever, MaxBody: 1 << 10,
			Timeout: 30 * time.Second, Handler: a.startConnect},
		{Method: "POST", Pattern: "/v1/connect/{id}/install", OperationID: "installConnect", Auth: httpx.SessionOrKey, MinRole: httpx.RoleAdmin,
			Purpose: "adding Deedglass to a tool's settings file on this computer", Scopes: []string{httpx.ScopeAdmin}, Idempotency: httpx.IdemNever, MaxBody: 1 << 10,
			Timeout: 30 * time.Second, Handler: a.installConnect},
		{Method: "POST", Pattern: "/v1/connect/{id}/undo", OperationID: "undoConnect", Auth: httpx.SessionOrKey, MinRole: httpx.RoleAdmin,
			Purpose: "taking Deedglass out of a tool's settings file on this computer", Scopes: []string{httpx.ScopeAdmin}, Idempotency: httpx.IdemNever, MaxBody: 1 << 10,
			Timeout: 30 * time.Second, Handler: a.undoConnect},
	}
}

func (a *API) local() error {
	if a == nil || a.Dir == "" {
		return httpx.E(httpx.CodeNotFound, httpx.Msgf("%s", MsgNotLocal))
	}
	return nil
}

func (a *API) now() time.Time {
	if a.Now != nil {
		return a.Now().UTC()
	}
	return time.Now().UTC()
}

// Local is GET /v1/local.
type Local struct {
	Object   string  `json:"object"` // "local"
	Local    bool    `json:"local"`
	Address  string  `json:"address"`
	FirstRun bool    `json:"first_run"`
	Path     *string `json:"path"`
}

func (a *API) view(s State) Local {
	return Local{Object: "local", Local: true, Address: a.Address, FirstRun: s.FirstRun, Path: s.Path}
}

func (a *API) getLocal(w http.ResponseWriter, r *http.Request) error {
	if err := a.local(); err != nil {
		return err
	}
	a.mu.Lock()
	s := ReadState(a.Dir)
	a.mu.Unlock()
	return httpx.WriteJSON(w, http.StatusOK, a.view(s))
}

type patchBody struct {
	Path     *string `json:"path"`
	FirstRun *bool   `json:"first_run"`
}

func (a *API) patchLocal(w http.ResponseWriter, r *http.Request) error {
	if err := a.local(); err != nil {
		return err
	}
	var b patchBody
	if err := httpx.DecodeJSON(r, &b); err != nil {
		return err
	}
	if b.Path == nil && b.FirstRun == nil {
		return httpx.ErrValidation("path", `Send {"path": "clinic"}, {"path": "build"}, {"path": "it"} or {"first_run": false}.`)
	}
	if b.Path != nil && !slices.Contains(Paths, *b.Path) {
		return httpx.ErrValidation("path", "path is clinic, build or it.")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	s := ReadState(a.Dir)
	if b.Path != nil {
		p := *b.Path
		s.Path = &p
	}
	if b.FirstRun != nil {
		s.FirstRun = *b.FirstRun
	}
	if err := WriteState(a.Dir, s); err != nil {
		return err
	}
	return httpx.WriteJSON(w, http.StatusOK, a.view(s))
}

// --- Connect ---

// Client ids, in the order the Connect screen shows them.
const (
	ClaudeDesktop = "claude-desktop"
	Cursor        = "cursor"
	VSCode        = "vscode"
	Terminal      = "terminal" // coding agents in a terminal: Claude Code, Codex, Gemini CLI, Copilot
	OpenAI        = "openai"   // the OpenAI Agents SDK
	Scribe        = "scribe"   // an AI scribe or anything that writes FHIR
	Database      = "database" // your own Postgres (deedglass connect)
)

// Clients are the Connect screen's items.
var Clients = []string{ClaudeDesktop, Cursor, VSCode, Terminal, OpenAI, Scribe, Database}

// agentName is the agent each client acts as (database has none: deedglass connect makes its own).
var agentName = map[string]string{
	ClaudeDesktop: "claude-desktop", Cursor: "cursor", VSCode: "vscode", Terminal: "coding-agents",
	OpenAI: "openai-agents", Scribe: "scribe",
}

var clientTitle = map[string]string{
	ClaudeDesktop: "Claude Desktop", Cursor: "Cursor", VSCode: "VS Code", Terminal: "Coding agents in a terminal",
	OpenAI: "OpenAI agents", Scribe: "AI scribe (FHIR)", Database: "Your database",
}

// Statuses.
const (
	NotConnected = "not_connected"
	Waiting      = "waiting"
	Connected    = "connected"
)

// Item is one Connect row. Links and commands never hold a key.
type Item struct {
	Object       string      `json:"object"` // "connect_item"
	ID           string      `json:"id"`
	Name         string      `json:"name"`
	Status       string      `json:"status"`
	Agent        *string     `json:"agent"`
	LastActionAt *httpx.Time `json:"last_action_at"`
	Detected     []string    `json:"detected"`
	Link         *string     `json:"link"`
	WebLink      *string     `json:"web_link"`
	Config       *string     `json:"config"`
	Command      *string     `json:"command"`
	Address      *string     `json:"address"`
	// Install is the tool's settings file on this computer and what the Connect button would do
	// to it (nil when the tool has no file Deedglass writes, or the app has no home directory).
	Install *Install `json:"install"`
}

// Install is a tool's settings file on this computer (internal/mcpinstall).
type Install struct {
	Object    string `json:"object"` // "connect_install"
	Tool      string `json:"tool"`   // the mcpinstall target: claude-desktop, cursor, vscode or gemini
	Title     string `json:"title"`
	File      string `json:"file"`
	Installed bool   `json:"installed"` // the tool's folder is on this computer
	Status    string `json:"status"`    // add, update, present, skip or absent
	Reason    string `json:"reason,omitempty"`
	Entry     string `json:"entry"` // the JSON the Connect button adds
	Backup    string `json:"backup,omitempty"`
	CanUndo   bool   `json:"can_undo"`
}

// installTarget is the mcpinstall target an item's Connect button writes. The terminal item writes
// Gemini CLI's settings (Claude Code and Codex add servers with their own commands).
var installTarget = map[string]string{ClaudeDesktop: mcpinstall.ClaudeDesktop, Cursor: mcpinstall.Cursor, VSCode: mcpinstall.VSCode, Terminal: mcpinstall.Gemini}

// installOptions is how mcpinstall finds the person's files and what the entry runs.
func (a *API) installOptions(id string) mcpinstall.Options {
	obj := a.server(id)
	return mcpinstall.Options{Home: a.Home, GOOS: a.GOOS, AppData: a.AppData, Command: obj.Command, Args: obj.Args, Now: a.Now}
}

// install fills an item's Install, planning only.
func (a *API) install(it *Item) {
	tool, ok := installTarget[it.ID]
	if !ok || a.Home == "" {
		return
	}
	c, err := mcpinstall.Plan(a.installOptions(it.ID), tool)
	if err != nil {
		return
	}
	it.Install = &Install{Object: "connect_install", Tool: tool, Title: c.Title, File: c.File, Installed: c.Installed, Status: string(c.Status),
		Reason: c.Reason, Entry: c.Entry, Backup: c.Backup, CanUndo: c.CanUndo}
}

func ptr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// server is the stdio server object a client's config runs.
func (a *API) server(id string) installlinks.ServerObject {
	args := append(slices.Clone(a.Run.Args), "mcp", "serve", "--client", id)
	return installlinks.ServerObject{Command: a.Run.Command, Args: args}
}

func compact(v any) string {
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
	return strings.TrimRight(b.String(), "\n")
}

// shellQuote quotes one word for a POSIX shell when it needs it.
func shellQuote(s string) string {
	if s != "" && strings.IndexFunc(s, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("-_./:=@", r))
	}) < 0 {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func (a *API) commandLine(args ...string) string {
	words := append([]string{a.Run.Command}, a.Run.Args...)
	words = append(words, args...)
	for i, w := range words {
		words[i] = shellQuote(w)
	}
	return strings.Join(words, " ")
}

// links fills an item's links, config, command and address.
func (a *API) links(it *Item) {
	obj := a.server(it.ID)
	cfg := compact(obj)
	mcpServers := compact(map[string]any{"mcpServers": map[string]any{installlinks.Name: obj}})
	b64 := base64.StdEncoding.EncodeToString([]byte(cfg))
	switch it.ID {
	case ClaudeDesktop:
		it.Config, it.WebLink = ptr(mcpServers), ptr(installlinks.ClaudeDesktopGuide)
	case Cursor:
		it.Link = ptr("cursor://anysphere.cursor-deeplink/mcp/install?name=" + installlinks.Name + "&config=" + url.QueryEscape(b64))
		it.WebLink = ptr("https://cursor.com/en/install-mcp?name=" + installlinks.Name + "&config=" + url.QueryEscape(b64))
		it.Config = ptr(mcpServers)
	case VSCode:
		withName := `{"name":"` + installlinks.Name + `",` + strings.TrimPrefix(cfg, "{")
		it.Link = ptr("vscode:mcp/install?" + url.QueryEscape(withName))
		it.WebLink = ptr("https://vscode.dev/redirect/mcp/install?" + url.Values{"name": {installlinks.Name}, "config": {cfg}}.Encode())
		it.Config = ptr(compact(map[string]any{"servers": map[string]any{installlinks.Name: obj}}))
		it.Command = ptr("code --add-mcp " + shellQuote(withName))
	case Terminal:
		words := append([]string{obj.Command}, obj.Args...)
		for i, w := range words {
			words[i] = shellQuote(w)
		}
		it.Command = ptr("claude mcp add --scope user " + installlinks.Name + " -- " + strings.Join(words, " "))
		it.Config = ptr(mcpServers)
	case OpenAI:
		// A JSON object of strings is also a Python dict literal.
		params := compact(map[string]any{"command": obj.Command, "args": obj.Args})
		it.Config = ptr("from agents.mcp import MCPServerStdio\n\ndeedglass = MCPServerStdio(params=" + params + ")\n")
		it.WebLink = ptr("https://docs.deedglass.com/sdks/adapters/openai-agents/")
		it.Address = ptr(strings.TrimSuffix(a.Address, "/") + "/mcp/native")
	case Scribe:
		it.Command = ptr("DEEDGLASS_SERVER=" + a.Address + " DEEDGLASS_API_KEY_FILE=" + shellQuote(AgentKeyPath(a.Dir, Scribe)))
		it.WebLink = ptr("https://docs.deedglass.com/guides/fhir-server/")
		it.Address = ptr(a.Address)
	case Database:
		it.Command = ptr(a.commandLine("connect"))
		it.WebLink = ptr("https://docs.deedglass.com/get-started/postgres/")
	}
}

// status works out an item's status from the org's agents, actions and connectors.
func (a *API) status(ctx context.Context, org ids.OrgID, s State, it *Item) error {
	if a.DB == nil {
		return httpx.E(httpx.CodeServiceUnavailable)
	}
	var last *time.Time
	err := a.DB.OrgReadTx(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		if it.ID == Database {
			var n int
			if err := tx.QueryRow(ctx, `SELECT pg_catalog.count(*)::integer FROM deedglass.connectors
			    WHERE org_id = $1 AND type = 'postgres' AND status = 'active'`, org).Scan(&n); err != nil {
				return err
			}
			if n > 0 {
				it.Status = Waiting
			}
			return tx.QueryRow(ctx, `SELECT pg_catalog.max(a.created_at) FROM deedglass.actions AS a
			    JOIN deedglass.connectors AS c ON c.org_id = a.org_id AND c.id = a.connector_id
			    WHERE a.org_id = $1 AND c.type = 'postgres'`, org).Scan(&last)
		}
		var agent string
		err := tx.QueryRow(ctx, `SELECT id FROM deedglass.agents WHERE org_id = $1 AND name = $2`, org, agentName[it.ID]).Scan(&agent)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		it.Agent, it.Status = &agent, Waiting
		return tx.QueryRow(ctx, `SELECT pg_catalog.max(created_at) FROM deedglass.actions WHERE org_id = $1 AND agent_id = $2`, org, agent).Scan(&last)
	})
	if err != nil {
		return err
	}
	if it.Status == NotConnected {
		if _, ok := s.Started[it.ID]; ok {
			it.Status = Waiting
		}
	}
	if last != nil {
		t := httpx.Time(last.UTC())
		it.LastActionAt, it.Status = &t, Connected
	}
	return nil
}

func (a *API) item(ctx context.Context, org ids.OrgID, s State, id string, detected []string) (Item, error) {
	it := Item{Object: "connect_item", ID: id, Name: clientTitle[id], Status: NotConnected, Detected: []string{}}
	if id == Terminal && detected != nil {
		it.Detected = detected
	}
	a.links(&it)
	a.install(&it)
	return it, a.status(ctx, org, s, &it)
}

func (a *API) detected() []string {
	if a.Detect == nil {
		return []string{}
	}
	if d := a.Detect(); d != nil {
		return d
	}
	return []string{}
}

func (a *API) listConnect(w http.ResponseWriter, r *http.Request) error {
	if err := a.local(); err != nil {
		return err
	}
	pr, ok := httpx.PrincipalFrom(r.Context())
	if !ok || pr.OrgID.IsZero() {
		return httpx.E(httpx.CodeAuthenticationRequired)
	}
	a.mu.Lock()
	s := ReadState(a.Dir)
	a.mu.Unlock()
	det := a.detected()
	out := httpx.List[Item]{Object: "list", Data: []Item{}}
	for _, id := range Clients {
		it, err := a.item(r.Context(), pr.OrgID, s, id, det)
		if err != nil {
			return err
		}
		out.Data = append(out.Data, it)
	}
	return httpx.WriteJSON(w, http.StatusOK, out)
}

func errUnknownClient(id string) error {
	return httpx.E(httpx.CodeNotFound, httpx.Param("id"),
		httpx.Msgf("There's no Connect item %s. Use one of: %s.", clip(id, 40), strings.Join(Clients, ", ")))
}

func clip(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

func (a *API) startConnect(w http.ResponseWriter, r *http.Request) error {
	if err := a.local(); err != nil {
		return err
	}
	id := r.PathValue("id")
	if !slices.Contains(Clients, id) {
		return errUnknownClient(id)
	}
	pr, ok := httpx.PrincipalFrom(r.Context())
	if !ok || pr.OrgID.IsZero() {
		return httpx.E(httpx.CodeAuthenticationRequired)
	}
	actor, err := identity.ActorOf(&pr)
	if err != nil {
		return err
	}
	ctx := r.Context()
	a.mu.Lock()
	defer a.mu.Unlock()
	if name := agentName[id]; name != "" {
		if err := a.ensureAgent(ctx, actor, pr.OrgID, id, name); err != nil {
			return err
		}
	}
	if id == Terminal && a.Protect != nil {
		if err := a.Protect(ctx); err != nil {
			return httpx.E(httpx.CodeValidationFailed, httpx.Msgf("Couldn't add the guard to your coding agents: %s. Run deedglass protect in a terminal to see why.", clip(err.Error(), 200)))
		}
	}
	s := ReadState(a.Dir)
	if s.Started == nil {
		s.Started = map[string]time.Time{}
	}
	if _, ok := s.Started[id]; !ok {
		s.Started[id] = a.now()
		if err := WriteState(a.Dir, s); err != nil {
			return err
		}
	}
	it, err := a.item(ctx, pr.OrgID, s, id, a.detected())
	if err != nil {
		return err
	}
	if it.Status == NotConnected {
		it.Status = Waiting
	}
	httpx.Annotate(ctx, pr.OrgID.String(), "", "")
	return httpx.WriteJSON(w, http.StatusOK, it)
}

// ensureAgent makes the client's agent and its key file, each once. A key file that exists is kept.
func (a *API) ensureAgent(ctx context.Context, actor identity.Actor, org ids.OrgID, id, name string) error {
	if a.People == nil {
		return httpx.E(httpx.CodeServiceUnavailable)
	}
	path := AgentKeyPath(a.Dir, id)
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	list, _, err := a.People.ListAgents(ctx, org, identity.AgentFilter{Name: name, Limit: 1})
	if err != nil {
		return err
	}
	var agentID string
	if len(list) > 0 {
		agentID = list[0].ID
	} else {
		res, err := a.People.CreateAgent(ctx, actor, org, identity.CreateAgentInput{Name: name,
			Description: clientTitle[id] + ", connected from the Deedglass app", Labels: map[string]string{"connect": id}})
		if err != nil {
			return err
		}
		agentID = res.ID
	}
	k, err := a.People.CreateAgentKey(ctx, actor, org, agentID, identity.CreateKeyInput{Name: "deedglass-app"})
	if err != nil {
		return err
	}
	if k.Secret == nil {
		return errors.New("localapi: the new key has no secret")
	}
	return writeSecret(path, []byte(*k.Secret+"\n"))
}

// installConnect is POST /v1/connect/{id}/install: the one-click Connect button. It makes the
// tool's agent and key file (as start does), then adds the entry to the tool's settings file after
// backing it up. Nothing is written on any other serve, for an unknown id, or for a tool without a
// settings file.
func (a *API) installConnect(w http.ResponseWriter, r *http.Request) error {
	if err := a.local(); err != nil {
		return err
	}
	id := r.PathValue("id")
	tool, ok := installTarget[id]
	if !ok {
		if !slices.Contains(Clients, id) {
			return errUnknownClient(id)
		}
		return httpx.E(httpx.CodeValidationFailed, httpx.Param("id"), httpx.Msgf("Deedglass doesn't write %s's settings. Use the link, command or address on its card.", clientTitle[id]))
	}
	if a.Home == "" {
		return httpx.E(httpx.CodeValidationFailed, httpx.Msgf("The app doesn't know your home folder, so it can't find %s's settings. Copy the settings instead.", clientTitle[id]))
	}
	pr, ok := httpx.PrincipalFrom(r.Context())
	if !ok || pr.OrgID.IsZero() {
		return httpx.E(httpx.CodeAuthenticationRequired)
	}
	actor, err := identity.ActorOf(&pr)
	if err != nil {
		return err
	}
	ctx := r.Context()
	a.mu.Lock()
	defer a.mu.Unlock()
	if name := agentName[id]; name != "" {
		if err := a.ensureAgent(ctx, actor, pr.OrgID, id, name); err != nil {
			return err
		}
	}
	o := a.installOptions(id)
	c, err := mcpinstall.Plan(o, tool)
	if err != nil {
		return err
	}
	switch c.Status {
	case mcpinstall.Absent:
		return httpx.E(httpx.CodeValidationFailed, httpx.Msgf("%s isn't installed on this computer (no %s). Install it, or copy the settings to the computer where it is.", c.Title, filepath.Dir(c.File)))
	case mcpinstall.Skip:
		return httpx.E(httpx.CodeValidationFailed, httpx.Msgf("%s's settings file was left alone: %s. Add the entry by hand from the settings shown.", c.Title, c.Reason))
	}
	if _, err := mcpinstall.Apply(o, c); err != nil {
		return httpx.E(httpx.CodeValidationFailed, httpx.Msgf("Couldn't write %s's settings: %s. Copy the settings instead.", c.Title, clip(err.Error(), 200)))
	}
	s := ReadState(a.Dir)
	if s.Started == nil {
		s.Started = map[string]time.Time{}
	}
	if _, ok := s.Started[id]; !ok {
		s.Started[id] = a.now()
		if err := WriteState(a.Dir, s); err != nil {
			return err
		}
	}
	it, err := a.item(ctx, pr.OrgID, s, id, a.detected())
	if err != nil {
		return err
	}
	if it.Status == NotConnected {
		it.Status = Waiting
	}
	httpx.Annotate(ctx, pr.OrgID.String(), "", "")
	return httpx.WriteJSON(w, http.StatusOK, it)
}

// undoConnect is POST /v1/connect/{id}/undo: puts the tool's settings file back as it was before
// the newest Connect click (or, when the person changed it since, takes only the entry out).
func (a *API) undoConnect(w http.ResponseWriter, r *http.Request) error {
	if err := a.local(); err != nil {
		return err
	}
	id := r.PathValue("id")
	tool, ok := installTarget[id]
	if !ok {
		if !slices.Contains(Clients, id) {
			return errUnknownClient(id)
		}
		return httpx.E(httpx.CodeValidationFailed, httpx.Param("id"), httpx.Msgf("Deedglass never wrote %s's settings, so there's nothing to undo.", clientTitle[id]))
	}
	if a.Home == "" {
		return httpx.E(httpx.CodeValidationFailed, httpx.Msgf("The app doesn't know your home folder, so there's nothing to undo."))
	}
	pr, ok := httpx.PrincipalFrom(r.Context())
	if !ok || pr.OrgID.IsZero() {
		return httpx.E(httpx.CodeAuthenticationRequired)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	u, err := mcpinstall.Undo(a.installOptions(id), tool)
	if err != nil {
		return httpx.E(httpx.CodeValidationFailed, httpx.Msgf("Couldn't undo: %s.", clip(err.Error(), 200)))
	}
	if u.Status == mcpinstall.Failed {
		return httpx.E(httpx.CodeValidationFailed, httpx.Msgf("Couldn't undo: %s. Open %s and remove the deedglass entry by hand.", u.Reason, u.File))
	}
	it, err := a.item(r.Context(), pr.OrgID, ReadState(a.Dir), id, a.detected())
	if err != nil {
		return err
	}
	httpx.Annotate(r.Context(), pr.OrgID.String(), "", "")
	return httpx.WriteJSON(w, http.StatusOK, it)
}
