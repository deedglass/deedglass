"""The Python quickstart: one blocked DROP TABLE, one UPDATE witnessed by Postgres (L2).

    pip install "deedglass[crypto]"
    export DEEDGLASS_API_KEY=… DEEDGLASS_SERVER=http://127.0.0.1:8080
    python examples/python/quickstart.py

Run it against `deedglass init` (the UPDATE waits for a person under postgres-safe; pass
--wait-for-approval 300 and countersign in Console › Approvals) or `make demo-up` (acme-demo lets
order-status updates run). It needs no model and no LLM key.
"""

from __future__ import annotations

import argparse

from _show import show

from deedglass import Deedglass, PolicyDeniedError

DROP = "DROP TABLE public.orders"
UPDATE = "UPDATE public.orders SET status = 'shipped' WHERE id = 42"


def main() -> None:
    p = argparse.ArgumentParser()
    p.add_argument("--scripted", action="store_true", help="accepted for symmetry with the other examples (always scripted)")
    p.add_argument("--wait-for-approval", type=float, default=0, help="seconds to wait for a person")
    args = p.parse_args()
    with Deedglass() as dg:
        try:
            dg.act("postgres.execute", {"sql": DROP})
        except PolicyDeniedError as e:
            show(e.action, str(e.message))
        a = dg.act("postgres.execute", {"sql": UPDATE}, wait_for_approval=args.wait_for_approval or None)
        show(a)
        print(f"  {a}")


if __name__ == "__main__":
    main()
