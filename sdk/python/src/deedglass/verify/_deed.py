"""One deed: the closed schema by kind, the hash and the seal, in Go's deed.VerifyEnvelope order.

The first failure wins, and every path that is not a clean pass is a VerifyError (DEED_SPEC.md §11).
"""

from __future__ import annotations

from dataclasses import dataclass
from typing import Any

from .._copy import CRYPTO_MISSING
from ._b64 import decode_b64url
from ._jcs import canonicalize
from ._keys import ALG_ED25519, KeySet, sha256_digest
from ._schema import (
    RE_ADMIN_ACT,
    RE_CURRENCY,
    RE_DIGEST,
    RE_ERROR_CODE,
    RE_HEADER_REF,
    RE_METHOD,
    RE_REF_ID,
    RE_REF_TYPE,
    RE_RULE_ID,
    RE_SEAL,
    Decoder,
    Obj,
    as_obj,
    closed,
    is_id,
    parse_time,
    plain_text,
)
from ._strict import StrictError, decode_utf8, malformed_from, parse_strict_value
from ._verror import VerifyError, malformed, schema_field, unsupported

MAX_INPUT_BYTES = 64 * 1024
MAX_ENVELOPE_BYTES = 16 * 1024
TAG_DEED = "deedglass/deed/v1\n"
# The accept policy's date (DEED_SPEC.md §11.4): Ed25519 alone only for deeds issued before
# 2035-01-01T00:00:00.000Z (NIST IR 8547 ipd), in milliseconds since the epoch.
ED25519_ALONE_UNTIL = 2051222400000
TAG_GENESIS = "deedglass/genesis\n"
MAX = 9007199254740991

_ENVELOPE = ("object", "id", "body", "body_hash", "seal", "seal_key_id")
_BODY = (
    "v",
    "kind",
    "deed_id",
    "org",
    "seq",
    "prev_hash",
    "issued_at",
    "agent",
    "on_behalf_of",
    "session",
    "action",
    "policy",
    "approval",
    "execution",
    "witness",
    "cost",
    "admin",
    "amends",
    "seal_key_id",
)
_AGENT = ("id", "name")
_ACTION = ("id", "tool", "connector", "mode", "request_digest", "intent_hash", "intent_signed")
_POLICY = ("id", "version_id", "version_hash", "mode", "decision", "rule_id")
_APPROVAL = ("id", "outcome", "decided_by", "decided_at", "deed")
_EXECUTION = ("status", "started_at", "finished_at", "latency_ms", "result_digest", "error_code")
_WITNESS = ("level", "verdict", "method", "detail_digest")
_COST = ("amount", "currency")
_ADMIN = ("event", "action", "actor", "target", "reason", "data_digest")
_REF = ("type", "id")


def genesis(org: str) -> str:
    """'sha256:' + hex(SHA-256('deedglass/genesis\\n' + org)): the prev_hash of an org's first deed."""
    return sha256_digest(TAG_GENESIS + org)


@dataclass
class DeedResult:
    """A deed that passed every check."""

    envelope: dict[str, Any]
    body: dict[str, Any]
    body_hash: str
    seq: int
    deed_id: str
    org: str
    issued_at: int
    seal_key_id: str
    canonical: bool


def _with_refs(e: VerifyError, body: dict[str, Any] | None) -> VerifyError:
    if not body:
        return e
    seq = body.get("seq")
    if isinstance(seq, int) and not isinstance(seq, bool) and seq > 0 and e.seq == 0:
        e.seq = seq
    did = body.get("deed_id")
    if isinstance(did, str) and e.deed_id == "" and is_id("deed", did):
        e.deed_id = did
    return e


