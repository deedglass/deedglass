# Seal keys

The seal key signs every deed. Anyone holding it can forge deeds that verify, so it lives in exactly
two offline copies and in `/etc/deedglass/seal` on the VM, and nowhere else: never in git, email,
chat, a cloud drive, an image, a backup, an environment variable or a log.

## 1. Generate it offline, on your Mac

```sh
mkdir -m 0700 -p ~/deedglass-keys && deedglass keys gen --dir ~/deedglass-keys
```

Expected: `✓ Seal key sk_… written to ~/deedglass-keys/sk_….json` (mode 0600).

## 2. Keep two copies

1. Your password manager: a secure note named `Deedglass seal key sk_…` with the file attached.
2. A USB drive at home, encrypted: `age -p -o /Volumes/<usb>/sk_….json.age ~/deedglass-keys/sk_….json`.

## 3. Install it on the VM

```sh
scp ~/deedglass-keys/sk_….json root@<ipv4>:/etc/deedglass/seal/
ssh root@<ipv4> 'chown 65532:65532 /etc/deedglass/seal/*.json && chmod 0400 /etc/deedglass/seal/*.json'
```

`preflight.sh` checks the owner and mode. The first `serve` registers the key.

## 4. Publish the public half twice

```sh
curl -s https://api.deedglass.com/.well-known/deedglass/keys.json > ~/txv/site/public/.well-known/deedglass/keys.json
```

Deploy the site. Auditors pin `https://deedglass.com/.well-known/deedglass/keys.json`;
`keys-mirror.sh` compares the two daily inside the health heartbeat and fails it on drift.

## 5. Rotate every 90 days, or on suspicion

`keys rotate` writes the new key and registers it in one step (S04; [docs/SEAL_KEYS.md](../../docs/SEAL_KEYS.md)),
so it runs on the VM, into the seal directory, with the database it registers in:

```sh
sudo docker compose -f /opt/deedglass/deploy/cloud/compose.yaml run --rm --no-deps \
  -v /etc/deedglass/seal:/run/deedglass/seal-rw deedglass keys rotate --overlap 24h --dir /run/deedglass/seal-rw
sudo chown 65532:65532 /etc/deedglass/seal/*.json && sudo chmod 0400 /etc/deedglass/seal/*.json
```

Point `DEEDGLASS_SEAL_KEY_FILE` at the new file, restart (`sudo systemctl restart deedglass-stack`),
copy the new file to your two offline copies (step 2) with `scp`, delete the copy on your Mac's disk,
then update the site's keys.json (step 4).

## 6. Suspected compromise

1. Stop every agent in every org: `docker compose run --rm deedglass org stop-all --org <slug> --reason "Seal key rotation"` for each org.
2. Rotate with no overlap: step 5 with `--overlap 0s`.
3. Mark the old key revoked through `deedglass keys` (S04's mechanism) and update both copies of keys.json.
4. Publish a dated notice with the affected `seq` range per org; re-run `deedglass ledger check` for every org.
5. Tell every beta org within 72 hours, in writing.

## What this doesn't cover

Hardware or cloud key management (KMS, HSM) is post-launch. One deployment has one active seal key
for every org; per-org keys are v2.
