package stamp

import (
	"encoding/asn1"
	"errors"
)

// Minimal DER helpers. encoding/asn1 can't tell optional implicitly tagged RawValue fields apart
// (a RawValue matches any tag), so the CMS structures are walked element by element.

var errDER = errors.New("stamp: malformed DER")

// elements splits the contents of a constructed value into its elements.
func elements(b []byte) ([]asn1.RawValue, error) {
	var out []asn1.RawValue
	for len(b) > 0 {
		var rv asn1.RawValue
		rest, err := asn1.Unmarshal(b, &rv)
		if err != nil {
			return nil, errDER
		}
		out = append(out, rv)
		b = rest
	}
	return out, nil
}

// one parses exactly one DER value with nothing after it.
func one(b []byte) (asn1.RawValue, error) {
	var rv asn1.RawValue
	rest, err := asn1.Unmarshal(b, &rv)
	if err != nil || len(rest) != 0 {
		return asn1.RawValue{}, errDER
	}
	return rv, nil
}

func isUniversal(rv asn1.RawValue, tag int) bool {
	return rv.Class == asn1.ClassUniversal && rv.Tag == tag
}

func isContext(rv asn1.RawValue, tag int) bool {
	return rv.Class == asn1.ClassContextSpecific && rv.Tag == tag
}

// seqOf returns the elements of a SEQUENCE.
func seqOf(rv asn1.RawValue) ([]asn1.RawValue, error) {
	if !isUniversal(rv, asn1.TagSequence) || !rv.IsCompound {
		return nil, errDER
	}
	return elements(rv.Bytes)
}
