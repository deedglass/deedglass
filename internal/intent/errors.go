package intent

import (
	"errors"
	"fmt"
)

// Code classifies why an intent was refused.
type Code string

// Strict-profile and canonical-form failures. The verifier reports these as
// NON_CANONICAL_INTENT (spec §10.3 check 4).
const (
	CodeTooLarge      Code = "TOO_LARGE"
	CodeInvalidUTF8   Code = "INVALID_UTF8"
	CodeSyntax        Code = "SYNTAX"
	CodeTooDeep       Code = "TOO_DEEP"
	CodeDuplicateKey  Code = "DUPLICATE_KEY"
	CodeNonInteger    Code = "NON_INTEGER_NUMBER"
	CodeIntegerRange  Code = "INTEGER_OUT_OF_RANGE"
	CodeLoneSurrogate Code = "LONE_SURROGATE"
	CodeNonCanonical  Code = "NON_CANONICAL"
)

// Schema failures for txv.intent/1. The verifier reports these as
// INTENT_SCHEMA (spec §10.3 check 4).
const (
	CodeUnknownKey Code = "UNKNOWN_KEY"
	CodeMissingKey Code = "MISSING_KEY"
	CodeWrongType  Code = "WRONG_TYPE"
	CodeBadValue   Code = "BAD_VALUE"
)

// Verifier reason codes (spec §10.3 check 4).
const (
	ReasonNonCanonical = "NON_CANONICAL_INTENT"
	ReasonSchema       = "INTENT_SCHEMA"
)

// Error is returned for every refused intent.
type Error struct {
	Code   Code
	Path   string // dotted JSON path, "" for the document itself
	Offset int    // byte offset for parse errors, -1 when not applicable
	Msg    string
}

func (e *Error) Error() string {
	loc := ""
	if e.Path != "" {
		loc = " at " + e.Path
	}
	if e.Offset >= 0 {
		loc += fmt.Sprintf(" (byte %d)", e.Offset)
	}
	return fmt.Sprintf("intent: %s%s: %s", e.Code, loc, e.Msg)
}

// Reason maps the error to the verifier reason code.
func (e *Error) Reason() string {
	switch e.Code {
	case CodeUnknownKey, CodeMissingKey, CodeWrongType, CodeBadValue:
		return ReasonSchema
	default:
		return ReasonNonCanonical
	}
}

// CodeOf returns the Code of err, or "" if err is not an *Error.
func CodeOf(err error) Code {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

func errAt(c Code, path string, off int, msg string) *Error {
	return &Error{Code: c, Path: path, Offset: off, Msg: msg}
}

func errField(c Code, path, msg string) *Error {
	return &Error{Code: c, Path: path, Offset: -1, Msg: msg}
}
