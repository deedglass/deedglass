"""wrap_tool (edge mode), signed-intent vectors, the shared error catalog and the scrub list."""

from __future__ import annotations

import json
import re
from typing import Any

import httpx
import pytest

from deedglass import (
    ApprovalPendingError,
    AsyncDeedglass,
    Deedglass,
    IntentKey,
    PolicyDeniedError,
    ToolExecutionError,
    args_digest,
    generate_intent_key,
    sign_intent,
    wrap_tool,
    wrap_tool_async,
)
from deedglass._errors import from_envelope
from deedglass._errors_gen import CODES
from deedglass._scrub import SCRUB_RULES, scrub
from deedglass.intents import intent_kid

from .conftest import repo
from .helpers import ACT, BASE, KEY, PENDING, action, ok
from .test_client import Script

AUTH = {"mode": "proxy", "status": "authorized", "state": "executing", "witness": None, "deed": None}


def sync_client(s: Script) -> Deedglass:
    c = Deedglass(KEY, base_url=BASE, http_client=httpx.Client(transport=httpx.MockTransport(s)))
    c._sleep = lambda _s: None
    return c


def test_edge_runs_once_and_reports() -> None:
    s = Script(ok(action(**AUTH)), ok(action(mode="proxy")))
    c = sync_client(s)
    ran: list[int] = []

    def refund(customer: str, amount: int) -> dict[str, Any]:
        ran.append(amount)
        return {"refunded": amount}

    w = wrap_tool(refund, client=c, tool="billing.refund_customer")
    assert w("cus_1", amount=2000) == {"refunded": 2000}
    assert ran == [2000]
    assert s.body(0)["args"] == {"customer": "cus_1", "amount": 2000} and s.body(0)["mode"] == "proxy"
    rep = s.body(1)
    assert rep["status"] == "succeeded" and rep["result"] == {"refunded": 2000}


def test_edge_exception_is_unknown_and_scrubbed() -> None:
    s = Script(ok(action(**AUTH)), ok(action(mode="proxy")))
    c = sync_client(s)

    def boom() -> None:
        raise RuntimeError("upstream said Bearer abcdefghijklmnop and dg_live_Zz99Yy88_" + "q" * 32)

    w = wrap_tool(boom, client=c, tool="app.boom")
    with pytest.raises(ToolExecutionError) as e:
        w()
    assert isinstance(e.value.__cause__, RuntimeError)
    rep = s.body(1)
    assert rep["status"] == "unknown" and rep["error_code"] == "tool_error"
    assert "abcdefghijklmnop" not in rep["error_message"] and "q" * 32 not in rep["error_message"]
    s2 = Script(ok(action(**AUTH)), ok(action(mode="proxy")))
    w2 = wrap_tool(boom, client=sync_client(s2), tool="app.boom", failure_means="not_applied")
    with pytest.raises(ToolExecutionError):
        w2()
    assert s2.body(1)["status"] == "failed"


def test_edge_denied_never_runs() -> None:
    body = {
        "error": {
            "type": "policy_error",
            "code": "policy_denied",
            "message": 'Blocked by policy "no-refunds": No refunds.',
            "action": action(
                status="denied", decision={"outcome": "deny", "message": 'Blocked by policy "no-refunds": No refunds.'}
            ),
        }
    }
    s = Script(ok(body, 403))
    ran: list[int] = []
    w = wrap_tool(lambda: ran.append(1), client=sync_client(s), tool="app.refund")
    with pytest.raises(PolicyDeniedError):
        w()
    assert ran == []


def test_edge_pending_registry_reuses_the_approval() -> None:
    pend = action(**{**PENDING, "mode": "proxy"})
    s = Script(ok(pend, 202), ok(pend), ok(action(**AUTH)), ok(action(mode="proxy")))
    c = sync_client(s)
    ran: list[int] = []
    w = wrap_tool(lambda amount: ran.append(amount), client=c, tool="app.refund")
    with pytest.raises(ApprovalPendingError) as e:
        w(amount=5)
    assert e.value.approval_id.startswith("apr_") and "Waiting for approval" in str(e.value)
    with pytest.raises(ApprovalPendingError):
        w(amount=5)  # the model retries: a GET of the same action, not a second approval
    assert [r.method for r in s.calls] == ["POST", "GET"]
    w(amount=5)  # authorized now
    assert ran == [5]


