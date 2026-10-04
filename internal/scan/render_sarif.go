package scan

import (
	"encoding/json"
	"sort"
	"strings"
)

// SARIF 2.1.0 (OASIS): one run, one rule per finding id, one result per finding location under
// --dir (user configs outside --dir are left out). GitHub code scanning reads it.

type sarifLog struct {
	Schema  string     `json:"$schema"`
	Version string     `json:"version"`
	Runs    []sarifRun `json:"runs"`
}

type sarifRun struct {
	Tool       sarifTool      `json:"tool"`
	Results    []sarifResult  `json:"results"`
	Properties map[string]any `json:"properties"`
}

type sarifTool struct {
	Driver sarifDriver `json:"driver"`
}

type sarifDriver struct {
	Name           string      `json:"name"`
	Version        string      `json:"version,omitempty"`
	InformationURI string      `json:"informationUri"`
	Rules          []sarifRule `json:"rules"`
}

type sarifRule struct {
	ID               string       `json:"id"`
	ShortDescription sarifMessage `json:"shortDescription"`
	FullDescription  sarifMessage `json:"fullDescription"`
	HelpURI          string       `json:"helpUri"`
}

type sarifMessage struct {
	Text string `json:"text"`
}

type sarifResult struct {
	RuleID    string          `json:"ruleId"`
	Level     string          `json:"level"`
	Message   sarifMessage    `json:"message"`
	Locations []sarifLocation `json:"locations,omitempty"`
}

type sarifLocation struct {
	PhysicalLocation sarifPhysical `json:"physicalLocation"`
}

type sarifPhysical struct {
	ArtifactLocation sarifArtifact `json:"artifactLocation"`
}

type sarifArtifact struct {
	URI string `json:"uri"`
}

// SARIFLevel maps a severity: high → error, medium → warning, low and info → note.
func SARIFLevel(sev string) string {
	switch sev {
	case "high":
		return "error"
	case "medium":
		return "warning"
	}
	return "note"
}

// RenderSARIF is deedglass-scan.sarif.
func RenderSARIF(r *Report) ([]byte, int, error) {
	rules := map[string]Finding{}
	var results []sarifResult
	for _, f := range r.Findings {
		if _, ok := rules[f.ID]; !ok {
			rules[f.ID] = f
		}
		res := sarifResult{RuleID: f.ID, Level: SARIFLevel(f.Severity), Message: sarifMessage{Text: f.Title + ". " + f.Detail}}
		for _, p := range f.Paths {
			if strings.HasPrefix(p, "~") || strings.HasPrefix(p, "/") || strings.HasPrefix(p, "..") {
				continue // outside --dir: never in SARIF
			}
			res.Locations = append(res.Locations, sarifLocation{PhysicalLocation: sarifPhysical{ArtifactLocation: sarifArtifact{URI: p}}})
		}
		results = append(results, res)
	}
	ids := make([]string, 0, len(rules))
	for id := range rules {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var rs []sarifRule
	for _, id := range ids {
		f := rules[id]
		rs = append(rs, sarifRule{ID: id, ShortDescription: sarifMessage{Text: f.Title}, FullDescription: sarifMessage{Text: f.Detail},
			HelpURI: "https://docs.deedglass.com/scan#" + id})
	}
	if rs == nil {
		rs = []sarifRule{}
	}
	if results == nil {
		results = []sarifResult{}
	}
	log := sarifLog{
		Schema:  "https://json.schemastore.org/sarif-2.1.0.json",
		Version: "2.1.0",
		Runs: []sarifRun{{
			Tool:       sarifTool{Driver: sarifDriver{Name: "deedglass", Version: r.DeedglassVersion, InformationURI: "https://docs.deedglass.com/scan", Rules: rs}},
			Results:    results,
			Properties: map[string]any{"deedglass_score": r.Score},
		}},
	}
	b, err := json.MarshalIndent(log, "", "  ")
	return append(b, '\n'), len(rs), err
}
