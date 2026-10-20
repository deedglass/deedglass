"""One deed: the closed schema (sections 4, 5, 9) and the ordered checks of section 11.1."""

from __future__ import annotations

import hashlib
import re
from dataclasses import dataclass
from typing import Any

from . import ed25519, mldsa
from .canon import MAX_INT, StrictError, jcs, parse_strict
from .errors import VerifyError
from .keys import ALG_RE, IMPLEMENTED, KeySet, b64url_decode, valid_time

MAX_INPUT = 64 * 1024
MAX_CANON = {1: 16384, 2: 24576}
ED25519_ALONE_BEFORE = "2035-01-01T00:00:00.000Z"

TAG_V1 = b"deedglass/deed/v1\n"
TAGS_V2 = {"Ed25519": b"deedglass/deed/v2/ed25519\n", "ML-DSA-65": b"deedglass/deed/v2/ml-dsa-65\n"}
PREFIX = {"Ed25519": "ed25519:", "ML-DSA-65": "ml-dsa-65:"}
SEAL_CHARS = {"Ed25519": 86, "ML-DSA-65": 4412}

DIGEST = re.compile(r"sha256:[0-9a-f]{64}")
HEADER_REF = re.compile(r"[A-Za-z0-9._~:@/+=-]{1,256}")
RULE_ID = re.compile(r"[A-Za-z0-9][A-Za-z0-9._:/-]{0,127}")
ERROR_CODE = re.compile(r"[a-z][a-z0-9_]{0,63}")
METHOD = re.compile(r"[a-z0-9][a-z0-9._-]{0,63}")
CURRENCY = re.compile(r"[a-z]{3}")
ADMIN_ACTION = re.compile(r"[a-z][a-z_]{0,30}\.[a-z][a-z_]{0,30}")
REF_TYPE = re.compile(r"[a-z][a-z_]{0,31}")
REF_ID = re.compile(r"[a-z][a-z0-9_-]{0,63}")
GENERIC_SEAL = re.compile(r"[a-z0-9][a-z0-9-]{0,31}:[A-Za-z0-9_-]+")


def _id(prefix: str) -> re.Pattern[str]:
    return re.compile(prefix + r"_[0-7][0-9a-hjkmnp-tv-z]{25}")


IDS = {p: _id(p) for p in ("deed", "org", "agt", "act", "con", "pol", "pv", "apr", "usr", "adm", "sk")}


def genesis(org: str) -> str:
    return "sha256:" + hashlib.sha256(b"deedglass/genesis\n" + org.encode()).hexdigest()


def body_hash(body: Any) -> str:
    return "sha256:" + hashlib.sha256(jcs(body)).hexdigest()


# ---- schema -----------------------------------------------------------------------------------
#
# Section 11.1 check 5 names what the closed schema covers but not the order inside it; the field a
# failure names depends on it. The published vectors (the parity corpora above all) pin this order:
#
#   1. the envelope's members (unknown, then missing), then the types of its members; a v2
#      envelope's seals are checked completely here (members, types, alg, seal shape, key id);
#   2. the body's members, then the types of its members in a fixed reading order, each nested
#      object's members checked when it is read (seal_keys, agent, action, policy, approval,
#      execution, witness, cost, admin);
#   3. the body's values (patterns, enums, lengths, presence by kind) in the order of `_values`;
#   4. the envelope's values: body_hash, then (v1) seal and seal_key_id.


class _Bad(Exception):
    def __init__(self, field: str) -> None:
        self.field = field


def _is_int(v: Any) -> bool:
    return isinstance(v, int) and not isinstance(v, bool)


def _plain(s: str, lo: int, hi: int, newline: bool = False) -> bool:
    """lo..hi code points of text with no control characters (a newline only when allowed)."""
    for c in s:
        o = ord(c)
        if (o < 0x20 and not (newline and o == 0x0A)) or 0x7F <= o <= 0x9F:
            return False
    return lo <= len(s) <= hi


def _at(path: str, k: str) -> str:
    return f"{path}.{k}" if path else k


