package fhir

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/connector/fhir/pgmap"
	"github.com/deedglass/deedglass/internal/connector/postgres"
	"github.com/deedglass/deedglass/internal/jsonleaf"
)

// Proven (L2) for FHIR servers on Postgres (HC-P05, ADR-105 §7, ADR-108). The decoder reads the
// server's own database (witness_dsn in the credential); pgmap turns each transaction into resource
// versions; a transaction is bound to the action whose execution evidence names the version the
// server reported; this file judges the bound transaction. The Gate and the Witness learn nothing
// about FHIR: they see an L2Target with Versions set and catalog codes.

// Witness is the connector's witness.* config.
type Witness struct {
	Enabled bool `json:"enabled"`
	// Server names the table mapping: hapi (HAPI FHIR JPA 8.x) or medplum (Medplum 5.x).
	Server string `json:"server,omitempty"`
	// L2Timeout is how long an action waits for its transaction in the change log (default 30s).
	L2Timeout string `json:"l2_timeout,omitempty"`
	// BindGrace is how long the decoder holds a transaction no action names yet (default 5s).
	BindGrace string `json:"bind_grace,omitempty"`
	// Author is the reference Medplum writes in meta.author for this connector's client
	// (ClientApplication/<id>); checked on every bound version when set.
	Author string `json:"author,omitempty"`
}

// Defaults and bounds of witness.*.
const (
	DefaultL2Timeout = 30 * time.Second
	DefaultBindGrace = 5 * time.Second
)

var reAuthor = regexp.MustCompile(`^[A-Z][A-Za-z]{1,63}/[A-Za-z0-9\-.]{1,64}$`)

func (w Witness) l2Timeout() time.Duration {
	if d, err := time.ParseDuration(w.L2Timeout); err == nil && d > 0 {
		return d
	}
	return DefaultL2Timeout
}

func (w Witness) bindGrace() time.Duration {
	if d, err := time.ParseDuration(w.BindGrace); err == nil && d > 0 {
		return d
	}
	return DefaultBindGrace
}

// validateWitness checks and normalizes witness.* (Validate calls it).
func validateWitness(w *Witness, add func(param, format string, a ...any)) {
	if !w.Enabled {
		if w.Server != "" || w.L2Timeout != "" || w.BindGrace != "" || w.Author != "" {
			add("config.witness.enabled", "witness.server, l2_timeout, bind_grace and author apply only with witness.enabled: true.")
		}
		return
	}
	if !slices.Contains(pgmap.Servers, w.Server) {
		add("config.witness.server", "witness.server must be %s: the FHIR server whose Postgres tables Deedglass reads.", strings.Join(pgmap.Servers, " or "))
	}
	dur := func(param, raw string, def, lo, hi time.Duration) string {
		if raw == "" {
			return def.String()
		}
		d, err := time.ParseDuration(raw)
		if err != nil || d < lo || d > hi {
			add(param, "%s must be a duration from %s to %s, like %s.", strings.TrimPrefix(param, "config."), lo, hi, def)
			return raw
		}
		return d.String()
	}
	w.L2Timeout = dur("config.witness.l2_timeout", w.L2Timeout, DefaultL2Timeout, time.Second, 5*time.Minute)
	w.BindGrace = dur("config.witness.bind_grace", w.BindGrace, DefaultBindGrace, time.Second, time.Minute)
	switch {
	case w.Author == "":
	case w.Server != "medplum":
		add("config.witness.author", "witness.author applies to Medplum, which records the writing client in meta.author.")
	case !reAuthor.MatchString(w.Author):
		add("config.witness.author", "witness.author is a reference like ClientApplication/<id>.")
	}
}

// witnessed reports whether an interaction's write is version-bound on this server: create, update
// and patch always; delete only where the server reports the deleted version (HAPI's
// Content-Location; Medplum reports none).
func (st *state) witnessed(inter string) bool {
	w := st.cfg.Witness
	if !w.Enabled {
		return false
	}
	switch inter {
	case "create", "update", "patch":
		return true
	case "delete":
		return w.Server == "hapi"
	}
	return false
}

