package connectorapi

import (
	"context"
	"log/slog"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/deed"
	"github.com/deedglass/deedglass/internal/ledger"
	"github.com/deedglass/deedglass/internal/store"
)

// Breaker is connector.Breaker over the store and the Ledger (S30): it disables the connector,
// records health.reason, seals connector.disabled as {system, deedglass} once, and drops the
// cached instance so the next call resolves the disabled row.
type Breaker struct {
	DB       *store.DB
	Ledger   *ledger.Ledger
	Registry *connector.Registry
	Log      *slog.Logger // the alert line; nil: slog.Default()
}

var _ connector.Breaker = (*Breaker)(nil)

// Trip implements connector.Breaker. detail goes in the deed's admin reason (Stripe's request id;
// never a key).
func (b *Breaker) Trip(ctx context.Context, inst *connector.Instance, reason, detail string) error {
	err := b.DB.OrgTx(ctx, inst.OrgID, func(ctx context.Context, tx pgx.Tx) error {
		changed, err := store.DisableConnector(ctx, tx, inst.ID.String(), reason)
		if err != nil || !changed {
			return err
		}
		_, err = b.Ledger.RecordAdmin(ctx, tx, ledger.AdminEvent{Action: ledger.AdminConnectorDisabled,
			Actor: deed.Ref{Type: "system", ID: "deedglass"}, Target: &deed.Ref{Type: "connector", ID: inst.ID.String()},
			Reason: detail, Data: map[string]any{"type": inst.Type, "connector": inst.Name, "reason": reason}})
		return err
	})
	if b.Registry != nil {
		b.Registry.Forget(inst.ID.String())
	}
	log := b.Log
	if log == nil {
		log = slog.Default()
	}
	log.ErrorContext(ctx, "connector: a safety breaker disabled the connector", "alert", "connector_disabled", "reason", reason,
		"connector", inst.Name, "type", inst.Type, "connector_id", inst.ID.String(), "org_id", inst.OrgID.String(), "detail", detail, "recorded", err == nil)
	return err
}
