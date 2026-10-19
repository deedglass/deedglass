# 06-timeout-unknown: a timed-out statement committed anyway, and the retry committed again

The agent credits 100 to account 1. The statement is slow and the agent's client gives up after
0.5 s and closes the connection, without asking the server to cancel. The server finishes and
commits. The agent, told nothing, retries once, and that commits too.

| | |
|---|---|
| The agent reports | credited 100 once (the first attempt timed out) |
| The client saw | no reply, then `UPDATE 1` |
| Postgres committed | two transactions, each an update of 1 row: the balance is 700, not 600 |
| Deedglass | both attempts VERIFIED at L2; the first with `outcome_unknown` |

Each attempt did what its statement said, so neither is a MISMATCH: the double credit shows as two
records with two committed transactions. Sending both with the same `Idempotency-Key` makes the
Gate return the first action instead of running the retry.
