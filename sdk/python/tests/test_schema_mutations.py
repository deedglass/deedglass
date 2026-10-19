"""Closed-schema refusals by kind: one mutation per rule of DEED_SPEC.md §5, each naming its field."""

from __future__ import annotations

import copy
import json
from typing import Any

import httpx
import pytest

from deedglass.verify import KeySet, VerifyError, canonicalize, parse_key_set, verify_deed
from deedglass.verify._fetch import KeysFetchError, check_keys_url, fetch_key_set
from deedglass.verify._keys import unsafe_key

from .conftest import repo

KEYS = parse_key_set(repo("testdata/deeds/keys/test-keys.json").read_bytes())
ACTION = json.loads(repo("testdata/deeds/valid/05-action-approved-then-executed.json").read_text())
ADMIN = json.loads(repo("testdata/deeds/valid/03-admin-agent-stopped.json").read_text())
AMEND = json.loads(repo("testdata/deeds/valid/04-amendment-mismatch.json").read_text())
X = "sha256:" + "0" * 64


def field_of(env: dict[str, Any], path: str, value: Any) -> str:
    e = copy.deepcopy(env)
    cur = e["body"]
    keys = path.split(".")
    for k in keys[:-1]:
        cur = cur[k]
    cur[keys[-1]] = value
    with pytest.raises(VerifyError) as err:
        verify_deed(canonicalize(e), KEYS)
    assert err.value.code == "schema_invalid", err.value.code
    return err.value.field


ACTION_CASES = [
    ("kind", "other", "body.kind"),
    ("prev_hash", "sha256:xyz", "body.prev_hash"),
    ("issued_at", "2026-02-30T00:00:00.000Z", "body.issued_at"),
    ("seal_key_id", "sk_x", "body.seal_key_id"),
    ("org", "org_x", "body.org"),
    ("agent", None, "body.agent"),
    ("agent.id", "agt_x", "body.agent.id"),
    ("agent.name", "a\u0001", "body.agent.name"),
    ("on_behalf_of", "a b", "body.on_behalf_of"),
    ("action", None, "body.action"),
    ("action.id", "act_x", "body.action.id"),
    ("action.tool", "", "body.action.tool"),
    ("action.connector", "con_x", "body.action.connector"),
    ("action.mode", "edge", "body.action.mode"),
    ("action.request_digest", "x", "body.action.request_digest"),
    ("action.intent_hash", "x", "body.action.intent_hash"),
    ("policy", None, "body.policy"),
    ("policy.id", "pol_x", "body.policy.id"),
    ("policy.version_id", "pv_x", "body.policy.version_id"),
    ("policy.version_hash", "x", "body.policy.version_hash"),
    ("policy.mode", "audit", "body.policy.mode"),
    ("policy.decision", "maybe", "body.policy.decision"),
    ("policy.rule_id", "-bad", "body.policy.rule_id"),
    ("approval.id", "apr_x", "body.approval.id"),
    ("approval.outcome", "maybe", "body.approval.outcome"),
    ("approval.decided_by", ["usr_x"], "body.approval.decided_by[0]"),
    ("approval.decided_by", [], "body.approval.decided_by"),
    ("approval.decided_at", "yesterday", "body.approval.decided_at"),
    ("approval.deed", "deed_x", "body.approval.deed"),
    ("execution.status", "maybe", "body.execution.status"),
    ("execution.started_at", "x", "body.execution.started_at"),
    ("execution.finished_at", "x", "body.execution.finished_at"),
    ("execution.finished_at", "2000-01-01T00:00:00.000Z", "body.execution.finished_at"),
    ("execution.result_digest", "x", "body.execution.result_digest"),
    ("execution.error_code", "Bad Code", "body.execution.error_code"),
    ("witness.level", "L9", "body.witness.level"),
    ("witness.verdict", "FINE", "body.witness.verdict"),
    ("witness.method", "", "body.witness.method"),
    ("witness.detail_digest", "x", "body.witness.detail_digest"),
    ("cost", {"amount": 1, "currency": "USD"}, "body.cost.currency"),
    (
        "admin",
        {
            "event": "x",
            "action": "y",
            "actor": {"type": "user", "id": "u"},
            "target": None,
            "reason": None,
            "data_digest": None,
        },
        "body.admin",
    ),
    ("amends", X, "body.amends"),
    ("v", "1", "body.v"),
    ("seq", "1", "body.seq"),
    ("agent", "x", "body.agent"),
    ("approval", {"id": "x"}, "body.approval.decided_by"),
]


@pytest.mark.parametrize(("path", "value", "field"), ACTION_CASES)
def test_action_rules(path: str, value: Any, field: str) -> None:
    got = field_of(ACTION, path, value)
    assert got == field or got.startswith(field.rsplit(".", 1)[0])


