package connector

import (
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"

	"github.com/deedglass/deedglass/internal/connector/schema"
)

// ValidateArgs checks args against a tool's input schema with connector/schema (S16): type,
// properties, required, enum, pattern, minimum, maximum, maxLength, items and
// additionalProperties: false. The error is a *Error of kind invalid_request with code
// validation_failed and a sentence the agent can act on; a schema that doesn't compile is a
// config error (the connector is misconfigured, and nothing is sent).
func ValidateArgs(raw, args json.RawMessage) error {
	s, err := compiled(raw)
	if err != nil {
		return &Error{Kind: KindConfig, Code: "connector_misconfigured", AgentMessage: "This tool's argument schema can't be read. An admin can check Console › Connectors.", Err: err}
	}
	if err := s.Validate(args); err != nil {
		var se *schema.Error
		if errors.As(err, &se) {
			return ErrValidation(se.Path, se.What)
		}
		return ErrValidation("args", "can't be checked")
	}
	return nil
}

// compiled caches compiled schemas by their text, up to maxCachedSchemas (REST configs bring their
// own; past the cap a schema is compiled per call).
var (
	schemaCache  sync.Map // string → *schema.Schema
	schemaCached atomic.Int64
)

const maxCachedSchemas = 4096

func compiled(raw json.RawMessage) (*schema.Schema, error) {
	if v, ok := schemaCache.Load(string(raw)); ok {
		return v.(*schema.Schema), nil
	}
	s, err := schema.Compile(raw)
	if err != nil {
		return nil, err
	}
	if schemaCached.Add(1) <= maxCachedSchemas {
		schemaCache.Store(string(raw), s)
	}
	return s, nil
}
