package scan

import (
	"encoding/base64"
	"encoding/json"
	"sort"

	"github.com/gowebpki/jcs"
)

// SharePayload is everything a share link carries: the grade, points, counts, finding ids and
// deduction ids with counts. It is built from this allow-list only: never a path, file, table,
// schema, role, database, server, package version, variable name or host (S32 §3.13.8).
type SharePayload struct {
	V  int            `json:"v"`
	G  string         `json:"g"`
	P  int            `json:"p"`
	C  map[string]int `json:"c"`
	F  []string       `json:"f"`
	D  []ShareDed     `json:"d"`
	FV int            `json:"fv"`
}

// ShareDed is a deduction id and how many deductions had it.
type ShareDed struct {
	ID string `json:"id"`
	N  int    `json:"n"`
}

// ShareBase is the page that renders a share link in the browser; the data after # is never sent.
const ShareBase = "https://deedglass.com/s#v1."

// NewShare builds the payload from a report.
func NewShare(r *Report) SharePayload {
	p := SharePayload{V: 1, G: r.Score.Grade, P: r.Score.Points, FV: r.Score.FormulaVersion,
		C: map[string]int{"high": 0, "medium": 0, "low": 0, "info": 0}, F: []string{}, D: []ShareDed{}}
	seen := map[string]bool{}
	for _, f := range r.Findings {
		p.C[f.Severity]++
		if !seen[f.ID] {
			seen[f.ID] = true
			p.F = append(p.F, f.ID)
		}
	}
	sort.Strings(p.F)
	n := map[string]int{}
	var ids []string
	for _, d := range r.Score.Deductions {
		if n[d.ID] == 0 {
			ids = append(ids, d.ID)
		}
		n[d.ID]++
	}
	sort.Strings(ids)
	for _, id := range ids {
		p.D = append(p.D, ShareDed{ID: id, N: n[id]})
	}
	return p
}

// ShareLink is https://deedglass.com/s#v1.<base64url(JCS(payload))>.
func ShareLink(p SharePayload) (string, error) {
	b, err := json.Marshal(p)
	if err != nil {
		return "", err
	}
	c, err := jcs.Transform(b)
	if err != nil {
		return "", err
	}
	return ShareBase + base64.RawURLEncoding.EncodeToString(c), nil
}
