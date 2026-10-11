# Sharing: exactly what each link holds

Every share link puts its data after `#`. Browsers never send that part to a server, so
deedglass.com receives nothing. Each link is opt-in and asks first (`[y/N]`).

| Link | Made by | Holds | Never holds |
|---|---|---|---|
| `deedglass.com/s#v1.…` | `deedglass scan --share` | grade, points, formula version, finding counts by severity, finding ids from the closed catalog, deduction ids and counts | paths, files, tables, schemas, roles, databases, servers, hosts, variable names, versions |
| `deedglass.com/verify#d=…&k=…` | `deedglass deeds share <id>` | one deed envelope (ids, agent name, tool, decision, verdict, digests) and the seal key set that sealed it | arguments, results, other deeds; links over 6,144 characters are refused |
| `deedglass.com/play#v1.…` | the playground's Share this scene | the scene number and the editor's text | anything else |
| `.deedglass/score.json` | `deedglass scan --badge` | the shields.io endpoint body: label, grade, colour | anything else; the file stays on your machine until you commit it |

A deed link carries its own key, so `/verify` shows only that the deed is unchanged under that key.
The page says so and shows the key's thumbprint to compare with the issuer's
`/.well-known/deedglass/keys.json`.

Tests: the scan share canary (no `CANARY_*` string survives), `TestDeedShareRoundTrip`, and the
playground's fragment round trip (2,049 bytes refused).