async def test_edge_async() -> None:
    s = Script(ok(action(**AUTH)), ok(action(mode="proxy")))
    c = AsyncDeedglass(KEY, base_url=BASE, http_client=httpx.AsyncClient(transport=httpx.MockTransport(s)))

    async def fn(n: int) -> int:
        return n * 2

    w = wrap_tool_async(fn, client=c, tool="app.double")
    assert await w(21) == 42
    assert s.body(1)["result"] == 42


def test_args_not_json() -> None:
    w = wrap_tool(lambda x: x, client=sync_client(Script(ok(action()))), tool="app.x")
    with pytest.raises(TypeError, match="Argument 'x' can't be sent"):
        w(object())


def test_args_rich_types_sanitized() -> None:
    import uuid
    from dataclasses import dataclass
    from datetime import datetime, timezone
    from decimal import Decimal
    from enum import Enum

    from pydantic import BaseModel

    class Status(str, Enum):
        ACTIVE = "active"

    @dataclass
    class Meta:
        tag: str

    class Payload(BaseModel):
        num: int

    s = Script(ok(action(**AUTH)), ok(action(mode="proxy")))
    w = wrap_tool(lambda a, b, c, d, e, f: "ok", client=sync_client(s), tool="app.rich")

    now = datetime(2026, 10, 29, 12, 0, 0, tzinfo=timezone.utc)
    uid = uuid.UUID("12345678-1234-5678-1234-567812345678")

    res = w(
        a=now,
        b=uid,
        c=Status.ACTIVE,
        d=Decimal("19.99"),
        e=Meta(tag="prod"),
        f=Payload(num=42),
    )
    assert res == "ok"
    body = s.body(0)
    assert body["args"]["a"] == "2026-10-29T12:00:00+00:00"
    assert body["args"]["b"] == "12345678-1234-5678-1234-567812345678"
    assert body["args"]["c"] == "active"
    assert body["args"]["d"] == 19.99
    assert body["args"]["e"] == {"tag": "prod"}
    assert body["args"]["f"] == {"num": 42}


# --- signed intents (testdata/intents) ---

KEYS = json.loads(repo("testdata/intents/keys.json").read_text())


def pem(name: str) -> str:
    return repo("testdata/intents/" + KEYS["keys"][name]["pem"]).read_text()


def _ms(iso: str) -> int:
    from datetime import datetime

    return int(datetime.fromisoformat(iso.replace("Z", "+00:00")).timestamp() * 1000)


VALID = sorted(p.name for p in repo("testdata/intents/valid").iterdir())


def test_eight_valid_vectors() -> None:
    assert len(VALID) == 8


@pytest.mark.parametrize("name", VALID)
def test_intent_vectors(name: str) -> None:
    v = json.loads(repo("testdata/intents/valid/" + name).read_text())
    s = v["sign"]
    args = json.loads(s["args"])
    got = sign_intent(
        IntentKey(pem(s["key"])),
        org=s["org"],
        agent=s["agent"],
        tool=s["tool"],
        args=args,
        on_behalf_of=s.get("on_behalf_of") or None,
        session=s.get("session") or None,
        ttl_ms=s["ttl_ms"],
        now=lambda: _ms(s["now"]),
        nonce=s["nonce"],
    )
    assert got.header == v["header"]
    assert got.intent_hash == v["expected"]["intent_hash"]
    assert got.kid == v["expected"]["kid"]
    assert args_digest(args) == v["expected"]["args_digest"]


def test_s09_cross_check_signature() -> None:
    v = json.loads(repo("testdata/intents/valid/v01-refund-obo.json").read_text())
    assert v["header"].split(".")[1].endswith("nqjJCA")
    s = v["sign"]
    got = sign_intent(
        IntentKey(pem("test1")),
        org=s["org"],
        agent=s["agent"],
        tool=s["tool"],
        args=json.loads(s["args"]),
        on_behalf_of=s["on_behalf_of"],
        session=s["session"],
        ttl_ms=60000,
        now=lambda: _ms(s["now"]),
        nonce=s["nonce"],
    )
    assert (
        got.header.split(".")[1]
        == "TQAgBevx6iwlDM2Z7px4mrRiCjdbrAX3GCacB_gMAQbvPagJqCsJUWYz7xS7VuQqyV2rOK71uyRHvym2nqjJCA"
    )


