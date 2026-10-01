# The vault: how Deedglass keeps connector credentials

A connector's credential (an API token, a password, a database URL) goes into Deedglass once,
write-only. From then on only the Gate's execute path decrypts it, at the moment a call is sent,
and no API, log line, error, event, deed or Console view ever shows it again (S16). This page
says what is stored, how it is encrypted, how to rotate the key, what to do if a key leaks, and
what the vault doesn't do.

## What is stored

One row per connector in `deedglass.credentials` (migrations 0004 and 0017):

| Column | In the clear | What it holds |
|---|---|---|
| `kind` | yes | `bearer`, `header`, `basic`, `query`, `dsn` or `fields` |
| `field_names` | yes | the names only: `token`, or `username` and `password` |
| `hint` | yes | the last 4 characters of a token of 24 or more (`…9f2c`), the username of basic auth, or `user@host:port/db` of a DSN; empty otherwise |
| `version` | yes | 1, 2, 3 … one more on every replacement |
| `kek_id` | yes | the id of the key that wraps the data key, like `kek:3f9a1c2b7d8e` |
| `wrapped_dek` | no | the data key, encrypted (60 bytes) |
| `ciphertext` | no | the credential, encrypted (at most 16 KiB of plaintext) |

The admin deed of every change (`connector.credentials_updated`, `connector.credentials_removed`,
`vault.rotated`) records the kind, the version and the key id. Never a value, and never a digest
of one: a hash of a low-entropy password invites offline guessing.

## The envelope (`A256GCM-ENV-1`)

```
             DEEDGLASS_VAULT_KEY (the key ring; first key active)
                              │  wraps
                              ▼
  fresh 32-byte data key ─── GCM(kek).Seal(dek, aad_dek) ──► wrapped_dek (60 bytes)
          │ encrypts
          ▼
  {"v":1,"kind":"bearer","fields":{"token":"…"}} ── GCM(dek).Seal(·, aad_secret) ──► ciphertext
```

- Every credential version gets a fresh 32-byte data key. Each AES-256-GCM key encrypts exactly
  one message with a random 96-bit nonce, prepended (`cipher.NewGCMWithRandomNonce`'s layout, 28
  bytes of overhead). The data key is zeroed after use.
- The associated data binds each ciphertext to where it belongs:

  ```
  aad_dek    = "deedglass/vault/dek/v1\n"    + org_id + "\n" + connector_id + "\n" + kek_id
  aad_secret = "deedglass/vault/secret/v1\n" + org_id + "\n" + connector_id + "\n" + version + "\n" + kind
  ```

  A row copied into another org or connector, or with its version or kind edited, fails to
  authenticate. The server logs the `vault_integrity` alert at error level, the call fails as
  `vault_unavailable`, and nothing is sent: Deedglass never falls back to a call without its auth.
- Replacing a credential overwrites the wrapped data key, which crypto-shreds the old secret.
  Deleting the connector or its credential does the same.
- The golden vector is `testdata/vault/envelope_v1.json` (pinned in `testdata/SHA256SUMS`;
  regenerate with `go test ./internal/vault -run TestGoldenEnvelope -update`).

## The key ring

`DEEDGLASS_VAULT_KEY` holds 1 to 4 comma-separated base64 keys of exactly 32 bytes. The first is
active: it wraps every new data key. The others only unwrap, during a rotation. A single key is
S01's format and stays valid. Each key is named by a derived id:

```
kek_id = "kek:" + first 12 hex of sha256("deedglass/vault/kek-id/v1\n" || key)
```

Example (fake keys, never use them):

```sh
DEEDGLASS_VAULT_KEY=Wlpa…new…=,AQEB…old…=
```

The config refuses a key of the wrong length, a key that isn't base64, a duplicate and a fifth key,
each with the position it found; prod refuses to start without a ring. `deedglass config print`
shows only the ids: `kek:3f9a1c2b7d8e (active), kek:77c0e1a9b442`. Dev generates a key in
`.deedglass/dev-secrets.json`.

Generate a key with:

```sh
openssl rand -base64 32
```

