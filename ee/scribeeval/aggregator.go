// Licensed under ee/LICENSE (FSL-1.1-ALv2).
package scribeeval

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"sync"
)

// CellSummary provides export evaluation metrics for a single aggregation cell.
type CellSummary struct {
	EventCount        int      `json:"event_count"`
	ClinicianCount    int      `json:"clinician_count"`
	Suppressed        bool     `json:"suppressed"`
	Verdict           string   `json:"verdict"`
	MedianLatencyMS   *int64   `json:"median_latency_ms,omitempty"`
	P95LatencyMS      *int64   `json:"p95_latency_ms,omitempty"`
	SilentDivergences int      `json:"silent_divergences"`
	DivergenceRate    *float64 `json:"divergence_rate,omitempty"`
}

type cellAccumulator struct {
	key               CohortCellKey
	codeSystem        string
	eventCount        int
	clinicians        map[string]struct{}
	latencies         []int64
	silentDivergences int
}

// CohortAggregator manages research cohort grouping and cell suppression.
type CohortAggregator struct {
	mu    sync.Mutex
	salt  []byte
	cells map[CohortCellKey]*cellAccumulator
}

// NewCohortAggregator initializes an aggregator with a volatile session salt.
func NewCohortAggregator(salt []byte) *CohortAggregator {
	if len(salt) == 0 {
		salt = []byte("deedglass-scribeeval-session-salt")
	}
	return &CohortAggregator{
		salt:  salt,
		cells: make(map[CohortCellKey]*cellAccumulator),
	}
}

// AddEvent incorporates a sanitized event into its corresponding aggregation cell.
func (ca *CohortAggregator) AddEvent(e *EvalEvent, clinicianRawID string) error {
	if e == nil {
		return ErrNilEvent
	}

	ca.mu.Lock()
	defer ca.mu.Unlock()

	key := CohortCellKey{
		ScribeAdapterFamily: e.ScribeAdapterFamily,
		CareSetting:         e.CareSetting,
		ConceptCode:         e.ConceptCode,
		DeltaClass:          e.DeltaClass,
		RegionJurisdiction:  e.RegionJurisdiction,
		FHIRResourceType:    e.FHIRResourceType,
	}

	acc, ok := ca.cells[key]
	if !ok {
		acc = &cellAccumulator{
			key:        key,
			codeSystem: e.CodeSystem,
			clinicians: make(map[string]struct{}),
		}
		ca.cells[key] = acc
	}

	acc.eventCount++
	if clinicianRawID != "" {
		// Generate an ephemeral one-way hash of clinician ID for distinct counting only
		h := sha256.New()
		h.Write(ca.salt)
		h.Write([]byte(clinicianRawID))
		acc.clinicians[hex.EncodeToString(h.Sum(nil))] = struct{}{}
	}

	if e.ReviewLatencyMS > 0 {
		acc.latencies = append(acc.latencies, e.ReviewLatencyMS)
	}
	if e.SilentDivergenceDetected {
		acc.silentDivergences++
	}

	return nil
}

// EvaluateCellSuppression evaluates k >= 11 and c >= 5 cell suppression thresholds.
func EvaluateCellSuppression(eventCount, distinctClinicians int) (bool, string) {
	if eventCount < 11 || distinctClinicians < 5 {
		return true, "[SUPPRESSED: k<11 or c<5]"
	}
	return false, "PASSED"
}

// SummarizeCell returns a computed summary for a given aggregation key.
func (ca *CohortAggregator) SummarizeCell(key CohortCellKey) (CellSummary, bool) {
	ca.mu.Lock()
	defer ca.mu.Unlock()

	acc, ok := ca.cells[key]
	if !ok {
		return CellSummary{}, false
	}

	return ca.computeSummary(acc), true
}

