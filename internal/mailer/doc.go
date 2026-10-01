// Package mailer sends sign-in and invite emails through one interface: a log mailer in dev,
// Resend in prod, and None when prod has no Resend key (contract §2).
//
// Invariants:
//   - Every message has an HTML body and a plain-text alternative (brand §13.5). The HTML is
//     table-based, inline-styled, and loads nothing remote: no images, fonts or tracking pixels.
//   - Links and codes in emails are one-time secrets. Resend's logs carry only ids, the kind and
//     the HTTP status: never the API key, the recipient, the subject, a body, a link or a code.
//   - Mail is never sent inside a database transaction or on the request path. Callers hand a
//     Job or a Message to a Queue, which sends it on a worker with a timeout; a full queue drops
//     the job and counts it rather than blocking the request.
//   - Resend sends are bounded: at most 3 attempts, each with a 10 s client timeout, with an
//     Idempotency-Key so a retried attempt never becomes a second email.
//
// API:
//
//	type Mailer interface{ Send(ctx context.Context, m Message) error }
//	func NewLog(w io.Writer, prod bool) (*Log, error)
//	func NewResend(o ResendOptions) (*Resend, error)
//	func NewQueue(m Mailer, o QueueOptions) *Queue
//	func LoginMessage(d LoginData) (Message, error)
//	func InviteMessage(d InviteData) (Message, error)
package mailer
