package learn

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
)

// ClaudePhraser rewords suggestions through the Anthropic Messages API (S28 §3.7, ADR-059). It
// is a small standard-library client, not the Go SDK: it reads nothing from the environment (no
// ANTHROPIC_API_KEY, ANTHROPIC_AUTH_TOKEN, ANTHROPIC_BASE_URL, profile or workload-identity
// variables), sends only the Deedglass key it was given, to the pinned host, through the egress
// door's first-party client (no proxy from the environment). One request, a 10 s timeout, and at
// most one retry, after a 429 (honouring retry-after up to 10 s), a 529 or a 5xx.

// AnthropicBaseURL is pinned in code.
const AnthropicBaseURL = "https://api.anthropic.com"

// AnthropicVersion is the Messages API version header.
const AnthropicVersion = "2023-06-01"

// PhrasingSystemPrompt is the system prompt (no org data).
const PhrasingSystemPrompt = "You reword short notes for Deedglass, a tool that records what AI agents did. Rules: calm, exact, second person. Use only the numbers given. Keep every {placeholder} unchanged. No exclamation marks, no emoji, no hype words. Title ≤ 90 characters, sentence case. Rationale ≤ 400 characters, at most 2 sentences."

// ClaudePhraser is the Phraser over the Messages API.
type ClaudePhraser struct {
	APIKey  string       // DEEDGLASS_ANTHROPIC_API_KEY; empty → no request is ever made
	ModelID string       // DEEDGLASS_ANTHROPIC_MODEL or DefaultPhrasingModel
	HTTP    *http.Client // egress.FirstParty (cmd/deedglass); tests pass an httptest client
	BaseURL string       // tests only; empty → AnthropicBaseURL
	Timeout time.Duration
	// Sleep waits before the retry (tests replace it).
	Sleep func(ctx context.Context, d time.Duration) error
}

// Model is the model id.
func (c *ClaudePhraser) Model() string {
	if c.ModelID == "" {
		return DefaultPhrasingModel
	}
	return c.ModelID
}

type claudeRequest struct {
	Model        string          `json:"model"`
	MaxTokens    int             `json:"max_tokens"`
	System       string          `json:"system"`
	Messages     []claudeMessage `json:"messages"`
	OutputConfig claudeOutput    `json:"output_config"`
}

type claudeMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type claudeOutput struct {
	Effort string       `json:"effort"`
	Format claudeFormat `json:"format"`
}

type claudeFormat struct {
	Type   string         `json:"type"`
	Schema map[string]any `json:"schema"`
}

var phrasingSchema = map[string]any{
	"type": "object", "additionalProperties": false, "required": []string{"title", "rationale"},
	"properties": map[string]any{
		"title":     map[string]any{"type": "string", "maxLength": MaxPhrasedTitle},
		"rationale": map[string]any{"type": "string", "maxLength": MaxPhrasedRationale},
	},
}

type claudeResponse struct {
	StopReason string `json:"stop_reason"`
	Content    []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	Usage struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

// RequestBody is the exact JSON sent for in (the privacy test reads it).
func (c *ClaudePhraser) RequestBody(in *PhrasingInput) ([]byte, error) {
	user, err := json.Marshal(in)
	if err != nil {
		return nil, err
	}
	return json.Marshal(claudeRequest{Model: c.Model(), MaxTokens: 1024, System: PhrasingSystemPrompt,
		Messages:     []claudeMessage{{Role: "user", Content: string(user)}},
		OutputConfig: claudeOutput{Effort: "low", Format: claudeFormat{Type: "json_schema", Schema: phrasingSchema}}})
}

// Phrase makes the call.
func (c *ClaudePhraser) Phrase(ctx context.Context, in *PhrasingInput) (*PhraseResult, error) {
	if c.APIKey == "" || c.HTTP == nil {
		return nil, &PhraseError{Reason: "not_configured"}
	}
	body, err := c.RequestBody(in)
	if err != nil {
		return nil, err
	}
	t0 := time.Now()
	for attempt := 0; ; attempt++ {
		res, wait, err := c.once(ctx, body)
		if err == nil {
			res.Duration = time.Since(t0)
			return res, nil
		}
		if wait < 0 || attempt >= 1 {
			return nil, err
		}
		sleep := c.Sleep
		if sleep == nil {
			sleep = func(ctx context.Context, d time.Duration) error {
				t := time.NewTimer(d)
				defer t.Stop()
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-t.C:
					return nil
				}
			}
		}
		if err := sleep(ctx, wait); err != nil {
			return nil, &PhraseError{Reason: "timeout"}
		}
	}
}

// once makes one request. wait ≥ 0 means the failure may be retried after wait.
func (c *ClaudePhraser) once(ctx context.Context, body []byte) (*PhraseResult, time.Duration, error) {
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	base := c.BaseURL
	if base == "" {
		base = AnthropicBaseURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return nil, -1, err
	}
	req.Header.Set("x-api-key", c.APIKey)
	req.Header.Set("anthropic-version", AnthropicVersion)
	req.Header.Set("content-type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, -1, &PhraseError{Reason: "timeout"}
		}
		return nil, -1, &PhraseError{Reason: "network"}
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return nil, -1, &PhraseError{Reason: "network"}
	}
	switch {
	case resp.StatusCode == http.StatusTooManyRequests:
		wait := time.Second
		if s, err := strconv.Atoi(resp.Header.Get("retry-after")); err == nil && s >= 0 {
			wait = time.Duration(s) * time.Second
		}
		if wait > 10*time.Second {
			return nil, -1, &PhraseError{Reason: "http_429"}
		}
		return nil, wait, &PhraseError{Reason: "http_429"}
	case resp.StatusCode == 529 || resp.StatusCode >= 500:
		return nil, time.Second, &PhraseError{Reason: "http_" + strconv.Itoa(resp.StatusCode)}
	case resp.StatusCode != http.StatusOK:
		return nil, -1, &PhraseError{Reason: "http_" + strconv.Itoa(resp.StatusCode)}
	}
	var r claudeResponse
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, -1, &PhraseError{Reason: "unreadable"}
	}
	res := &PhraseResult{Model: c.Model(), InputTokens: r.Usage.InputTokens, OutputTokens: r.Usage.OutputTokens}
	if r.StopReason != "end_turn" {
		return nil, -1, &PhraseError{Reason: r.StopReason, Result: res}
	}
	text := ""
	for _, b := range r.Content {
		if b.Type == "text" {
			text += b.Text
		}
	}
	var out struct {
		Title     string `json:"title"`
		Rationale string `json:"rationale"`
	}
	d := json.NewDecoder(bytes.NewReader([]byte(text)))
	d.DisallowUnknownFields()
	if err := d.Decode(&out); err != nil {
		return nil, -1, &PhraseError{Reason: "validation:schema", Result: res}
	}
	res.Title, res.Rationale = out.Title, out.Rationale
	return res, 0, nil
}

// String never prints the key.
func (c *ClaudePhraser) String() string { return fmt.Sprintf("ClaudePhraser{model=%s}", c.Model()) }
