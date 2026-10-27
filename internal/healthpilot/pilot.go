// Package healthpilot is `deedglass health pilot`: a self-guided demo for a hospital's IT team, on
// synthetic data, with no Docker and no server.
//
// Everything it shows is computed now by the product's own code:
//
//   - the decisions come from policy.Evaluate on the healthcare-fhir pack (india-health with
//     --region india), carried in this binary and checked against its seal, then from lockdown.Apply,
//     the overlay `deedglass serve` runs when lockdown is on; PHI mode is on for the pilot's org;
//   - the writes go through the FHIR connector (internal/connector/fhir), the code a real
//     connector runs, to an in-memory FHIR R4 store inside this process (fhirtest, synthetic
//     data only), or with --fhir-url to the hospital's own FHIR sandbox;
//   - each write is read back by the connector's NativeReadBack and compared with what was sent;
//   - the records are sealed with a key made for this run and checked by deed.VerifyStream, the
//     code behind `deedglass verify`; one byte is changed and the check fails.
//
// The pilot has no Console and no database, so the approvals a lockdown write needs are
// countersigned by a stand-in admin it names, and the high-risk write stays held: it prints the
// Console link a real server would send, and nothing reaches the record system.
package healthpilot

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	healthdocs "github.com/deedglass/deedglass/docs/healthcare"
	"github.com/deedglass/deedglass/internal/cli/ui"
	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/connector/egress"
	"github.com/deedglass/deedglass/internal/connector/fhir"
	"github.com/deedglass/deedglass/internal/connector/fhir/fhirtest"
	"github.com/deedglass/deedglass/internal/deed"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/lockdown"
	"github.com/deedglass/deedglass/internal/packs"
	"github.com/deedglass/deedglass/internal/policy"
	"github.com/deedglass/deedglass/internal/vault"
	"github.com/deedglass/deedglass/internal/witness"
)

// Regions pick the policy pack.
const (
	RegionDefault = "global" // healthcare-fhir
	RegionIndia   = "india"  // india-health (ABDM and NHCX)
)

// Where a scene's write went.
const (
	StoreSynthetic = "synthetic" // the in-memory FHIR R4 store inside this process
	StoreSandbox   = "sandbox"   // the FHIR server --fhir-url names
)

// DefaultConsoleURL is where a local `deedglass serve` puts the Console.
const DefaultConsoleURL = "http://127.0.0.1:8080"

const (
	connName   = "ehr"
	agentName  = "pilot-agent"
	pilotOwner = "pilot-owner@example.test"
	tagSystem  = "urn:deedglass:pilot"
	patientID  = "pat-synthetic-1"
	bodyIndent = "     "
	keyWidth   = 7
	scenes     = 4
)

// Options tune a run.
type Options struct {
	Out    io.Writer
	Style  ui.Style // the zero value prints plain text
	Width  int      // terminal columns (0: unknown); laid out at most ui.MaxWidth wide
	Region string   // RegionDefault ("" too) or RegionIndia
	// FHIRURL, when set, sends scene 1 to that FHIR R4 server (auth none) instead of the synthetic
	// store. Scene 2 always runs on the synthetic store: it needs a server that changes what it saves.
	FHIRURL    string
	ConsoleURL string // "" means DefaultConsoleURL
	// Dir receives REPORT.md, IT_REVIEW_PACK.md, deeds.jsonl and keys.json ("" writes nothing).
	Dir        string
	DisplayDir string // how Dir is printed ("" prints Dir)
	// Pace is called before each line with the pause it asks for; nil means no pauses.
	Pace  func(ctx context.Context, d time.Duration) error
	Now   func() time.Time                    // nil: time.Now
	NewID func(prefix string) (string, error) // nil: ids.NewString
	Seed  []byte                              // the seal key's 32-byte seed; nil: random
}

// Result is what a run found, for tests and --json.
type Result struct {
	Region      string            `json:"region"`
	Pack        PackRef           `json:"pack"`
	Store       string            `json:"store"`
	PHIMode     bool              `json:"phi_mode"`
	Lockdown    bool              `json:"lockdown"`
	Scenes      []Scene           `json:"scenes"`
	StoreWrites int               `json:"synthetic_store_writes"`
	Records     int               `json:"records"`
	ChainOK     bool              `json:"chain_intact"`
	TamperSeen  bool              `json:"tamper_detected"`
	Files       map[string]string `json:"files,omitempty"`
}