// L2Target implements connector.L2Witnesser: a version-bound target for the writes the server
// versions, nil otherwise.
func (c *Connector) L2Target(inst *connector.Instance, call connector.Call) (*connector.L2Target, error) {
	st, err := parse(inst)
	if err != nil {
		return nil, err
	}
	r, err := st.build(call.Tool.Name, call.Args)
	if err != nil || !st.witnessed(r.inter) {
		return nil, nil
	}
	return &connector.L2Target{Tool: call.Tool.Name, Versions: true, Timeout: st.cfg.Witness.l2Timeout(),
		Method: "l2.fhir." + st.cfg.Witness.Server}, nil
}

// L2Triggers implements connector.L2Witnesser: a FHIR server's triggers aren't Deedglass's to name.
func (c *Connector) L2Triggers(context.Context, *connector.Instance, []string, bool) (map[string][]string, error) {
	return nil, nil
}

// ErrWitnessDSNMissing is a witnessed fhir connector whose credential has no witness_dsn.
var ErrWitnessDSNMissing = errors.New("fhir: the credential has no witness_dsn (the FHIR server's database, for Proven)")

// witnessSecret opens the credential and returns its witness_dsn.
func witnessDSN(ctx context.Context, inst *connector.Instance) (string, error) {
	sec, err := inst.Secret(ctx)
	if err != nil {
		return "", err
	}
	defer sec.Destroy()
	raw := sec.Field("witness_dsn")
	if raw == "" {
		return "", ErrWitnessDSNMissing
	}
	return raw, nil
}

// leafKey keys the content leaf digests: HMAC-SHA-256 under the witness DSN (a secret only this
// connector's credential holds), so a frame's digests can't be matched against guesses without it.
func leafKey(dsn string) []byte {
	m := hmac.New(sha256.New, []byte(dsn))
	m.Write([]byte("deedglass fhir l2 content leaves v1"))
	return m.Sum(nil)
}

// WitnessTargetOf returns the decoder's target for a witnessed fhir connector: the server's
// database through the witness DSN (S17's parsing rules), the table mapping, the leaf key, the
// tables the publication must hold, and the author and grace settings.
func WitnessTargetOf(ctx context.Context, inst *connector.Instance) (*postgres.WitnessTarget, error) {
	st, err := parse(inst)
	if err != nil {
		return nil, err
	}
	w := st.cfg.Witness
	vm, err := pgmap.For(w.Server)
	if err != nil {
		return nil, err
	}
	raw, err := witnessDSN(ctx, inst)
	if err != nil {
		return nil, err
	}
	d, err := postgres.ParseDSN(raw, inst.Env)
	if err != nil {
		return nil, err
	}
	watched := vm.Required()
	if w.Server == "medplum" {
		watched = pgmap.MedplumRequired(st.cfg.AllowedResourceTypes)
	}
	return &postgres.WitnessTarget{Conn: d.Config, DSNHint: d.String(), Warning: d.Warning, Enabled: w.Enabled, Watched: watched,
		Versions: vm, Key: leafKey(raw), Author: w.Author, BindGrace: w.bindGrace()}, nil
}

// versionEvidence adds the binding evidence to a write's result (ADR-108 §4): the version the server
// reported as "Type/id/version", only when the write made a new version (a conditional create that
// matched and an update the server kept make none), and the token the call carried.
func versionEvidence(ev map[string]string, r *req, prior string, tok string) {
	if ev == nil {
		return
	}
	if tok != "" {
		ev[connector.EvidenceBindingToken] = tok
	}
	status, id, vid := ev["fhir_status"], ev["fhir_id"], ev["fhir_version"]
	switch {
	case len(status) != 3 || status[0] != '2' || id == "" || vid == "":
	case r.inter == "create" && ev["fhir_conditional"] == "true" && status == "200":
	case (r.inter == "update" || r.inter == "patch") && prior != "" && prior == vid:
	default:
		ev[connector.EvidenceVersionKey] = r.typ + "/" + id + "/" + vid
	}
}