def _closed(obj: Any, path: str, keys: list[str]) -> dict[str, Any]:
    """An object closed to keys: unknown members first (lowest code point order), then missing in spec order."""
    if not isinstance(obj, dict):
        raise _Bad(path or "(top level)")
    unknown = sorted(k for k in obj if k not in keys)
    if unknown:
        raise _Bad(_at(path, unknown[0]))
    for k in keys:
        if k not in obj:
            raise _Bad(_at(path, k))
    return obj


def _s(o: dict[str, Any], path: str, k: str) -> None:
    if not isinstance(o[k], str):
        raise _Bad(_at(path, k))


def _ns(o: dict[str, Any], path: str, k: str) -> None:
    if o[k] is not None and not isinstance(o[k], str):
        raise _Bad(_at(path, k))


def _i(o: dict[str, Any], path: str, k: str) -> None:
    if not _is_int(o[k]):
        raise _Bad(_at(path, k))


def _obj(o: dict[str, Any], path: str, k: str, keys: list[str]) -> dict[str, Any] | None:
    v = o[k]
    if v is None:
        return None
    if not isinstance(v, dict):
        raise _Bad(_at(path, k))
    return _closed(v, _at(path, k), keys)


def _is_id(prefix: str, v: Any) -> bool:
    return isinstance(v, str) and IDS[prefix].fullmatch(v) is not None


def _m(rx: re.Pattern[str], v: Any) -> bool:
    return isinstance(v, str) and rx.fullmatch(v) is not None


ENVELOPE_KEYS = {1: ["object", "id", "body", "body_hash", "seal", "seal_key_id"], 2: ["object", "id", "body", "body_hash", "seals"]}
BODY_KEYS = ["v", "kind", "deed_id", "org", "seq", "prev_hash", "issued_at", "agent", "on_behalf_of", "session",
             "action", "policy", "approval", "execution", "witness", "cost", "admin", "amends"]
AGENT = ["id", "name"]
ACTION = ["id", "tool", "connector", "mode", "request_digest", "intent_hash", "intent_signed"]
POLICY = ["id", "version_id", "version_hash", "mode", "decision", "rule_id"]
APPROVAL = ["id", "outcome", "decided_by", "decided_at", "deed"]
EXECUTION = ["status", "started_at", "finished_at", "latency_ms", "result_digest", "error_code"]
WITNESS = ["level", "verdict", "method", "detail_digest"]
COST = ["amount", "currency"]
ADMIN = ["event", "action", "actor", "target", "reason", "data_digest"]
REF = ["type", "id"]


def _seal_shape(alg: str, seal: str) -> bool:
    if alg in PREFIX:
        pre = PREFIX[alg]
        rest = seal[len(pre):]
        return seal.startswith(pre) and len(rest) == SEAL_CHARS[alg] and re.fullmatch(r"[A-Za-z0-9_-]+", rest) is not None
    return GENERIC_SEAL.fullmatch(seal) is not None and seal.startswith(alg.lower() + ":")


def _envelope_types(e: dict[str, Any], version: int) -> None:
    _closed(e, "", ENVELOPE_KEYS[version])
    _s(e, "", "id")
    _s(e, "", "body_hash")
    if version == 1:
        _s(e, "", "seal")
        _s(e, "", "seal_key_id")
        return
    seals = e["seals"]
    if not isinstance(seals, list) or not 1 <= len(seals) <= 4:
        raise _Bad("seals")
    for i, x in enumerate(seals):
        q = f"seals[{i}]"
        _closed(x, q, ["alg", "seal", "seal_key_id"])
        for k in ("alg", "seal", "seal_key_id"):
            _s(x, q, k)
        if not ALG_RE.fullmatch(x["alg"]):
            raise _Bad(q + ".alg")
        if not _seal_shape(x["alg"], x["seal"]):
            raise _Bad(q + ".seal")
        if not _is_id("sk", x["seal_key_id"]):
            raise _Bad(q + ".seal_key_id")