// PackRef names the policy pack the run enforced.
type PackRef struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// Scene is one scene's outcome.
type Scene struct {
	N              int      `json:"n"`
	ID             string   `json:"id"`
	Title          string   `json:"title"`
	Store          string   `json:"store,omitempty"`
	Tool           string   `json:"tool,omitempty"`
	Resource       string   `json:"resource_type,omitempty"`
	PolicyDecision string   `json:"policy_decision,omitempty"`
	PolicyRule     string   `json:"policy_rule,omitempty"`
	Decision       string   `json:"decision,omitempty"`
	Rule           string   `json:"rule_id,omitempty"`
	Reason         string   `json:"reason,omitempty"`
	Approvers      []string `json:"approvers,omitempty"`
	Required       int      `json:"required,omitempty"`
	ExpiresIn      int64    `json:"expires_in,omitempty"`
	Approval       string   `json:"approval,omitempty"`
	ConsoleURL     string   `json:"console_url,omitempty"`
	ApprovedBy     string   `json:"approved_by,omitempty"`
	Executed       bool     `json:"executed"`
	HTTPStatus     int      `json:"http_status,omitempty"`
	Target         string   `json:"target,omitempty"`
	Level          string   `json:"level,omitempty"`
	Verdict        string   `json:"verdict,omitempty"`
	Fields         []Field  `json:"fields,omitempty"`
	Action         string   `json:"action,omitempty"`
}

// Field is one compared field of a read-back.
type Field struct {
	Field    string `json:"field"`
	Expected any    `json:"expected,omitempty"`
	Observed any    `json:"observed,omitempty"`
	Match    bool   `json:"match"`
}

// ReviewPack is the hospital IT review pack (docs/healthcare/IT_REVIEW_PACK.md), as this binary
// carries it.
func ReviewPack() []byte { return bytes.Clone(healthdocs.ITReviewPack) }

// store is one FHIR server the connector writes to.
type store struct {
	kind string
	srv  *fhirtest.Server // nil for a sandbox
	inst *connector.Instance
	spec connector.ToolSpec
	base string
}

type run struct {
	ctx    context.Context
	o      Options
	st     ui.Style
	res    *Result
	pol    *policy.Compiled
	conn   *fhir.Connector
	org    string
	agent  string
	tz     string
	seeded int // writes the synthetic store held before scene 1
	sealed []sealedAction
	deeds  []byte // the sealed records, one per line
	keys   []byte // keys.json for them
}

type sealedAction struct {
	scene   int
	action  string
	tool    string
	args    json.RawMessage
	d       policy.Decision
	at      time.Time
	latency time.Duration
	witness *deed.WitnessSection
	status  int
}