// L2Judge implements connector.L2VersionJudge (ADR-108 §6): the reported version in the bound
// transaction against what the agent sent, every other version in that transaction, and the notes
// about other writers. It never decides a verdict: the Witness's catalog gives each code its class.
func (c *Connector) L2Judge(ctx context.Context, inst *connector.Instance, call connector.Call, res connector.Result, frames []connector.L2Frame) (connector.L2Judgement, error) {
	st, err := parse(inst)
	if err != nil {
		return connector.L2Judgement{}, err
	}
	vm, err := pgmap.For(st.cfg.Witness.Server)
	if err != nil {
		return connector.L2Judgement{}, err
	}
	r, err := st.build(call.Tool.Name, call.Args)
	if err != nil {
		return connector.L2Judgement{}, err
	}
	j := connector.L2Judgement{Method: "l2.fhir." + vm.Server()}
	want := res.Evidence[connector.EvidenceVersionKey]
	resource, version := splitKey(want)
	me := call.ActionID.String()
	var bound, tagged, reused, oob []connector.L2Frame
	for _, f := range frames {
		switch f.Class {
		case "bound":
			if f.ActionID == me {
				bound = append(bound, f)
			}
		case "version_tag_mismatch":
			if f.ActionID == me {
				tagged = append(tagged, f)
			}
		case "request_id_reused":
			if f.ActionID == me {
				reused = append(reused, f)
			}
		case "unattributed":
			oob = append(oob, f)
		}
	}
	add := func(code string, vars map[string]any) *connector.L2Finding {
		j.Findings = append(j.Findings, connector.L2Finding{Code: code, Vars: vars})
		return &j.Findings[len(j.Findings)-1]
	}
	if len(bound) == 0 {
		if len(tagged) > 0 {
			j.Primary = tagged[0].ID
			add("version_tag_mismatch", map[string]any{"resource": resource, "version": version})
		}
		return j, nil // nothing bound yet: the wait goes on
	}
	if len(bound) > 1 {
		add("multiple_transactions", map[string]any{"n": len(bound)})
	}
	p := bound[0]
	j.Primary = p.ID
	vf := vm.Map(p.Tables, p.Changes, p.Flags)
	if vf.Layout != "" {
		j.Method = "l2.fhir." + vf.Layout
	}
	if vf.Unknown != "" {
		add("fhir_schema_unknown", map[string]any{"table": vf.Unknown})
		return j, nil
	}
	var mine *connector.ResourceVersion
	var extras []string
	for i := range vf.Versions {
		v := &vf.Versions[i]
		if v.Key() == want && mine == nil {
			mine = v
			continue
		}
		extras = append(extras, v.Resource()+" version "+v.Version)
	}
	if mine == nil {
		// The decoder bound the frame by this key, so it is there unless the images were cut.
		add("keys_unknown", map[string]any{"table": "the server's version table", "why": "the transaction has more changes than decoder.max_images_per_tx"})
		return j, nil
	}
	if len(extras) > 0 {
		add("fhir_extra_versions", map[string]any{"n": len(extras) + 1, "resources": strings.Join(extras, ", ")}).Evidence =
			map[string]any{"reported": want, "extra": extras}
	}
	if vf.Truncated {
		add("keys_unknown", map[string]any{"table": "the server's version table", "why": "the transaction has more changes than decoder.max_images_per_tx"})
	}
	key, err := witnessDSN(ctx, inst)
	if err != nil {
		return connector.L2Judgement{}, err
	}
	paths, gap := c.compareVersion(st, r, *mine, leafKey(key))
	switch {
	case gap != "":
		add("large_object_not_replicated", map[string]any{"resource": resource, "version": version, "why": gap})
	case len(paths) > 0:
		f := add("version_field_changed", map[string]any{"resource": resource, "version": version, "path": paths[0], "tool": toolName(call)})
		for _, p := range paths {
			f.Diff = append(f.Diff, connector.L2Diff{Path: p, Expected: "the value sent", Observed: "another value"})
		}
		f.Evidence = map[string]any{"paths": paths}
	default:
		add("version_matched", map[string]any{"resource": resource, "version": version, "tool": toolName(call)})
	}
	// Notes: other writers of the same resource while the call was open, and forged tokens.
	for _, f := range oob {
		ov := vm.Map(f.Tables, f.Changes, f.Flags)
		for _, v := range ov.Versions {
			if v.Resource() == mine.Resource() && !f.CommitTime.After(p.CommitTime) {
				add("out_of_band", map[string]any{"resource": v.Resource(), "version": v.Version}).Evidence = map[string]any{"frame": f.ID}
			}
		}
	}
	for _, f := range reused {
		rv := vm.Map(f.Tables, f.Changes, f.Flags)
		for _, v := range rv.Versions {
			if v.Tag == token(call.ActionID) {
				add("request_id_reused", map[string]any{"resource": v.Resource(), "version": v.Version}).Evidence = map[string]any{"frame": f.ID}
			}
		}
	}
	return j, nil
}

