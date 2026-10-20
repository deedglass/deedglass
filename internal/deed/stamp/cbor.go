package stamp

import (
	"encoding/binary"
	"errors"
)

// The one CBOR shape this package decodes by hand: an RFC 9942 RFC9162_SHA256 inclusion path,
// [tree_size: uint, leaf_index: uint, inclusion_path: [+ bstr]], definite lengths only. Writing
// it out keeps fxamacker/cbor an indirect dependency (go-cose's) and refuses anything else.

var errInclusionCBOR = errors.New("an inclusion path is not [tree_size, leaf_index, [hash…]] in CBOR")

// cborHead reads one CBOR head: major type and argument.
func cborHead(b []byte) (major byte, arg uint64, rest []byte, err error) {
	if len(b) == 0 {
		return 0, 0, nil, errInclusionCBOR
	}
	major, info := b[0]>>5, b[0]&0x1f
	b = b[1:]
	switch {
	case info < 24:
		return major, uint64(info), b, nil
	case info == 24 && len(b) >= 1:
		return major, uint64(b[0]), b[1:], nil
	case info == 25 && len(b) >= 2:
		return major, uint64(binary.BigEndian.Uint16(b)), b[2:], nil
	case info == 26 && len(b) >= 4:
		return major, uint64(binary.BigEndian.Uint32(b)), b[4:], nil
	case info == 27 && len(b) >= 8:
		return major, binary.BigEndian.Uint64(b), b[8:], nil
	}
	return 0, 0, nil, errInclusionCBOR // indefinite lengths and reserved values
}

func decodeInclusion(b []byte) (size, index uint64, path [][]byte, err error) {
	m, n, b, err := cborHead(b)
	if err != nil || m != 4 || n != 3 {
		return 0, 0, nil, errInclusionCBOR
	}
	if m, size, b, err = cborHead(b); err != nil || m != 0 {
		return 0, 0, nil, errInclusionCBOR
	}
	if m, index, b, err = cborHead(b); err != nil || m != 0 {
		return 0, 0, nil, errInclusionCBOR
	}
	if m, n, b, err = cborHead(b); err != nil || m != 4 || n > maxPathHash {
		return 0, 0, nil, errInclusionCBOR
	}
	for i := uint64(0); i < n; i++ {
		var l uint64
		if m, l, b, err = cborHead(b); err != nil || m != 2 || l > uint64(len(b)) {
			return 0, 0, nil, errInclusionCBOR
		}
		path = append(path, b[:l])
		b = b[l:]
	}
	if len(b) != 0 {
		return 0, 0, nil, errInclusionCBOR
	}
	return size, index, path, nil
}

// EncodeInclusion is the CBOR of an inclusion path (the fake service in stamptest writes it).
func EncodeInclusion(size, index uint64, path [][]byte) []byte {
	out := []byte{0x83}
	out = cborAppend(out, 0, size)
	out = cborAppend(out, 0, index)
	out = cborAppend(out, 4, uint64(len(path)))
	for _, p := range path {
		out = cborAppend(out, 2, uint64(len(p)))
		out = append(out, p...)
	}
	return out
}

func cborAppend(b []byte, major byte, n uint64) []byte {
	m := major << 5
	switch {
	case n < 24:
		return append(b, m|byte(n))
	case n <= 0xff:
		return append(b, m|24, byte(n))
	case n <= 0xffff:
		return binary.BigEndian.AppendUint16(append(b, m|25), uint16(n))
	case n <= 0xffffffff:
		return binary.BigEndian.AppendUint32(append(b, m|26), uint32(n))
	}
	return binary.BigEndian.AppendUint64(append(b, m|27), n)
}