def _body_from_tree(v: Any) -> dict[str, Any]:
    try:
        o = as_obj(v, "")
    except VerifyError:
        raise schema_field("body", "must be an object") from None
    ver = o.m.get("v")
    if isinstance(ver, int) and not isinstance(ver, bool) and ver != 1:
        raise unsupported(ver)
    closed(o, _BODY)
    d = Decoder()
    b: dict[str, Any] = {
        "v": d.i(o, "v"),
        "kind": d.s(o, "kind"),
        "deed_id": d.s(o, "deed_id"),
        "org": d.s(o, "org"),
        "seq": d.i(o, "seq"),
        "prev_hash": d.s(o, "prev_hash"),
        "issued_at": d.s(o, "issued_at"),
        "on_behalf_of": d.ns(o, "on_behalf_of"),
        "session": d.ns(o, "session"),
        "amends": d.ns(o, "amends"),
        "seal_key_id": d.s(o, "seal_key_id"),
        "agent": None,
        "action": None,
        "policy": None,
        "approval": None,
        "execution": None,
        "witness": None,
        "cost": None,
        "admin": None,
    }
    a = d.obj(o, "agent", _AGENT)
    if a:
        b["agent"] = {"id": d.s(a, "id"), "name": d.s(a, "name")}
    a = d.obj(o, "action", _ACTION)
    if a:
        b["action"] = {
            "id": d.s(a, "id"),
            "tool": d.s(a, "tool"),
            "connector": d.ns(a, "connector"),
            "mode": d.s(a, "mode"),
            "request_digest": d.s(a, "request_digest"),
            "intent_hash": d.ns(a, "intent_hash"),
            "intent_signed": d.b(a, "intent_signed"),
        }
    p = d.obj(o, "policy", _POLICY)
    if p:
        b["policy"] = {
            "id": d.ns(p, "id"),
            "version_id": d.ns(p, "version_id"),
            "version_hash": d.ns(p, "version_hash"),
            "mode": d.s(p, "mode"),
            "decision": d.s(p, "decision"),
            "rule_id": d.ns(p, "rule_id"),
        }
    a = d.obj(o, "approval", _APPROVAL)
    if a:
        apr: dict[str, Any] = {
            "id": d.s(a, "id"),
            "outcome": d.s(a, "outcome"),
            "decided_at": d.ns(a, "decided_at"),
            "deed": d.ns(a, "deed"),
            "decided_by": [],
        }
        b["approval"] = apr
        if d.err is None:
            apr["decided_by"] = d.strs(a, "decided_by")
    e = d.obj(o, "execution", _EXECUTION)
    if e:
        b["execution"] = {
            "status": d.s(e, "status"),
            "started_at": d.s(e, "started_at"),
            "finished_at": d.s(e, "finished_at"),
            "latency_ms": d.i(e, "latency_ms"),
            "result_digest": d.ns(e, "result_digest"),
            "error_code": d.ns(e, "error_code"),
        }
    w = d.obj(o, "witness", _WITNESS)
    if w:
        b["witness"] = {
            "level": d.s(w, "level"),
            "verdict": d.s(w, "verdict"),
            "method": d.s(w, "method"),
            "detail_digest": d.ns(w, "detail_digest"),
        }
    c = d.obj(o, "cost", _COST)
    if c:
        b["cost"] = {"amount": d.i(c, "amount"), "currency": d.s(c, "currency")}
    a = d.obj(o, "admin", _ADMIN)
    if a:
        adm: dict[str, Any] = {
            "event": d.s(a, "event"),
            "action": d.s(a, "action"),
            "reason": d.ns(a, "reason"),
            "data_digest": d.ns(a, "data_digest"),
            "actor": {"type": "", "id": ""},
            "target": None,
        }
        b["admin"] = adm
        if d.err is None:
            try:
                actor = as_obj(a.m.get("actor"), f"{a.path}.actor")
                closed(actor, _REF)
                adm["actor"] = {"type": d.s(actor, "type"), "id": d.s(actor, "id")}
            except VerifyError as err:
                d.err = err
        t = d.obj(a, "target", _REF)
        if t:
            adm["target"] = {"type": d.s(t, "type"), "id": d.s(t, "id")}
    d.check()
    return b