def test_intent_refusals() -> None:
    base: dict[str, Any] = {
        "org": "org_01k6h7kp6msmzrhdzwtcr3jnkp",
        "agent": "agt_01k633rrmh3bcz2p39gg4vp46q",
        "tool": "stripe.refund",
        "args": {},
    }
    k = IntentKey(pem("test1"))

    def code(**over: Any) -> tuple[str, str | None]:
        try:
            sign_intent(k, **{**base, **over})
        except Exception as e:
            return getattr(e, "code", ""), getattr(e, "param", None)
        return "", None

    assert code(args={"n": 9007199254740992})[0] == "big_int"
    assert code(ttl_ms=300_001)[1] == "ttl_ms"
    assert code(tool="Stripe.Refund")[1] == "tool"
    assert code(org="org_bad")[1] == "org"
    assert code(on_behalf_of="a b")[1] == "on_behalf_of"
    assert code(nonce="XYZ")[1] == "nonce"
    assert sign_intent(
        k, **{**base, "on_behalf_of": "u" * 256, "session": "s" * 256, "tool": "stripe." + "r" * 120}
    ).header
    assert code()[0] == ""
    deep: Any = 1
    for _ in range(40):
        deep = [deep]
    assert code(args={"deep": deep})[0] == "validation_failed"
    try:
        sign_intent(IntentKey("not a pem"), **base)
    except Exception as e:
        assert getattr(e, "param", None) == "intent_key"
    try:
        sign_intent(IntentKey(pem("test1"), kid="ffff"), **base)
    except Exception as e:
        assert getattr(e, "param", None) == "intent_key.kid"


def test_generate_intent_key() -> None:
    pem_s, pub, kid = generate_intent_key()
    assert re.match(r"^ed25519:[A-Za-z0-9_-]{43}$", pub)
    import base64

    raw = base64.urlsafe_b64decode(pub[len("ed25519:") :] + "=")
    assert intent_kid(raw) == kid
    s = sign_intent(
        IntentKey(pem_s),
        org="org_01k6h7kp6msmzrhdzwtcr3jnkp",
        agent="agt_01k633rrmh3bcz2p39gg4vp46q",
        tool="billing.refund_customer",
        args={"a": 1},
    )
    assert s.kid == kid and len(s.header.split(".")) == 2
    assert "PRIVATE" not in repr(IntentKey(pem_s))


# --- the shared error catalog (sdk/errors.json, written by gensdkerrors) ---


def test_error_catalog_matches_python_classes() -> None:
    doc = json.loads(repo("sdk/errors.json").read_text())
    assert doc["object"] == "sdk_error_catalog" and len(doc["codes"]) == len(CODES)
    for e in doc["codes"]:
        got = from_envelope(e["status"], {"error": {"type": e["type"], "code": e["code"], "message": "m."}}, None, 0)
        assert type(got).__name__ == e["class"], e["code"]
        assert CODES[e["code"]] == (e["status"], e["type"], e["class"])


def test_scrub_matches_go() -> None:
    """The Python list has as many rules as Go's vault.Scrub patterns and redacts the same samples."""
    go = repo("internal/vault/redact.go").read_text()
    block = go[go.index("var scrubRules") :]
    block = block[: block.index("\n}\n")]
    go_patterns = re.findall(r"regexp\.MustCompile\(`([^`]*)`\)", block)
    py_patterns = [("(?i)" if rx.flags & re.IGNORECASE else "") + rx.pattern for rx, _ in SCRUB_RULES]
    assert py_patterns == go_patterns
    for sample in ("dg_live_Ab12Cd34_" + "x" * 32, "sk_test_" + "a" * 24, "postgres://u:p@h/db", "ghp_" + "a" * 36):
        assert sample not in scrub(f"before {sample} after")
    assert scrub("é" * 2000).endswith("…") and len(scrub("é" * 2000).encode()) <= 1024


def test_unknown_code_still_typed() -> None:
    e = from_envelope(
        418, {"error": {"type": "invalid_request_error", "code": "brand_new", "message": "Hi."}}, "req_1", 0
    )
    assert type(e).__name__ == "InvalidRequestError" and e.request_id == "req_1"
    e2 = from_envelope(502, None, None, 0)
    assert type(e2).__name__ == "APIError"
    assert ACT