def _body_types(b: Any, version: int) -> None:
    p = "body"
    if not isinstance(b, dict):
        raise _Bad("body")
    _closed(b, p, BODY_KEYS + (["seal_key_id"] if version == 1 else ["seal_keys"]))
    _i(b, p, "v")
    for k in ("kind", "deed_id", "org"):
        _s(b, p, k)
    _i(b, p, "seq")
    _s(b, p, "prev_hash")
    _s(b, p, "issued_at")
    for k in ("on_behalf_of", "session", "amends"):
        _ns(b, p, k)
    if version == 1:
        _s(b, p, "seal_key_id")
    else:
        sk = b["seal_keys"]
        if not isinstance(sk, list) or not 1 <= len(sk) <= 4:
            raise _Bad(p + ".seal_keys")
        for i, x in enumerate(sk):
            q = f"{p}.seal_keys[{i}]"
            _closed(x, q, ["alg", "id"])
            _s(x, q, "alg")
            _s(x, q, "id")
    a = _obj(b, p, "agent", AGENT)
    if a is not None:
        _s(a, p + ".agent", "id")
        _s(a, p + ".agent", "name")
    a = _obj(b, p, "action", ACTION)
    if a is not None:
        q = p + ".action"
        for k in ("id", "tool"):
            _s(a, q, k)
        _ns(a, q, "connector")
        _s(a, q, "mode")
        _s(a, q, "request_digest")
        _ns(a, q, "intent_hash")
        if not isinstance(a["intent_signed"], bool):
            raise _Bad(q + ".intent_signed")
    a = _obj(b, p, "policy", POLICY)
    if a is not None:
        q = p + ".policy"
        for k in ("id", "version_id", "version_hash"):
            _ns(a, q, k)
        _s(a, q, "mode")
        _s(a, q, "decision")
        _ns(a, q, "rule_id")
    a = _obj(b, p, "approval", APPROVAL)
    if a is not None:
        q = p + ".approval"
        _s(a, q, "id")
        _s(a, q, "outcome")
        _ns(a, q, "decided_at")
        _ns(a, q, "deed")
        db = a["decided_by"]
        if not isinstance(db, list):
            raise _Bad(q + ".decided_by")
        for i, u in enumerate(db):
            if not isinstance(u, str):
                raise _Bad(f"{q}.decided_by[{i}]")
    a = _obj(b, p, "execution", EXECUTION)
    if a is not None:
        q = p + ".execution"
        for k in ("status", "started_at", "finished_at"):
            _s(a, q, k)
        _i(a, q, "latency_ms")
        _ns(a, q, "result_digest")
        _ns(a, q, "error_code")
    a = _obj(b, p, "witness", WITNESS)
    if a is not None:
        q = p + ".witness"
        for k in ("level", "verdict", "method"):
            _s(a, q, k)
        _ns(a, q, "detail_digest")
    a = _obj(b, p, "cost", COST)
    if a is not None:
        _i(a, p + ".cost", "amount")
        _s(a, p + ".cost", "currency")
    a = _obj(b, p, "admin", ADMIN)
    if a is not None:
        q = p + ".admin"
        _s(a, q, "event")
        _s(a, q, "action")
        _ns(a, q, "reason")
        _ns(a, q, "data_digest")
        actor = _closed(a["actor"], q + ".actor", REF)
        _s(actor, q + ".actor", "type")
        _s(actor, q + ".actor", "id")
        t = _obj(a, q, "target", REF)
        if t is not None:
            _s(t, q + ".target", "type")
            _s(t, q + ".target", "id")


def _ref_values(r: dict[str, Any], path: str) -> None:
    if not _m(REF_TYPE, r["type"]):
        raise _Bad(path + ".type")
    if not _m(REF_ID, r["id"]):
        raise _Bad(path + ".id")


