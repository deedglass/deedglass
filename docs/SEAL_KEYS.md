# Seal keys

A seal key is the Ed25519 key pair your Deedglass instance seals deeds with. The private half (a
32-byte seed) lives in one file on the server. The public half is published at
`/.well-known/deedglass/keys.json`, so anyone can check your deeds offline. The format and the
rules a verifier applies are in `docs/DEED_SPEC.md` §10.

## Generate a key

```
$ deedglass keys gen
✓ Seal key created · sk_01j9zq4m6e8r0t2v4x6z8b0d2f
  file        .deedglass/keys/sk_01j9zq4m6e8r0t2v4x6z8b0d2f.json (0600)
  thumbprint  sha256:3f9a…c21e
  next        export DEEDGLASS_SEAL_KEY_FILE=.deedglass/keys/sk_01j9zq4m6e8r0t2v4x6z8b0d2f.json
```

`keys gen` works offline. It creates the directory with mode 0700 and the file with mode 0600, and
never overwrites an existing file. Use `--dir` to put it elsewhere, and `--json` for scripts (the
seed is never printed).

In **dev**, you can skip this: when `DEEDGLASS_SEAL_KEY_FILE` isn't set, `deedglass serve` uses the
newest key in `.deedglass/keys/`, or creates one there and says so. In **prod**, a missing key is an
error.

## The key file

```json
{"alg":"Ed25519","created_at":"2026-10-02T09:12:41.000Z","id":"sk_…","object":"seal_key_file","public_key":"<base64url>","seed":"<base64url>","v":1}
```

`deedglass serve` refuses to start, with exit code 1 and a next step, when the file:

| Problem | What you see |
|---|---|
| Group or others can read or write it | `✕ Seal key file … can be read by other users (mode 0644).` / `next  chmod 600 …` |
| Its public key is small-order or not canonical | `✕ Seal key sk_… is not a safe Ed25519 key (small-order point). It was not registered.` |
| Its seed doesn't derive its public key | `✕ Seal key file … is damaged: its seed does not match its public key.` |
| It is a published test-vector key, in prod | `✕ This is a published test key. It must never seal real deeds.` |
| The database has this id with another public key | `✕ Seal key sk_… in the file does not match the registered public key.` |
| The key was retired or revoked | `✕ Seal key sk_… was retired at … Deedglass will not seal deeds with it.` |

On the first start with a new file, `serve` registers the key as active and prints
`✓ Seal key · sk_… · sha256:3f9a…c21e`. It logs the key id and thumbprint, never the seed.
`GET /v1/health` reports `seal_key`: `ok`, `degraded` (the key is inside a rotation overlap:
restart with the new one) or `down`.

## Rotate

```
$ deedglass keys rotate
◐ Rotating the seal key · overlap 5m
✓ New seal key sk_01j9zr8k2m4p6r8t0v2x4z6b8d is active
✓ sk_01j9x0c3e5g7j9m1p3r5t7v9x1 retires at 09:22:14 UTC (in 5m)
  next  Point DEEDGLASS_SEAL_KEY_FILE at .deedglass/keys/sk_01j9zr8k2m4p6r8t0v2x4z6b8d.json
        and restart every instance before 09:22 UTC. Deeds sealed with either key keep verifying.
```

Step by step:

1. Run `deedglass keys rotate` where the server's database URL is set (`DEEDGLASS_DATABASE_URL`).
   It writes the new key file next to the current one (or in `--dir`), then, in one transaction,
   registers the new key as active and sets every active key's `retired_at` to now + `--overlap`
   (default 5m, from 1s to 168h). If the database refuses, the new file is removed and nothing
   changes.
2. Copy the new key file to every server, with mode 0600.
3. Point `DEEDGLASS_SEAL_KEY_FILE` at it and restart each instance before the old key retires.
4. Check: `deedglass keys list` shows one `✓ active` key and the old one `retired`.

During the overlap an instance that hasn't restarted keeps sealing with the old key, until 30
seconds before `retired_at`; after that it refuses to seal (the Gate retries once the instance
restarts) rather than sealing at the edge of the window. A server restarted with the old file
refuses to start.

New keys reach `keys.json` within 60 seconds (the server caches it that long; clients may cache it
for 300 seconds).

## What verifiers see

Every key ever registered stays in `keys.json`, oldest first, with its `activated_at` and, once
retired, `retired_at`. A deed verifies under the key it names when
`activated_at ≤ issued_at < retired_at`. After a rotation, old deeds keep verifying with the old
key; new deeds name the new one.

```
$ deedglass keys list --server https://api.example.com
ID                            STATUS    ACTIVATED             RETIRES / RETIRED     THUMBPRINT
sk_01j9zr8k2m4p6r8t0v2x4z6b8d  ✓ active  2026-10-02 09:17 UTC  —                     sha256:9c1e…2c4e
sk_01j9x0c3e5g7j9m1p3r5t7v9x1  retired   2026-10-01 18:40 UTC  2026-10-02 09:22 UTC  sha256:77b0…9e04
2 seal keys · 1 active · published at https://api.example.com/.well-known/deedglass/keys.json
```

`keys list --server` reads the public `keys.json` (no credentials) and checks every key the way a
verifier does; it refuses a set that contains an unsafe key.

## Revocation (planned)

`deedglass keys revoke` is planned (S04 stretch). The verifier side is in place: a key with
`revoked_at` set fails every deed it sealed, whatever their `issued_at`, because someone holding a
stolen key can write any `issued_at`. Until the command ships, revoke a key by setting
`status = 'revoked'`, `revoked_at` and `revocation_reason` on its `deedglass.seal_keys` row as the
runtime role, then rotate.

## Backups

- Keep a copy of each seed offline (an encrypted backup or a password manager), separate from the
  database backup.
- Losing a seed only stops **new** seals with that key: every deed it sealed still verifies with the
  public key in `keys.json`. Rotate to a new key and carry on.
- A leaked seed is worse than a lost one: rotate at once, then revoke.

## Later

- **KMS and HSM signing.** The Sealer signs through a small `Signer` interface (`KeyID`,
  `PublicKey`, `Sign`); a KMS-backed signer can satisfy it without changing the deed format.
- **Per-org keys** (v2), to narrow what one key can seal in a shared cloud deployment.
- **Reload without restart** (SIGHUP), so a rotation needs no restarts at all.

## What this doesn't do

- It doesn't protect a seed on a compromised host: whoever can read the file can seal deeds.
- It doesn't revoke keys from the CLI yet.
- It doesn't push new keys to verifiers: they fetch `keys.json`, which updates within 60 seconds.
- Seal keys are shared by every org of one deployment in v1.
