package decoder

import (
	"bytes"
	"encoding/binary"
	"fmt"

	"github.com/jackc/pglogrepl"
)

// SafeParse is the only way a pgoutput message reaches the decoder (ADR-002 amendment A). It
// checks the message's complete wire layout first, then calls pglogrepl.Parse inside recover.
// Nothing may follow a message's last field. Any failure is a *MalformedError, which halts the
// runner before anything from that peek is persisted.
//
// The layouts are those of "Logical Replication Message Formats" in the PostgreSQL 18 manual,
// https://www.postgresql.org/docs/18/protocol-logicalrep-message-formats.html (protocol version
// 1, re-read 2026-10-02): streaming and two-phase messages are never requested, and binary
// tuple data ('b') is refused because the decoder asks for text.
//
// On the spike's corpus (S3), raw pglogrepl.Parse panicked on 55,510 of 59,077 strict prefixes and
// silently accepted 1,854 truncated 'M' messages; TestSafeParsePrefixes holds SafeParse at 0 and 0.
func SafeParse(b []byte) (msg pglogrepl.Message, err error) {
	if err := Validate(b); err != nil {
		return nil, err
	}
	defer func() {
		if r := recover(); r != nil {
			msg, err = nil, &MalformedError{Type: b[0], Offset: 0, Reason: "parser_panic", Detail: fmt.Sprint(r)}
		}
	}()
	m, perr := pglogrepl.Parse(b)
	if perr != nil {
		return nil, &MalformedError{Type: b[0], Offset: 0, Reason: "parser_error", Detail: perr.Error()}
	}
	return m, nil
}

// MalformedError is a message that doesn't match the pgoutput v1 format.
type MalformedError struct {
	Type   byte   // the message type byte (0 when the message is empty)
	Offset int    // the byte where the problem is
	Reason string // a code: truncated, trailing_bytes, declared_length, unexpected_message_type, …
	Detail string // more words for the log
}

func (e *MalformedError) Error() string {
	t := "empty"
	if e.Type >= 0x20 && e.Type < 0x7f {
		t = string(rune(e.Type))
	} else if e.Type != 0 {
		t = fmt.Sprintf("0x%02x", e.Type)
	}
	s := fmt.Sprintf("malformed pgoutput message (type %s, byte %d): %s", t, e.Offset, e.Reason)
	if e.Detail != "" {
		s += ": " + e.Detail
	}
	return s
}

// reader walks a message, recording the first problem.
type reader struct {
	b   []byte
	off int
	err *MalformedError
}

func (r *reader) fail(reason string) {
	if r.err == nil {
		var t byte
		if len(r.b) > 0 {
			t = r.b[0]
		}
		r.err = &MalformedError{Type: t, Offset: r.off, Reason: reason}
	}
}

func (r *reader) need(n int) bool {
	if r.err != nil {
		return false
	}
	if n < 0 || len(r.b)-r.off < n {
		r.fail("truncated")
		return false
	}
	return true
}

func (r *reader) skip(n int) {
	if r.need(n) {
		r.off += n
	}
}

func (r *reader) u8() byte {
	if !r.need(1) {
		return 0
	}
	v := r.b[r.off]
	r.off++
	return v
}

func (r *reader) u16() uint16 {
	if !r.need(2) {
		return 0
	}
	v := binary.BigEndian.Uint16(r.b[r.off:])
	r.off += 2
	return v
}

func (r *reader) u32() uint32 {
	if !r.need(4) {
		return 0
	}
	v := binary.BigEndian.Uint32(r.b[r.off:])
	r.off += 4
	return v
}

// cstring skips a NUL-terminated string that ends inside the message.
func (r *reader) cstring() {
	if r.err != nil {
		return
	}
	i := bytes.IndexByte(r.b[r.off:], 0)
	if i < 0 {
		r.fail("unterminated_string")
		return
	}
	r.off += i + 1
}

func (r *reader) end() {
	if r.err == nil && r.off != len(r.b) {
		r.fail("trailing_bytes")
	}
}

// tuple checks TupleData: Int16 ncols, then per column a kind and, for 't', Int32 length + bytes.
func (r *reader) tuple() {
	n := int(r.u16())
	for i := 0; i < n && r.err == nil; i++ {
		switch r.u8() {
		case 'n', 'u':
		case 't':
			l := r.u32()
			if r.err == nil && l > uint32(len(r.b)-r.off) {
				r.fail("declared_length")
				return
			}
			r.skip(int(l))
		case 'b':
			r.off--
			r.fail("binary_tuple_data")
		default:
			r.off--
			r.fail("unknown_tuple_kind")
		}
	}
}

// Validate checks b's layout without parsing it into values (SafeParse calls it first).
func Validate(b []byte) error {
	r := &reader{b: b}
	if len(b) == 0 {
		return &MalformedError{Reason: "empty"}
	}
	r.off = 1
	switch b[0] {
	case 'B': // final LSN, commit time, xid
		r.skip(8 + 8 + 4)
	case 'C': // flags (0), commit LSN, end LSN, commit time
		if f := r.u8(); r.err == nil && f != 0 {
			r.off--
			r.fail("commit_flags")
		}
		r.skip(8 + 8 + 8)
	case 'O': // origin commit LSN, name
		r.skip(8)
		r.cstring()
	case 'R': // relid, namespace, name, replica identity, ncols, columns
		r.skip(4)
		r.cstring()
		r.cstring()
		switch id := r.u8(); {
		case r.err != nil:
		case id != 'd' && id != 'n' && id != 'f' && id != 'i':
			r.off--
			r.fail("replica_identity")
		}
		n := int(r.u16())
		for i := 0; i < n && r.err == nil; i++ {
			if f := r.u8(); r.err == nil && f > 1 {
				r.off--
				r.fail("column_flags")
			}
			r.cstring()
			r.skip(4 + 4)
		}
	case 'Y': // type oid, namespace, name
		r.skip(4)
		r.cstring()
		r.cstring()
	case 'I': // relid, 'N', tuple
		r.skip(4)
		if t := r.u8(); r.err == nil && t != 'N' {
			r.off--
			r.fail("tuple_marker")
		}
		r.tuple()
	case 'U': // relid, optional ('K'|'O') tuple, 'N', tuple
		r.skip(4)
		t := r.u8()
		if r.err == nil && (t == 'K' || t == 'O') {
			r.tuple()
			t = r.u8()
		}
		if r.err == nil && t != 'N' {
			r.off--
			r.fail("tuple_marker")
		}
		r.tuple()
	case 'D': // relid, ('K'|'O'), tuple
		r.skip(4)
		if t := r.u8(); r.err == nil && t != 'K' && t != 'O' {
			r.off--
			r.fail("tuple_marker")
		}
		r.tuple()
	case 'T': // nrels, options, relids
		n := r.u32()
		if r.err == nil && n > uint32(len(b)) {
			r.fail("declared_length")
			break
		}
		if o := r.u8(); r.err == nil && o > 3 {
			r.off--
			r.fail("truncate_options")
		}
		r.skip(4 * int(n))
	case 'M': // flags (0|1), LSN, prefix, length, content
		if f := r.u8(); r.err == nil && f > 1 {
			r.off--
			r.fail("message_flags")
		}
		r.skip(8)
		r.cstring()
		l := r.u32()
		if r.err == nil && l != uint32(len(b)-r.off) {
			r.fail("declared_length")
			break
		}
		r.skip(int(l))
	default:
		r.off = 0
		r.fail("unexpected_message_type")
	}
	r.end()
	if r.err != nil {
		return r.err
	}
	return nil
}