func (ca *CohortAggregator) computeSummary(acc *cellAccumulator) CellSummary {
	cCount := len(acc.clinicians)
	suppressed, verdict := EvaluateCellSuppression(acc.eventCount, cCount)

	sum := CellSummary{
		EventCount:        acc.eventCount,
		ClinicianCount:    cCount,
		Suppressed:        suppressed,
		Verdict:           verdict,
		SilentDivergences: acc.silentDivergences,
	}

	if !suppressed && acc.eventCount > 0 {
		rate := float64(acc.silentDivergences) / float64(acc.eventCount)
		sum.DivergenceRate = &rate

		if len(acc.latencies) > 0 {
			sort.Slice(acc.latencies, func(i, j int) bool { return acc.latencies[i] < acc.latencies[j] })
			med := acc.latencies[len(acc.latencies)/2]
			p95Idx := int(float64(len(acc.latencies)) * 0.95)
			if p95Idx >= len(acc.latencies) {
				p95Idx = len(acc.latencies) - 1
			}
			p95 := acc.latencies[p95Idx]
			sum.MedianLatencyMS = &med
			sum.P95LatencyMS = &p95
		}
	}

	return sum
}

// ExportCohortJSONL writes a JSON Lines stream of CohortExportEvents.
func (ca *CohortAggregator) ExportCohortJSONL(w io.Writer, windowISOWeek string, kMin, cMin int) (int, int, error) {
	ca.mu.Lock()
	defer ca.mu.Unlock()

	if kMin <= 0 {
		kMin = 11
	}
	if cMin <= 0 {
		cMin = 5
	}

	totalCells := 0
	suppressedCells := 0
	enc := json.NewEncoder(w)

	// Sort keys for deterministic output
	keys := make([]CohortCellKey, 0, len(ca.cells))
	for k := range ca.cells {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].ScribeAdapterFamily != keys[j].ScribeAdapterFamily {
			return keys[i].ScribeAdapterFamily < keys[j].ScribeAdapterFamily
		}
		if keys[i].CareSetting != keys[j].CareSetting {
			return keys[i].CareSetting < keys[j].CareSetting
		}
		if keys[i].ConceptCode != keys[j].ConceptCode {
			return keys[i].ConceptCode < keys[j].ConceptCode
		}
		return keys[i].DeltaClass < keys[j].DeltaClass
	})

	for _, k := range keys {
		acc := ca.cells[k]
		totalCells++
		cCount := len(acc.clinicians)
		suppressed := acc.eventCount < kMin || cCount < cMin

		export := CohortExportEvent{
			SchemaVersion:       "scribe-eval-cohort/v1",
			CohortWindowISOWeek: windowISOWeek,
			RegionJurisdiction:  k.RegionJurisdiction,
			CareSetting:         k.CareSetting,
			ScribeAdapterFamily: k.ScribeAdapterFamily,
			FHIRResourceType:    k.FHIRResourceType,
			CodeSystem:          acc.codeSystem,
			ConceptCode:         k.ConceptCode,
			DeltaClass:          string(k.DeltaClass),
			EventCount:          acc.eventCount,
			DistinctClinicians:  cCount,
			SuppressionApplied:  suppressed,
		}

		if suppressed {
			suppressedCells++
		} else {
			if acc.eventCount > 0 {
				rate := float64(acc.silentDivergences) / float64(acc.eventCount)
				export.SilentDivergenceRate = &rate
			}
			if len(acc.latencies) > 0 {
				sort.Slice(acc.latencies, func(i, j int) bool { return acc.latencies[i] < acc.latencies[j] })
				med := acc.latencies[len(acc.latencies)/2]
				p95Idx := int(float64(len(acc.latencies)) * 0.95)
				if p95Idx >= len(acc.latencies) {
					p95Idx = len(acc.latencies) - 1
				}
				p95 := acc.latencies[p95Idx]
				export.MedianReviewLatencyMS = &med
				export.P95ReviewLatencyMS = &p95
			}
		}

		if err := enc.Encode(&export); err != nil {
			return totalCells, suppressedCells, fmt.Errorf("scribe-eval: jsonl encode failed: %w", err)
		}
	}

	return totalCells, suppressedCells, nil
}

// TotalUnderlyingEvents returns the sum of events across all cells.
func (ca *CohortAggregator) TotalUnderlyingEvents() int {
	ca.mu.Lock()
	defer ca.mu.Unlock()
	total := 0
	for _, acc := range ca.cells {
		total += acc.eventCount
	}
	return total
}
