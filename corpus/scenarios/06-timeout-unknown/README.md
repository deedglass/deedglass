# 06-timeout-unknown: a timed-out statement committed anyway, and the retry committed again

The agent credits 100 to account 1. The statement is slow and the agent's client gives up after
0.5 s and closes the connection, without asking the server to cancel. The server finishes and
commits. The agent, told nothing, retries once, and that commits too.

| | |
|---|---|
| The agent reports | credited 100 once (the first attempt timed out) |
| The client saw | no reply, then `UPDATE 1` |
| Postgres committed | two transactions, each an update of 1 row: the balance is 700, not 600 |
| Deedglass | both attempts UNVERIFIABLE at L0: `footprint_unknown` (it can't read the slow function) |

Each attempt did what its statement said, so neither is a MISMATCH: the double credit shows as two
records with two committed transactions, and Deedglass doesn't flag it. In the benchmark
(`make corpus-bench`) both attempts are UNVERIFIABLE because `public.corpus_slow`, the stand-in for
a lock wait, is a function Deedglass can't derive a footprint for; and the agent's timeout doesn't
cut the Gate's own connection, so the first execution is recorded as succeeded, not unknown. Sending both with the same `Idempotency-Key` makes the
Gate return the first action instead of running the retry.
