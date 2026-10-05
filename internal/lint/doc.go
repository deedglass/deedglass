// Package lint holds Deedglass's static guards G1 to G5 (S18, security pass 1) and G6 to G10 (S34,
// security pass 2). Each guard reads
// the source with go/parser and go/ast only, so the package imports nothing outside the standard
// library and runs without type information. Check walks every non-test .go file under a root,
// skipping vendor, .tmp, node_modules, web, testdata and hidden directories and generated files.
// TestRepoClean runs every guard over this repo.
//
// The guards:
//
//   - G1, no SQL built from values. SQL text built with fmt.Sprintf, fmt.Sprint or + is reported
//     when a string literal in it holds an SQL keyword (SELECT, INSERT, UPDATE, DELETE, CREATE,
//     ALTER, DROP, GRANT, REVOKE, TRUNCATE, COPY, SET ROLE, SET LOCAL, WHERE), at least one operand
//     is not a constant, and the result reaches Exec, Query, QueryRow, Queue or CopyFrom, either
//     as an argument or through a local variable of the same function. Text made only of literals
//     and named constants passes. Why: CLAUDE.md rule 4 forbids dynamic SQL built from user input;
//     values go in as $n parameters. A file that must build SQL from generated or validated
//     identifiers carries a line comment //deedglass:allow-sql-build followed by its reason; a
//     marker with no reason is itself a finding.
//   - G2, the single egress door. Only internal/connector/egress builds an http.Client or an
//     http.Transport, dials (net.Dial, net.DialTimeout, net.DialTCP, net.DialUDP, net.DialIP,
//     net.Dialer, tls.Dial, tls.DialWithDialer), or uses the package-level clients
//     (http.DefaultClient, http.DefaultTransport, http.Get, Post, Head, PostForm). Why: S16
//     invariant 3; every outbound request passes the destination policy. The allow-list mirrors
//     TestSingleEgressDoor in internal/archtest exactly.
//   - G3, no secrets in logs. No log/slog attribute keyed password, secret, token, dsn,
//     authorization or api_key, or ending in _token, _secret or _password (case-insensitive),
//     outside internal/vault. It checks the key of slog.String, slog.Any and the other attribute
//     constructors, and every key position of the key/value list passed to Info, Warn, Error,
//     Debug, Log, their Context forms and With. Why: a value logged under such a key is a
//     credential in a log line, and log lines leave the process.
//   - G4, no InsecureSkipVerify, as a selector or a composite literal key. Why: TLS checks are
//     never switched off in shipped code; tests use their own roots.
//   - G5, no math/rand or math/rand/v2 import. Why: tokens, codes, nonces and jitter come from
//     crypto/rand, so no secret ever depends on a predictable generator.
//   - G6, no os/exec outside files marked //deedglass:allow-exec <reason>: init's Docker and serve,
//     mcp wrap's supervisor, opening a browser, the attack harness. Why: every process Deedglass
//     starts is a deliberate, reviewed choice.
//   - G7, no unsafe import outside files marked //deedglass:allow-unsafe <reason> (one: the
//     terminal-width ioctl), and no reflect.SliceHeader or reflect.StringHeader anywhere. Why:
//     memory safety is the parsers' first defence.
//   - G8, secrets compared in constant time. A ==, != , bytes.Equal or strings.EqualFold whose
//     operand names match mac, signature, token, hash, secret or csrf (case-insensitive) is
//     reported, unless the other side is a literal, nil or len(), the name is an imported package
//     or a constant of the same package, or the line carries //deedglass:allow-compare <reason>
//     (public digests: prev_hash, body_hash, version hashes). Why: a timing difference must not
//     reveal a credential byte by byte; subtle.ConstantTimeCompare and hmac.Equal don't.
//   - G9, no text/template outside files marked //deedglass:allow-text-template <reason> (one: an
//     email's plain-text part). Why: text/template doesn't escape HTML, so anything a browser or
//     mail client renders uses html/template.
//   - G10, every http.Server composite literal sets ReadHeaderTimeout, and no http.ListenAndServe,
//     ListenAndServeTLS, Serve or ServeTLS shortcut. Why: a server without a header deadline can
//     be held open by slow clients.
//
// Filled by: S18 (G1 to G5) and S34 (G6 to G10)
package lint
