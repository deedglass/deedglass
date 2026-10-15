# Licensing and Pro

Everything Deedglass does for safety is free and stays free: the rules, the checks, records and
their offline verification, Stop, Undo, Insights, `deedglass scan` and the scan Action on public
repos. Pro adds new features on top. It never takes a free one away.

## What Pro adds

| Feature | Plan |
|---|---|
| `deedglass scan --full`: the full scan report, rendered on your machine | the full scan report, founding partner, Pro |
| Pro policy packs and the fix writer (as they ship) | the full scan report, founding partner, Pro |
| The scan Action on private repos (as it ships) | founding partner, Pro |
| Undo a whole watched session in one step (`deedglass observe undo`, S50) | founding partner, Pro |
| The security review pack: the sealed document for a reviewer (`deedglass security-pack build`, S51; the readiness list and the offline check stay free) | founding partner, Pro |

The full scan report plan lasts 30 days, a founding partner place six months; Pro renews until you
cancel. Prices are on deedglass.com/pricing. Dodo Payments is the reseller and handles sales tax.

## Turning Pro on

```sh
deedglass licence activate DG-…      # the key from Dodo's email, on each machine (3 machines per key)
deedglass licence show               # offline: plan, last check, how long it keeps working offline
deedglass licence refresh            # check with Dodo now
deedglass licence deactivate         # free this machine's activation
```

`activate` registers the machine with Dodo under the first 8 hex characters of sha256(hostname),
never the hostname, then asks deedglass.com to re-check the key with Dodo and sign a licence token.
Two files are written, both mode 0600: `.deedglass/licence.json` (the signed token, which holds
only the key's sha256) and `.deedglass/licence.key` (the key, so the CLI can check it again).
`DEEDGLASS_LICENCE_FILE` moves them. `DEEDGLASS_LICENCE_MODE=test` accepts Dodo test-mode keys only in a
development build (`go build -tags licencetest ./cmd/deedglass`); release binaries refuse it, because a
test-mode purchase costs nothing. Test-mode tokens are also signed by their own key, which only a
`licencetest` build carries, so a release trusts live tokens only.

A third file, `.deedglass/licence.clock` (0600), keeps the latest time this machine has seen for the
licence. If the computer's clock runs more than 5 minutes behind it, or behind the last check, Pro
pauses until `deedglass licence refresh` (or the next Pro feature) checks the licence online. A
missing or edited `licence.clock` counts the same way. Everything free keeps working.

## Does Pro phone home?

At most once a day, only when a Pro feature runs, and it keeps working 14 days offline.
`DEEDGLASS_NO_NETWORK=1` stops every call; Pro then runs on the cached token inside those 14 days.
Free commands never open the licence (a test fails the build if any free code imports it). There is
no telemetry.

## The token and how to check it

The token is a JSON object signed like a record, with its own domain tag and its own key family:

```
body_hash = "sha256:" + hex(sha256(JCS(every field except body_hash and sig)))
sig       = "ed25519:" + base64url(Ed25519(lk_ key, "deedglass/licence/v1\n" + body_hash))
```

Licence keys (`lk_…`) never seal records, and seal keys (`sk_…`) never sign licences; both sides
refuse the other. The public keys are built into the binary and published at
`https://deedglass.com/.well-known/deedglass/licence-keys.json`.

## Features old installs can read

A newer site may sign features an older build doesn't know. Since v0.1.0 a build accepts any
well-formed feature name in a token (lowercase letters, digits and `_`, 3 to 32 characters; at most
32 of them), keeps it so the signature still verifies, and grants only the features it knows.

The token request also names what the build knows: `features_known` (for this build:
`scan_full`, `scan_fix`, `packs_pro`, `action_private`, `undo_session`, `security_pack`, `agency`). The site signs the plan's
features that the request lists, in plan order. A request without the field (v0.1.0) gets the list
those builds knew (`action_private`, `scan_full`, `scan_fix`, `packs_pro`), so a new feature never
reaches a client that predates it. A list with nothing in common with the plan gets that list too.

A sku can add features to its plan: `agency_monthly` (the Agency plan, S52) is plan `pro` with Pro's
features plus `agency`, and the site signs `agency` only when `features_known` lists it. A Pro or
founding token never carries `agency`. `agency` turns on the Clients view (`GET /v1/clients`,
`deedglass clients`) and `deedglass report --all-clients` and `--brand` ([AGENCIES.md](AGENCIES.md));
each org's own report and its Monday email never read the licence.

`deedglass serve` asks the licence once at start and then once a day; the answer turns the one-step
session undo, the security review pack's build and the Clients view on or off for the next request. An undo already
started completes even if the licence lapses before its countersign.

## When Pro works

```
ok_validation = now ≤ validated_at + 14 days
ok_period     = period_end is null, or now ≤ period_end + 14 days
pro_active    = signature valid, and the token's mode matches, and ok_validation, and ok_period
```

After that Pro is read-only: reports you already have still open and verify, and `scan --full`
says `Pro paused: your licence was last checked 15 days ago. Run deedglass licence refresh.
Everything free still works.` A refund, cancellation or expiry is Dodo's: the next check fails and
the local licence is removed. Deedglass keeps no list of its own.

## Where the code lives

`internal/licence` (Apache-2.0) only verifies. Pro features live in `ee/`, licensed under
`ee/LICENSE` (FSL-1.1-ALv2): you can read, run and change it for anything except offering a
competing product, and each release becomes Apache-2.0 two years after it ships. The root
`LICENSE` says which files `ee/LICENSE` covers.

## What it doesn't do

- No seats, no usage metering, no team billing yet.
- The full report is sealed only on your own Deedglass server (`DEEDGLASS_SERVER` and an API key);
  without one it says how to seal it later with `deedglass deeds seal-file`.
- A 14-day trial and the Monday summary email are planned, not built.
