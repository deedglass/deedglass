// Package fhirtest is a small in-memory FHIR R4 server for tests (HC-P02): the connector's unit
// tests and the attack suite's group I point a fhir connector at it. It answers the way HAPI FHIR
// JPA v8.12 did in HC-P05's spike (internal/connector/fhir/testdata/hapi holds the recorded
// answers): 201 with Location and ETag for a create, 200 with the existing resource for a
// conditional create that matches, the same version for an update that changes nothing, 409 for a
// stale If-Match, 410 after a delete. Knobs make it lie (Mutate), fail after committing (FailNext)
// or store the request id in meta.source (StoreRequestID). Synthetic data only.
package fhirtest

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
)

// Server is the in-memory server. Its fields may be set before or between requests.
type Server struct {
	*httptest.Server

	mu       sync.Mutex
	versions map[string][]map[string]any // "Type/id" → versions (nil entry: deleted)
	nextID   int
	hits     int
	writes   int

	// Mutate, when set, changes a resource as it is stored (a server that alters what it was sent).
	Mutate func(typ string, res map[string]any)
	// FailNext answers the next write with this status after committing it (a 5xx after commit).
	FailNext int
	// StoreRequestID keeps the X-Request-ID header in meta.source as "<base>#<id>", as HAPI does with
	// store_meta_source_information=SOURCE_URI_AND_REQUEST_ID.
	StoreRequestID bool
	// Requests records every request's method, path and headers.
	Requests []Request

	// With UsePostgres (postgres.go): HAPI's tables in a database, and the knobs that make the
	// server commit more than the call asked for.
	db     *pgx.Conn
	resIDs map[string]int64
	tx     []pending
	// PlaceholderRefs: a create whose subject names an unknown Patient commits that Patient too,
	// in the same transaction (HAPI's auto_create_placeholder_reference_targets; I07).
	PlaceholderRefs bool
	// LegacyText stores the text in a large object (JSONC), as HAPI did before 7.0 (I08).
	LegacyText bool
	// InterleaveNext: the next update first commits another writer's version of the same resource
	// in its own transaction, while the call is open (I05).
	InterleaveNext bool
	// ForgeNext: the next write that carries an X-Request-ID first commits another client's create
	// carrying the same request id, in its own transaction, while the call is open (I06).
	ForgeNext bool
	// DBErr is the last error writing the Postgres store.
	DBErr error
}

// Request is one request the server saw.
type Request struct {
	Method, Path string
	Header       http.Header
	Body         []byte
}

// New starts a server; Close stops it.
func New() *Server {
	s := &Server{versions: map[string][]map[string]any{}, nextID: 1000}
	s.Server = httptest.NewServer(http.HandlerFunc(s.serve))
	return s
}

// Base is the FHIR base URL (…/fhir).
func (s *Server) Base() string { return s.URL + "/fhir" }

// Hits is how many requests the server answered.
func (s *Server) Hits() int { s.mu.Lock(); defer s.mu.Unlock(); return s.hits }

// Writes is how many versions the server has stored (creates, updates, patches, deletes).
func (s *Server) Writes() int { s.mu.Lock(); defer s.mu.Unlock(); return s.writes }

// Put stores a resource directly, as another writer would (no request).
func (s *Server) Put(res map[string]any) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	typ, _ := res["resourceType"].(string)
	id, _ := res["id"].(string)
	if id == "" {
		s.nextID++
		id = strconv.Itoa(s.nextID)
	}
	out := s.store(typ, id, clone(res), "").(string)
	s.commit()
	return out
}

// Current returns a resource's current version, or nil.
func (s *Server) Current(typ, id string) map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	vs := s.versions[typ+"/"+id]
	if len(vs) == 0 {
		return nil
	}
	return clone(vs[len(vs)-1])
}

// Versions is how many versions a resource has (deletes included).
func (s *Server) Versions(typ, id string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.versions[typ+"/"+id])
}

func clone(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	b, _ := json.Marshal(m)
	var out map[string]any
	_ = json.Unmarshal(b, &out)
	return out
}

// store writes a new version (res nil: a delete) and returns its id; s.mu is held.
func (s *Server) store(typ, id string, res map[string]any, reqID string) any {
	key := typ + "/" + id
	vid := strconv.Itoa(len(s.versions[key]) + 1)
	if res != nil {
		if s.Mutate != nil {
			s.Mutate(typ, res)
		}
		res["id"] = id
		meta := map[string]any{"versionId": vid, "lastUpdated": time.Now().UTC().Format("2006-01-02T15:04:05.000+00:00")}
		if s.StoreRequestID && reqID != "" {
			meta["source"] = "#" + reqID
		}
		res["meta"] = meta
	}
	s.versions[key] = append(s.versions[key], res)
	s.writes++
	if s.db != nil {
		if reqID == "" || len(reqID) > 16 || strings.ContainsAny(reqID, "/.;") {
			reqID = newRequestID() // HAPI makes its own when the header is missing or unusable
		}
		s.tx = append(s.tx, pending{typ: typ, id: id, vid: vid, res: clone(res), reqID: reqID})
	}
	return id
}

