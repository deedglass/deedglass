# Head timestamps

Every sealed record names the one before it, so the chain shows a record changed or removed from
the middle. It can't, on its own, show that records were removed from the **end**, or that the whole
chain was rewritten and sealed again by someone holding the seal key. Head timestamps close part of
that gap (TY-03, ADR-112): the chain's head (the last record's seq, id and body hash) gets
timestamps from parties you choose, and anyone holding one later can tell whether the chain they
are shown still contains that head.

**Off by default.** With no TSA and no transparency service in `deedglass.yaml`, nothing is
timestamped and no request goes out.

## Turn it on

```yaml
timestamps:
  tsa_urls: https://tsa-one.example/tsr, https://tsa-two.example/tsr
  min_tsa: 2
  # Optional: a transparency service (IETF SCITT) and its public key as a JWK file.
  scitt_url: https://ts.example
  scitt_key_file: .deedglass/scitt-key.jwk
  at_export: true
  timeout: 10s
```

| Setting | What it does |
|---|---|
| `timestamps.tsa_urls` | RFC 3161 time-stamping authorities (TSAs), comma-separated https URLs, at most 8. Pick TSAs run by different organisations. |
| `timestamps.min_tsa` | How many TSAs must answer (default 2). Fewer, and the timestamp fails. |
| `timestamps.scitt_url` | A transparency service to register each head with: Deedglass posts the signed head to `<url>/entries`. |
| `timestamps.scitt_key_file` | The service's public key (EC P-256 or OKP Ed25519 JWK), used to check its answer before it is kept. |
| `timestamps.at_export` | Each `tar.gz` export timestamps its own last record and carries `timestamps.json` (default true). |
| `timestamps.timeout` | How long one TSA or the service has to answer. |

Every request leaves through the one egress door ([`NETWORK_EGRESS.md`](NETWORK_EGRESS.md)), like a
connector's: https, no private or metadata addresses (unless you allow them), no environment proxy.

## Timestamp the head on a schedule

```sh
deedglass deeds timestamp --out /backup/acme/timestamps.json             # once
deedglass deeds timestamp --out /backup/acme/timestamps.json --every 15m # keep running
```

Each run asks every TSA for a token over the head and, with a service set, registers the head with
it, then adds the result to the file. `--every` timestamps again whenever the head has moved. Run it
from cron or a systemd timer if you prefer one run at a time.

**Where the file lives matters.** A timestamp only helps against someone who can't also delete it.
Keep the file where the operator of the Deedglass server can't rewrite it (another account, a
bucket with object lock, a customer or auditor), or rely on the transparency service, which keeps
its own log of every registered head.

## Check a chain against a timestamp

```sh
deedglass verify deedglass-org_…-1-1204.tar.gz --keys https://api.example.com/.well-known/deedglass/keys.json \
  --timestamps timestamps.json --tsa-roots tsa-roots.pem --scitt-key scitt-key.jwk
```

```
✓ 1,204 deeds · chain intact · 1 seal key · 2026-10-02 → 2026-10-20
✓ Bundle manifest matches · sealed by sk_01j9zr8k…
✓ Deed 1,204 timestamped 2026-10-20 09:30:00 UTC by 2 TSAs and 1 transparency service · matches the chain
```

A bundle's own `timestamps.json` is checked without `--timestamps`. Everything is checked offline:

- each RFC 3161 token: the CMS signature, the signed attributes, the TSA's time-stamping key usage,
  and that its message imprint is SHA-256 of the head; with `--tsa-roots`, that the TSA chains to a
  root you trust at the token's time;
- each registration: the issuer's signed head (a COSE_Sign1 sealed with the issuer's Ed25519 key from
  the key set), and the service's inclusion proof (RFC 9942, RFC9162_SHA256) with the service key;
  with `--scitt-key`, only a key you passed is accepted.

Every piece of evidence present must verify; one good token never covers a bad one. Then each head
is compared with the records:

| Head | Answer |
|---|---|
| Inside the records, same body hash | `✓ … matches the chain` |
| Inside the records, another body hash | `✕ Deed N doesn't match the head timestamped at T: the chain was changed after that time.` (`timestamp_mismatch`, exit 1) |
| Past the last record | `✕ Deeds after M are missing: deed N was timestamped at T.` (`chain_tail_missing`, exit 1) |
| Before the first record | `○ … could not be compared with these deeds.` (a warning) |
| A token, statement or proof that doesn't verify | `✕ The timestamp of deed N doesn't verify: …` (`timestamp_invalid`, exit 1) |

Without `--tsa-roots` or `--scitt-key` the answer is the same, with a warning: the TSA chains and the
service key came from the file, so they show the file is intact, not who signed it.

## What timestamps.json holds

One org's heads, seq ascending, each with its evidence (canonical JSON; the format is in
[`spec/v2/BUNDLE_FORMAT.md`](../spec/v2/BUNDLE_FORMAT.md) §9):

```json
{"heads":[{"head":{"body_hash":"sha256:…","deed_id":"deed_…","issued_at":"2026-10-20T09:29:58.120Z","object":"chain_head","org":"org_…","seq":1204,"v":1},
  "rfc3161":[{"tsa":"https://tsa-one.example/tsr","token":"MIIG…"}],
  "scitt":[{"inclusion":"0oRD…","service":"https://ts.example","service_key":{"crv":"P-256","kty":"EC","x":"…","y":"…"},"statement":"0oRY…"}]}],
 "object":"head_timestamps","org":"org_…","v":1}
```

Nothing in it is secret: the head is copied from a public record, and the tokens, the signed head and
the inclusion proof are public signatures. No API key or credential is stored or sent.

## What it does not show

- **Only heads that were timestamped, and only from that time on.** Records added after the last
  timestamp and then removed leave no trace.
- **Only if the reader has the timestamp.** If someone can delete both the records and every copy of
  the timestamps, nothing is left to compare. Keep a copy elsewhere, or use a transparency service.
- **Not that a record is true.** A timestamp shows that a head existed by a time. It says nothing about
  whether the action it records was right.
- More in [`LIMITATIONS.md`](LIMITATIONS.md#head-timestamps).

## Independent verifiers

The Python and JavaScript verifiers in `verifiers/` accept a bundle that carries `timestamps.json`
(its bytes are checked against the manifest) but don't check the tokens or the inclusion proof, and
don't compare heads with the chain. Use `deedglass verify` for that.
