// Package recordexport writes sealed Deedglass records in other formats (TY-06, DL-109): the
// OpenTelemetry GenAI semantic conventions (OTLP/JSON), OCSF 1.9 API Activity events, a W3C
// PROV-JSON document, and FHIR R4 AuditEvent and Provenance (internal/fhirexport).
//
// An export is a view. The sealed record stays the primary form; its format knows nothing about
// any of these standards, and `deedglass verify` checks the record, never an export. Each format
// has a mapping doc under docs/exports/ (FHIR: docs/EXPORTS.md) that lists what is lost or
// approximated. The package is pure (standard library and fhirexport only) and offline: it reads
// record envelopes and writes bytes, with no clock, network or database, so the same records
// always give the same output.
package recordexport

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

// Formats, as `deedglass export --format` names them.
const (
	FormatOTel = "otel-genai"
	FormatOCSF = "ocsf"
	FormatPROV = "prov-json"
	FormatFHIR = "fhir"
)

// Formats lists every format, in the order help shows them.
var Formats = []string{FormatOTel, FormatOCSF, FormatPROV, FormatFHIR}

// The pinned versions of the standards each format follows. A change here is a change to the
// export and needs its mapping doc, goldens and schema files updated together.
const (
	// OTelSemconvVersion is the OpenTelemetry semantic conventions release whose gen_ai registry
	// the export follows (v1.41.0, 2026-04-28; status Development). Later releases moved the GenAI
	// conventions to open-telemetry/semantic-conventions-genai, which had no release on 2026-10-19.
	OTelSemconvVersion = "1.41.0"
	OTelSchemaURL      = "https://opentelemetry.io/schemas/1.41.0"
	// OCSFVersion is the OCSF schema release (1.9.0, 2026-08-03): API Activity (6003) with the
	// ai_operation and security_control profiles.
	OCSFVersion = "1.9.0"
	// PROVJSONVersion names the W3C Member Submission "PROV-JSON" of 2013-04-24.
	PROVJSONVersion = "2013-04-24"
)

// ErrFormat is returned for a format this package doesn't write.
var ErrFormat = errors.New("recordexport: the format must be otel-genai, ocsf, prov-json or fhir")

// Options are the export's own facts. None of them comes from the records.
type Options struct {
	// Producer is the Deedglass version writing the export (OTel scope version, OCSF
	// metadata.product.version, PROV bundle attribute).
	Producer string
	// Gateway is the Deedglass server's base URL, when known (FHIR only; see fhirexport.Options).
	Gateway string
}

// Stats counts what an export read and wrote.
type Stats struct {
	Records int `json:"records"` // records read
	Written int `json:"written"` // spans, events or resources written (PROV: records described)
	Skipped int `json:"skipped"` // records with nothing to write in this format
}

// Writer converts records one by one. Close writes anything held back (PROV writes its one
// document there) and must be called once at the end.
type Writer interface {
	Write(envelope []byte) error
	Close() error
	Stats() Stats
}

// NewWriter returns a Writer for format.
func NewWriter(w io.Writer, format string, opt Options) (Writer, error) {
	switch format {
	case FormatOTel:
		return &otelWriter{w: w, opt: opt}, nil
	case FormatOCSF:
		return &ocsfWriter{w: w, opt: opt}, nil
	case FormatPROV:
		return newPROVWriter(w, opt), nil
	case FormatFHIR:
		return &fhirWriter{w: w, opt: opt}, nil
	}
	return nil, ErrFormat
}

// Extension is the file name suffix an export in format gets by default.
func Extension(format string) string {
	switch format {
	case FormatOTel:
		return ".otlp.jsonl"
	case FormatOCSF:
		return ".ocsf.ndjson"
	case FormatPROV:
		return ".prov.json"
	case FormatFHIR:
		return ".fhir.ndjson"
	}
	return ""
}

// ContentType is the media type of an export in format.
func ContentType(format string) string {
	switch format {
	case FormatPROV:
		return "application/json"
	case FormatFHIR:
		return "application/fhir+ndjson"
	}
	return "application/x-ndjson"
}

// --- the record, as the export reads it (spec/v1/DEED_SPEC.md §4 and §9) ---

type ref struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

