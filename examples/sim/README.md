# Simulated traffic

`deedglass dev simulate` pushes a week of seeded, simulated traffic through the real Gate:
policies, budgets, approvals and sealing are real; only the tools and the Witness are scripted.
Everything here is made up for that purpose.

| File | What it is |
|---|---|
| [`acme.yaml`](acme.yaml) | The default scenario: four agents, their hours, volume and tools, and six incidents. |
| [`acme-sim.v1.yaml`](acme-sim.v1.yaml) | Version 1 of the `acme-sim` policy, enforced. Refunds over $100.00 never run, order updates wait for a person, refunds are capped at $4,000.00 a day. |
| [`acme-sim.v2.yaml`](acme-sim.v2.yaml) | Version 2, in shadow mode from day 3: refunds over $30.00 wait for a person and Slack posts run only in #support and #launches. |
| [`acme-sim.v3.yaml`](acme-sim.v3.yaml) | Version 3, the replay demo: order status updates run on their own and the refund limit drops to $75.00. |
| [`embed.go`](embed.go) | Package `simfiles`: the four files above, embedded. |

Each policy has its cases next to it. Test one version at a time (this folder also holds the
scenario, which is not a policy):

```
deedglass policy test examples/sim/acme-sim.v1.yaml --require-coverage --voice
```

The scenario format, its defaults and the incident kinds are described in
`internal/learn/sim` (package documentation). The same scenario, seed and end time always give
the same plan.