def _values(b: dict[str, Any]) -> None:
    """Section 4 and 5 values, in the order the vectors pin."""
    p = "body."
    kind = b["kind"]
    if kind not in ("action", "admin", "amendment"):
        raise _Bad(p + "kind")
    if not _is_id("deed", b["deed_id"]):
        raise _Bad(p + "deed_id")
    if not 1 <= b["seq"] <= MAX_INT:
        raise _Bad(p + "seq")
    if not _m(DIGEST, b["prev_hash"]):
        raise _Bad(p + "prev_hash")
    if not valid_time(b["issued_at"]):
        raise _Bad(p + "issued_at")
    if b["v"] == 1:
        if not _is_id("sk", b["seal_key_id"]):
            raise _Bad(p + "seal_key_id")
    else:
        sk = b["seal_keys"]
        for i, k in enumerate(sk):
            q = f"{p}seal_keys[{i}]"
            if not ALG_RE.fullmatch(k["alg"]):
                raise _Bad(q + ".alg")
            if not _is_id("sk", k["id"]):
                raise _Bad(q + ".id")
            for prev in sk[:i]:
                if prev["alg"] == k["alg"]:
                    raise _Bad(q + ".alg")
                if prev["id"] == k["id"]:
                    raise _Bad(q + ".id")
    if not _is_id("org", b["org"]):
        raise _Bad(p + "org")

    action, admin, amend = kind == "action", kind == "admin", kind == "amendment"
    ag = b["agent"]
    if ag is None:
        if not admin:
            raise _Bad(p + "agent")
    else:
        if not _is_id("agt", ag["id"]):
            raise _Bad(p + "agent.id")
        if not _plain(ag["name"], 1, 64):
            raise _Bad(p + "agent.name")
    for k in ("on_behalf_of", "session"):
        v = b[k]
        if v is None:
            continue
        if admin or not _m(HEADER_REF, v):
            raise _Bad(p + k)
    a = b["action"]
    if a is None:
        if not admin:
            raise _Bad(p + "action")
    else:
        q = p + "action."
        if admin:
            raise _Bad(p + "action")
        if not _is_id("act", a["id"]):
            raise _Bad(q + "id")
        n = len(a["tool"].encode("utf-8"))
        if n == 0 or n > 200 or not _plain(a["tool"], 1, 200):
            raise _Bad(q + "tool")
        if a["connector"] is not None and not _is_id("con", a["connector"]):
            raise _Bad(q + "connector")
        if a["mode"] not in ("gateway", "proxy"):
            raise _Bad(q + "mode")
        if not _m(DIGEST, a["request_digest"]):
            raise _Bad(q + "request_digest")
        if a["intent_hash"] is not None and not _m(DIGEST, a["intent_hash"]):
            raise _Bad(q + "intent_hash")
        if a["intent_signed"] and a["intent_hash"] is None:
            raise _Bad(q + "intent_hash")
    pol = b["policy"]
    if (pol is None and action) or (pol is not None and not action):
        raise _Bad(p + "policy")
    if pol is not None:
        q = p + "policy."
        if pol["id"] is not None and not _is_id("pol", pol["id"]):
            raise _Bad(q + "id")
        if pol["version_id"] is not None and not _is_id("pv", pol["version_id"]):
            raise _Bad(q + "version_id")
        if pol["version_hash"] is not None and not _m(DIGEST, pol["version_hash"]):
            raise _Bad(q + "version_hash")
        if pol["version_id"] is not None and pol["version_hash"] is None:
            raise _Bad(q + "version_hash")
        if pol["mode"] not in ("enforce", "shadow"):
            raise _Bad(q + "mode")
        if pol["decision"] not in ("allow", "deny", "require_approval"):
            raise _Bad(q + "decision")
        if pol["rule_id"] is not None and not _m(RULE_ID, pol["rule_id"]):
            raise _Bad(q + "rule_id")
    ap = b["approval"]
    if ap is not None:
        q = p + "approval."
        if not action:
            raise _Bad(p + "approval")
        if not _is_id("apr", ap["id"]):
            raise _Bad(q + "id")
        if ap["outcome"] not in ("approved", "denied", "expired", "cancelled"):
            raise _Bad(q + "outcome")
        db = ap["decided_by"]
        for i, u in enumerate(db):
            if not _is_id("usr", u) or (i > 0 and db[i - 1] >= u):
                raise _Bad(f"{q}decided_by[{i}]")
        if ap["outcome"] in ("approved", "denied") and not db:
            raise _Bad(q + "decided_by")
        if ap["outcome"] == "expired" and db:
            raise _Bad(q + "decided_by")
        if ap["decided_at"] is not None and not valid_time(ap["decided_at"]):
            raise _Bad(q + "decided_at")
        if ap["deed"] is not None and not _is_id("deed", ap["deed"]):
            raise _Bad(q + "deed")
    ex = b["execution"]
    if ex is not None:
        q = p + "execution."
        if not action:
            raise _Bad(p + "execution")
        if ex["status"] not in ("succeeded", "failed", "unknown"):
            raise _Bad(q + "status")
        if not valid_time(ex["started_at"]):
            raise _Bad(q + "started_at")
        if not valid_time(ex["finished_at"]) or ex["finished_at"] < ex["started_at"]:
            raise _Bad(q + "finished_at")
        if not 0 <= ex["latency_ms"] <= MAX_INT:
            raise _Bad(q + "latency_ms")
        if ex["result_digest"] is not None and not _m(DIGEST, ex["result_digest"]):
            raise _Bad(q + "result_digest")
        if ex["error_code"] is not None and not _m(ERROR_CODE, ex["error_code"]):
            raise _Bad(q + "error_code")
    w = b["witness"]
    if (w is None and amend) or (w is not None and admin):
        raise _Bad(p + "witness")
    if w is not None:
        q = p + "witness."
        if w["level"] not in ("L0", "L1", "L2"):
            raise _Bad(q + "level")
        if w["verdict"] not in ("VERIFIED", "MISMATCH", "UNVERIFIABLE"):
            raise _Bad(q + "verdict")
        if not _m(METHOD, w["method"]):
            raise _Bad(q + "method")
        if w["detail_digest"] is not None and not _m(DIGEST, w["detail_digest"]):
            raise _Bad(q + "detail_digest")
    c = b["cost"]
    if c is not None:
        if not action:
            raise _Bad(p + "cost")
        if not 0 <= c["amount"] <= MAX_INT:
            raise _Bad(p + "cost.amount")
        if not _m(CURRENCY, c["currency"]):
            raise _Bad(p + "cost.currency")
    ad = b["admin"]
    if (ad is None and admin) or (ad is not None and not admin):
        raise _Bad(p + "admin")
    if ad is not None:
        q = p + "admin."
        if not _is_id("adm", ad["event"]):
            raise _Bad(q + "event")
        if not _m(ADMIN_ACTION, ad["action"]):
            raise _Bad(q + "action")
        _ref_values(ad["actor"], q + "actor")
        if ad["target"] is not None:
            _ref_values(ad["target"], q + "target")
        if ad["reason"] is not None and not _plain(ad["reason"], 1, 280, newline=True):
            raise _Bad(q + "reason")
        if ad["data_digest"] is not None and not _m(DIGEST, ad["data_digest"]):
            raise _Bad(q + "data_digest")
    am = b["amends"]
    if (am is None and amend) or (am is not None and not amend) or (am is not None and not _is_id("deed", am)):
        raise _Bad(p + "amends")


