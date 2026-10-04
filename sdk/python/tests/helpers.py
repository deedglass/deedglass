from __future__ import annotations

import json
from typing import Any

import httpx

# A sentinel key shape built at run time; it is no credential.
SECRET = "SENTINELsentinel" * 2
KEY = "dg_test_" + "Ab12Cd34" + "_" + SECRET
BASE = "http://dg.test"
ACT = "act_01j9zw0000000000000000000a"
DEED = "deed_01j9zw0000000000000000000a"
APR = "apr_01j9zw0000000000000000000a"


def action(**over: Any) -> dict[str, Any]:
    a: dict[str, Any] = {
        "object": "action",
        "id": ACT,
        "created_at": "2026-10-10T10:00:00.000Z",
        "livemode": False,
        "agent": {"id": "agt_01j9zw0000000000000000000a", "name": "quickstart-agent"},
        "tool": "postgres.execute",
        "connector": "postgres",
        "mode": "gateway",
        "state": "sealed",
        "status": "executed",
        "decision": {
            "outcome": "allow",
            "code": None,
            "source": "rule",
            "rule_id": "allow-reads",
            "reason": None,
            "message": None,
            "policy": None,
            "policy_version": None,
        },
        "approval": None,
        "execution": {
            "status": "succeeded",
            "started_at": "2026-10-10T10:00:00.010Z",
            "finished_at": "2026-10-10T10:00:00.020Z",
            "latency_ms": 10,
            "error_code": None,
            "result": {"rows": 1},
        },
        "witness": {"object": "witness", "level": "L2", "verdict": "VERIFIED", "method": "postgres.l2", "reasons": []},
        "cost": None,
        "deed": DEED,
        "next_action": None,
    }
    a.update(over)
    return a


PENDING = {
    "state": "pending_approval",
    "status": "pending_approval",
    "decision": {
        "outcome": "require_approval",
        "message": "Writes to public.orders wait for a person.",
        "rule_id": "writes-need-approval",
    },
    "approval": {"id": APR, "status": "pending", "expires_at": "2026-10-10T10:42:00.000Z"},
    "execution": None,
    "witness": None,
    "deed": None,
    "next_action": {"type": "await_approval", "approval": APR, "expires_at": "2026-10-10T10:42:00.000Z"},
}

DENY_MSG = 'Blocked by policy "block-destructive-sql": DROP is never allowed for agents.'


def denied_body() -> dict[str, Any]:
    a = action(
        state="sealed",
        status="denied",
        decision={"outcome": "deny", "rule_id": "block-destructive-sql", "message": DENY_MSG, "reason": "DROP"},
        execution=None,
        witness=None,
    )
    return {
        "error": {
            "type": "policy_error",
            "code": "policy_denied",
            "message": DENY_MSG,
            "param": None,
            "request_id": "req_01k6",
            "doc_url": "https://docs.deedglass.com/errors/policy_denied",
            "action": a,
        }
    }


def ok(body: Any, status: int = 200, **headers: str) -> httpx.Response:
    h = {"deedglass-request-id": "req_test", "content-type": "application/json", **headers}
    return httpx.Response(status, content=json.dumps(body).encode(), headers=h)


def err(status: int, type_: str, code: str, message: str = "x.", **headers: str) -> httpx.Response:
    return ok({"error": {"type": type_, "code": code, "message": message, "request_id": "req_e"}}, status, **headers)