def _validate(b: dict[str, Any]) -> None:
    if b["v"] != 1:
        raise unsupported(b["v"])
    kind = b["kind"]
    if kind not in ("action", "admin", "amendment"):
        raise schema_field("kind", "must be action, admin or amendment")
    if not is_id("deed", b["deed_id"]):
        raise schema_field("deed_id", "must be a deed_ id")
    if b["seq"] < 1 or b["seq"] > MAX:
        raise schema_field("seq", "must be 1 to 2^53-1")
    if not RE_DIGEST.match(b["prev_hash"]):
        raise schema_field("prev_hash", "must be sha256:<64 hex>")
    if parse_time(b["issued_at"]) is None:
        raise schema_field("issued_at", "must be YYYY-MM-DDTHH:MM:SS.mmmZ")
    if not is_id("sk", b["seal_key_id"]):
        raise schema_field("seal_key_id", "must be an sk_ id")
    if not is_id("org", b["org"]):
        raise schema_field("org", "must be an org_ id")
    action, admin, amend = kind == "action", kind == "admin", kind == "amendment"
    ag = b["agent"]
    if ag is None:
        if not admin:
            raise schema_field("agent", f"is required for {kind} deeds")
    else:
        if not is_id("agt", ag["id"]):
            raise schema_field("agent.id", "must be an agt_ id")
        if not plain_text(ag["name"], 1, 64, False):
            raise schema_field("agent.name", "must be 1-64 characters of text")
    for name in ("on_behalf_of", "session"):
        v = b[name]
        if v is None:
            continue
        if admin:
            raise schema_field(name, "must be null in admin deeds")
        if not RE_HEADER_REF.match(v):
            raise schema_field(name, "must be 1-256 characters of A-Z a-z 0-9 . _ ~ : @ / + = -")
    a = b["action"]
    if a is None:
        if not admin:
            raise schema_field("action", f"is required for {kind} deeds")
    else:
        if admin:
            raise schema_field("action", "must be null in admin deeds")
        if not is_id("act", a["id"]):
            raise schema_field("action.id", "must be an act_ id")
        tb = len(a["tool"].encode("utf-8"))
        if tb == 0 or tb > 200 or not plain_text(a["tool"], 1, 200, False):
            raise schema_field("action.tool", "must be 1-200 bytes of text")
        if a["connector"] is not None and not is_id("con", a["connector"]):
            raise schema_field("action.connector", "must be a con_ id or null")
        if a["mode"] not in ("gateway", "proxy"):
            raise schema_field("action.mode", "must be gateway or proxy")
        if not RE_DIGEST.match(a["request_digest"]):
            raise schema_field("action.request_digest", "must be sha256:<64 hex>")
        if a["intent_hash"] is not None and not RE_DIGEST.match(a["intent_hash"]):
            raise schema_field("action.intent_hash", "must be sha256:<64 hex> or null")
        if a["intent_signed"] and a["intent_hash"] is None:
            raise schema_field("action.intent_hash", "is required when intent_signed is true")
    p = b["policy"]
    if p is None and action:
        raise schema_field("policy", "is required for action deeds")
    if p is not None and not action:
        raise schema_field("policy", f"must be null in {kind} deeds")
    if p is not None:
        if p["id"] is not None and not is_id("pol", p["id"]):
            raise schema_field("policy.id", "must be a pol_ id or null")
        if p["version_id"] is not None and not is_id("pv", p["version_id"]):
            raise schema_field("policy.version_id", "must be a pv_ id or null")
        if p["version_hash"] is not None and not RE_DIGEST.match(p["version_hash"]):
            raise schema_field("policy.version_hash", "must be sha256:<64 hex> or null")
        if p["version_id"] is not None and p["version_hash"] is None:
            raise schema_field("policy.version_hash", "is required when version_id is set")
        if p["mode"] not in ("enforce", "shadow"):
            raise schema_field("policy.mode", "must be enforce or shadow")
        if p["decision"] not in ("allow", "deny", "require_approval"):
            raise schema_field("policy.decision", "must be allow, deny or require_approval")
        if p["rule_id"] is not None and not RE_RULE_ID.match(p["rule_id"]):
            raise schema_field("policy.rule_id", "must be 1-128 characters of A-Z a-z 0-9 . _ : / -")
    ap = b["approval"]
    if ap is not None:
        if not action:
            raise schema_field("approval", f"must be null in {kind} deeds")
        if not is_id("apr", ap["id"]):
            raise schema_field("approval.id", "must be an apr_ id")
        if ap["outcome"] not in ("approved", "denied", "expired", "cancelled"):
            raise schema_field("approval.outcome", "must be approved, denied, expired or cancelled")
        by = ap["decided_by"]
        for i, u in enumerate(by):
            if not is_id("usr", u):
                raise schema_field(f"approval.decided_by[{i}]", "must be a usr_ id")
            if i > 0 and by[i - 1].encode("utf-16-be") >= u.encode("utf-16-be"):
                raise schema_field(f"approval.decided_by[{i}]", "must be sorted with no duplicates")
        if ap["outcome"] in ("approved", "denied") and not by:
            raise schema_field("approval.decided_by", "must name who decided")
        if ap["outcome"] == "expired" and by:
            raise schema_field("approval.decided_by", "must be empty when the approval expired")
        if ap["decided_at"] is not None and parse_time(ap["decided_at"]) is None:
            raise schema_field("approval.decided_at", "must be YYYY-MM-DDTHH:MM:SS.mmmZ or null")
        if ap["deed"] is not None and not is_id("deed", ap["deed"]):
            raise schema_field("approval.deed", "must be a deed_ id or null")
    e = b["execution"]
    if e is not None:
        if not action:
            raise schema_field("execution", f"must be null in {kind} deeds")
        if e["status"] not in ("succeeded", "failed", "unknown"):
            raise schema_field("execution.status", "must be succeeded, failed or unknown")
        start = parse_time(e["started_at"])
        if start is None:
            raise schema_field("execution.started_at", "must be YYYY-MM-DDTHH:MM:SS.mmmZ")
        end = parse_time(e["finished_at"])
        if end is None:
            raise schema_field("execution.finished_at", "must be YYYY-MM-DDTHH:MM:SS.mmmZ")
        if end < start:
            raise schema_field("execution.finished_at", "must not be before started_at")
        if e["latency_ms"] < 0 or e["latency_ms"] > MAX:
            raise schema_field("execution.latency_ms", "must be 0 to 2^53-1")
        if e["result_digest"] is not None and not RE_DIGEST.match(e["result_digest"]):
            raise schema_field("execution.result_digest", "must be sha256:<64 hex> or null")
        if e["error_code"] is not None and not RE_ERROR_CODE.match(e["error_code"]):
            raise schema_field("execution.error_code", "must be a snake_case code of at most 64 characters, or null")
    w = b["witness"]
    if w is None and amend:
        raise schema_field("witness", "is required for amendment deeds")
    if w is not None and admin:
        raise schema_field("witness", "must be null in admin deeds")
    if w is not None:
        if w["level"] not in ("L0", "L1", "L2"):
            raise schema_field("witness.level", "must be L0, L1 or L2")
        if w["verdict"] not in ("VERIFIED", "MISMATCH", "UNVERIFIABLE"):
            raise schema_field("witness.verdict", "must be VERIFIED, MISMATCH or UNVERIFIABLE")
        if not RE_METHOD.match(w["method"]):
            raise schema_field("witness.method", "must be 1-64 characters of a-z 0-9 . _ -")
        if w["detail_digest"] is not None and not RE_DIGEST.match(w["detail_digest"]):
            raise schema_field("witness.detail_digest", "must be sha256:<64 hex> or null")
    c = b["cost"]
    if c is not None:
        if not action:
            raise schema_field("cost", f"must be null in {kind} deeds")
        if c["amount"] < 0 or c["amount"] > MAX:
            raise schema_field("cost.amount", "must be 0 to 2^53-1 minor units")
        if not RE_CURRENCY.match(c["currency"]):
            raise schema_field("cost.currency", "must be a lowercase ISO 4217 code")
    ad = b["admin"]
    if ad is None and admin:
        raise schema_field("admin", "is required for admin deeds")
    if ad is not None and not admin:
        raise schema_field("admin", f"must be null in {kind} deeds")
    if ad is not None:
        if not is_id("adm", ad["event"]):
            raise schema_field("admin.event", "must be an adm_ id")
        if not RE_ADMIN_ACT.match(ad["action"]):
            raise schema_field("admin.action", "must be <object>.<past verb>, for example agent.stopped")
        _validate_ref(ad["actor"], "admin.actor")
        if ad["target"] is not None:
            _validate_ref(ad["target"], "admin.target")
        if ad["reason"] is not None and not plain_text(ad["reason"], 1, 280, True):
            raise schema_field("admin.reason", "must be 1-280 characters of text or null")
        if ad["data_digest"] is not None and not RE_DIGEST.match(ad["data_digest"]):
            raise schema_field("admin.data_digest", "must be sha256:<64 hex> or null")
    am = b["amends"]
    if am is None and amend:
        raise schema_field("amends", "is required for amendment deeds")
    if am is not None and not amend:
        raise schema_field("amends", f"must be null in {kind} deeds")
    if am is not None and not is_id("deed", am):
        raise schema_field("amends", "must be a deed_ id")


