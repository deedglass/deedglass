package deed

import (
	"errors"
	"fmt"
)

// Code names why a deed, a chain or a key set failed verification. Codes are stable: S05's CLI,
// the browser verifier and testdata/deeds/invalid/*/expected.json use them, and they are never
// renamed (a new failure gets a new code).
type Code string

const (
	CodeMalformed          Code = "malformed"           // not strict JSON, too large, or not canonical-able
	CodeUnsupportedVersion Code = "unsupported_version" // body.v is not 1
	CodeSchemaInvalid      Code = "schema_invalid"      // closed schema, enums, patterns, lengths
	CodeIDMismatch         Code = "id_mismatch"         // envelope.id != body.deed_id
	CodeBodyHashMismatch   Code = "body_hash_mismatch"  // sha256(JCS(body)) != body_hash
	CodeSealKeyMismatch    Code = "seal_key_mismatch"   // envelope.seal_key_id != body.seal_key_id
	CodeSealKeyUnknown     Code = "seal_key_unknown"    // the key set has no such key
	CodeSealKeyRejected    Code = "seal_key_rejected"   // the key set holds an unsafe or inconsistent key
	CodeSealKeyNotValidAt  Code = "seal_key_not_valid_at"
	CodeSealKeyRevoked     Code = "seal_key_revoked"
	CodeSealInvalid        Code = "seal_invalid"  // the Ed25519 signature does not verify
	CodeNonCanonical       Code = "non_canonical" // Strict: the bytes are not JCS(envelope)

	CodeChainGenesisMismatch  Code = "chain_genesis_mismatch"
	CodeChainSeqGap           Code = "chain_seq_gap"
	CodeChainSeqOutOfOrder    Code = "chain_seq_out_of_order"
	CodeChainPrevHashMismatch Code = "chain_prev_hash_mismatch"
	CodeChainOrgMismatch      Code = "chain_org_mismatch"
	CodeChainTimeRegression   Code = "chain_time_regression"
)

// Codes lists every Code in the order the spec documents them.
var Codes = []Code{
	CodeMalformed, CodeUnsupportedVersion, CodeSchemaInvalid, CodeIDMismatch, CodeBodyHashMismatch,
	CodeSealKeyMismatch, CodeSealKeyUnknown, CodeSealKeyRejected, CodeSealKeyNotValidAt, CodeSealKeyRevoked,
	CodeSealInvalid, CodeNonCanonical,
	CodeChainGenesisMismatch, CodeChainSeqGap, CodeChainSeqOutOfOrder, CodeChainPrevHashMismatch,
	CodeChainOrgMismatch, CodeChainTimeRegression,
}

// VerifyError is every verification failure. Only Code is always set; the other fields give the
// context the messages in messages.go need. Error() is for logs; people see Message().
type VerifyError struct {
	Code     Code
	Seq      int64  // the deed's seq, 0 when unknown (for example malformed input)
	DeedID   string // the deed's id, "" when unknown
	Field    string // schema_invalid: the dotted path of the field
	Detail   string // malformed: what the parser saw; otherwise extra context for logs
	Key      string // the seal key id involved
	IssuedAt string // seal_key_not_valid_at
	Version  int64  // unsupported_version: the v that was found
	Prev     int64  // chain errors: the seq of the previous deed
	Want     int64  // chain_seq_gap: the seq that was expected
	Org      string // chain_org_mismatch: the deed's org
	WantOrg  string // chain_org_mismatch: the chain's org
}

func (e *VerifyError) Error() string {
	s := "deed: " + string(e.Code)
	if e.Seq > 0 {
		s += fmt.Sprintf(" at seq %d", e.Seq)
	}
	if e.Field != "" {
		s += " (" + e.Field + ")"
	}
	if e.Detail != "" {
		s += ": " + e.Detail
	}
	return s
}

// Is makes errors.Is(err, ErrSealInvalid) and friends match on the code alone.
func (e *VerifyError) Is(target error) bool {
	t, ok := target.(*VerifyError)
	return ok && t.Code == e.Code && t.Seq == 0 && t.DeedID == "" && t.Field == ""
}

// CodeOf returns the Code of err, or "" when err is not a *VerifyError.
func CodeOf(err error) Code {
	var ve *VerifyError
	if errors.As(err, &ve) {
		return ve.Code
	}
	return ""
}

// Sentinels for errors.Is.
var (
	ErrMalformed             = &VerifyError{Code: CodeMalformed}
	ErrUnsupportedVersion    = &VerifyError{Code: CodeUnsupportedVersion}
	ErrSchemaInvalid         = &VerifyError{Code: CodeSchemaInvalid}
	ErrIDMismatch            = &VerifyError{Code: CodeIDMismatch}
	ErrBodyHashMismatch      = &VerifyError{Code: CodeBodyHashMismatch}
	ErrSealKeyMismatch       = &VerifyError{Code: CodeSealKeyMismatch}
	ErrSealKeyUnknown        = &VerifyError{Code: CodeSealKeyUnknown}
	ErrSealKeyRejected       = &VerifyError{Code: CodeSealKeyRejected}
	ErrSealKeyNotValidAt     = &VerifyError{Code: CodeSealKeyNotValidAt}
	ErrSealKeyRevoked        = &VerifyError{Code: CodeSealKeyRevoked}
	ErrSealInvalid           = &VerifyError{Code: CodeSealInvalid}
	ErrNonCanonical          = &VerifyError{Code: CodeNonCanonical}
	ErrChainGenesisMismatch  = &VerifyError{Code: CodeChainGenesisMismatch}
	ErrChainSeqGap           = &VerifyError{Code: CodeChainSeqGap}
	ErrChainSeqOutOfOrder    = &VerifyError{Code: CodeChainSeqOutOfOrder}
	ErrChainPrevHashMismatch = &VerifyError{Code: CodeChainPrevHashMismatch}
	ErrChainOrgMismatch      = &VerifyError{Code: CodeChainOrgMismatch}
	ErrChainTimeRegression   = &VerifyError{Code: CodeChainTimeRegression}
)

func errSchemaField(field, detail string) *VerifyError {
	return &VerifyError{Code: CodeSchemaInvalid, Field: field, Detail: detail}
}

func errMalformed(detail string) *VerifyError {
	return &VerifyError{Code: CodeMalformed, Detail: detail}
}

func errUnsupported(v int64) *VerifyError {
	return &VerifyError{Code: CodeUnsupportedVersion, Version: v, Detail: fmt.Sprintf("v=%d", v)}
}

func errKeyRejected(key, detail string) *VerifyError {
	return &VerifyError{Code: CodeSealKeyRejected, Key: key, Detail: detail}
}

// Sealing errors (the Sealer and key files). They are plain errors: nothing about them is shown to
// a verifier.
var (
	// ErrSealKeyRetired: the key retires within 30 s of issued_at, has retired, or was revoked.
	// The caller (the S13 worker) retries after the server loads the active key.
	ErrSealKeyRetired = errors.New("deed: the seal key is retired or retires within 30 s")
	// ErrSealKeyNotActive: the key is not registered, or its activated_at is after issued_at.
	ErrSealKeyNotActive = errors.New("deed: the seal key is not registered as active")
	// ErrChainConflict: the insert hit (org_id, seq), (org_id, prev_hash) or the chain trigger.
	// The caller retries once in a new transaction, then reports internal_error.
	ErrChainConflict = errors.New("deed: chain conflict; retry in a new transaction")
	// ErrDraft: the draft sets a field the Sealer owns, or is not a valid deed body.
	ErrDraft = errors.New("deed: invalid draft")
)
