package saas

import (
	"context"
	"errors"

	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/connector/egress"
)

// DestinationDoc is the doc page of a refused destination.
const DestinationDoc = "https://docs.deedglass.com/errors/destination_blocked"

// DestinationCheck is the connector test's destination line (S30, from S29's GitHub check): base
// resolved through the instance's egress policy, "api.stripe.com → 3.18.12.63 · public".
func DestinationCheck(ctx context.Context, inst *connector.Instance, base string) (connector.Check, bool) {
	pol := inst.Env.Egress
	if pol == nil {
		pol = &egress.Policy{Env: egress.EnvProd}
	}
	chk, err := pol.CheckURL(ctx, base)
	var re *egress.ResolveError
	switch be, blocked := egress.IsBlocked(err); {
	case blocked:
		detail := be.Host
		if be.Addr != "" && be.Addr != be.Host {
			detail += " → " + be.Addr
		}
		if be.Class != "" {
			detail += " · " + be.Class.Label()
		}
		return connector.Check{ID: "destination", Title: "destination", Status: connector.CheckFail, Detail: detail, Fix: be.Reason, DocURL: DestinationDoc}, false
	case errors.As(err, &re):
		return connector.Check{ID: "destination", Title: "destination", Status: connector.CheckFail, Detail: re.Error()}, false
	case err != nil:
		return connector.Check{ID: "destination", Title: "destination", Status: connector.CheckFail, Detail: err.Error()}, false
	}
	d := chk.Host
	if len(chk.Addrs) > 0 && chk.Addrs[0].Addr().String() != chk.Host {
		d += " → " + chk.Addrs[0].Addr().String()
	}
	return connector.Check{ID: "destination", Title: "destination", Status: connector.CheckPass, Detail: d + " · " + chk.Class.Label()}, true
}