// Run plays the four scenes and reports what it found.
func Run(ctx context.Context, o Options) (*Result, error) {
	if o.Out == nil {
		return nil, errors.New("health pilot: no output")
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.NewID == nil {
		o.NewID = ids.NewString
	}
	if o.ConsoleURL == "" {
		o.ConsoleURL = DefaultConsoleURL
	}
	o.ConsoleURL = strings.TrimSuffix(o.ConsoleURL, "/")
	if o.DisplayDir == "" {
		o.DisplayDir = o.Dir
	}
	region := o.Region
	switch region {
	case "", RegionDefault:
		region = RegionDefault
	case RegionIndia:
	default:
		return nil, fmt.Errorf("health pilot: unknown region %q (use %s or %s)", o.Region, RegionDefault, RegionIndia)
	}
	ctx = vault.WithRedactor(ctx, vault.NewRedactor())
	r := &run{ctx: ctx, o: o, st: o.Style, conn: fhir.New(), tz: "UTC",
		res: &Result{Region: region, Store: StoreSynthetic, PHIMode: true, Lockdown: true, Files: map[string]string{}}}
	if region == RegionIndia {
		r.tz = "Asia/Kolkata"
	}
	var err error
	if r.pol, r.res.Pack, err = loadPack(region); err != nil {
		return nil, err
	}
	if r.org, err = o.NewID("org"); err != nil {
		return nil, err
	}
	if r.agent, err = o.NewID("agt"); err != nil {
		return nil, err
	}

	// The synthetic store, and the sandbox when --fhir-url names one.
	srv := fhirtest.New()
	defer srv.Close()
	srv.Put(syntheticPatient())
	r.seeded = srv.Writes()
	synth, err := r.store(StoreSynthetic, srv.Base(), srv)
	if err != nil {
		return nil, err
	}
	first := synth
	if o.FHIRURL != "" {
		if first, err = r.store(StoreSandbox, o.FHIRURL, nil); err != nil {
			return nil, err
		}
		r.res.Store = StoreSandbox
	}

	if err := r.intro(first); err != nil {
		return r.res, err
	}
	subject := "Patient/" + patientID
	if first.kind == StoreSandbox {
		if subject, err = r.seed(first); err != nil {
			return r.res, err
		}
	}

	// 1. A write that matches.
	s1 := Scene{N: 1, ID: "write-matches", Title: "An agent writes a record, and the read-back matches"}
	if err := r.scene(s1, ""); err != nil {
		return r.res, err
	}
	if err := r.write(&s1, first, observation("8867-4", "Heart rate", 72, "/min", subject), "Observation: heart rate 72 /min, "+subject); err != nil {
		return r.res, err
	}
	r.res.Scenes = append(r.res.Scenes, s1)

	// 2. 201 Created, but the stored value differs.
	s2 := Scene{N: 2, ID: "stored-differs", Title: `"201 Created", but the stored value differs`}
	intro := "The synthetic store is set to change one value as it saves it, as a faulty interface mapping might."
	if first.kind == StoreSandbox {
		intro += " This scene runs on the synthetic store, not your sandbox."
	}
	if err := r.scene(s2, intro); err != nil {
		return r.res, err
	}
	srv.Mutate = func(typ string, res map[string]any) {
		if vq, ok := res["valueQuantity"].(map[string]any); ok && typ == "Observation" {
			vq["value"] = 37.4
		}
	}
	err = r.write(&s2, synth, observation("8310-5", "Body temperature", 38.4, "Cel", "Patient/"+patientID), "Observation: body temperature 38.4 Cel, Patient/"+patientID)
	srv.Mutate = nil
	if err != nil {
		return r.res, err
	}
	r.res.Scenes = append(r.res.Scenes, s2)

	// 3. A high-risk write waits for a person.
	s3 := Scene{N: 3, ID: "held-for-approval", Title: "A high-risk write waits for a person"}
	if err := r.scene(s3, ""); err != nil {
		return r.res, err
	}
	res, label := medicationRequest(subject), "MedicationRequest: a synthetic test order, "+subject
	if region == RegionIndia {
		res, label = preauthClaim(subject), "Claim: a pre-authorisation of INR 1,20,000 (synthetic)"
	}
	before := srv.Writes()
	if err := r.hold(&s3, first, res, label); err != nil {
		return r.res, err
	}
	if srv.Writes() != before {
		return r.res, errors.New("health pilot: the held write reached the store; please file a bug")
	}
	r.res.Scenes = append(r.res.Scenes, s3)
	r.res.StoreWrites = srv.Writes() - r.seeded

	// 4. Seal and verify.
	s4 := Scene{N: 4, ID: "seal-and-verify", Title: "Seal the records and check them offline"}
	if err := r.scene(s4, ""); err != nil {
		return r.res, err
	}
	if err := r.records(); err != nil {
		return r.res, err
	}
	r.res.Scenes = append(r.res.Scenes, s4)

	if o.Dir != "" {
		if err := r.writeFiles(); err != nil {
			return r.res, err
		}
	}
	return r.res, r.tail()
}

// loadPack reads the region's pack from the snapshot this binary carries, after checking its files against the index (and its seal, when it has one).
func loadPack(region string) (*policy.Compiled, PackRef, error) {
	name := "healthcare-fhir"
	if region == RegionIndia {
		name = "india-health"
	}
	ks, err := packs.EmbeddedKeys()
	if err != nil {
		return nil, PackRef{}, err
	}
	chk, err := packs.Verify(packs.Embedded(), ks, false)
	if err != nil {
		return nil, PackRef{}, fmt.Errorf("health pilot: the packs in this binary don't check out: %w", err)
	}
	e, ok := chk.Index.Find(name, "")
	if !ok {
		return nil, PackRef{}, fmt.Errorf("health pilot: no %s pack in this binary", name)
	}
	src := chk.Source.Files[e.Name+"/"+e.Policy()]
	c, diags := policy.Compile(src, policy.Options{})
	for _, d := range diags {
		if d.Severity == policy.SevError {
			return nil, PackRef{}, fmt.Errorf("health pilot: %s doesn't compile (line %d: %s)", name, d.Line, d.Message)
		}
	}
	if c == nil {
		return nil, PackRef{}, fmt.Errorf("health pilot: %s doesn't compile", name)
	}
	return c, PackRef{Name: e.Name, Version: e.Version}, nil
}

func (r *run) store(kind, base string, srv *fhirtest.Server) (*store, error) {
	cfg, _ := json.Marshal(map[string]any{"base_url": base, "auth": map[string]any{"kind": "none"},
		"allowed_resource_types": []string{"Claim", "MedicationRequest", "Observation", "Patient"},
		"allowed_interactions":   []string{"read", "create"}})
	env := connector.Env{Name: "dev", Egress: &egress.Policy{Env: egress.EnvDev, AllowPrivate: true}}
	norm, errs := r.conn.Validate(r.ctx, env, cfg)
	if len(errs) > 0 {
		return nil, fmt.Errorf("health pilot: the FHIR URL can't be used: %s", errs[0].Message)
	}
	connID, err := r.o.NewID("con")
	if err != nil {
		return nil, err
	}
	cid, err := ids.Parse[ids.ConnectorKind](connID)
	if err != nil {
		return nil, err
	}
	oid, err := ids.Parse[ids.OrgKind](r.org)
	if err != nil {
		return nil, err
	}
	inst := connector.NewInstance(cid, oid, connName, fhir.Type, norm, "sha256:pilot", env, noSecrets{})
	specs, err := r.conn.Tools(r.ctx, inst)
	if err != nil {
		return nil, err
	}
	s := &store{kind: kind, srv: srv, inst: inst, base: base}
	for _, sp := range specs {
		if sp.Name == "create" {
			s.spec = sp
		}
	}
	if s.spec.Name == "" {
		return nil, errors.New("health pilot: the FHIR connector has no create tool")
	}
	return s, nil
}

// noSecrets: the pilot's connectors use auth none.
type noSecrets struct{}

func (noSecrets) Open(context.Context, vault.Ref) (*vault.Secret, error) {
	return nil, vault.ErrNotFound
}

// --- the decision ---

func (r *run) decide(s *store, args json.RawMessage) (policy.Decision, policy.Decision, error) {
	enr, err := r.conn.Enrich(r.ctx, s.inst, s.spec, args)
	if err != nil {
		return policy.Decision{}, policy.Decision{}, err
	}
	var a map[string]any
	_ = json.Unmarshal(args, &a)
	in := map[string]any{
		"tool":    map[string]any{"name": connName + "." + s.spec.Name, "connector": connName, "connector_type": fhir.Type, "side_effect": string(s.spec.SideEffect)},
		"args":    a,
		"agent":   map[string]any{"id": r.agent, "name": agentName, "labels": map[string]any{lockdown.LabelConnector: connName}},
		"org":     map[string]any{"id": r.org, "timezone": r.tz, "phi_mode": true},
		"request": map[string]any{"fhir": enr["fhir"]},
		"time":    r.o.Now().UTC().Format(time.RFC3339),
	}
	raw, _ := json.Marshal(in)
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return policy.Decision{}, policy.Decision{}, err
	}
	input, p := policy.BuildInput(policy.NormalizeJSONValue(v))
	if p != nil {
		return policy.Decision{}, policy.Decision{}, fmt.Errorf("health pilot: the policy input: %s", p.Message)
	}
	pd := policy.Evaluate(r.ctx, r.pol, input, policy.EvalOptions{})
	levels := []string{string(policy.L0)}
	if s.spec.L1 != "" {
		levels = append(levels, string(policy.L1))
	}
	d, _ := lockdown.Apply(lockdown.State{Config: true}, pd, input, lockdown.Facts{PHIMode: true, ToolLevels: levels})
	return pd, d, nil
}