@pytest.mark.parametrize(
    ("env", "path", "value", "field"),
    [
        (ADMIN, "on_behalf_of", "usr_1", "body.on_behalf_of"),
        (ADMIN, "action", ACTION["body"]["action"], "body.action"),
        (ADMIN, "policy", ACTION["body"]["policy"], "body.policy"),
        (ADMIN, "admin", None, "body.admin"),
        (ADMIN, "admin.event", "adm_x", "body.admin.event"),
        (ADMIN, "admin.action", "Stopped", "body.admin.action"),
        (ADMIN, "admin.actor", {"type": "User", "id": "u"}, "body.admin.actor.type"),
        (ADMIN, "admin.actor", {"type": "user", "id": "U!"}, "body.admin.actor.id"),
        (ADMIN, "admin.reason", "", "body.admin.reason"),
        (ADMIN, "admin.data_digest", "x", "body.admin.data_digest"),
        (ADMIN, "witness", ACTION["body"]["witness"], "body.witness"),
        (AMEND, "amends", None, "body.amends"),
        (AMEND, "witness", None, "body.witness"),
        (AMEND, "approval", ACTION["body"]["approval"], "body.approval"),
        (AMEND, "execution", ACTION["body"]["execution"], "body.execution"),
        (AMEND, "cost", {"amount": 1, "currency": "usd"}, "body.cost"),
    ],
)
def test_kind_rules(env: dict[str, Any], path: str, value: Any, field: str) -> None:
    got = field_of(env, path, value)
    assert got.startswith(field)


def test_envelope_rules() -> None:
    for k, v in (("body_hash", "x"), ("seal", "ed25519:x"), ("seal_key_id", "sk_x")):
        e = copy.deepcopy(ACTION)
        e[k] = v
        with pytest.raises(VerifyError) as err:
            verify_deed(canonicalize(e), KEYS)
        assert err.value.field == k
    e = copy.deepcopy(ACTION)
    e["body"]["v"] = 2
    with pytest.raises(VerifyError) as err:
        verify_deed(canonicalize(e), KEYS)
    assert err.value.code == "unsupported_version"
    assert verify_deed(ACTION, KEYS).seq == ACTION["body"]["seq"]
    with pytest.raises(VerifyError):
        verify_deed(b"[]", KEYS)
    with pytest.raises(VerifyError):
        verify_deed(b'{"object":"thing"}', KEYS)


def test_key_set_refusals() -> None:
    good = json.loads(repo("testdata/deeds/keys/test-keys.json").read_text())

    def refused(mut: Any) -> str:
        d = copy.deepcopy(good)
        mut(d)
        with pytest.raises(VerifyError) as e:
            parse_key_set(json.dumps(d))
        return e.value.code

    assert refused(lambda d: d.update(object="x")) == "schema_invalid"
    assert refused(lambda d: d.update(updated_at="x")) == "schema_invalid"
    assert refused(lambda d: d.update(keys={})) == "schema_invalid"
    assert refused(lambda d: d["keys"][0].update(alg="RS 256")) == "seal_key_rejected"
    assert refused(lambda d: d["keys"][0].update(public_key="AAAA")) == "seal_key_rejected"
    assert refused(lambda d: d["keys"][0].update(thumbprint=X)) == "seal_key_rejected"
    assert refused(lambda d: d["keys"][0]["jwk"].update(use="enc")) == "seal_key_rejected"
    assert refused(lambda d: d["keys"][0].update(status="retired")) == "seal_key_rejected"
    assert refused(lambda d: d["keys"][0].update(activated_at="x")) == "schema_invalid"
    assert refused(lambda d: d["keys"].append(copy.deepcopy(d["keys"][0]))) == "seal_key_rejected"
    assert refused(lambda d: d["keys"][0].update(id="sk_x")) == "schema_invalid"
    assert refused(lambda d: d["keys"][0].update(jwk=None)) == "schema_invalid"
    with pytest.raises(VerifyError):
        parse_key_set(b"[1]")
    with pytest.raises(VerifyError):
        parse_key_set(b"{")
    assert unsafe_key(b"\x01" + b"\x00" * 31) == "small-order point"
    assert unsafe_key(b"\x00" * 31) == "not 32 bytes"
    assert KeySet.from_json(repo("testdata/deeds/keys/test-keys.json").read_bytes()).thumbprints()


def test_fetch_rules() -> None:
    for bad in ("http://example.com/keys.json", "ftp://x/keys.json", "https:///nohost"):
        with pytest.raises(KeysFetchError):
            check_keys_url(bad)
    check_keys_url("http://127.0.0.1:8080/.well-known/deedglass/keys.json")
    body = repo("testdata/deeds/keys/test-keys.json").read_bytes()
    seen: list[httpx.Request] = []

    def handler(req: httpx.Request) -> httpx.Response:
        seen.append(req)
        if req.url.path == "/big":
            return httpx.Response(200, content=b" " * (2 << 20))
        if req.url.path == "/404":
            return httpx.Response(404)
        return httpx.Response(200, content=body)

    c = httpx.Client(transport=httpx.MockTransport(handler))
    assert fetch_key_set("https://dg.test/keys.json", client=c).keys
    with pytest.raises(KeysFetchError, match="1 MiB"):
        fetch_key_set("https://dg.test/big", client=c)
    with pytest.raises(KeysFetchError, match="HTTP 404"):
        fetch_key_set("https://dg.test/404", client=c)
