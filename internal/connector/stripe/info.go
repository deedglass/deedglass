package stripe

import (
	"encoding/json"

	"github.com/deedglass/deedglass/internal/connector"
)

// Info implements connector.Connector: the catalog entry (S30 §3.4).
func (c *Connector) Info() connector.TypeInfo {
	return connector.TypeInfo{Object: "connector_type", Type: Type, Title: "Stripe",
		Description: "Refunds, invoice items, customer metadata and subscription cancellations in a Stripe sandbox. Live keys are refused, every write carries the action id as its Idempotency-Key, and every write is read back from Stripe.",
		Summary:     "Refunds, invoice items and subscriptions in a Stripe sandbox.",
		Status:      "beta", Icon: "credit-card", Modes: []string{"sandbox_only"},
		Levels: []string{"L0", "L1"},
		CredentialKinds: []connector.CredentialKind{{Kind: "bearer", Fields: []connector.CredentialField{
			{Name: FieldToken, Secret: true, Help: "A restricted key from your Stripe sandbox.", PrefixHint: "rk_test_"}}}},
		ConfigSchema: json.RawMessage(configSchema), DocsURL: DocsURL,
		Setup: []connector.SetupStep{
			{Title: "Create a restricted key", Detail: "In your Stripe sandbox: Developers › API keys › Create restricted key. Don't tag it as an agent key: Deedglass is the approval surface.", URL: KeysURL},
			{Title: "Create the connector", Command: "deedglass connector create --config examples/connectors/stripe.yaml"},
			{Title: "Store the key", Command: "deedglass connector credentials set stripe --from-env STRIPE_TEST_KEY"},
			{Title: "Test the connection", Command: "deedglass connector test stripe"},
		},
		Limits: []string{
			"Sandbox only. Live keys are refused, and a live-mode answer disables the connector.",
			"No payouts, transfers or payment creation.",
			"No Stripe Connect accounts.",
			"L1 shows Stripe's record, not whether the refund was right.",
			"A refund Stripe holds for its own approval is UNVERIFIABLE: Deedglass can't see Stripe's reviewers.",
		},
		Tools: connector.CatalogTools(Type, toolSpecs()),
	}
}