## Rotating the key

1. Add the new key **first** in the ring and keep the old one second:
   `DEEDGLASS_VAULT_KEY=<new>,<old>`.
2. Restart every Deedglass instance, so each can open both.
3. On the server host, run `deedglass vault rotate`. It walks every org, rewraps up to 100 data
   keys per transaction (`FOR UPDATE SKIP LOCKED`), never re-encrypts a secret, and seals one
   `vault.rotated` deed per org it changed (counts only). It is safe to interrupt and run again,
   and safe to run on two hosts at once.

   ```
   $ deedglass vault rotate
   ◐ Rewrapping data keys · 42 of 42
   ✓ All 42 credentials use kek:3f9a1c2b7d8e. You can remove kek:77c0e1a9b442 from DEEDGLASS_VAULT_KEY.
   ```
4. Remove the old key from the ring and restart again. `deedglass vault status` shows how many
   credentials each key wraps before you do.

If a row is wrapped by a key that isn't in the ring, `vault rotate` stops and names it:
`2 credentials use kek:77c0e1a9b442, which isn't in DEEDGLASS_VAULT_KEY. Add it back as the second
key, restart, then run deedglass vault rotate.` Calls to those connectors fail as
`vault_unavailable` until then, and the server logs a `vault_unavailable` alert.

## If a key leaks

Rotation can't un-leak anything: a leaked key plus a copy of the database reads every credential
wrapped by it. So:

1. Rotate to a new key (above).
2. **Replace every upstream credential** at its source (revoke the old API tokens, change the
   passwords), then store the new ones with `deedglass connector credentials set`.
3. Remove the leaked key from the ring.

## Using a KMS (planned)

The vault wraps data keys through one seam, `vault.KEKProvider`
(`ActiveID`, `Wrap`, `Unwrap`). The ring from `DEEDGLASS_VAULT_KEY` is today's only implementation;
an AWS KMS or GCP KMS provider is v2. Nothing else in the envelope changes.

## Redaction

Two layers keep values out of everything Deedglass writes:

- **Exact values.** When the Gate opens a credential for a call, every value of at least 6 bytes
  joins the call's redactor in every form it travels in: raw, query- and path-escaped (upper and
  lower hex), base64 (standard and URL, padded or not), `base64(user:pass)`, JSON-escaped, and a
  DSN password raw or percent-encoded. The connector's output, its error messages and every log
  line written for the call are redacted with it. Prod refuses to store a secret shorter than 6
  bytes, because it can't be told apart from ordinary text.
- **Patterns.** `vault.Scrub` catches secrets Deedglass doesn't hold, by shape: Deedglass API keys,
  Stripe secret and restricted keys, `Bearer …`, URL passwords, AWS access key ids, GitHub tokens
  and Slack tokens. The server's logger applies both layers to every message and string attribute,
  nested groups included.

The tests push the sentinel `dgSENTINEL7Q2vK9xT4mB8nR3` through errors, upstream bodies, panics and
log groups, and sweep the server log, every response, every CLI output and the `actions`,
`action_events`, `deeds`, `admin_events`, `idempotency_keys` and `connectors` tables for it
(`TestConnectorsLive`: 0 hits). `TestOpenerCallSites` keeps the decrypt call inside
`internal/connector` and the vault.

## What the vault doesn't do

- **Go can't reliably zero memory.** Deedglass zeroes the data key and the decrypted bytes it
  holds, but the garbage collector may have copied them, and HTTP header values are Go strings.
- **A key plus a database dump reads every credential.** Keep the ring out of the database's
  backups and out of `deedglass.yaml` (prod refuses secrets there).
- **It doesn't protect against a compromised Deedglass process**, or against the upstream service
  itself misusing the credential.
- **No rollback protection.** Restoring an older, consistent row (with its matching wrapped key)
  brings the older credential back.
- **Hints are deliberate echoes.** The last 4 characters of a long token, a username, or a DSN's
  user, host and database are visible to admins and viewers.
- **No KMS yet** (v2), and no per-org keys: one ring per deployment.