func (r *run) fill(sc *Scene, s *store, label string, pd, d policy.Decision) {
	sc.Store, sc.Tool = s.kind, connName+"."+s.spec.Name
	sc.Resource, _, _ = strings.Cut(label, ":")
	sc.PolicyDecision, sc.PolicyRule = string(pd.Outcome), pd.RuleID
	sc.Decision, sc.Rule, sc.Reason = string(d.Outcome), d.RuleID, d.Reason
	if d.Approval != nil {
		sc.Approvers, sc.Required, sc.ExpiresIn = d.Approval.Approvers, d.Approval.Required, d.Approval.ExpiresIn
	}
}

func (r *run) ruleRows(pd, d policy.Decision, pack string) error {
	switch d.Outcome {
	case policy.Deny:
		return r.row(350*time.Millisecond, "rule", ui.Fail, "denied by "+d.RuleID+"\n"+d.Reason)
	case policy.Allow:
		return r.row(350*time.Millisecond, "rule", ui.OK, "allowed by "+d.RuleID)
	}
	if d.RuleID == lockdown.RuleWriteNeedsPerson {
		return r.row(350*time.Millisecond, "rule", ui.Pending, fmt.Sprintf("waits for a person: %s\n%s allowed it (%s, %s); lockdown makes every write wait",
			d.RuleID, pack, pd.RuleID, levelName(d.Verify)))
	}
	return r.row(350*time.Millisecond, "rule", ui.Pending, "waits for a person: "+d.RuleID+"\n"+d.Reason)
}

// --- scenes 1 and 2: a write ---

