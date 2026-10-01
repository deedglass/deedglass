package rest

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/vault"
)

var _ connector.ReadBacker = (*Connector)(nil)

// ReadBack implements connector.ReadBacker (S19): it runs the read tool named in call through the
// same path as any call (S16's path rules, egress and the pinned address, the credential added
// last, redaction, the size cap) on the read client. It isn't an action: no deed, no budget. call
// is the read tool's call; res (the write's result) isn't needed for REST.
func (c *Connector) ReadBack(ctx context.Context, inst *connector.Instance, call connector.Call, _ connector.Result) (connector.Observation, error) {
	st, err := parse(inst)
	if err != nil {
		return connector.Observation{}, &connector.Error{Kind: connector.KindConfig, Code: "connector_misconfigured",
			AgentMessage: "This connector's config can't be read. An admin can check Console › Connectors.", Err: err}
	}
	t, ok := st.tools[call.Tool.Name]
	if !ok {
		return connector.Observation{}, &connector.Error{Kind: connector.KindInvalidRequest, Code: "tool_unknown",
			AgentMessage: fmt.Sprintf("The connector %s has no tool named %s.", inst.Name, clip(call.Tool.Name))}
	}
	if t.sideOf() != connector.SideEffectRead {
		// Config validation refuses this; a read-back must never write.
		return connector.Observation{}, &connector.Error{Kind: connector.KindConfig, Code: "connector_misconfigured",
			AgentMessage: fmt.Sprintf("%s writes, so it can't be a read-back.", clip(t.Name))}
	}
	args, err := decodeArgs(call.Args)
	if err != nil {
		return connector.Observation{}, err
	}
	b, err := st.build(t, args)
	if err != nil {
		return connector.Observation{}, err
	}
	vault.RedactorFrom(ctx).Add(b.secrets...)
	var sec *vault.Secret
	if st.cfg.Auth.Kind != "none" {
		if sec, err = inst.Secret(ctx); err != nil {
			return connector.Observation{}, err
		}
		defer sec.Destroy()
	}
	read, _ := st.clients(inst)
	res, err := st.do(ctx, read, t, b, sec, call)
	obs := connector.Observation{Meta: res.Meta, LatencyMS: res.Latency.Milliseconds()}
	if s, ok := res.Meta["http_status"].(int); ok {
		obs.Status = s
	}
	if err != nil {
		return obs, err
	}
	var out struct {
		Body json.RawMessage `json:"body"`
	}
	if json.Unmarshal(res.Output, &out) == nil {
		obs.Body = out.Body
	}
	return obs, nil
}
