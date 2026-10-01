package witness

import (
	"errors"
	"strings"

	"github.com/deedglass/deedglass/internal/connector/schema"
)

// L0 methods.
const (
	MethodReported       = "reported"
	MethodEdgeReported   = "edge_reported"
	MethodResponseSchema = "response.schema"
)

// EvaluateL0 is the tool's own answer (S19 §3.4): a success, checked against the tool's output schema
// when it has one; an unknown outcome is a gap. It returns the reasons and the L0 method.
func EvaluateL0(s Subject) ([]Reason, string) {
	method := MethodReported
	if s.Mode == ModeProxy {
		method = MethodEdgeReported
	}
	if s.Result.Status != StatusSucceeded {
		cause := s.Result.Cause
		if cause == "" {
			cause = "outcome_unknown"
		}
		// evidence.cause lets the Gate say what happened ("the tool did not answer in time").
		return []Reason{R(L0, CodeOutcomeUnknown, "status", map[string]any{"cause": cause}).
			WithEvidence(map[string]any{"cause": cause})}, method
	}
	var out []Reason
	if s.Mode == ModeProxy {
		out = append(out, R(L0, CodeEdgeReportedSuccess, "status", nil))
	} else {
		out = append(out, R(L0, CodeToolReportedSuccess, "status", nil))
	}
	if len(s.Spec.OutputSchema) == 0 {
		return out, method
	}
	if s.Result.Output == nil || s.Result.OutputBytes > MaxOutputBytes || len(s.Result.Output) > MaxOutputBytes {
		if s.Result.OutputBytes > 0 || len(s.Result.Output) > 0 {
			out = append(out, R(L0, CodeSchemaSkipped, "response.schema", nil))
		}
		return out, method
	}
	sc, err := schema.Compile(s.Spec.OutputSchema)
	if err != nil {
		// The schema itself is broken: Deedglass can't read the response with confidence.
		return append(out, R(L0, CodeResponseSchemaInvalid, "response.schema", map[string]any{"path": "(schema)"})), method
	}
	if err := sc.Validate(s.Result.Output); err != nil {
		path := "(response)"
		var se *schema.Error
		if errors.As(err, &se) {
			path = outputPath(se.Path)
		}
		// response_schema_valid isn't added: L0 is in doubt until the response can be read.
		return append(out, R(L0, CodeResponseSchemaInvalid, "response.schema", map[string]any{"path": path})), method
	}
	return append(out, R(L0, CodeResponseSchemaValid, "response.schema", nil)), MethodResponseSchema
}

// outputPath turns the schema package's "args.body.status" into "body.status".
func outputPath(p string) string {
	p = strings.TrimPrefix(p, "args")
	p = strings.TrimPrefix(p, ".")
	if p == "" {
		return "(response)"
	}
	return p
}