func (r *run) write(sc *Scene, s *store, res map[string]any, label string) error {
	args, _ := json.Marshal(map[string]any{"resource_type": res["resourceType"], "resource": res})
	if err := r.row(250*time.Millisecond, "request", "", connName+".create "+label); err != nil {
		return err
	}
	pd, d, err := r.decide(s, args)
	if err != nil {
		return err
	}
	r.fill(sc, s, label, pd, d)
	if err := r.ruleRows(pd, d, r.res.Pack.Name); err != nil {
		return err
	}
	if d.Outcome == policy.Deny {
		return fmt.Errorf("health pilot: the pack refused scene %d (%s); please file a bug", sc.N, d.RuleID)
	}
	if d.Outcome == policy.RequireApproval {
		sc.ApprovedBy = pilotOwner
		if err := r.row(300*time.Millisecond, "approve", ui.OK, "countersigned by "+pilotOwner+", the pilot's stand-in admin; on your server a named person does this"); err != nil {
			return err
		}
	}
	act, err := r.o.NewID("act")
	if err != nil {
		return err
	}
	aid, err := ids.Parse[ids.ActionKind](act)
	if err != nil {
		return err
	}
	oid, _ := ids.Parse[ids.OrgKind](r.org)
	call := connector.Call{ActionID: aid, OrgID: oid, Tool: s.spec, Qualified: connName + ".create", Args: args, IdempotencyKey: act}
	at := r.o.Now()
	out, err := r.conn.Execute(r.ctx, s.inst, call)
	sa := sealedAction{scene: sc.N, action: act, tool: call.Qualified, args: args, d: d, at: at, latency: out.Latency}
	sc.Action = act
	if err != nil {
		return fmt.Errorf("health pilot: the write didn't run on the %s store: %w", s.kind, err)
	}
	st, _ := strconv.Atoi(out.Evidence["fhir_status"])
	sc.Executed, sc.HTTPStatus, sa.status = true, st, st
	sc.Target = "Observation/" + out.Evidence["fhir_id"] + " version " + out.Evidence["fhir_version"]
	if err := r.row(300*time.Millisecond, "server", "", fmt.Sprintf("%d %s · %s", st, statusText(st), sc.Target)); err != nil {
		return err
	}
	_, fs, rerr := r.conn.NativeReadBack(r.ctx, s.inst, call, out)
	sc.Level = string(policy.L1)
	glyph, text := ui.Unknown, "Checked · Can't check: the read-back didn't finish"
	switch {
	case rerr != nil:
		sc.Verdict = "UNVERIFIABLE"
	default:
		sc.Verdict = verdictOf(call.Qualified, sc.Target, fs)
		for _, f := range fs {
			sc.Fields = append(sc.Fields, Field{Field: f.Field, Expected: f.Expected, Observed: f.Observed, Match: f.Match})
		}
		switch sc.Verdict {
		case "VERIFIED":
			glyph, text = ui.OK, fmt.Sprintf("Checked · Matches: %d fields read back at version %s", len(fs), out.Evidence["fhir_version"])
		case "MISMATCH":
			glyph, text = ui.Fail, "Checked · Doesn't match: the server answered 201, but stored something else"
			for _, f := range fs {
				if !f.Match {
					text += fmt.Sprintf("\n%s  sent %v, stored %v", f.Field, jsonText(f.Expected), jsonText(f.Observed))
				}
			}
		}
	}
	sa.witness = &deed.WitnessSection{Level: sc.Level, Verdict: sc.Verdict, Method: "readback.fhir.create"}
	r.sealed = append(r.sealed, sa)
	return r.row(400*time.Millisecond, "check", glyph, text)
}

// verdictOf asks the Witness: the findings become its catalog reasons (as for any native read-back)
// and Decide gives the verdict at L1. The pilot never names a verdict itself.
func verdictOf(tool, target string, fs []connector.L1Finding) string {
	wf := make([]witness.Finding, 0, len(fs))
	for _, f := range fs {
		wf = append(wf, witness.Finding{Field: f.Field, Expected: f.Expected, Observed: f.Observed, Match: f.Match, Code: witness.Code(f.Code)})
	}
	rs := witness.NativeReasons(witness.Subject{Tool: tool}, target, wf)
	d := witness.Decide(witness.L1, map[witness.Level][]witness.Reason{witness.L1: rs}, map[witness.Level]string{witness.L1: "readback.native"})
	return string(d.Verdict)
}

// --- scene 3: a held write ---

func (r *run) hold(sc *Scene, s *store, res map[string]any, label string) error {
	args, _ := json.Marshal(map[string]any{"resource_type": res["resourceType"], "resource": res})
	if err := r.row(250*time.Millisecond, "request", "", connName+".create "+label); err != nil {
		return err
	}
	pd, d, err := r.decide(s, args)
	if err != nil {
		return err
	}
	r.fill(sc, s, label, pd, d)
	if err := r.ruleRows(pd, d, r.res.Pack.Name); err != nil {
		return err
	}
	if d.Outcome != policy.RequireApproval || d.Approval == nil {
		return fmt.Errorf("health pilot: scene 3 should wait for a person, got %s (%s); please file a bug", d.Outcome, d.RuleID)
	}
	who := strings.Join(d.Approval.Approvers, " or ")
	if d.Approval.Required > 1 {
		who = fmt.Sprintf("%d different people from %s (four-eyes)", d.Approval.Required, strings.Join(d.Approval.Approvers, ", "))
	}
	exp := time.Duration(d.Approval.ExpiresIn) * time.Second
	if err := r.row(250*time.Millisecond, "who", "", fmt.Sprintf("%s\nexpires in %s; timeout means no (lockdown)", who, minutes(exp))); err != nil {
		return err
	}
	if sc.Approval, err = r.o.NewID("apr"); err != nil {
		return err
	}
	if sc.Action, err = r.o.NewID("act"); err != nil {
		return err
	}
	sc.ConsoleURL = r.o.ConsoleURL + "/app/approvals/" + sc.Approval
	if err := r.row(250*time.Millisecond, "console", "", sc.ConsoleURL+"\nthe link a server sends to the approvers; this pilot runs none"); err != nil {
		return err
	}
	return r.row(300*time.Millisecond, "result", "", "Nothing reached the record system. It runs only after a person approves.")
}

