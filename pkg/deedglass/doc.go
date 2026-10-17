// Package deedglass is the Go client for a Deedglass server. Rules decide each AI agent tool call;
// Deedglass checks what really changed and seals a record.
//
// Client.Act sends one tool call through the server's rules and returns the action (allowed,
// denied, or waiting for a person); GetAction waits for its outcome and ReportResult reports the
// result of a call the agent ran itself. Tools lists the tools an agent may call, EventStream
// follows the live event stream, and IntentSigner signs intents for orgs that require them.
//
// The API key is set by the caller (NewClient or Client.APIKey) and is never logged or included in
// an error. A nil Client.HTTP gets a 30 s timeout, and every POST gets an Idempotency-Key unless the
// caller sets one. Records are checked offline with the CLI (deedglass verify), not with this package.
//
// Documentation: https://docs.deedglass.com/sdks/go/
package deedglass