// commit writes the versions stored since the last commit in one transaction (postgres.go).
func (s *Server) commit() {
	if len(s.tx) == 0 {
		return
	}
	if err := s.persist(s.tx...); err != nil {
		s.DBErr = err
	}
	s.tx = nil
}

// another commits a write by another client before the call's own (InterleaveNext, ForgeNext).
func (s *Server) another(typ, id string, res map[string]any, reqID string) {
	if id == "" {
		s.nextID++
		id = strconv.Itoa(s.nextID)
	}
	s.store(typ, id, res, reqID)
	s.commit()
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hits++
	s.Requests = append(s.Requests, Request{Method: r.Method, Path: r.URL.Path, Header: r.Header.Clone(), Body: body})
	parts := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, "/fhir"), "/"), "/")
	reqID := r.Header.Get("X-Request-ID")
	if reqID != "" {
		w.Header().Set("X-Request-ID", reqID)
	}
	w.Header().Set("Content-Type", "application/fhir+json;charset=UTF-8")
	if parts[0] == "metadata" {
		writeJSON(w, 200, map[string]any{"resourceType": "CapabilityStatement", "fhirVersion": "4.0.1",
			"rest": []any{map[string]any{"resource": []any{map[string]any{"type": "Observation"}, map[string]any{"type": "Patient"}, map[string]any{"type": "Condition"}}}}})
		return
	}
	typ := parts[0]
	fail := func(status int) bool {
		if s.FailNext == 0 {
			return false
		}
		st := s.FailNext
		s.FailNext = 0
		outcome(w, st, "exception", "The server failed after it committed.")
		_ = status
		return true
	}
	switch {
	case r.Method == http.MethodPost && len(parts) == 1:
		var res map[string]any
		if json.Unmarshal(body, &res) != nil || res["resourceType"] != typ {
			outcome(w, 400, "processing", "The body isn't a "+typ+".")
			return
		}
		if q := r.Header.Get("If-None-Exist"); q != "" {
			if id, vid, ok := s.match(typ, q); ok {
				s.answer(w, 200, typ, id, vid)
				return
			}
		}
		if s.ForgeNext && reqID != "" {
			s.ForgeNext = false
			s.another(typ, "", map[string]any{"resourceType": typ, "status": "final", "code": map[string]any{"text": "forged"}}, reqID)
		}
		s.nextID++
		id := strconv.Itoa(s.nextID)
		if pid, ok := s.placeholder(res); ok {
			s.store("Patient", pid, map[string]any{"resourceType": "Patient", "extension": []any{map[string]any{
				"url": "http://hapifhir.io/fhir/StructureDefinition/resource-placeholder", "valueBoolean": true}}}, reqID)
		}
		s.store(typ, id, res, reqID)
		s.commit()
		if fail(201) {
			return
		}
		s.answer(w, 201, typ, id, "1")
	case (r.Method == http.MethodPut || r.Method == http.MethodPatch) && len(parts) == 2:
		id := parts[1]
		key := typ + "/" + id
		vs := s.versions[key]
		var cur map[string]any
		if len(vs) > 0 {
			cur = vs[len(vs)-1]
		}
		if im := r.Header.Get("If-Match"); im != "" && (cur == nil || `W/"`+strconv.Itoa(len(vs))+`"` != im) {
			outcome(w, 409, "processing", "Trying to update "+key+" but this is not the current version.")
			return
		}
		var res map[string]any
		if r.Method == http.MethodPatch {
			if cur == nil {
				outcome(w, 404, "not-found", key+" isn't known.")
				return
			}
			res = clone(cur)
			var ops []map[string]any
			if json.Unmarshal(body, &ops) != nil {
				outcome(w, 400, "processing", "Only JSON Patch is supported here.")
				return
			}
			for _, op := range ops {
				p := strings.TrimPrefix(op["path"].(string), "/")
				switch op["op"] {
				case "replace", "add":
					res[p] = op["value"]
				case "remove":
					delete(res, p)
				}
			}
		} else if json.Unmarshal(body, &res) != nil || res["resourceType"] != typ || res["id"] != id {
			outcome(w, 400, "processing", "The body's id must equal the URL's.")
			return
		}
		if cur != nil && same(cur, res) {
			s.answer(w, 200, typ, id, strconv.Itoa(len(vs))) // a no-change update keeps the version
			return
		}
		status := 200
		if cur == nil {
			status = 201
		}
		if s.InterleaveNext && cur != nil {
			s.InterleaveNext = false
			o := clone(cur)
			o["status"] = "registered"
			s.another(typ, id, o, "")
		}
		if s.ForgeNext && reqID != "" {
			s.ForgeNext = false
			s.another(typ, "", map[string]any{"resourceType": typ, "status": "final", "code": map[string]any{"text": "forged"}}, reqID)
		}
		s.store(typ, id, res, reqID)
		s.commit()
		if fail(status) {
			return
		}
		s.answer(w, status, typ, id, strconv.Itoa(len(s.versions[key])))
	case r.Method == http.MethodDelete && len(parts) == 2:
		key := typ + "/" + parts[1]
		if vs := s.versions[key]; len(vs) > 0 && vs[len(vs)-1] != nil {
			s.store(typ, parts[1], nil, reqID)
			s.commit()
			if fail(200) {
				return
			}
			// HAPI names the deleted version in Content-Location.
			w.Header().Set("Content-Location", s.Base()+"/"+key+"/_history/"+strconv.Itoa(len(s.versions[key])))
		}
		outcome(w, 200, "informational", "Deleted.")
	case r.Method == http.MethodGet && len(parts) == 2:
		vs := s.versions[typ+"/"+parts[1]]
		switch {
		case len(vs) == 0:
			outcome(w, 404, "not-found", "Not known.")
		case vs[len(vs)-1] == nil:
			outcome(w, 410, "deleted", "Deleted.")
		default:
			s.answer(w, 200, typ, parts[1], strconv.Itoa(len(vs)))
		}
	case r.Method == http.MethodGet && len(parts) == 4 && parts[2] == "_history":
		vs := s.versions[typ+"/"+parts[1]]
		n, err := strconv.Atoi(parts[3])
		if err != nil || n < 1 || n > len(vs) || vs[n-1] == nil {
			outcome(w, 404, "not-found", "No such version.")
			return
		}
		s.answer(w, 200, typ, parts[1], parts[3])
	case r.Method == http.MethodGet && (len(parts) == 2 && parts[1] == "_history" || len(parts) == 3 && parts[2] == "_history"):
		since, _ := time.Parse(time.RFC3339, r.URL.Query().Get("_since"))
		var entries []any
		for key, vs := range s.versions {
			if !strings.HasPrefix(key, typ+"/") || len(parts) == 3 && key != typ+"/"+parts[1] {
				continue
			}
			for _, v := range vs {
				if v == nil {
					continue
				}
				meta, _ := v["meta"].(map[string]any)
				lu, _ := time.Parse("2006-01-02T15:04:05.000+00:00", fmt.Sprint(meta["lastUpdated"]))
				if lu.Before(since) {
					continue
				}
				entries = append(entries, map[string]any{"resource": clone(v)})
			}
		}
		writeJSON(w, 200, map[string]any{"resourceType": "Bundle", "type": "history", "total": len(entries), "entry": entries})
	default:
		outcome(w, 400, "not-supported", "Not supported here.")
	}
}

