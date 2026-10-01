package connector

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/deedglass/deedglass/internal/connector/egress"
	"github.com/deedglass/deedglass/internal/vault"
)

// The connector error catalog (S16 §5). Agent messages are terse and never name a credential, an
// address or a network; admin details go to the log and the Console.

// EnvVarFor suggests the environment variable a connector's token comes from: ORDERS_TOKEN.
func EnvVarFor(name string) string {
	return strings.ToUpper(strings.ReplaceAll(name, "-", "_")) + "_TOKEN"
}

// CredentialsMissingMessage is credentials_missing's sentence.
func CredentialsMissingMessage(name string) string {
	return fmt.Sprintf(`The connector "%s" has no credentials yet. Run deedglass connector credentials set %s --from-env %s.`, name, name, EnvVarFor(name))
}

// ErrCredentialsMissing: the connector has no credentials, so nothing was sent.
func ErrCredentialsMissing(name string) *Error {
	return &Error{Kind: KindCredentialsMissing, Code: CodeCredentialsMissing, AgentMessage: CredentialsMissingMessage(name)}
}

// VaultAgentMessage is vault_unavailable's sentence to an agent.
const VaultAgentMessage = "Deedglass couldn't open this connector's credentials. An admin has been alerted."

// ErrVaultUnavailable: the credential couldn't be opened (a key not in the ring, or a row that
// fails to authenticate). Nothing was sent; it is never retried.
func ErrVaultUnavailable(cause error) *Error {
	detail := "The credential failed to authenticate (vault_integrity). Store it again with deedglass connector credentials set."
	var uk *vault.UnknownKEKError
	switch {
	case errors.As(cause, &uk):
		detail = uk.Error()
	case errors.Is(cause, vault.ErrNoVault):
		detail = "DEEDGLASS_VAULT_KEY isn't set on this server, so no credential can be opened. Set it and restart."
	case !errors.Is(cause, vault.ErrIntegrity):
		detail = "The credential couldn't be read from the database. Check the server log, then retry."
	}
	return &Error{Kind: KindVault, Code: CodeVaultUnavailable, AgentMessage: VaultAgentMessage, AdminDetail: detail, Err: cause}
}

// ErrDestinationBlocked: egress refused the destination. Nothing was sent.
func ErrDestinationBlocked(be *egress.BlockedError) *Error {
	return &Error{Kind: KindDestinationBlocked, Code: CodeDestinationBlocked, AgentMessage: be.AgentMessage(), AdminDetail: be.Error(), Err: be}
}

// ErrResponseTooLarge: the response passed the cap. The call ran.
func ErrResponseTooLarge(max int64) *Error {
	return &Error{Kind: KindTooLarge, Code: CodeResponseTooLarge,
		AgentMessage: fmt.Sprintf("The service answered with more than %s, the connector's limit, so Deedglass kept none of it. The call ran.", Bytes(max))}
}

// ErrTimeout: no answer before the deadline. The call may have run.
func ErrTimeout(cause error) *Error {
	return &Error{Kind: KindTimeout, Code: CodeTimeout, AgentMessage: "The service did not answer in time. The call may have run.", Err: cause}
}

// ErrUnreachable: the connection failed before anything was sent (refused, DNS, TLS).
func ErrUnreachable(cause error) *Error {
	return &Error{Kind: KindConfig, Code: CodeConnectorUnavailable,
		AgentMessage: "Deedglass couldn't reach the service, so nothing was sent. Try again later; an admin can run deedglass connector test.", Err: cause}
}

// ErrValidation is a refused argument: path names it.
func ErrValidation(path, what string) *Error {
	return &Error{Kind: KindInvalidRequest, Code: CodeValidationFailed,
		AgentMessage: fmt.Sprintf("%s %s. Check the tool's input schema and send the call again.", path, what)}
}

// FromStatus maps an upstream HTTP status (S16 §3.4 step 4): 401/403 auth, 404 not_found, 429
// rate_limited, other 4xx invalid_request (upstream_error), 5xx upstream.
func FromStatus(status int, retryAfter time.Duration) *Error {
	e := &Error{HTTPStatus: status}
	switch {
	case status == 401 || status == 403:
		e.Kind, e.Code = KindAuth, CodeUpstreamAuthFailed
		e.AgentMessage = fmt.Sprintf("The service refused the connector's credentials (HTTP %d). Nothing was changed. An admin can replace them in Console › Connectors.", status)
	case status == 404:
		e.Kind, e.Code = KindNotFound, CodeUpstreamNotFound
		e.AgentMessage = "The service has nothing at that address (HTTP 404). Check the arguments."
	case status == 429:
		e.Kind, e.Code, e.Retryable, e.RetryAfter = KindRateLimited, CodeUpstreamRateLimited, true, retryAfter
		e.AgentMessage = "The service is limiting requests (HTTP 429). Wait, then send the call again."
		if retryAfter > 0 {
			e.AgentMessage = fmt.Sprintf("The service is limiting requests (HTTP 429). Wait %s, then send the call again.", retryAfter.Round(time.Second))
		}
	case status >= 400 && status < 500:
		e.Kind, e.Code = KindInvalidRequest, CodeUpstreamError
		e.AgentMessage = fmt.Sprintf("The service refused the request (HTTP %d). Check the arguments.", status)
	default:
		e.Kind, e.Code, e.Retryable = KindUpstream, CodeUpstreamError, true
		e.AgentMessage = fmt.Sprintf("The service failed (HTTP %d). The call may have run.", status)
	}
	return e
}

// Bytes renders a byte count for messages: 4 MiB, 512 KiB, 900 bytes.
func Bytes(n int64) string {
	switch {
	case n >= 1<<20 && n%(1<<20) == 0:
		return fmt.Sprintf("%d MiB", n>>20)
	case n >= 1<<10 && n%(1<<10) == 0:
		return fmt.Sprintf("%d KiB", n>>10)
	}
	return fmt.Sprintf("%d bytes", n)
}
