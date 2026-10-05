# What the cloud beta costs

INR per month, from S35's plan (prices checked 2026-10-01). Confirm each live price before you buy;
the founder pays every line.

| Item | Choice | ₹/month |
|---|---|---|
| VM | Hetzner CX23 (2 vCPU, 4 GB, 40 GB) + IPv4 | ≈ 470–650 |
| Object storage | Cloudflare R2 free tier (check the current allowance) | 0 |
| DNS and TLS | Cloudflare DNS, Caddy with Let's Encrypt | 0 |
| Email | Resend free tier | 0 |
| Monitoring | Better Stack free (10 monitors, heartbeats) | 0 |
| Domain | deedglass.com renewal, spread over 12 months | ≈ 85 |
| **Total** | | **≈ 555–735 (target ≤ 1,500)** |

R2 asks for a payment method even on the free tier. Check R2's current free allowance against the
size of 35 days of backups (`du -sh /var/backups/deedglass` times five) before you rely on it.

## What this doesn't cover

Paid monitoring, a second VM for high availability, and managed Postgres, all post-launch.
