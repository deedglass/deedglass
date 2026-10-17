# Sharing: exactly what each link holds

Every share link puts its data after `#`. Browsers never send that part to a server, so
deedglass.com receives nothing. Each link is opt-in: only a flag, a command or a button you choose
makes one. `deedglass deeds share` also asks first (`[y/N]`, unless `--yes`). For
`deedglass scan --badge-share` the flag itself is the opt-in: it doesn't prompt, and it prints a
warning that the link goes in a public README.

| Link | Made by | Holds | Never holds |
|---|---|---|---|
| `deedglass.com/s#v1.…` | `deedglass scan --share` | grade, points, formula version, finding counts by severity, finding ids from the closed catalog, deduction ids and counts | paths, files, tables, schemas, roles, databases, servers, hosts, variable names, versions |
| `deedglass.com/s#v1.…` in a README badge | `deedglass scan --badge-share` | grade, points, formula version, finding counts by severity; the finding and deduction lists are empty (`"f":[]`, `"d":[]`) | finding ids, deduction ids, and everything the `--share` row never holds. It goes in your README, so anyone who reads it can open it; it keeps that run's result |
| `deedglass.com/verify#d=…&k=…` | `deedglass deeds share <id>` | one deed envelope (ids, agent name, tool, decision, verdict, digests) and the seal key set that sealed it | arguments, results, other deeds; links over 6,144 characters are refused |
| `deedglass.com/play#v1.…` | the playground's Share this scene | the scene number and the editor's text | anything else |
| `.deedglass/score.json` | `deedglass scan --badge` | the shields.io endpoint body: label, grade, colour | anything else; the file stays on your machine until you commit it |

A deed link carries its own key, so `/verify` shows only that the deed is unchanged under that key.
The page says so and shows the key's thumbprint to compare with the issuer's
`/.well-known/deedglass/keys.json`.

Tests: the scan share canary (no `CANARY_*` string survives), `TestBadgeShare` (the badge link's
exact payload, with empty finding and deduction lists), `TestDeedShareRoundTrip`, and the
playground's fragment round trip (2,049 bytes refused).
