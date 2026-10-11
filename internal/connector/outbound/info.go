//go:build outboundmail

package outbound

import (
	"encoding/json"

	"github.com/deedglass/deedglass/internal/connector"
)

// Info implements connector.Connector. The type is never in the public catalog (the registry
// leaves org-limited types out); this entry serves deedglass connector types in the ops build.
func (c *Connector) Info() connector.TypeInfo {
	return connector.TypeInfo{Object: "connector_type", Type: Type, Title: "Outbound mail (ops build)",
		Description: "Send plain-text email from one Google Workspace mailbox on a sending domain, inside a window, exactly as approved, and read Gmail's Sent copy back. Ops build only, for the orgs listed in connectors.outbound_mail.orgs.",
		Summary:     "Sends from one Workspace mailbox. Ops build only.",
		Status:      "beta", Icon: "mail", Modes: []string{"send"},
		Levels: []string{"L0", "L1"},
		CredentialKinds: []connector.CredentialKind{{Kind: "fields", Fields: []connector.CredentialField{
			{Name: FieldClientID, Secret: false, Help: "The Internal Desktop OAuth client's id. deedglass connector outbound authorize stores it."},
			{Name: FieldClientSecret, Secret: true, Help: "The Desktop OAuth client's secret."},
			{Name: FieldRefreshToken, Secret: true, Help: "Written by deedglass connector outbound authorize; never pasted by hand."},
		}}},
		ConfigSchema: json.RawMessage(configSchema), DocsURL: DocsURL,
		Setup: []connector.SetupStep{
			{Title: "Create a Google Cloud project inside your Workspace organisation", Detail: "Enable the Gmail API and set the OAuth consent screen to Internal.", URL: "https://console.cloud.google.com/apis/library/gmail.googleapis.com"},
			{Title: "Create a Desktop OAuth client", Detail: "Download the JSON and chmod 600 it.", URL: "https://console.cloud.google.com/apis/credentials"},
			{Title: "Build the ops binary", Command: "make ops-build"},
			{Title: "Authorize", Command: "bin/deedglass-ops connector outbound authorize mail --client-file ./client_secret.json"},
			{Title: "Test the connection", Command: "bin/deedglass-ops connector test mail"},
		},
		Limits: []string{
			"Plain text only: no HTML, attachments, images or tracking.",
			"One recipient per message; no cc or bcc.",
			"send is never retried. A lost answer is unknown and is read back by Message-ID, never sent again.",
			"From is always the authorised mailbox; deedglass.com is always refused.",
			"Ops build only; release builds can't send.",
		},
		Tools: connector.CatalogTools(Type, toolSpecs()),
	}
}
