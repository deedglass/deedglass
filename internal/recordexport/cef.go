package recordexport

import (
	"fmt"
	"io"
	"strconv"
	"strings"
)

// Common Event Format (CEF) v0: one syslog/SIEM event per sealed record, conforming to the
// Micro Focus / ArcSight CEF Implementation Standard.
// Compatible with Microsoft Sentinel (via CEF/Syslog data connector), Splunk, IBM QRadar,
// and hospital security operation centers (SOC).
// Format: CEF:Version|Device Vendor|Device Product|Device Version|Device Event Class ID|Name|Severity|[Extension]

type cefWriter struct {
	w   io.Writer
	opt Options
	st  Stats
}

func (x *cefWriter) Stats() Stats { return x.st }
func (x *cefWriter) Close() error { return nil }

func (x *cefWriter) Write(raw []byte) error {
	x.st.Records++
	e, err := parse(raw)
	if err != nil {
		return err
	}
	line := cefLine(&e, x.opt)
	if _, err := io.WriteString(x.w, line+"\n"); err != nil {
		return err
	}
	x.st.Written++
	return nil
}

func cefEscapeHeader(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `|`, `\|`)
	return s
}

func cefEscapeExt(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `=`, `\=`)
	s = strings.ReplaceAll(s, "\n", `\n`)
	s = strings.ReplaceAll(s, "\r", `\r`)
	return s
}

func cefLine(e *envelope, opt Options) string {
	b := &e.Body
	producer := opt.Producer
	if producer == "" {
		producer = "0.15.0"
	}

	classID := "act_committed"
	name := "Action Committed"
	sev := 1 // Low / Informational

	switch b.Kind {
	case "admin":
		classID = "admin_event"
		name = "Admin Event"
		if b.Admin != nil && b.Admin.Event != "" {
			name = "Admin Event " + b.Admin.Event
		}
		sev = 5
	case "amendment":
		classID = "act_amended"
		name = "Record Amended"
		sev = 3
	default:
		// action
		if b.Witness != nil && b.Witness.Verdict == "MISMATCH" {
			classID = "witness_mismatch"
			name = "Witness Mismatch Detected"
			sev = 8
		} else if b.Policy != nil && b.Policy.Decision == "deny" {
			classID = "act_blocked"
			name = "Action Blocked by Policy"
			sev = 5
		} else if b.Execution != nil && b.Execution.Status != "succeeded" {
			classID = "act_failed"
			name = "Action Failed Execution"
			sev = 4
		} else if b.Action != nil {
			actName := op(b.Action.Tool)
			switch actName {
			case "create", "insert", "update", "patch", "delete", "remove":
				sev = 3
			}
		}
	}

	header := fmt.Sprintf("CEF:0|%s|%s|%s|%s|%s|%d",
		cefEscapeHeader("Deedglass"),
		cefEscapeHeader("Deedglass"),
		cefEscapeHeader(producer),
		cefEscapeHeader(classID),
		cefEscapeHeader(name),
		sev,
	)

	exts := make([]string, 0, 16)
	add := func(k, v string) {
		if v != "" {
			exts = append(exts, k+"="+cefEscapeExt(v))
		}
	}

	// Event timestamp (epoch milliseconds)
	t := mustTime(b.IssuedAt)
	if !t.IsZero() {
		add("rt", strconv.FormatInt(t.UnixMilli(), 10))
	}

	add("externalId", b.DeedID)
	add("cn2", strconv.FormatInt(b.Seq, 10))
	add("cn2Label", "SequenceNumber")
	add("cs6", b.DeedID)
	add("cs6Label", "DeedID")

	if b.OnBehalfOf != nil && *b.OnBehalfOf != "" {
		add("duser", *b.OnBehalfOf)
	}

	if b.Kind == "admin" && b.Admin != nil {
		add("suser", b.Admin.Actor.ID)
		add("cs1", b.Admin.Event)
		add("cs1Label", "AdminEvent")
		add("cs2", b.Admin.Action)
		add("cs2Label", "AdminAction")
		if b.Admin.Reason != nil && *b.Admin.Reason != "" {
			add("msg", *b.Admin.Reason)
		}
	} else {
		// action or amendment
		if b.Agent != nil {
			add("suser", b.Agent.ID)
			if b.OnBehalfOf == nil || *b.OnBehalfOf == "" {
				add("duser", b.Agent.Name)
			}
		}
		if b.Action != nil {
			add("act", b.Action.Tool)
			add("cs5", b.Action.ID)
			add("cs5Label", "ActionID")
		}
		if b.Policy != nil {
			add("cs2", b.Policy.Decision)
			add("cs2Label", "PolicyDecision")
			if b.Policy.RuleID != nil && *b.Policy.RuleID != "" {
				add("cs1", *b.Policy.RuleID)
				add("cs1Label", "RuleID")
			}
		}
		if b.Witness != nil {
			add("cs3", b.Witness.Level)
			add("cs3Label", "WitnessLevel")
			add("cs4", b.Witness.Verdict)
			add("cs4Label", "WitnessVerdict")
		}
		if b.Execution != nil {
			add("cn1", strconv.FormatInt(b.Execution.LatencyMS, 10))
			add("cn1Label", "LatencyMs")
			if b.Execution.Status == "succeeded" {
				add("outcome", "success")
			} else {
				add("outcome", "failure")
				if b.Execution.ErrorCode != nil && *b.Execution.ErrorCode != "" {
					add("reason", *b.Execution.ErrorCode)
				}
			}
		} else if b.Policy != nil && b.Policy.Decision == "deny" {
			add("outcome", "failure")
			add("reason", "policy_deny")
		}
		if b.Kind == "amendment" && b.Amends != nil {
			add("cs5", *b.Amends)
			add("cs5Label", "AmendsDeedID")
		}
		add("msg", summary(b))
	}

	if len(exts) == 0 {
		return header
	}
	return header + "|" + strings.Join(exts, " ")
}