def _envelope_values(e: dict[str, Any], version: int) -> None:
    if not _m(DIGEST, e["body_hash"]):
        raise _Bad("body_hash")
    if version == 1:
        if not _seal_shape("Ed25519", e["seal"]):
            raise _Bad("seal")
        if not _is_id("sk", e["seal_key_id"]):
            raise _Bad("seal_key_id")


# ---- the checks -------------------------------------------------------------------------------


@dataclass
class Deed:
    envelope: dict[str, Any]
    body: dict[str, Any]
    seq: int
    org: str
    deed_id: str
    issued_at: str
    body_hash: str
    kind: str
    key_ids: list[str]


def _seq_of(v: Any) -> int | None:
    """The seq a failure names: the body's, whenever it is a positive integer, even in an invalid deed."""
    if isinstance(v, dict) and isinstance(v.get("body"), dict):
        s = v["body"].get("seq")
        if _is_int(s) and s > 0:
            return s
    return None


def verify_deed(raw: bytes, keys: KeySet | None, strict: bool = False) -> Deed:
    """Runs the checks of section 11.1 in order; raises the first failure as a VerifyError."""
    if len(raw) > MAX_INPUT:
        raise VerifyError("malformed", detail="larger than 64 KiB")
    try:
        env = parse_strict(raw)
    except StrictError as e:
        raise VerifyError("malformed", detail=str(e)) from None
    if not isinstance(env, dict):
        raise VerifyError("malformed", detail="the top level is not an object")
    seq = _seq_of(env)

    def fail(code: str, **kw: Any) -> VerifyError:
        return VerifyError(code, seq=seq, **kw)

    if env.get("object") != "deed":
        raise VerifyError("schema_invalid", field="object")
    body = env.get("body")
    if isinstance(body, dict) and isinstance(env.get("id"), str) and isinstance(body.get("deed_id"), str):
        if env["id"] != body["deed_id"]:
            raise fail("id_mismatch")
    v = body.get("v") if isinstance(body, dict) else None
    if _is_int(v) and v not in (1, 2):
        raise fail("unsupported_version", v=v, detail=f"v={v}")
    version = v if _is_int(v) else 1
    try:
        _envelope_types(env, version)
        _body_types(body, version)
        _values(body)
        _envelope_values(env, version)
    except _Bad as b:
        raise fail("schema_invalid", field=b.field) from None

    canon = jcs(env)
    if len(canon) > MAX_CANON[version]:
        raise fail("malformed", detail=f"the canonical form is larger than {MAX_CANON[version]} bytes")
    bh = body_hash(body)
    if bh != env["body_hash"]:
        raise fail("body_hash_mismatch", expected=env["body_hash"], computed=bh)

    if version == 1:
        if env["seal_key_id"] != body["seal_key_id"]:
            raise fail("seal_key_mismatch")
        seals = [{"alg": "Ed25519", "seal": env["seal"], "seal_key_id": env["seal_key_id"]}]
    else:
        seals = env["seals"]
        named = {s["seal_key_id"] for s in seals}
        for sk in body["seal_keys"]:
            if sk["id"] not in named:
                raise fail("seal_missing", detail=f"{sk['alg']} {sk['id']}")
        want = [(sk["alg"], sk["id"]) for sk in body["seal_keys"]]
        got = [(s["alg"], s["seal_key_id"]) for s in seals]
        if want != got:
            raise fail("seal_key_mismatch")

    issued = body["issued_at"]
    for s in seals:
        alg, kid = s["alg"], s["seal_key_id"]
        if alg not in IMPLEMENTED:
            raise fail("unsupported_algorithm", detail=alg)
        key = keys.get(kid) if keys is not None else None
        if key is None:
            raise fail("keys_missing" if keys is None else "seal_key_unknown", key=kid)
        if key.alg != alg:
            raise fail("seal_invalid", key=kid)
        if key.revoked:
            raise fail("seal_key_revoked", key=kid)
        if not key.active_at(issued):
            raise fail("seal_key_not_valid_at", key=kid, issued_at=issued)
        sig = b64url_decode(s["seal"].split(":", 1)[1])
        msg = (TAG_V1 if version == 1 else TAGS_V2[alg]) + bh.encode()
        ok = sig is not None and (ed25519.verify if alg == "Ed25519" else mldsa.verify)(key.public_key, msg, sig)
        if not ok:
            raise fail("seal_invalid", key=kid)

    # Section 11.4, the accept policy: by issued_at only, never the verifier's clock.
    algs = {s["alg"] for s in seals}
    if not ("ML-DSA-65" in algs or ("Ed25519" in algs and issued < ED25519_ALONE_BEFORE)):
        raise fail("seal_missing", detail="no seal is accepted alone at " + issued)
    assert keys is not None
    for alg in sorted(IMPLEMENTED):
        if alg in algs:
            continue
        if any(k.alg == alg and not k.revoked and k.active_at(issued) for k in keys.keys.values()):
            raise fail("seal_missing", detail=f"the key set holds an active {alg} key")

    if strict and raw != canon:
        raise fail("non_canonical")

    return Deed(env, body, body["seq"], body["org"], body["deed_id"], issued, bh, body["kind"],
                [s["seal_key_id"] for s in seals])
