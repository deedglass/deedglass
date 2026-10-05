# Invite a beta org

The beta is invite-only (`DEEDGLASS_SIGNUP=invite`). You pick each org from the waitlist.

## 1. Export the waitlist

```sh
cd ~/txv/site && node scripts/waitlist-export.mjs > ~/Desktop/waitlist.csv
```

## 2. Create the org and send the invite

```sh
sudo docker compose -f /opt/deedglass/deploy/cloud/compose.yaml run --rm deedglass \
  org create --name "Acme Robotics" --owner-email priya@acme.com
```

Expected: `✓ Created org acme …` and `✓ Invite sent to priya@acme.com` (through Resend). Use
`--print-link` only for your own test org, never for someone else's.

## Beta limits are soft

```sh
sudo /opt/deedglass/deploy/cloud/scripts/usage-report.sh
```

It lists each org's actions this month and its connectors, and marks orgs above 10,000 actions. It
blocks nothing: the Gate's rate limits are the hard edge, and the beta terms say limits are soft.
Hard plan quotas come after launch.

## What this doesn't cover

Self-serve sign-up, billing and plan quotas.
