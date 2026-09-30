# Benchmarks

Measured results only. Each result cites a raw file in `bench/results/` and the hardware it ran on. **Owner: S35**, which runs the measurements and fills this page.

## Targets (contract §12), not results

| Metric | Target |
|---|---|
| Gate overhead (allow, no witness) | p50 < 5 ms, p99 < 25 ms |
| Policy evaluation (50 rules) | p99 < 1 ms |
| Stop propagation | < 2 s across instances |
| L2 witness, commit to verdict | p50 < 1 s at 100 writes/s |
| `deedglass verify` | 100k deeds in < 5 s |
| Quickstart | first blocked and first verified action in < 5 min |

## What this page does not do

It shows no numbers yet. A target is a goal, not a result, and nothing here may be quoted as a measurement until S35 files the raw output.
