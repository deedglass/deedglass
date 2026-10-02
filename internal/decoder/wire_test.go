package decoder

import (
	"encoding/binary"
	"time"
)

// A tiny pgoutput v1 encoder for tests: it writes the layouts SafeParse checks, so pure tests
// can build any stream without a database. The captured corpus (testdata/decoder/pgoutput) holds
// the real thing.

type wcol struct {
	name  string
	oid   uint32
	key   bool
	typmd int32
}

type wval struct {
	kind byte // 'n', 'u', 't'
	text string
}

func tv(s string) wval { return wval{kind: 't', text: s} }

var (
	vNull      = wval{kind: 'n'}
	vUnchanged = wval{kind: 'u'}
)

func be16(b []byte, v uint16) []byte { return binary.BigEndian.AppendUint16(b, v) }
func be32(b []byte, v uint32) []byte { return binary.BigEndian.AppendUint32(b, v) }
func be64(b []byte, v uint64) []byte { return binary.BigEndian.AppendUint64(b, v) }
func cstr(b []byte, s string) []byte { return append(append(b, s...), 0) }

const pgEpochMicros = 946684800 * 1000000

func pgTime(t time.Time) uint64 { return uint64(t.UnixMicro() - pgEpochMicros) }

func encBegin(final LSN, at time.Time, xid uint32) []byte {
	b := []byte{'B'}
	b = be64(b, uint64(final))
	b = be64(b, pgTime(at))
	return be32(b, xid)
}

func encCommit(commit, end LSN, at time.Time) []byte {
	b := []byte{'C', 0}
	b = be64(b, uint64(commit))
	b = be64(b, uint64(end))
	return be64(b, pgTime(at))
}

func encOrigin(lsn LSN, name string) []byte {
	b := []byte{'O'}
	b = be64(b, uint64(lsn))
	return cstr(b, name)
}

func encRelation(relid uint32, ns, name string, identity byte, cols []wcol) []byte {
	b := []byte{'R'}
	b = be32(b, relid)
	b = cstr(b, ns)
	b = cstr(b, name)
	b = append(b, identity)
	b = be16(b, uint16(len(cols)))
	for _, c := range cols {
		f := byte(0)
		if c.key {
			f = 1
		}
		b = append(b, f)
		b = cstr(b, c.name)
		b = be32(b, c.oid)
		b = be32(b, uint32(c.typmd))
	}
	return b
}

func encType(oid uint32, ns, name string) []byte {
	b := []byte{'Y'}
	b = be32(b, oid)
	b = cstr(b, ns)
	return cstr(b, name)
}

func encTuple(b []byte, vals []wval) []byte {
	b = be16(b, uint16(len(vals)))
	for _, v := range vals {
		b = append(b, v.kind)
		if v.kind == 't' {
			b = be32(b, uint32(len(v.text)))
			b = append(b, v.text...)
		}
	}
	return b
}

func encInsert(relid uint32, vals []wval) []byte {
	b := []byte{'I'}
	b = be32(b, relid)
	b = append(b, 'N')
	return encTuple(b, vals)
}

// encUpdate: oldKind 0 (no old tuple), 'K' or 'O'.
func encUpdate(relid uint32, oldKind byte, old, nw []wval) []byte {
	b := []byte{'U'}
	b = be32(b, relid)
	if oldKind != 0 {
		b = append(b, oldKind)
		b = encTuple(b, old)
	}
	b = append(b, 'N')
	return encTuple(b, nw)
}

func encDelete(relid uint32, oldKind byte, old []wval) []byte {
	b := []byte{'D'}
	b = be32(b, relid)
	b = append(b, oldKind)
	return encTuple(b, old)
}

func encTruncate(opts byte, relids ...uint32) []byte {
	b := []byte{'T'}
	b = be32(b, uint32(len(relids)))
	b = append(b, opts)
	for _, r := range relids {
		b = be32(b, r)
	}
	return b
}

func encMessage(transactional bool, lsn LSN, prefix string, content []byte) []byte {
	f := byte(0)
	if transactional {
		f = 1
	}
	b := []byte{'M', f}
	b = be64(b, uint64(lsn))
	b = cstr(b, prefix)
	b = be32(b, uint32(len(content)))
	return append(b, content...)
}