type body struct {
	V          int     `json:"v"`
	Kind       string  `json:"kind"`
	DeedID     string  `json:"deed_id"`
	Org        string  `json:"org"`
	Seq        int64   `json:"seq"`
	PrevHash   string  `json:"prev_hash"`
	IssuedAt   string  `json:"issued_at"`
	OnBehalfOf *string `json:"on_behalf_of"`
	Session    *string `json:"session"`
	Agent      *struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"agent"`
	Action *struct {
		ID            string  `json:"id"`
		Tool          string  `json:"tool"`
		Connector     *string `json:"connector"`
		Mode          string  `json:"mode"`
		RequestDigest string  `json:"request_digest"`
		IntentHash    *string `json:"intent_hash"`
		IntentSigned  bool    `json:"intent_signed"`
	} `json:"action"`
	Policy *struct {
		ID          *string `json:"id"`
		VersionID   *string `json:"version_id"`
		VersionHash *string `json:"version_hash"`
		Mode        string  `json:"mode"`
		Decision    string  `json:"decision"`
		RuleID      *string `json:"rule_id"`
	} `json:"policy"`
	Approval *struct {
		ID        string   `json:"id"`
		Outcome   string   `json:"outcome"`
		DecidedBy []string `json:"decided_by"`
		DecidedAt *string  `json:"decided_at"`
		Deed      *string  `json:"deed"`
	} `json:"approval"`
	Execution *struct {
		Status       string  `json:"status"`
		StartedAt    string  `json:"started_at"`
		FinishedAt   string  `json:"finished_at"`
		LatencyMS    int64   `json:"latency_ms"`
		ResultDigest *string `json:"result_digest"`
		ErrorCode    *string `json:"error_code"`
	} `json:"execution"`
	Witness *struct {
		Level        string  `json:"level"`
		Verdict      string  `json:"verdict"`
		Method       string  `json:"method"`
		DetailDigest *string `json:"detail_digest"`
	} `json:"witness"`
	Cost *struct {
		Amount   int64  `json:"amount"`
		Currency string `json:"currency"`
	} `json:"cost"`
	Admin *struct {
		Event      string  `json:"event"`
		Action     string  `json:"action"`
		Actor      ref     `json:"actor"`
		Target     *ref    `json:"target"`
		Reason     *string `json:"reason"`
		DataDigest *string `json:"data_digest"`
	} `json:"admin"`
	Amends    *string `json:"amends"`
	SealKeyID string  `json:"seal_key_id"`
}

type envelope struct {
	ID        string `json:"id"`
	Body      body   `json:"body"`
	BodyHash  string `json:"body_hash"`
	Seal      string `json:"seal"`
	SealKeyID string `json:"seal_key_id"`
}

// parse reads one envelope. It checks only what the export relies on: the record's shape is the
// verifier's job (`deedglass verify`), and the export never repairs a record.
func parse(raw []byte) (envelope, error) {
	var e envelope
	if err := json.Unmarshal(raw, &e); err != nil {
		return e, fmt.Errorf("recordexport: a line isn't a record envelope: %w", err)
	}
	b := &e.Body
	if b.DeedID == "" || b.Org == "" || b.Seq < 1 {
		return e, errors.New("recordexport: a record has no deed_id, org or seq")
	}
	if _, ok := parseTime(b.IssuedAt); !ok {
		return e, fmt.Errorf("recordexport: record %s has no valid issued_at", b.DeedID)
	}
	switch b.Kind {
	case "action":
		if b.Agent == nil || b.Action == nil || b.Policy == nil {
			return e, fmt.Errorf("recordexport: action record %s lacks agent, action or policy", b.DeedID)
		}
	case "amendment":
		if b.Agent == nil || b.Action == nil || b.Witness == nil || b.Amends == nil {
			return e, fmt.Errorf("recordexport: amendment %s lacks agent, action, witness or amends", b.DeedID)
		}
	case "admin":
		if b.Admin == nil {
			return e, fmt.Errorf("recordexport: admin record %s lacks admin", b.DeedID)
		}
	default:
		return e, fmt.Errorf("recordexport: record %s has kind %q", b.DeedID, b.Kind)
	}
	return e, nil
}

const timeLayout = "2006-01-02T15:04:05.000Z"

func parseTime(s string) (time.Time, bool) {
	t, err := time.Parse(timeLayout, s)
	return t, err == nil
}

// mustTime is for times parse or the record schema already checked; a bad one reads as the zero
// instant rather than failing a whole export.
func mustTime(s string) time.Time {
	t, _ := parseTime(s)
	return t
}

// derivedID is a stable hex id of n bytes for a Deedglass id, under a domain tag, so the same
// record always gets the same OTel trace and span ids.
func derivedID(tag, id string, n int) string {
	h := sha256.Sum256([]byte("deedglass/export/" + tag + "\n" + id))
	return hex.EncodeToString(h[:n])
}

// op is the part of a tool name after the connector: "postgres.execute" → "execute",
// "stripe.refunds.create" → "create".
func op(tool string) string {
	if i := strings.LastIndexByte(tool, '.'); i >= 0 {
		return tool[i+1:]
	}
	return tool
}

// service is the part of a tool name before the first dot: the connector's name.
func service(tool string) string {
	s, _, _ := strings.Cut(tool, ".")
	return s
}

func str(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// summary is one plain sentence about an action or amendment, for the formats that carry a
// human-readable message. Raw enum values, not Console wording: these are machine exports.
func summary(b *body) string {
	var s strings.Builder
	s.WriteString(b.Action.Tool)
	s.WriteString(" by ")
	s.WriteString(b.Agent.Name)
	if b.Kind == "amendment" {
		fmt.Fprintf(&s, ": later check of %s, %s", str(b.Amends), b.Witness.Verdict)
		fmt.Fprintf(&s, " at %s.", b.Witness.Level)
		return s.String()
	}
	fmt.Fprintf(&s, ": policy %s", b.Policy.Decision)
	if b.Policy.RuleID != nil {
		fmt.Fprintf(&s, " (rule %s)", *b.Policy.RuleID)
	}
	if b.Approval != nil {
		fmt.Fprintf(&s, ", approval %s", b.Approval.Outcome)
	}
	if b.Execution != nil {
		fmt.Fprintf(&s, ", execution %s", b.Execution.Status)
	} else {
		s.WriteString(", not run")
	}
	if b.Witness != nil {
		fmt.Fprintf(&s, ", check %s at %s", b.Witness.Verdict, b.Witness.Level)
	}
	s.WriteString(".")
	return s.String()
}

func writeLine(w io.Writer, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = w.Write(append(b, '\n'))
	return err
}