// seed creates the synthetic Patient on a sandbox, through the same decision and connector.
func (r *run) seed(s *store) (string, error) {
	res := syntheticPatient()
	delete(res, "id")
	args, _ := json.Marshal(map[string]any{"resource_type": "Patient", "resource": res})
	_, d, err := r.decide(s, args)
	if err != nil {
		return "", err
	}
	if d.Outcome == policy.Deny {
		return "", fmt.Errorf("health pilot: the pack refused the synthetic Patient (%s)", d.RuleID)
	}
	act, err := r.o.NewID("act")
	if err != nil {
		return "", err
	}
	aid, _ := ids.Parse[ids.ActionKind](act)
	oid, _ := ids.Parse[ids.OrgKind](r.org)
	out, err := r.conn.Execute(r.ctx, s.inst, connector.Call{ActionID: aid, OrgID: oid, Tool: s.spec, Qualified: connName + ".create", Args: args, IdempotencyKey: act})
	if err != nil {
		return "", fmt.Errorf("health pilot: your sandbox didn't take the synthetic Patient: %w", err)
	}
	ref := "Patient/" + out.Evidence["fhir_id"]
	return ref, r.row(200*time.Millisecond, "seed", ui.OK, ref+" created on your sandbox (synthetic, identifier system "+tagSystem+")")
}

// --- scene 4: records ---

func (r *run) records() error {
	seed := r.o.Seed
	if seed == nil {
		seed = make([]byte, 32)
		if _, err := rand.Read(seed); err != nil {
			return fmt.Errorf("health pilot: couldn't make a key: %w", err)
		}
	}
	keyID, err := r.o.NewID("sk")
	if err != nil {
		return err
	}
	signer, err := deed.NewFileSigner(keyID, seed)
	if err != nil {
		return fmt.Errorf("health pilot: couldn't make a key: %w", err)
	}
	now := r.o.Now().UTC().Truncate(time.Millisecond)
	prev := deed.Genesis(r.org)
	var buf bytes.Buffer
	for i, a := range r.sealed {
		dig, err := deed.DigestValue(json.RawMessage(a.args))
		if err != nil {
			return err
		}
		snap := deed.ActionSnapshot{Org: r.org, Agent: deed.AgentRef{ID: r.agent, Name: agentName}, ActionID: a.action, Tool: a.tool, Mode: "gateway",
			RequestDigest: dig, Policy: deed.PolicySection{Mode: "enforce", Decision: string(a.d.Outcome)}}
		if a.d.RuleID != "" {
			rule := a.d.RuleID
			snap.Policy.RuleID = &rule
		}
		at := now.Add(time.Duration(i) * time.Second)
		lat := a.latency.Truncate(time.Millisecond)
		if r.o.Seed != nil {
			lat = 17 * time.Millisecond // a fixed run prints the same records
		}
		snap.Execution = &deed.ExecutionSnapshot{Status: "succeeded", StartedAt: at, FinishedAt: at.Add(lat)}
		snap.Witness = a.witness
		if a.d.Outcome == policy.RequireApproval {
			apr, err := r.o.NewID("apr")
			if err != nil {
				return err
			}
			usr, err := r.o.NewID("usr")
			if err != nil {
				return err
			}
			snap.Approval = &deed.ApprovalSnapshot{ID: apr, Outcome: "approved", DecidedBy: []string{usr}, DecidedAt: &at}
		}
		dr, err := deed.ForAction(snap)
		if err != nil {
			return fmt.Errorf("health pilot: record %d: %w", i+1, err)
		}
		b := dr.Body
		if b.DeedID, err = r.o.NewID("deed"); err != nil {
			return err
		}
		b.Seq, b.PrevHash, b.SealKeyID = int64(i+1), prev, keyID
		b.IssuedAt = deed.FormatTime(at.Add(lat + 3*time.Millisecond))
		env, err := deed.SealBody(signer, b)
		if err != nil {
			return fmt.Errorf("health pilot: sealing record %d: %w", i+1, err)
		}
		line, err := env.MarshalCanonical()
		if err != nil {
			return err
		}
		buf.Write(line)
		buf.WriteByte('\n')
		prev = env.BodyHash
	}
	ks, err := deed.NewKeySet("deedglass-health-pilot", []deed.KeyRecord{{ID: keyID, PublicKey: signer.PublicKey(), Status: deed.KeyActive, ActivatedAt: now.Add(-time.Minute)}})
	if err != nil {
		return err
	}
	r.res.Records = len(r.sealed)
	if err := r.row(250*time.Millisecond, "records", "", fmt.Sprintf("%d sealed with %s, a key made for this run\nthe held write is sealed when a person decides or it expires",
		len(r.sealed), ui.ShortID(keyID))); err != nil {
		return err
	}
	rep, err := deed.VerifyStream(r.ctx, bytes.NewReader(buf.Bytes()), deed.StreamOptions{Keys: ks, KeysPinned: true, Org: r.org})
	if err != nil {
		return fmt.Errorf("health pilot: the check didn't run: %w", err)
	}
	r.res.ChainOK = rep.Result == deed.ResultValid && rep.Chain.Status == deed.ChainIntact
	if r.res.ChainOK {
		err = r.row(400*time.Millisecond, "verify", ui.OK, fmt.Sprintf("%d deeds, chain intact, 1 seal key", rep.Deeds))
	} else {
		err = r.row(400*time.Millisecond, "verify", ui.Fail, fmt.Sprintf("the chain didn't check out (%s)", rep.Result))
	}
	if err != nil {
		return err
	}
	changed, seq, ok := tamper(buf.Bytes())
	if !ok {
		return errors.New("health pilot: found no byte to change")
	}
	if err := r.row(500*time.Millisecond, "tamper", "", fmt.Sprintf("one byte changed in deed %d", seq)); err != nil {
		return err
	}
	rep2, err := deed.VerifyStream(r.ctx, bytes.NewReader(changed), deed.StreamOptions{Keys: ks, KeysPinned: true, Org: r.org})
	if err != nil {
		return fmt.Errorf("health pilot: the check didn't run: %w", err)
	}
	if rep2.Result == deed.ResultInvalid && len(rep2.Failures) > 0 {
		r.res.TamperSeen = true
		err = r.row(400*time.Millisecond, "verify", ui.Fail, rep2.Failures[0].Message)
	} else {
		err = r.row(400*time.Millisecond, "verify", ui.Fail, "the changed record was not caught; please file a bug")
	}
	if err != nil {
		return err
	}
	r.deeds = buf.Bytes()
	if r.keys, err = json.MarshalIndent(ks, "", "  "); err != nil {
		return err
	}
	r.keys = append(r.keys, '\n')
	if r.o.Dir != "" {
		d := r.o.DisplayDir
		return r.row(250*time.Millisecond, "files", "", fmt.Sprintf("deeds.jsonl and keys.json in %s; check them yourself\nthere with: deedglass verify deeds.jsonl --keys keys.json", d))
	}
	return nil
}

