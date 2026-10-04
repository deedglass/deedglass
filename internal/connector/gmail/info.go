package gmail

import (
	"encoding/json"

	"github.com/deedglass/deedglass/internal/connector"
)

// Info implements connector.Connector: the catalog entry (S30 §3.4).
func (c *Connector) Info() connector.TypeInfo {
	return connector.TypeInfo{Object: "connector_type", Type: Type, Title: "Gmail",
		Description: "Write and read drafts in one Gmail mailbox. No tool sends, and the connector can't reach a send endpoint, so people send drafts themselves. Every draft is read back from Gmail.",
		Summary:     "Drafts in one Gmail mailbox. People send them.",
		Status:      "beta", Icon: "mail", Modes: []string{"drafts_only"},
		Levels: []string{"L0", "L1"},
		CredentialKinds: []connector.CredentialKind{{Kind: "fields", Fields: []connector.CredentialField{
			{Name: FieldClientID, Secret: false, Help: "The Desktop OAuth client's id. deedglass connector gmail authorize stores it."},
			{Name: FieldClientSecret, Secret: true, Help: "The Desktop OAuth client's secret."},
			{Name: FieldRefreshToken, Secret: true, Help: "Written by deedglass connector gmail authorize; never pasted by hand."},
			{Name: FieldServiceAccountJSON, Secret: true, Help: "Google Workspace only: a service-account key with domain-wide delegation for gmail.compose."},
		}}},
		ConfigSchema: json.RawMessage(configSchema), DocsURL: DocsURL,
		Setup: []connector.SetupStep{
			{Title: "Create a Google Cloud project", Detail: "Enable the Gmail API, set the OAuth consent screen to External and Testing, and add yourself as a test user.", URL: "https://console.cloud.google.com/apis/library/gmail.googleapis.com"},
			{Title: "Create a Desktop OAuth client", Detail: "Credentials › Create credentials › OAuth client ID › Desktop app. Download the JSON and chmod 600 it.", URL: "https://console.cloud.google.com/apis/credentials"},
			{Title: "Create the connector", Command: "deedglass connector create --config examples/connectors/gmail.yaml"},
			{Title: "Authorize", Detail: "Sign in with Google in the browser tab it opens. Apps in Testing need this again every 7 days.", Command: "deedglass connector gmail authorize gmail --client-file ./client_secret.json"},
			{Title: "Test the connection", Command: "deedglass connector test gmail"},
		},
		Limits: []string{
			"Drafts only. Nothing sends: people send drafts from Gmail.",
			"Google's gmail.compose scope itself permits sending, so drafts only is enforced by Deedglass, not by Google.",
			"No attachments, labels or deletes.",
			"Google apps in Testing get 7-day refresh tokens: authorize again every week.",
		},
		Tools: connector.CatalogTools(Type, toolSpecs(DefaultMaxRecipients)),
	}
}