// match finds the resource a conditional create's If-None-Exist names (identifier=system|value).
func (s *Server) match(typ, q string) (id, vid string, ok bool) {
	v, err := url.ParseQuery(q)
	if err != nil {
		return "", "", false
	}
	want := v.Get("identifier")
	for key, vs := range s.versions {
		if !strings.HasPrefix(key, typ+"/") || len(vs) == 0 || vs[len(vs)-1] == nil {
			continue
		}
		ids, _ := vs[len(vs)-1]["identifier"].([]any)
		for _, x := range ids {
			m, _ := x.(map[string]any)
			if fmt.Sprintf("%v|%v", m["system"], m["value"]) == want {
				return strings.TrimPrefix(key, typ+"/"), strconv.Itoa(len(vs)), true
			}
		}
	}
	return "", "", false
}

func same(a, b map[string]any) bool {
	x, y := clone(a), clone(b)
	delete(x, "meta")
	delete(y, "meta")
	return reflect.DeepEqual(x, y)
}

func (s *Server) answer(w http.ResponseWriter, status int, typ, id, vid string) {
	n, _ := strconv.Atoi(vid)
	res := s.versions[typ+"/"+id][n-1]
	if status == 201 {
		w.Header().Set("Location", s.Base()+"/"+typ+"/"+id+"/_history/"+vid)
	}
	w.Header().Set("ETag", `W/"`+vid+`"`)
	writeJSON(w, status, res)
}

func outcome(w http.ResponseWriter, status int, code, diag string) {
	sev := "error"
	if status < 300 {
		sev = "information"
	}
	writeJSON(w, status, map[string]any{"resourceType": "OperationOutcome", "issue": []any{map[string]any{"severity": sev, "code": code, "diagnostics": diag}}})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
