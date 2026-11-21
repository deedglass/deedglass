package recordexport

import (
	"encoding/json"
	"io"
)

// Splunk HTTP Event Collector (HEC) format: one JSON event per line (NDJSON), formatted for
// direct ingestion into Splunk HEC (/services/collector/event) or Splunk Universal Forwarder.
// Each event envelopes deed metadata, agent identity, policy decision, witness verdict, and
// execution latency in the standard Splunk payload format.

type splunkHECWriter struct {
	w   io.Writer
	opt Options
	st  Stats
}

func (x *splunkHECWriter) Stats() Stats { return x.st }
func (x *splunkHECWriter) Close() error { return nil }

func (x *splunkHECWriter) Write(raw []byte) error {
	x.st.Records++
	e, err := parse(raw)
	if err != nil {
		return err
	}
	ev := splunkHECEvent(&e, x.opt)
	b, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	if _, err := x.w.Write(append(b, '\n')); err != nil {
		return err
	}
	x.st.Written++
	return nil
}

func splunkHECEvent(e *envelope, opt Options) map[string]any {
	b := &e.Body
	t := mustTime(b.IssuedAt)
	epochSec := float64(t.UnixMilli()) / 1000.0

	host := "deedglass"
	if opt.Gateway != "" {
		host = opt.Gateway
	}

	payload := map[string]any{
		"deed_id":   b.DeedID,
		"seq":       b.Seq,
		"issued_at": b.IssuedAt,
		"org":       b.Org,
		"kind":      b.Kind,
	}

	if b.PrevHash != "" {
		payload["prev_hash"] = b.PrevHash
	}
	if e.BodyHash != "" {
		payload["body_hash"] = e.BodyHash
	}
	if e.SealKeyID != "" {
		payload["seal_key_id"] = e.SealKeyID
	}
	if b.OnBehalfOf != nil && *b.OnBehalfOf != "" {
		payload["on_behalf_of"] = *b.OnBehalfOf
	}
	if b.Session != nil && *b.Session != "" {
		payload["session"] = *b.Session
	}

	if b.Kind == "admin" && b.Admin != nil {
		payload["admin_event"] = b.Admin.Event
		payload["admin_action"] = b.Admin.Action
		payload["actor"] = map[string]any{
			"type": b.Admin.Actor.Type,
			"id":   b.Admin.Actor.ID,
		}
		if b.Admin.Target != nil {
			payload["target"] = map[string]any{
				"type": b.Admin.Target.Type,
				"id":   b.Admin.Target.ID,
			}
		}
		if b.Admin.Reason != nil {
			payload["reason"] = *b.Admin.Reason
		}
		if b.Admin.DataDigest != nil {
			payload["data_digest"] = *b.Admin.DataDigest
		}
	} else {
		// action or amendment
		if b.Agent != nil {
			payload["agent_id"] = b.Agent.ID
			payload["agent_name"] = b.Agent.Name
		}
		if b.Action != nil {
			payload["action_id"] = b.Action.ID
			payload["tool"] = b.Action.Tool
			payload["connector"] = str(b.Action.Connector)
			payload["mode"] = b.Action.Mode
			payload["request_digest"] = b.Action.RequestDigest
			if b.Action.IntentHash != nil {
				payload["intent_hash"] = *b.Action.IntentHash
				payload["intent_signed"] = b.Action.IntentSigned
			}
		}
		if b.Policy != nil {
			payload["decision"] = b.Policy.Decision
			payload["policy_mode"] = b.Policy.Mode
			if b.Policy.RuleID != nil {
				payload["rule_id"] = *b.Policy.RuleID
			}
			if b.Policy.ID != nil {
				payload["policy_id"] = *b.Policy.ID
			}
		}
		if b.Approval != nil {
			payload["approval_outcome"] = b.Approval.Outcome
			payload["decided_by"] = b.Approval.DecidedBy
			if b.Approval.DecidedAt != nil {
				payload["approval_decided_at"] = *b.Approval.DecidedAt
			}
		}
		if b.Execution != nil {
			payload["status"] = b.Execution.Status
			payload["latency_ms"] = b.Execution.LatencyMS
			if b.Execution.ResultDigest != nil {
				payload["result_digest"] = *b.Execution.ResultDigest
			}
			if b.Execution.ErrorCode != nil {
				payload["error_code"] = *b.Execution.ErrorCode
			}
		}
		if b.Witness != nil {
			payload["witness_level"] = b.Witness.Level
			payload["witness_verdict"] = b.Witness.Verdict
			payload["witness_method"] = b.Witness.Method
			if b.Witness.DetailDigest != nil {
				payload["detail_digest"] = *b.Witness.DetailDigest
			}
		}
		if b.Cost != nil {
			payload["cost_amount"] = b.Cost.Amount
			payload["cost_currency"] = b.Cost.Currency
		}
		if b.Amends != nil {
			payload["amends_deed_id"] = *b.Amends
		}
		payload["summary"] = summary(b)
	}

	indexName := "clinical_audit"
	if b.Kind == "admin" {
		indexName = "admin_audit"
	}

	return map[string]any{
		"time":       epochSec,
		"host":       host,
		"source":     "deedglass:records",
		"sourcetype": "_json",
		"index":      indexName,
		"event":      payload,
	}
}
