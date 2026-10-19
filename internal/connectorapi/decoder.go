package connectorapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/connector/postgres"
	"github.com/deedglass/deedglass/internal/connector/postgres/setup"
	"github.com/deedglass/deedglass/internal/decoder"
	"github.com/deedglass/deedglass/internal/deed"
	"github.com/deedglass/deedglass/internal/httpx"
	"github.com/deedglass/deedglass/internal/identity"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/store"
)

// The decoder routes (S20 §3.14): GET and DELETE /v1/connectors/{id}/decoder.

// DecoderControl is what the routes and the connector writes need from the decoder's supervisor.
type DecoderControl interface {
	Drop(ctx context.Context, org ids.OrgID, connectorID ids.ConnectorID, actor deed.Ref, requestID string) (decoder.DropResult, error)
	Retire(ctx context.Context, org ids.OrgID, connectorID ids.ConnectorID, reason string) error
	Sync(ctx context.Context) error
}

// DefaultRecent is how many recent frames GET …/decoder shows without ?recent=.
const DefaultRecent = 10

// DecoderRoutes are the two decoder routes.
func DecoderRoutes(a *API) []httpx.Route {
	admin := []string{httpx.ScopeAdmin}
	return []httpx.Route{
		{Method: "GET", Pattern: "/v1/connectors/{id}/decoder", OperationID: "getConnectorDecoder", Auth: httpx.SessionOrKey,
			MinRole: identity.RoleViewer, Scopes: admin, AgentKeyMessage: AgentKeyMessage, Handler: a.getDecoder},
		{Method: "DELETE", Pattern: "/v1/connectors/{id}/decoder", OperationID: "dropConnectorDecoder", Auth: httpx.SessionOrKey,
			MinRole: identity.RoleAdmin, Scopes: admin, AgentKeyMessage: AgentKeyMessage, Handler: a.dropDecoder,
			MaxBody: 1 << 10, Purpose: "dropping a decoder slot"},
	}
}

// witnessed reports whether c is a postgres connector, or (ADR-108) a fhir connector, with
// witness.enabled.
func witnessed(c store.Connector) bool {
	switch c.Type {
	case postgres.Type:
		cfg, errs := postgres.ParseConfig(c.Config)
		return len(errs) == 0 && cfg.Witness.Enabled
	case "fhir":
		var cfg struct {
			Witness struct {
				Enabled bool `json:"enabled"`
			} `json:"witness"`
		}
		return json.Unmarshal(c.Config, &cfg) == nil && cfg.Witness.Enabled
	}
	return false
}

func notWitnessed(c store.Connector) *httpx.Error {
	if c.Type == "fhir" {
		return httpx.E(httpx.CodeWitnessNotConfigured, httpx.Msgf(
			"Proven checks aren't on for the connector %q, so it has no decoder. Set witness.enabled and witness.server in its config and witness_dsn in its credential (docs/connectors/fhir.md).", c.Name))
	}
	return httpx.E(httpx.CodeWitnessNotConfigured, httpx.Msgf(
		"Checks aren't on for the connector %q, so it has no decoder. Set witness.enabled to true in its config, then run deedglass connector postgres check %s.", c.Name, c.Name))
}

func (a *API) getDecoder(w http.ResponseWriter, r *http.Request) error {
	p, err := principal(r)
	if err != nil {
		return err
	}
	n := DefaultRecent
	if q := r.URL.Query().Get("recent"); q != "" {
		v, err := strconv.Atoi(q)
		if err != nil || v < 0 || v > decoder.MaxRecent {
			return httpx.E(httpx.CodeValidationFailed, httpx.Param("recent"), httpx.Msg("recent must be a whole number from 0 to 50."))
		}
		n = v
	}
	var out decoder.View
	err = a.DB.OrgReadTx(r.Context(), p.OrgID, func(ctx context.Context, tx pgx.Tx) error {
		c, err := lookup(ctx, tx, r.PathValue("id"), false)
		if err != nil {
			return err
		}
		row, err := store.GetDecoderSlot(ctx, tx, c.ID, false)
		switch {
		case errors.Is(err, store.ErrNotFound) && witnessed(c):
			// Witnessed, but no runner has picked it up yet: the next sync creates the row.
			row = store.DecoderSlot{OrgID: c.OrgID, ConnectorID: c.ID, SlotName: setup.SlotName(c.ID), Publication: setup.Publication,
				Status: decoder.StatusPending, StatusSince: c.UpdatedAt, UpdatedAt: c.UpdatedAt}
		case errors.Is(err, store.ErrNotFound):
			return notWitnessed(c)
		case err != nil:
			return err
		}
		recent, err := store.RecentFrames(ctx, tx, c.ID, n)
		if err != nil {
			return err
		}
		out = decoder.BuildView(row, c.Name, recent)
		return nil
	})
	if err != nil {
		return err
	}
	return httpx.WriteJSON(w, http.StatusOK, out)
}

