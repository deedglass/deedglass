# bench

Performance measurements M1-M6 (spec 20_ §15). Raw output goes to `results/<date>-<host>.json`;
docs cite only those files, always with the hardware. No number is stated anywhere until it is
measured and filed here. Laptop spike numbers (plan §3.0 M3) are never quoted publicly.

- `results/<date>-<host>-store.json` (`make bench-store`, S02): the control-plane store budget (OrgTx,
  insert action + event, list 50 agents) as raw `go test -json`; the `PERF` lines hold p50, p99 and mean.

TODO(G4)