// tamper changes one digit of the first record's latency_ms.
func tamper(b []byte) ([]byte, int64, bool) {
	out := bytes.Clone(b)
	const key = `"latency_ms":`
	i := bytes.Index(out, []byte(key))
	if i < 0 {
		return nil, 0, false
	}
	j := i + len(key)
	for j < len(out) && out[j] >= '0' && out[j] <= '9' {
		j++
	}
	if j == i+len(key) {
		return nil, 0, false
	}
	out[j-1] = '0' + (out[j-1]-'0'+1)%10
	return out, 1, true
}

// --- layout ---

func (r *run) pause(d time.Duration) error {
	if r.o.Pace == nil || d <= 0 {
		return r.ctx.Err()
	}
	return r.o.Pace(r.ctx, d)
}

func (r *run) line(d time.Duration, s string) error {
	if err := r.pause(d); err != nil {
		return err
	}
	_, err := fmt.Fprintln(r.o.Out, s)
	return err
}

func (r *run) lines(d time.Duration, ls []string) error {
	for _, l := range ls {
		if err := r.line(d, l); err != nil {
			return err
		}
		d = 0
	}
	return nil
}

func (r *run) row(d time.Duration, key, glyph, value string) error {
	return r.lines(d, r.st.KV(bodyIndent, r.o.Width, ui.Row{Key: ui.Pad(key, keyWidth), Glyph: glyph, Value: value}))
}

func (r *run) scene(s Scene, intro string) error {
	if err := r.line(700*time.Millisecond, ""); err != nil {
		return err
	}
	head := r.st.Dim(fmt.Sprintf("%d/%d", s.N, scenes)) + "  " + r.st.Bold(s.Title)
	if err := r.lines(0, ui.Wrap(head, ui.LayoutWidth(r.o.Width), 0)); err != nil {
		return err
	}
	if intro == "" {
		return nil
	}
	return r.lines(200*time.Millisecond, ui.Wrap(bodyIndent+intro, ui.LayoutWidth(r.o.Width), 0))
}

func (r *run) intro(first *store) error {
	if err := r.line(60*time.Millisecond, r.st.Header("deedglass health pilot", "synthetic data", "about 15 minutes to read")); err != nil {
		return err
	}
	if err := r.line(0, ""); err != nil {
		return err
	}
	storeLine := "an in-memory FHIR R4 store inside this binary: synthetic records only\nnothing leaves this machine"
	if first.kind == StoreSandbox {
		storeLine = "your FHIR sandbox at " + hostOf(first.base) + " (scene 1), and the in-memory\nsynthetic store (scene 2); synthetic records only"
	}
	return r.lines(60*time.Millisecond, r.st.KV("  ", r.o.Width,
		ui.Row{Key: "pack", Value: fmt.Sprintf("%s %s, enforced; PHI mode on; lockdown on", r.res.Pack.Name, r.res.Pack.Version)},
		ui.Row{Key: "store", Value: storeLine},
		ui.Row{Key: "agent", Value: agentName + ", labelled connector=" + connName},
		ui.Row{Key: "rules", Value: "decided now by the policy engine deedglass serve runs; writes go\nthrough the FHIR connector's own code and are read back"},
	))
}

