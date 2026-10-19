package recordexport

import (
	"encoding/json"
	"io"

	"github.com/deedglass/deedglass/internal/fhirexport"
)

// FHIR R4 is the existing exporter (internal/fhirexport, ADR-107): for each record, its
// AuditEvent and then, for a successful write, its Provenance, as one FHIR NDJSON stream. From a
// bundle the record stands alone, so the resource, the server and the SMART client id are unknown
// (docs/EXPORTS.md). Records that aren't FHIR interactions are skipped.

type fhirWriter struct {
	w   io.Writer
	opt Options
	st  Stats
}

func (x *fhirWriter) Stats() Stats { return x.st }
func (x *fhirWriter) Close() error { return nil }

func (x *fhirWriter) Write(raw []byte) error {
	x.st.Records++
	if _, err := parse(raw); err != nil {
		return err
	}
	rec := fhirexport.Record{Envelope: raw}
	opt := fhirexport.Options{Gateway: x.opt.Gateway}
	wrote := false
	for _, conv := range []func(fhirexport.Record, fhirexport.Options) (map[string]any, bool, error){fhirexport.AuditEvent, fhirexport.Provenance} {
		res, ok, err := conv(rec, opt)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		b, err := json.Marshal(res)
		if err != nil {
			return err
		}
		if _, err := x.w.Write(append(b, '\n')); err != nil {
			return err
		}
		x.st.Written++
		wrote = true
	}
	if !wrote {
		x.st.Skipped++
	}
	return nil
}
