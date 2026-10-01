package jsonrpc

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"unicode/utf8"
)

// Limits (S14 §3.3): both directions.
const (
	DefaultMaxLine = 4 << 20
	MaxDepth       = 128
)

// Reader reads one message per line.
type Reader struct {
	br      *bufio.Reader
	MaxLine int
	// Tail holds the last bytes of the most recent oversize line, so a caller can still find the
	// id of a response it had to refuse.
	Head, Tail []byte
}

// NewReader reads from r with the default 4 MiB line limit.
func NewReader(r io.Reader) *Reader {
	return &Reader{br: bufio.NewReaderSize(r, 64<<10), MaxLine: DefaultMaxLine}
}

// Next returns the next message and its line (without the newline). Blank lines are skipped. A
// framing error (ErrTooLarge, ErrParse, ErrBatch, ErrTooDeep, ErrNotUTF8, ErrShape) leaves the
// reader at the next line; io.EOF and other read errors end the stream. A final line without a
// newline is still a message.
func (r *Reader) Next() (Envelope, []byte, error) {
	for {
		line, err := r.line()
		if err != nil {
			return nil, line, err
		}
		line = bytes.TrimRight(line, "\r")
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		e, perr := Parse(line)
		return e, line, perr
	}
}

// line reads up to the newline, enforcing MaxLine without buffering more than it.
func (r *Reader) line() ([]byte, error) {
	max := r.MaxLine
	if max <= 0 {
		max = DefaultMaxLine
	}
	var buf []byte
	tooLarge := false
	r.Head, r.Tail = nil, nil
	for {
		chunk, err := r.br.ReadSlice('\n')
		if tooLarge {
			r.keepTail(chunk)
		} else if len(buf)+len(chunk) > max+1 { // +1: the newline itself
			tooLarge = true
			r.Head = append(append([]byte(nil), buf...), chunk...)
			if len(r.Head) > 64<<10 {
				r.Head = r.Head[:64<<10]
			}
			r.keepTail(chunk)
			buf = nil
		} else {
			buf = append(buf, chunk...)
		}
		switch {
		case err == nil:
			if tooLarge {
				return nil, ErrTooLarge
			}
			return buf[:len(buf)-1], nil
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case errors.Is(err, io.EOF):
			if tooLarge {
				return nil, ErrTooLarge
			}
			if len(bytes.TrimSpace(buf)) > 0 {
				return buf, nil
			}
			return nil, io.EOF
		default:
			return nil, err
		}
	}
}

func (r *Reader) keepTail(chunk []byte) {
	r.Tail = append(r.Tail, chunk...)
	if len(r.Tail) > 4096 {
		r.Tail = append([]byte(nil), r.Tail[len(r.Tail)-4096:]...)
	}
}

// Parse checks one line and splits it into members.
func Parse(line []byte) (Envelope, error) {
	if !utf8.Valid(line) {
		return nil, ErrNotUTF8
	}
	t := bytes.TrimSpace(line)
	if len(t) > 0 && t[0] == '[' {
		if json.Valid(t) {
			return nil, ErrBatch
		}
		return nil, ErrParse
	}
	if !json.Valid(t) {
		return nil, ErrParse
	}
	if len(t) == 0 || t[0] != '{' {
		return nil, ErrShape
	}
	if depth(t) > MaxDepth {
		return nil, ErrTooDeep
	}
	var e Envelope
	if err := json.Unmarshal(t, &e); err != nil {
		return nil, ErrParse
	}
	if dupTopLevel(t, len(e)) {
		return nil, ErrShape // two "method" or "params" members could be read two ways upstream
	}
	var v string
	if raw, ok := e["jsonrpc"]; !ok || json.Unmarshal(raw, &v) != nil || v != "2.0" || e.Kind() == Invalid {
		return nil, ErrShape
	}
	return e, nil
}

// depth is the deepest nesting of objects and arrays in valid JSON.
func depth(b []byte) int {
	d, max := 0, 0
	in, esc := false, false
	for _, c := range b {
		switch {
		case in:
			switch {
			case esc:
				esc = false
			case c == '\\':
				esc = true
			case c == '"':
				in = false
			}
		case c == '"':
			in = true
		case c == '{' || c == '[':
			d++
			if d > max {
				max = d
			}
		case c == '}' || c == ']':
			d--
		}
	}
	return max
}

// dupTopLevel reports whether the object has more top-level keys than distinct ones.
func dupTopLevel(b []byte, distinct int) bool {
	dec := json.NewDecoder(bytes.NewReader(b))
	if _, err := dec.Token(); err != nil {
		return true
	}
	n := 0
	for dec.More() {
		if _, err := dec.Token(); err != nil {
			return true
		}
		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			return true
		}
		n++
	}
	return n > distinct
}