func (r *run) tail() error {
	st := r.st
	ls := []string{"", st.Status(ui.OK, st.Bold("Pilot complete.")+" 4 scenes on synthetic data.")}
	if r.o.Dir != "" {
		d := r.o.DisplayDir
		ls = append(ls, st.KV("  ", r.o.Width,
			ui.Row{Key: "report", Value: filepath.Join(d, "REPORT.md") + ", ready to forward"},
			ui.Row{Key: "review", Value: filepath.Join(d, "IT_REVIEW_PACK.md") + ", for your security review"})...)
	}
	// The Next block (CLI_STYLE.md › Next steps): the pilot's own next step, then the first run.
	first := [2]string{"deedglass health pilot --fhir-url " + ui.More, "the first scene on your FHIR sandbox"}
	if r.res.Store == StoreSandbox {
		first = [2]string{"docs/guides/fhir-server.md", "put the gate in front of your agent"}
	}
	ls = append(ls, "", st.Section("Next"))
	ls = append(ls, st.Commands("  ", r.o.Width, first,
		[2]string{"deedglass start", "a wrong row count, caught on Postgres"},
		[2]string{"deedglass protect", "guard your coding agents"})...)
	return r.lines(80*time.Millisecond, ls)
}

func levelName(l policy.Level) string {
	switch l {
	case policy.L1:
		return "Checked (L1)"
	case policy.L2:
		return "Proven (L2)"
	}
	return "Claimed (L0)"
}

func minutes(d time.Duration) string {
	if d%time.Minute != 0 || d < time.Minute {
		return d.String()
	}
	if d == time.Minute {
		return "1 minute"
	}
	return fmt.Sprintf("%d minutes", int(d/time.Minute))
}

func statusText(code int) string {
	switch code {
	case 200:
		return "OK"
	case 201:
		return "Created"
	}
	return ""
}

func jsonText(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(b)
}

func hostOf(base string) string {
	if u, err := url.Parse(base); err == nil && u.Host != "" {
		return u.Host
	}
	return base
}

// --- synthetic resources (non-clinical test values) ---

// syntheticID marks every resource the pilot writes: an identifier in the pilot's own system.
func syntheticID(v string) []any {
	return []any{map[string]any{"system": tagSystem, "value": "synthetic-" + v}}
}

func syntheticPatient() map[string]any {
	return map[string]any{"resourceType": "Patient", "id": patientID, "identifier": syntheticID("patient-1"),
		"name": []any{map[string]any{"family": "Synthetic", "given": []any{"Test"}}}}
}

func observation(code, display string, value float64, unit, subject string) map[string]any {
	return map[string]any{"resourceType": "Observation", "status": "final", "identifier": syntheticID("obs-" + code),
		"code":          map[string]any{"coding": []any{map[string]any{"system": "http://loinc.org", "code": code, "display": display}}},
		"subject":       map[string]any{"reference": subject},
		"valueQuantity": map[string]any{"value": value, "unit": unit, "system": "http://unitsofmeasure.org", "code": unit}}
}

func medicationRequest(subject string) map[string]any {
	return map[string]any{"resourceType": "MedicationRequest", "status": "active", "intent": "order", "identifier": syntheticID("order-1"),
		"medicationCodeableConcept": map[string]any{"text": "Synthetic test medication (not a real product)"},
		"subject":                   map[string]any{"reference": subject}}
}

func preauthClaim(subject string) map[string]any {
	return map[string]any{"resourceType": "Claim", "status": "active", "use": "preauthorization", "identifier": syntheticID("claim-1"),
		"type":     map[string]any{"coding": []any{map[string]any{"system": "http://terminology.hl7.org/CodeSystem/claim-type", "code": "institutional"}}},
		"patient":  map[string]any{"reference": subject},
		"created":  "2026-10-27",
		"provider": map[string]any{"display": "Synthetic Test Hospital"},
		"priority": map[string]any{"coding": []any{map[string]any{"code": "normal"}}},
		"insurance": []any{map[string]any{"sequence": 1, "focal": true,
			"coverage": map[string]any{"display": "Synthetic test coverage"}}},
		"total": map[string]any{"value": 120000, "currency": "INR"}}
}

// --- files ---

func (r *run) writeFiles() error {
	if err := os.MkdirAll(r.o.Dir, 0o755); err != nil {
		return fmt.Errorf("health pilot: couldn't create %s: %w", r.o.DisplayDir, err)
	}
	files := []struct {
		key, name string
		body      []byte
	}{
		{"report", "REPORT.md", r.report()},
		{"review_pack", "IT_REVIEW_PACK.md", ReviewPack()},
		{"deeds", "deeds.jsonl", r.deeds},
		{"keys", "keys.json", r.keys},
	}
	for _, f := range files {
		p := filepath.Join(r.o.Dir, f.name)
		if err := os.WriteFile(p, f.body, 0o644); err != nil {
			return fmt.Errorf("health pilot: couldn't write %s: %w", f.name, err)
		}
		r.res.Files[f.key] = p
	}
	return nil
}