// toolName is the call's qualified tool name ("fhir.update").
func toolName(call connector.Call) string {
	if call.Qualified != "" {
		return call.Qualified
	}
	return Type + "." + call.Tool.Name
}

func splitKey(k string) (resource, version string) {
	i := strings.LastIndexByte(k, '/')
	if i < 0 {
		return k, ""
	}
	return k[:i], k[i+1:]
}

// compareVersion compares the stored version with what the call sent (ADR-105 §6's normalising, on
// leaf digests): id and meta are the server's own, a generated narrative is the server's, and
// fields only the server has are allowed. It returns the differing paths, or why the content can't
// be compared.
func (c *Connector) compareVersion(st *state, r *req, v connector.ResourceVersion, key []byte) (paths []string, gap string) {
	if r.inter == "delete" {
		if v.Op != "delete" {
			return []string{"(deleted)"}, ""
		}
		return nil, ""
	}
	if v.Content == nil {
		switch v.Unreadable {
		case "large_object":
			return nil, "a large object"
		case "unchanged_toast":
			return nil, "a value the change log didn't repeat"
		}
		return nil, "storage outside the row"
	}
	var got pgmap.Content
	if err := json.Unmarshal(v.Content, &got); err != nil {
		return nil, "content Deedglass can't read"
	}
	observed := jsonleaf.Leaves(got.Leaves).Without("/id", "/meta")
	generated := jsonleaf.Digest(key, "/text/status", "s:generated")
	if observed["/text/status"] == generated {
		observed = observed.Without("/text")
	}
	if r.inter == "patch" {
		return patchPaths(r.patch, observed, key), ""
	}
	sent, err := jsonleaf.Of(key, sentResource(r))
	if err != nil {
		return nil, "content Deedglass can't read"
	}
	sent = sent.Without("/id", "/meta")
	if sent["/text/status"] == generated {
		sent = sent.Without("/text")
	}
	paths = jsonleaf.Diff(sent, observed)
	paths = append(paths, metaPaths(st, r, got, key)...)
	sort.Strings(paths)
	return slices.Compact(paths), ""
}

// sentResource is the resource the call sent, decoded with exact numbers.
func sentResource(r *req) any {
	b, _ := json.Marshal(r.resource)
	var m map[string]any
	_ = decodeJSON(b, &m)
	return m
}