type dropBody struct {
	Confirm string `json:"confirm"`
}

// DropResult is DELETE …/decoder's answer.
type DropResult struct {
	Object    string `json:"object"` // "decoder"
	Connector string `json:"connector"`
	Name      string `json:"name"`
	Slot      string `json:"slot"`
	Dropped   bool   `json:"dropped"`
	Deed      string `json:"deed"`
	Status    string `json:"status"`
}

func (a *API) dropDecoder(w http.ResponseWriter, r *http.Request) error {
	p, actor, err := caller(r)
	if err != nil {
		return err
	}
	var in dropBody
	if err := httpx.DecodeJSON(r, &in); err != nil {
		return err
	}
	var c store.Connector
	err = a.DB.OrgReadTx(r.Context(), p.OrgID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		if c, err = lookup(ctx, tx, r.PathValue("id"), false); err != nil {
			return err
		}
		row, err := store.GetDecoderSlot(ctx, tx, c.ID, false)
		if errors.Is(err, store.ErrNotFound) || (err == nil && row.Status == decoder.StatusDisabled) {
			return notWitnessed(c)
		}
		return err
	})
	if err != nil {
		return err
	}
	if in.Confirm != c.Name {
		return httpx.E(httpx.CodeValidationFailed, httpx.Param("confirm"),
			httpx.Msgf("Type the connector's name to confirm: confirm must be %q. Dropping the slot starts a new baseline.", c.Name))
	}
	if a.Decoder == nil {
		return httpx.E(httpx.CodeServiceUnavailable, httpx.Msg("The decoder isn't running on this server. Restart deedglass serve, then retry."))
	}
	cid, err := ids.Parse[ids.ConnectorKind](c.ID)
	if err != nil {
		return err
	}
	res, err := a.Decoder.Drop(r.Context(), p.OrgID, cid, actor.Ref, httpx.RequestID(r.Context()))
	switch {
	case errors.Is(err, decoder.ErrSlotBusy):
		return httpx.E(httpx.CodeConflict, httpx.Msgf("Another process is reading slot %s right now. Stop the other Deedglass process, or wait a minute and retry.", res.Slot))
	case err != nil:
		if ce := connector.AsError(err); ce != nil && ce.Code == connector.CodeCredentialsMissing {
			return httpx.E(httpx.CodeCredentialsMissing, httpx.Cause(err))
		}
		return httpx.E(httpx.CodeServiceUnavailable, httpx.Cause(err),
			httpx.Msgf("Deedglass couldn't reach the database of %q to drop its slot, so nothing changed. Check the connector (deedglass connector test %s), then retry.", c.Name, c.Name))
	}
	return httpx.WriteJSON(w, http.StatusOK, DropResult{Object: "decoder", Connector: c.ID, Name: c.Name, Slot: res.Slot, Dropped: true,
		Deed: res.Deed, Status: decoder.StatusDropped})
}

// syncDecoder tells the decoder a connector changed (created, updated, credentials stored).
func (a *API) syncDecoder(ctx context.Context) {
	if a.Decoder == nil {
		return
	}
	if err := a.Decoder.Sync(context.WithoutCancel(ctx)); err != nil {
		a.Log.Warn("decoder: sync after a connector change", "err", err)
	}
}

// retireDecoder drops a connector's slot before the connector is deleted or stops being witnessed.
func (a *API) retireDecoder(ctx context.Context, org ids.OrgID, c store.Connector, reason string) {
	if a.Decoder == nil || c.Type != postgres.Type {
		return
	}
	cid, err := ids.Parse[ids.ConnectorKind](c.ID)
	if err != nil {
		return
	}
	if err := a.Decoder.Retire(context.WithoutCancel(ctx), org, cid, reason); err != nil {
		a.Log.Warn("decoder: retiring a slot", "connector", c.ID, "err", err)
	}
}
