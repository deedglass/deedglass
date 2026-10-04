package slack

import (
	"encoding/json"
	"fmt"
	"unicode/utf8"
)

// Slack's Block Kit limits the card stays inside (S29 §3.4). Lengths are in characters.
const (
	limitBlocks        = 50
	limitSectionText   = 3000
	limitFields        = 10
	limitFieldText     = 2000
	limitHeaderText    = 150
	limitContextItems  = 10
	limitContextText   = 3000
	limitActions       = 25
	limitButtonText    = 75
	limitActionID      = 255
	limitBlockID       = 255
	limitButtonValue   = 2000
	limitButtonURL     = 3000
	limitConfirmTitle  = 100
	limitConfirmText   = 300
	limitConfirmButton = 30
	limitCardBytes     = 16 * 1024 // the whole blocks array, as JSON, stays under this
)

// limitBlock is the part of a block ValidateLimits reads.
type limitBlock struct {
	Type     string            `json:"type"`
	BlockID  string            `json:"block_id"`
	Text     *limitText        `json:"text"`
	Fields   []limitText       `json:"fields"`
	Elements []json.RawMessage `json:"elements"`
}

type limitText struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type limitElement struct {
	Type     string     `json:"type"`
	Text     *limitText `json:"text"`
	ActionID string     `json:"action_id"`
	Value    string     `json:"value"`
	URL      string     `json:"url"`
	Confirm  *struct {
		Title   limitText `json:"title"`
		Text    limitText `json:"text"`
		Confirm limitText `json:"confirm"`
		Deny    limitText `json:"deny"`
	} `json:"confirm"`
}

// ValidateLimits checks a Block Kit blocks array against Slack's limits: at most 50 blocks, section
// text 3,000 characters, 10 fields of 2,000, header 150, button text 75, action_id 255, and the
// whole array under 16 KiB of JSON. Card calls it on every card it builds.
func ValidateLimits(blocks json.RawMessage) error {
	if len(blocks) >= limitCardBytes {
		return fmt.Errorf("slack card: %d bytes of JSON, the limit is under %d", len(blocks), limitCardBytes)
	}
	var bs []limitBlock
	if err := json.Unmarshal(blocks, &bs); err != nil {
		return fmt.Errorf("slack card: blocks are not a JSON array of blocks: %w", err)
	}
	if len(bs) == 0 {
		return fmt.Errorf("slack card: no blocks")
	}
	if len(bs) > limitBlocks {
		return fmt.Errorf("slack card: %d blocks, the limit is %d", len(bs), limitBlocks)
	}
	for i, b := range bs {
		if err := limitCheckLen(i, "block_id", b.BlockID, limitBlockID); err != nil {
			return err
		}
		switch b.Type {
		case "header":
			if b.Text == nil || b.Text.Type != "plain_text" {
				return fmt.Errorf("slack card: block %d: a header needs plain_text", i)
			}
			if err := limitCheckLen(i, "header text", b.Text.Text, limitHeaderText); err != nil {
				return err
			}
		case "section":
			if b.Text == nil && len(b.Fields) == 0 {
				return fmt.Errorf("slack card: block %d: a section needs text or fields", i)
			}
			if b.Text != nil {
				if err := limitCheckLen(i, "section text", b.Text.Text, limitSectionText); err != nil {
					return err
				}
			}
			if len(b.Fields) > limitFields {
				return fmt.Errorf("slack card: block %d: %d fields, the limit is %d", i, len(b.Fields), limitFields)
			}
			for _, f := range b.Fields {
				if err := limitCheckLen(i, "field", f.Text, limitFieldText); err != nil {
					return err
				}
			}
		case "context":
			if len(b.Elements) == 0 || len(b.Elements) > limitContextItems {
				return fmt.Errorf("slack card: block %d: %d context elements, the limit is 1 to %d", i, len(b.Elements), limitContextItems)
			}
			for _, raw := range b.Elements {
				var t limitText
				if err := json.Unmarshal(raw, &t); err != nil {
					return fmt.Errorf("slack card: block %d: %w", i, err)
				}
				if err := limitCheckLen(i, "context text", t.Text, limitContextText); err != nil {
					return err
				}
			}
		case "actions":
			if len(b.Elements) == 0 || len(b.Elements) > limitActions {
				return fmt.Errorf("slack card: block %d: %d actions, the limit is 1 to %d", i, len(b.Elements), limitActions)
			}
			for _, raw := range b.Elements {
				if err := limitCheckElement(i, raw); err != nil {
					return err
				}
			}
		case "divider":
		default:
			return fmt.Errorf("slack card: block %d: unexpected block type %q", i, b.Type)
		}
	}
	return nil
}

func limitCheckElement(i int, raw json.RawMessage) error {
	var e limitElement
	if err := json.Unmarshal(raw, &e); err != nil {
		return fmt.Errorf("slack card: block %d: %w", i, err)
	}
	if e.Type != "button" {
		return fmt.Errorf("slack card: block %d: unexpected element type %q", i, e.Type)
	}
	if e.Text == nil || e.Text.Text == "" {
		return fmt.Errorf("slack card: block %d: a button needs text", i)
	}
	if e.ActionID == "" {
		return fmt.Errorf("slack card: block %d: a button needs an action_id", i)
	}
	for _, c := range []struct {
		what string
		s    string
		max  int
	}{
		{"button text", e.Text.Text, limitButtonText},
		{"action_id", e.ActionID, limitActionID},
		{"button value", e.Value, limitButtonValue},
		{"button url", e.URL, limitButtonURL},
	} {
		if err := limitCheckLen(i, c.what, c.s, c.max); err != nil {
			return err
		}
	}
	if e.Confirm != nil {
		for _, c := range []struct {
			what string
			s    string
			max  int
		}{
			{"confirm title", e.Confirm.Title.Text, limitConfirmTitle},
			{"confirm text", e.Confirm.Text.Text, limitConfirmText},
			{"confirm button", e.Confirm.Confirm.Text, limitConfirmButton},
			{"deny button", e.Confirm.Deny.Text, limitConfirmButton},
		} {
			if err := limitCheckLen(i, c.what, c.s, c.max); err != nil {
				return err
			}
		}
	}
	return nil
}

func limitCheckLen(i int, what, s string, max int) error {
	if n := utf8.RuneCountInString(s); n > max {
		return fmt.Errorf("slack card: block %d: %s is %d characters, the limit is %d", i, what, n, max)
	}
	return nil
}
