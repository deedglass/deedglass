// Licensed under ee/LICENSE (FSL-1.1-ALv2).
package scribeeval

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"
)

// WardLightTelemetryHook provides event bus ingestion and research export for Ward Light reviews (§20).
type WardLightTelemetryHook interface {
	EmitReviewEvent(ctx context.Context, event *EvalEvent) error
	FlushCohort(ctx context.Context, tenantID string, windowISOWeek string) (*CohortExportManifest, []byte, error)
}

// WardLightHookOptions configures the default Ward Light telemetry hook.
type WardLightHookOptions struct {
	Sanitizer       Sanitizer
	RingBuffer      *RingBuffer
	Aggregator      *CohortAggregator
	SigningKey      ed25519.PrivateKey
	SigningKeyID    string
	InstitutionCode string
	DataOrigin      Origin
}

// DefaultWardLightHook dispatches clinician decisions to volatile RAM and research aggregates.
type DefaultWardLightHook struct {
	mu              sync.Mutex
	sanitizer       Sanitizer
	ringBuffer      *RingBuffer
	aggregator      *CohortAggregator
	signingKey      ed25519.PrivateKey
	signingKeyID    string
	institutionCode string
	dataOrigin      Origin
}

// NewWardLightTelemetryHook creates a hook with volatile memory guarantees.
func NewWardLightTelemetryHook(opts WardLightHookOptions) *DefaultWardLightHook {
	sanitizer := opts.Sanitizer
	if sanitizer == nil {
		sanitizer = &DefaultSanitizer{}
	}
	ringBuffer := opts.RingBuffer
	if ringBuffer == nil {
		ringBuffer = NewRingBuffer(512)
	}
	aggregator := opts.Aggregator
	if aggregator == nil {
		aggregator = NewCohortAggregator(nil)
	}
	origin := opts.DataOrigin
	if origin == "" {
		origin = OriginSyntheticStudy1
	}
	instCode := opts.InstitutionCode
	if instCode == "" {
		instCode = "MAHE-KMC-MCODS"
	}
	keyID := opts.SigningKeyID
	if keyID == "" {
		keyID = "kmc-research-node-01"
	}

	return &DefaultWardLightHook{
		sanitizer:       sanitizer,
		ringBuffer:      ringBuffer,
		aggregator:      aggregator,
		signingKey:      opts.SigningKey,
		signingKeyID:    keyID,
		institutionCode: instCode,
		dataOrigin:      origin,
	}
}

