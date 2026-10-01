package postgres

import (
	"bytes"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

// Values to JSON (S17 §3.8). Every column is read in Postgres's text format (one result-format
// code for all columns), with TimeZone UTC, DateStyle ISO and IntervalStyle postgres set for the
// transaction, then converted by type OID:
//
//	int2, int4, oid          → number
//	int8                     → number if |v| ≤ 2^53−1, else string
//	numeric                  → string ("0.1")
//	float4, float8           → number; NaN, Infinity, -Infinity → string
//	bool                     → true / false
//	text family, uuid, enum, inet, cidr, macaddr, time, timetz → string
//	bytea                    → "\\x…"
//	date                     → "2026-10-06"
//	timestamptz              → RFC 3339 UTC with µs ("2026-10-06T11:12:00.123456Z")
//	timestamp                → the same without a zone
//	interval                 → Postgres text ("1 day 02:00:00")
//	json, jsonb              → embedded JSON
//	arrays                   → arrays, element by element
//	NULL                     → null
//
// Anything else (domains over unknown types, ranges, geometry) is its Postgres text as a string.

// maxSafeInt is 2^53−1, the largest integer every JSON reader keeps exactly.
const maxSafeInt = 1<<53 - 1

var typeMap = pgtype.NewMap()

// TypeName is a column's type name: int8, text, int4[] …; "" when pgx doesn't know the OID.
func TypeName(oid uint32) string {
	t, ok := typeMap.TypeForOID(oid)
	if !ok {
		return ""
	}
	if ac, ok := t.Codec.(*pgtype.ArrayCodec); ok && ac.ElementType != nil {
		return ac.ElementType.Name + "[]"
	}
	return t.Name
}

// ToJSON converts one text-format value. raw nil is NULL.
func ToJSON(oid uint32, raw []byte) (json.RawMessage, error) {
	if raw == nil {
		return json.RawMessage("null"), nil
	}
	s := string(raw)
	switch oid {
	case pgtype.Int2OID, pgtype.Int4OID, pgtype.OIDOID, pgtype.XIDOID, pgtype.CIDOID:
		if _, err := strconv.ParseInt(s, 10, 64); err != nil {
			return quote(s), nil
		}
		return json.RawMessage(s), nil
	case pgtype.Int8OID, pgtype.XID8OID:
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil || n > maxSafeInt || n < -maxSafeInt {
			return quote(s), nil
		}
		return json.RawMessage(strconv.FormatInt(n, 10)), nil
	case pgtype.NumericOID:
		return quote(s), nil
	case pgtype.Float4OID, pgtype.Float8OID:
		f, err := strconv.ParseFloat(s, 64)
		if err != nil || s == "NaN" || strings.Contains(s, "Infinity") {
			return quote(s), nil
		}
		b, _ := json.Marshal(f)
		return b, nil
	case pgtype.BoolOID:
		switch s {
		case "t":
			return json.RawMessage("true"), nil
		case "f":
			return json.RawMessage("false"), nil
		}
		return quote(s), nil
	case pgtype.TimestamptzOID:
		t, err := time.Parse("2006-01-02 15:04:05.999999-07", s)
		if err != nil {
			if t, err = time.Parse("2006-01-02 15:04:05.999999-07:00", s); err != nil {
				return quote(s), nil // infinity, BC dates
			}
		}
		return quote(t.UTC().Format("2006-01-02T15:04:05.000000Z07:00")), nil
	case pgtype.TimestampOID:
		t, err := time.Parse("2006-01-02 15:04:05.999999", s)
		if err != nil {
			return quote(s), nil
		}
		return quote(t.Format("2006-01-02T15:04:05.000000")), nil
	case pgtype.JSONOID, pgtype.JSONBOID:
		if json.Valid(raw) {
			var buf bytes.Buffer
			if err := json.Compact(&buf, raw); err == nil {
				return buf.Bytes(), nil
			}
		}
		return quote(s), nil
	}
	if t, ok := typeMap.TypeForOID(oid); ok {
		if ac, ok := t.Codec.(*pgtype.ArrayCodec); ok && ac.ElementType != nil {
			return arrayToJSON(ac.ElementType.OID, s)
		}
	}
	return quote(s), nil
}

func quote(s string) json.RawMessage {
	b, _ := json.Marshal(s)
	return b
}

// arrayToJSON parses Postgres's array text ({1,2,"a b",NULL,{3,4}}) and converts each element.
func arrayToJSON(elem uint32, s string) (json.RawMessage, error) {
	if strings.HasPrefix(s, "[") { // explicit bounds: [0:2]={…}
		if i := strings.Index(s, "="); i > 0 {
			s = s[i+1:]
		}
	}
	p := arrayParser{s: s, elem: elem}
	out, err := p.array()
	if err != nil || p.i != len(p.s) {
		return quote(s), nil
	}
	return out, nil
}

type arrayParser struct {
	s    string
	i    int
	elem uint32
}

var errArray = errors.New("postgres: unreadable array text")

func (p *arrayParser) array() (json.RawMessage, error) {
	if p.i >= len(p.s) || p.s[p.i] != '{' {
		return nil, errArray
	}
	p.i++
	var buf bytes.Buffer
	buf.WriteByte('[')
	first := true
	if p.i < len(p.s) && p.s[p.i] == '}' {
		p.i++
		return json.RawMessage("[]"), nil
	}
	for {
		if !first {
			buf.WriteByte(',')
		}
		first = false
		var v json.RawMessage
		var err error
		switch {
		case p.i < len(p.s) && p.s[p.i] == '{':
			v, err = p.array()
		case p.i < len(p.s) && p.s[p.i] == '"':
			var str string
			if str, err = p.quoted(); err == nil {
				v, err = ToJSON(p.elem, []byte(str))
			}
		default:
			start := p.i
			for p.i < len(p.s) && p.s[p.i] != ',' && p.s[p.i] != '}' {
				p.i++
			}
			tok := p.s[start:p.i]
			if tok == "NULL" {
				v = json.RawMessage("null")
			} else {
				v, err = ToJSON(p.elem, []byte(tok))
			}
		}
		if err != nil {
			return nil, err
		}
		buf.Write(v)
		if p.i >= len(p.s) {
			return nil, errArray
		}
		switch p.s[p.i] {
		case ',':
			p.i++
		case '}':
			p.i++
			buf.WriteByte(']')
			return buf.Bytes(), nil
		default:
			return nil, errArray
		}
	}
}

func (p *arrayParser) quoted() (string, error) {
	p.i++ // opening quote
	var sb strings.Builder
	for p.i < len(p.s) {
		c := p.s[p.i]
		switch c {
		case '\\':
			if p.i+1 >= len(p.s) {
				return "", errArray
			}
			sb.WriteByte(p.s[p.i+1])
			p.i += 2
		case '"':
			p.i++
			return sb.String(), nil
		default:
			sb.WriteByte(c)
			p.i++
		}
	}
	return "", errArray
}
