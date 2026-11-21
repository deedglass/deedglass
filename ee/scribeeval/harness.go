// Licensed under ee/LICENSE (FSL-1.1-ALv2).
package scribeeval

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
)

// BatchIngestReport records statistics from a synthetic or shadow batch ingestion run.
type BatchIngestReport struct {
	TotalRead              int `json:"total_read"`
	TotalIngested          int `json:"total_ingested"`
	TotalRejected          int `json:"total_rejected"`
	PHISentinelsCaught     int `json:"phi_sentinels_caught"`
	PromptInjectionsCaught int `json:"prompt_injections_caught"`
	SilentDivergencesCount int `json:"silent_divergences_count"`
}

// IngestBatch streams an input JSON array or JSON Lines stream into the sanitizer and aggregator.
func IngestBatch(r io.Reader, sanitizer Sanitizer, agg *CohortAggregator, buf *RingBuffer, defaultClinician string) (*BatchIngestReport, error) {
	if r == nil {
		return nil, errors.New("nil reader")
	}
	if sanitizer == nil {
		sanitizer = &DefaultSanitizer{}
	}

	report := &BatchIngestReport{}
	scanner := bufio.NewScanner(r)

	// Scan lines
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		// Skip array delimiters if present
		if bytes.Equal(line, []byte("[")) || bytes.Equal(line, []byte("]")) || bytes.Equal(line, []byte(",")) {
			continue
		}
		line = bytes.TrimSuffix(line, []byte(","))

		report.TotalRead++
		event, err := sanitizer.ValidateJSONClosed(line)
		if err != nil {
			report.TotalRejected++
			if errors.Is(err, ErrPHISentinelMatch) {
				report.PHISentinelsCaught++
			}
			if errors.Is(err, ErrPromptInjectionDetected) {
				report.PromptInjectionsCaught++
			}
			continue
		}

		if event.SilentDivergenceDetected {
			report.SilentDivergencesCount++
		}

		if buf != nil {
			_ = buf.Push(event)
		}
		if agg != nil {
			_ = agg.AddEvent(event, defaultClinician)
		}
		report.TotalIngested++
	}

	if err := scanner.Err(); err != nil {
		return report, fmt.Errorf("scribe-eval: batch scan error: %w", err)
	}

	return report, nil
}