def _validate_ref(r: dict[str, str], path: str) -> None:
    if not RE_REF_TYPE.match(r["type"]):
        raise schema_field(f"{path}.type", "must match ^[a-z][a-z_]{0,31}$")
    if not RE_REF_ID.match(r["id"]):
        raise schema_field(f"{path}.id", "must be an id or a lowercase name of at most 64 characters")


@dataclass
class _Parsed:
    env: dict[str, Any]
    body: dict[str, Any]
    body_jcs: str
    env_jcs: str


def parse_envelope(raw: bytes | str) -> _Parsed:
    """Reads an envelope in the verifier's order (port of parseEnvelope). Raises VerifyError."""
    size = len(raw.encode("utf-8", "surrogatepass")) if isinstance(raw, str) else len(raw)
    if size > MAX_INPUT_BYTES:
        raise malformed(f"larger than {MAX_INPUT_BYTES >> 10} KiB")
    try:
        tree = parse_strict_value(raw if isinstance(raw, str) else decode_utf8(raw), True)
    except StrictError as e:
        raise malformed_from(e) from None
    if not isinstance(tree, dict):
        raise malformed("a deed must be a JSON object")
    top = tree
    if top.get("object") != "deed":
        raise schema_field("object", 'must be "deed"')
    body_tree = top.get("body") if isinstance(top.get("body"), dict) else None
    did_env = top.get("id")
    if isinstance(did_env, str) and body_tree is not None:
        did = body_tree.get("deed_id")
        if isinstance(did, str) and did != did_env:
            raise _with_refs(
                VerifyError("id_mismatch", detail=f"envelope id {did_env}, body deed_id {did}"),
                body_tree,
            )
    if body_tree is not None:
        n = body_tree.get("v")
        if isinstance(n, int) and not isinstance(n, bool) and n != 1:
            raise _with_refs(unsupported(n), body_tree)
    o = Obj(top, "")
    try:
        closed(o, _ENVELOPE)
    except VerifyError as e:
        raise _with_refs(e, body_tree) from None
    d = Decoder()
    env: dict[str, Any] = {
        "object": "deed",
        "id": d.s(o, "id"),
        "body_hash": d.s(o, "body_hash"),
        "seal": d.s(o, "seal"),
        "seal_key_id": d.s(o, "seal_key_id"),
    }
    if d.err is not None:
        raise _with_refs(d.err, body_tree)
    try:
        body = _body_from_tree(top.get("body"))
    except VerifyError as ve:
        if ve.code == "schema_invalid" and ve.field not in ("", "body"):
            ve.field = "body." + ve.field
        raise _with_refs(ve, body_tree) from None
    env["body"] = body
    try:
        _validate(body)
    except VerifyError as ve:
        if ve.code == "schema_invalid":
            ve.field = "body." + ve.field
        raise _with_refs(ve, body_tree) from None
    if not RE_DIGEST.match(env["body_hash"]):
        raise _with_refs(schema_field("body_hash", "must be sha256:<64 hex>"), body_tree)
    if not RE_SEAL.match(env["seal"]):
        raise _with_refs(schema_field("seal", "must be ed25519:<86 base64url characters>"), body_tree)
    if not is_id("sk", env["seal_key_id"]):
        raise _with_refs(schema_field("seal_key_id", "must be an sk_ id"), body_tree)
    body_jcs = canonicalize(top["body"])
    env_jcs = (
        '{"body":'
        + body_jcs
        + ',"body_hash":"'
        + env["body_hash"]
        + '","id":"'
        + env["id"]
        + '","object":"deed","seal":"'
        + env["seal"]
        + '","seal_key_id":"'
        + env["seal_key_id"]
        + '"}'
    )
    if len(env_jcs.encode("utf-8")) > MAX_ENVELOPE_BYTES:
        raise _with_refs(malformed(f"its canonical form is larger than {MAX_ENVELOPE_BYTES >> 10} KiB"), body_tree)
    return _Parsed(env, body, body_jcs, env_jcs)


