// Package mailer sends sign-in and approval emails through one interface: a log mailer in dev
// and Resend in prod (contract §2).
//
// Invariants:
//   - Messages are plain, with a text alternative (brand §13.5).
//   - Links in emails carry one-time secrets; the mailer never logs a link or a body.
//   - Every send has a timeout, and a failed send never blocks the request that caused it.
//
// Planned API (finalized by S07):
//
//	type Mailer interface{ Send(ctx context.Context, m Message) error }
//	func NewResend(key config.Secret, from string) Mailer
//	func NewLog(l *slog.Logger) Mailer
//
// Filled by: S07
package mailer