// EmitReviewEvent sanitizes, enqueues to volatile RAM, and aggregates clinician review telemetry.
func (h *DefaultWardLightHook) EmitReviewEvent(ctx context.Context, event *EvalEvent) error {
	if event == nil {
		return ErrNilEvent
	}

	// Strict de-identification and boundary assertions
	if err := h.sanitizer.ValidateAndSanitize(event); err != nil {
		return fmt.Errorf("scribe-eval: hook sanitize error: %w", err)
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	// Push into ephemeral RAM ring buffer
	if err := h.ringBuffer.Push(event); err != nil {
		return fmt.Errorf("scribe-eval: ring buffer error: %w", err)
	}

	// Add to cohort aggregator
	clinicianRef := event.ClinicianRoleBucket
	if err := h.aggregator.AddEvent(event, clinicianRef); err != nil {
		return fmt.Errorf("scribe-eval: aggregator error: %w", err)
	}

	return nil
}

// FlushCohort exports aggregated cells, computes cryptographic hash, and seals the manifest.
func (h *DefaultWardLightHook) FlushCohort(ctx context.Context, tenantID string, windowISOWeek string) (*CohortExportManifest, []byte, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if windowISOWeek == "" {
		windowISOWeek = time.Now().UTC().Format("2006-W01")
	}

	var buf bytes.Buffer
	totalCells, suppressedCells, err := h.aggregator.ExportCohortJSONL(&buf, windowISOWeek, 11, 5)
	if err != nil {
		return nil, nil, fmt.Errorf("scribe-eval: flush cohort jsonl export error: %w", err)
	}

	payloadBytes := buf.Bytes()
	payloadHash := sha256.Sum256(payloadBytes)
	payloadHashHex := hex.EncodeToString(payloadHash[:])

	exportUUID, err := generateUUIDv4()
	if err != nil {
		return nil, nil, err
	}

	totalEvents := h.aggregator.TotalUnderlyingEvents()
	cellsReported := totalCells - suppressedCells

	manifest := &CohortExportManifest{
		Schema:                "https://deedglass.com/schemas/scribe-eval-export-manifest-v1.json",
		ManifestVersion:       "scribe-eval-manifest/v1",
		ExportID:              exportUUID,
		GeneratedAtUTC:        time.Now().UTC().Format(time.RFC3339),
		InstitutionCode:       h.institutionCode,
		DataOrigin:            string(h.dataOrigin),
		ISOWeekWindow:         windowISOWeek,
		TotalAggregatedCells:  totalCells,
		TotalUnderlyingEvents: totalEvents,
		SuppressionSummary: SuppressionSummary{
			CellsReported:   cellsReported,
			CellsSuppressed: suppressedCells,
			KThreshold:      11,
			CThreshold:      5,
		},
		PayloadSHA256:      payloadHashHex,
		SigningKeyID:       h.signingKeyID,
		SignatureAlgorithm: "Ed25519",
	}

	if len(h.signingKey) > 0 {
		if err := SignCohortManifest(manifest, h.signingKey); err != nil {
			return nil, nil, fmt.Errorf("scribe-eval: manifest signing error: %w", err)
		}
	}

	return manifest, payloadBytes, nil
}

// CanonicalManifestBytes formats a deterministic canonical string for Ed25519 signing.
func CanonicalManifestBytes(m *CohortExportManifest) []byte {
	if m == nil {
		return nil
	}
	return []byte(fmt.Sprintf("%s|%s|%s|%s|%s|%s|%d|%d|%d|%d|%s|%s|%s",
		m.ManifestVersion,
		m.ExportID,
		m.GeneratedAtUTC,
		m.InstitutionCode,
		m.DataOrigin,
		m.ISOWeekWindow,
		m.TotalAggregatedCells,
		m.TotalUnderlyingEvents,
		m.SuppressionSummary.CellsReported,
		m.SuppressionSummary.CellsSuppressed,
		m.PayloadSHA256,
		m.SigningKeyID,
		m.SignatureAlgorithm,
	))
}

// SignCohortManifest signs the canonical representation of the export manifest using Ed25519.
func SignCohortManifest(manifest *CohortExportManifest, privKey ed25519.PrivateKey) error {
	if manifest == nil {
		return errors.New("scribe-eval: nil manifest")
	}
	if len(privKey) != ed25519.PrivateKeySize {
		return errors.New("scribe-eval: invalid ed25519 private key size")
	}

	data := CanonicalManifestBytes(manifest)
	sig := ed25519.Sign(privKey, data)
	manifest.Signature = hex.EncodeToString(sig)
	return nil
}

// VerifyCohortManifest checks that the payload matches PayloadSHA256 and validates the Ed25519 signature.
func VerifyCohortManifest(manifest *CohortExportManifest, payloadJSONL []byte, pubKey ed25519.PublicKey) (bool, error) {
	if manifest == nil {
		return false, errors.New("scribe-eval: nil manifest")
	}
	if len(pubKey) != ed25519.PublicKeySize {
		return false, errors.New("scribe-eval: invalid ed25519 public key size")
	}

	// 1. Verify payload SHA-256 integrity
	h := sha256.Sum256(payloadJSONL)
	computedHashHex := hex.EncodeToString(h[:])
	if subtle.ConstantTimeCompare([]byte(computedHashHex), []byte(manifest.PayloadSHA256)) != 1 {
		return false, fmt.Errorf("scribe-eval: payload sha256 mismatch (expected %s, got %s)", manifest.PayloadSHA256, computedHashHex)
	}

	// 2. Decode signature hex
	sigBytes, err := hex.DecodeString(manifest.Signature)
	if err != nil {
		return false, fmt.Errorf("scribe-eval: malformed signature hex: %w", err)
	}

	// 3. Verify Ed25519 signature
	data := CanonicalManifestBytes(manifest)
	valid := ed25519.Verify(pubKey, data, sigBytes)
	return valid, nil
}
