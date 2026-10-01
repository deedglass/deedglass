package httpx

import (
	"bytes"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
)

// DecodeJSON reads the request body (already bounded by the route's MaxBody) and decodes it into v
// strictly: json/v2 rejects duplicate member names, invalid UTF-8 and trailing data by default, and
// RejectUnknownMembers refuses fields v doesn't declare. Errors are *Error values ready to render;
// none of them quotes the body.
func DecodeJSON(r *http.Request, v any) error {
	b, err := readBody(r)
	if err != nil {
		return err
	}
	return decodeBytes(b, v)
}

// DecodeJSONOptional is DecodeJSON for routes whose body may be left out entirely (POST …/stop
// with no reason): an empty body leaves v unchanged.
func DecodeJSONOptional(r *http.Request, v any) error {
	b, err := readBody(r)
	if err != nil {
		return err
	}
	if len(bytes.TrimSpace(b)) == 0 {
		return nil
	}
	return decodeBytes(b, v)
}

func readBody(r *http.Request) ([]byte, error) {
	if r.Body == nil {
		return nil, nil
	}
	b, err := io.ReadAll(r.Body)
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			return nil, errTooLarge(mbe.Limit)
		}
		return nil, E(CodeInvalidJSON, Msg("The body couldn't be read completely. Send it again."), Cause(err))
	}
	return b, nil
}

func decodeBytes(b []byte, v any) error {
	if len(bytes.TrimSpace(b)) == 0 {
		return E(CodeInvalidJSON, Msg("The body is empty. Send a JSON object, and retry."))
	}
	err := json.Unmarshal(b, v, json.RejectUnknownMembers(true))
	if err == nil {
		return nil
	}
	var se *jsontext.SyntacticError
	if errors.As(err, &se) {
		line, col := position(b, se.ByteOffset)
		return E(CodeInvalidJSON, Msgf("The body isn't valid JSON (line %d, column %d). Fix it and retry.", line, col), Cause(err))
	}
	var sem *json.SemanticError
	if errors.As(err, &sem) {
		param := pointerParam(sem.JSONPointer)
		if errors.Is(err, json.ErrUnknownName) {
			return E(CodeValidationFailed, Param(param),
				Msgf("`%s` isn't a known field. Check the spelling, or the API version.", clip(param, 64)), Cause(err))
		}
		if param == "" {
			return E(CodeValidationFailed, Msg("The body has the wrong shape. Send a JSON object with the documented fields."), Cause(err))
		}
		return E(CodeValidationFailed, Param(param),
			Msgf("`%s` has the wrong type: it must be %s.", clip(param, 64), expected(sem.GoType)), Cause(err))
	}
	return E(CodeInvalidJSON, Msg("The body isn't valid JSON. Fix it and retry."), Cause(err))
}

// position turns a byte offset into a 1-based line and column (columns count bytes).
func position(b []byte, off int64) (line, col int) {
	if off < 0 {
		off = 0
	}
	if off > int64(len(b)) {
		off = int64(len(b))
	}
	head := b[:off]
	line = 1 + bytes.Count(head, []byte{'\n'})
	col = int(off) - (bytes.LastIndexByte(head, '\n') + 1) + 1
	return line, col
}

// pointerParam turns a JSON Pointer ("/items/0/name") into the param form ("items.0.name").
func pointerParam(p jsontext.Pointer) string {
	s := strings.TrimPrefix(string(p), "/")
	if s == "" {
		return ""
	}
	parts := strings.Split(s, "/")
	for i, x := range parts {
		x = strings.ReplaceAll(x, "~1", "/")
		parts[i] = strings.ReplaceAll(x, "~0", "~")
	}
	return strings.Join(parts, ".")
}

func expected(t reflect.Type) string {
	if t == nil {
		return "the documented type"
	}
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Implements(reflect.TypeFor[interface{ UnmarshalText([]byte) error }]()) ||
		reflect.PointerTo(t).Implements(reflect.TypeFor[interface{ UnmarshalText([]byte) error }]()) {
		return "a string in the documented format"
	}
	switch t.Kind() {
	case reflect.String:
		return "a string"
	case reflect.Bool:
		return "true or false"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return "a whole number"
	case reflect.Float32, reflect.Float64:
		return "a number"
	case reflect.Slice, reflect.Array:
		return "an array"
	case reflect.Map, reflect.Struct:
		return "an object"
	}
	return fmt.Sprintf("the documented type (%s)", t.Kind())
}
