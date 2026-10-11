---
description: Grade this project's agent setup with deedglass scan. Read-only, runs on this machine, uploads nothing.
allowed-tools: Bash(npx -y deedglass scan:*)
---

Run `npx -y deedglass scan` in the project root. It reads the MCP client configs, the agent
packages and, if a database URL is set, the Postgres role's permissions. It changes nothing and
sends nothing anywhere.

Then tell the user, in plain words:

1. The grade line exactly as printed (for example `Blast radius: D · 3 write paths with no check`).
2. Each deduction and the points it cost.
3. The one thing to fix first, and the docs page the scan names for it.
4. The footnote the scan prints: `A means a small blast radius, not a safe agent.`

Don't edit any file, policy or config yourself. If the user asks to fix something, show the
change and let them decide.
