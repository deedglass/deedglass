# Licensing: free while we grow

Every Deedglass feature is free to use, with no licence key, while we grow. That includes the
features in `ee/` that used to need a Pro licence:

| Feature | Without a key |
|---|---|
| `deedglass scan --full`: the full scan report, rendered on your machine | yes |
| Pro policy packs (`money-movement` and the packs after it) and the fix writer (as it ships) | yes |
| Undo a whole watched session in one step (`deedglass observe undo`, S50) | yes |
| The security review pack: the sealed document for a reviewer (`deedglass security-pack build`, S51) | yes |
| The Clients view and reports for every client (`deedglass clients`, `report --all-clients`, `--brand`, S52) | yes |

Everything that was always free stays free: the rules, the checks, records and their offline
verification, Stop, Undo, Insights, `deedglass scan`, and the scan Action's scan, job summary and
SARIF on every repo.

Later we plan paid support, SLAs and a hosted version for organisations. Anything free today stays free for people who run Deedglass themselves.

## How the switch works

One policy in `internal/licence/policy.go` decides whether a feature needs a key.
`licence.Current` is `FreeWhileWeGrow`: every entitlement is on, so no command and no server
opens a licence file or calls Dodo Payments or deedglass.com. The verifier, the token format and
the `deedglass licence` commands below stay in the code, so a paid option can ask for a key again
later by changing that one policy, not by rewriting features. `deedglass serve` logs
`licence: feature free while we grow` once per feature at start.

## The licence path, kept for later

Nothing below is needed today. It describes how a key works if a later build asks for one.

### Turning a licence on

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

### Does a licence phone home?

Every feature is free while we grow, so nothing checks a licence and nothing is sent to deedglass.com: the scan reads files on your machine and, with `--postgres-dsn-env`, connects only to the database you name. The `deedglass licence` commands, kept for a later paid option, are the only ones that call Dodo Payments or deedglass.com, and only when you run them; `DEEDGLASS_NO_NETWORK=1` stops those calls. `deedglass version --check` and the `--latest` option of `deedglass policy install` and `policy packs` contact GitHub when you use them. Head timestamps are off by default; with TSAs or a transparency service listed in deedglass.yaml, `deedglass deeds timestamp` and each export contact only those.

If a check can't reach Dodo Payments or deedglass.com, the next Pro feature that asks tries again.
`refresh` asks Dodo, then deedglass.com; `activate` first registers the machine with Dodo;
`deactivate` asks only Dodo. Each request carries the licence key and this machine's activation (at
`activate`, the hostname hash above), and the request to deedglass.com also lists the features this
build knows. Nothing about your files, agents or data goes with them. With the network off, Pro runs
on its cached token for up to 14 days after the last successful check. Free commands never open the
licence (a test fails the build if any free code imports it).

### The token and how to check it

The token is a JSON object signed like a record, with its own domain tag and its own key family:

```
body_hash = "sha256:" + hex(sha256(JCS(every field except body_hash and sig)))
sig       = "ed25519:" + base64url(Ed25519(lk_ key, "deedglass/licence/v1\n" + body_hash))
```

Licence keys (`lk_…`) never seal records, and seal keys (`sk_…`) never sign licences; both sides
refuse the other. The public keys are built into the binary and published at
`https://deedglass.com/.well-known/deedglass/licence-keys.json`.

### Features old installs can read

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

### When a licence works

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

`internal/licence` (Apache-2.0) only verifies and holds the policy. The features that used to be
Pro live in `ee/`, licensed under `ee/LICENSE` (FSL-1.1-ALv2): you can read, run and change it for
anything except offering a competing product, and each release becomes Apache-2.0 two years after
it ships. The root `LICENSE` says which files `ee/LICENSE` covers. Free to use is not the same as
a change of licence: `ee/` stays under FSL-1.1-ALv2.

## What it doesn't do

- No seats, no usage metering, no team billing.
- No install count or other telemetry: free features send nothing anywhere.
- The full report is sealed only on your own Deedglass server (`DEEDGLASS_SERVER` and an API key);
  without one it says how to seal it later with `deedglass deeds seal-file`.