# L, the order of the Ed25519 base point: a seal with s ≥ L is refused first, as Go does.
_L = 2**252 + 27742317777372353535851937790883648493


def _ed25519_verify(pub: bytes, msg: bytes, sig: bytes) -> bool:
    try:
        from cryptography.exceptions import InvalidSignature
        from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PublicKey
    except ImportError:  # pragma: no cover - exercised only without the extra
        raise ImportError(CRYPTO_MISSING) from None
    if len(sig) != 64 or int.from_bytes(sig[32:], "little") >= _L:
        return False
    try:
        Ed25519PublicKey.from_public_bytes(pub).verify(sig, msg)
    except (InvalidSignature, ValueError):
        return False
    return True


def open_seal(keys: KeySet, key_id: str, msg: str, seal: str) -> bool:
    if not RE_SEAL.match(seal):
        return False
    sig = decode_b64url(seal[len("ed25519:") :])
    e = keys.lookup(key_id)
    if sig is None or len(sig) != 64 or e is None:
        return False
    return _ed25519_verify(e.pub, msg.encode("utf-8"), sig)


def verify_envelope(raw: bytes | str, keys: KeySet, *, strict: bool = False) -> DeedResult:
    """Checks one deed against a key set in Go's order. Raises VerifyError on the first failure."""
    p = parse_envelope(raw)
    env, b = p.env, p.body

    def fail(code: str) -> VerifyError:
        return VerifyError(code, seq=b["seq"], deed_id=b["deed_id"], key=env["seal_key_id"], issued_at=b["issued_at"])

    if sha256_digest(p.body_jcs) != env["body_hash"]:
        raise fail("body_hash_mismatch")
    if env["seal_key_id"] != b["seal_key_id"]:
        raise fail("seal_key_mismatch")
    e = keys.lookup(env["seal_key_id"])
    if e is None:
        raise fail("seal_key_unknown")
    if e.key.alg != ALG_ED25519:
        raise fail("seal_invalid")  # a seal is checked only with a key of its own algorithm (ADR-109)
    issued = parse_time(b["issued_at"])
    assert issued is not None
    c = keys.valid_at(env["seal_key_id"], issued)
    if c:
        raise fail(c)
    if not open_seal(keys, env["seal_key_id"], TAG_DEED + env["body_hash"], env["seal"]):
        raise fail("seal_invalid")
    # The accept policy (DEED_SPEC.md §11.4), by issued_at only.
    if issued >= ED25519_ALONE_UNTIL:
        raise fail("seal_missing")
    if any(a != ALG_ED25519 for a in keys.active_algs(issued)):
        raise fail("seal_missing")
    raw_b = raw.encode("utf-8") if isinstance(raw, str) else raw
    canonical = raw_b == p.env_jcs.encode("utf-8")
    if strict and not canonical:
        raise fail("non_canonical")
    return DeedResult(env, b, env["body_hash"], b["seq"], b["deed_id"], b["org"], issued, env["seal_key_id"], canonical)


def computed_body_hash(raw: bytes | str) -> tuple[dict[str, Any], str] | None:
    try:
        p = parse_envelope(raw)
    except VerifyError:
        return None
    return p.env, sha256_digest(p.body_jcs)
