// Package config loads Deedglass settings into one typed struct: the process environment, then
// .env (dev only), then deedglass.yaml, then .deedglass/dev-secrets.json (dev only), then defaults.
// Every key is declared once in keys.go; config print, unknown-name detection, did-you-mean and
// docs/CONFIG.md derive from that table (ADR-015).
//
// Invariants:
//   - Secrets are the Secret type and never print: every fmt verb, JSON, text, slog and panic
//     messages show ••••••••. Reveal is the only accessor. Problems never repeat a secret value.
//   - Load returns every problem at once, each with a next step.
//   - Prod refuses: a non-https base URL, a missing vault key or pepper, private networks, a remote
//     database URL without sslmode=require or verify-full, secrets in deedglass.yaml, and unknown
//     DEEDGLASS_* names. Prod never reads .env or dev secrets.
//   - Any TXV_* variable is an error in both modes, with its new name.
//   - deedglass.yaml has no anchors, aliases or merge keys, one document, and at most 64 KiB.
//   - .deedglass/ is refused when it is a symlink or writable by others; the dev secrets file is
//     created with O_EXCL and mode 0600.
//   - Stdlib plus go.yaml.in/yaml/v3 only (internal/archtest enforces it).
//
// Filled by: S01 (later sessions add rows to the key table)
package config