// metaPaths compares the tags, profiles and security labels the agent sent (and only those) with
// the stored version's: HAPI's rebuilt from its tag tables, Medplum's from the document's leaves.
// The server's own tag systems (server_tag_systems) are left out on both sides.
func metaPaths(st *state, r *req, got pgmap.Content, key []byte) []string {
	meta, _ := r.resource["meta"].(map[string]any)
	if meta == nil {
		return nil
	}
	var out []string
	if hasHAPIMeta(got) {
		if got.Meta == nil || got.Meta.Unresolved > 0 {
			// A tag's definition was written in an earlier transaction, so the rebuilt meta is
			// incomplete: meta isn't compared here (the read-back compares it).
			return nil
		}
		// HAPI: compare the rebuilt lists (codes in clear) as sets.
		if want := codings(meta["tag"], st.cfg.ServerTagSystems); want != nil && !sameSet(want, codingKeys(got.Meta.Tags, st.cfg.ServerTagSystems)) {
			out = append(out, "meta.tag")
		}
		if want := codings(meta["security"], st.cfg.ServerTagSystems); want != nil && !sameSet(want, codingKeys(got.Meta.Security, st.cfg.ServerTagSystems)) {
			out = append(out, "meta.security")
		}
		if ps, ok := meta["profile"].([]any); ok && !sameSet(strs(ps), got.Meta.Profiles) {
			out = append(out, "meta.profile")
		}
		return out
	}
	// Medplum (meta inside the document): the agent's meta lists, leaf by leaf.
	for _, k := range []string{"tag", "profile", "security"} {
		v, ok := meta[k]
		if !ok {
			continue
		}
		want, err := jsonleaf.Of(key, map[string]any{"meta": map[string]any{k: v}})
		if err != nil {
			continue
		}
		obs := jsonleaf.Leaves{}
		for p, d := range got.Leaves {
			if jsonleaf.Under(p, "/meta/"+k) {
				obs[p] = d
			}
		}
		if len(jsonleaf.Diff(want, obs)) > 0 {
			out = append(out, "meta."+k)
		}
	}
	return out
}

// hasHAPIMeta: the version's meta was rebuilt from columns (HAPI keeps id and meta out of the
// document, so its leaves hold no /meta).
func hasHAPIMeta(c pgmap.Content) bool {
	for p := range c.Leaves {
		if jsonleaf.Under(p, "/meta") {
			return false
		}
	}
	return true
}

func codings(v any, server []string) []string {
	l, ok := v.([]any)
	if !ok {
		return nil
	}
	out := []string{}
	for _, e := range l {
		m, _ := e.(map[string]any)
		sys, _ := m["system"].(string)
		code, _ := m["code"].(string)
		if slices.Contains(server, sys) {
			continue
		}
		out = append(out, sys+"|"+code)
	}
	return out
}

func codingKeys(cs []pgmap.Coding, server []string) []string {
	out := []string{}
	for _, c := range cs {
		if !slices.Contains(server, c.System) {
			out = append(out, c.System+"|"+c.Code)
		}
	}
	return out
}

func strs(l []any) []string {
	out := []string{}
	for _, e := range l {
		if s, ok := e.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func sameSet(a, b []string) bool {
	x, y := slices.Clone(a), slices.Clone(b)
	sort.Strings(x)
	sort.Strings(y)
	return slices.Equal(slices.Compact(x), slices.Compact(y))
}

// patchPaths checks a JSON Patch's add, replace and remove operations on the stored leaves: the
// value at an add or replace path is exactly what the patch set; nothing is left at a remove path.
func patchPaths(ops []any, observed jsonleaf.Leaves, key []byte) []string {
	var out []string
	for _, o := range ops {
		m, _ := o.(map[string]any)
		op, _ := m["op"].(string)
		p, _ := m["path"].(string)
		field := strings.ReplaceAll(strings.TrimPrefix(p, "/"), "/", ".")
		if strings.HasPrefix(p, "/meta") || strings.HasPrefix(p, "/id") || strings.HasSuffix(p, "/-") {
			continue
		}
		under := jsonleaf.Leaves{}
		for q, d := range observed {
			if jsonleaf.Under(q, p) {
				under[q] = d
			}
		}
		switch op {
		case "add", "replace":
			want, err := jsonleaf.Of(key, nest(p, m["value"]))
			if err != nil {
				continue
			}
			if !mapsEqual(want, under) {
				out = append(out, field)
			}
		case "remove":
			if len(under) > 0 {
				out = append(out, field)
			}
		}
	}
	return out
}

// nest puts v at pointer p in an otherwise empty document, so its leaves carry p's pointers.
func nest(p string, v any) any {
	segs := jsonleaf.Segments(p)
	for i := len(segs) - 1; i >= 0; i-- {
		v = map[string]any{segs[i]: v}
	}
	return v
}

func mapsEqual(a, b jsonleaf.Leaves) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

// String for errors.
func (w Witness) String() string {
	return fmt.Sprintf("witness{enabled:%v server:%s}", w.Enabled, w.Server)
}
