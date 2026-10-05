package console

import "github.com/deedglass/deedglass/internal/httpx"

// CSP is the Console pages' Content-Security-Policy (S24 §3.10): nothing inline, nothing from
// another origin, and Trusted Types 'none', so every string-to-HTML sink throws in Chromium. The
// Console builds DOM only through web/src/lib/dom.ts (text nodes); a build that trips this fails
// the CSP test. Dropping a directive needs an ADR.
const CSP = "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self' data:; font-src 'self'; " +
	"connect-src 'self'; manifest-src 'self'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'; " +
	"object-src 'none'; require-trusted-types-for 'script'; trusted-types 'none'"

// PermissionsPolicy turns off every powerful feature the Console never uses.
const PermissionsPolicy = httpx.PermissionsPolicy

// ADR names the ADR that adds the Console page routes (GET /app, GET /app/{path...}).
const ADR = "ADR-054"
