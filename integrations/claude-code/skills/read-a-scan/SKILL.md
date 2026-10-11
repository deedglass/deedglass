---
name: read-a-scan
description: Explain a deedglass scan result or a Deedglass block message in plain words. Use when the session shows "Blast radius:" output from deedglass scan, or a tool call refused with "Blocked by policy" or "Waiting for approval under policy".
---

# Reading a Deedglass result

Deedglass sits between an agent and its tools. It decides what each call may do, runs it, checks
what really happened, and seals a record (a deed).

## A scan result

- The grade (A to F) measures blast radius: what the agent setup could change if an agent went
  wrong. It is not a safety rating. Repeat the scan's own line: `A means a small blast radius,
  not a safe agent.`
- Points start at 100. A superuser or BYPASSRLS database role costs 35, each high finding 20 (at
  most 2), each medium finding 8 (at most 3), each write path with no check 4 (at most 5).
- A write path with no check is a tool or table that can change data and isn't routed through
  `deedglass mcp wrap` with an active policy.
- Point to the matching page: https://deedglass.com/score and https://docs.deedglass.com.

## A block or approval message

- `Blocked by policy "<rule>": <reason>` means the policy denied the call before it ran. Nothing
  changed. The reason says why; the rule id names the rule in the policy file.
- `Waiting for approval under policy "<rule>"` means a person must approve in the Console, Slack
  or the emailed link before the call runs.
- Results use plain names: Claimed (the tool's own answer), Checked (read back from the target),
  Proven (the database's change log); and Matches, Doesn't match, Can't check.

Never change a policy, key or config on your own. Explain, then let the user decide.
