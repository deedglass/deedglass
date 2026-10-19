"""Deed v2 (ADR-109) in the Python verifier: key sets that list other algorithms still verify the
Ed25519 deeds; v2 deeds are unsupported_version (cannot decide); the v1 accept policy holds."""

from __future__ import annotations

import json

from deedglass.verify import Verifier, parse_key_set, split_lines
from deedglass.verify._report import cannot_determine

from .conftest import repo

V2KEYS = "testdata/deeds/v2/keys.json"


def first_failure(path: str) -> tuple[str | None, int | None]:
    ks = parse_key_set(repo(V2KEYS).read_bytes())
    v = Verifier(ks, keys_pinned=True)
    data = repo(path).read_bytes()
    if path.endswith(".jsonl"):
        v.chain(split_lines(data), path)
    else:
        v.loose(data, path)
    f = v.report().failures
    return (f[0].code, f[0].seq) if f else (None, None)


def test_key_set_with_unknown_algorithm_is_kept() -> None:
    ks = parse_key_set(repo(V2KEYS).read_bytes())
    algs = {k.alg for k in ks.keys}
    assert "SLH-DSA-SHA2-128s" in algs and "Ed25519" in algs


def test_v1_deeds_before_v2_verify_and_v2_cannot_be_decided() -> None:
    assert first_failure("testdata/deeds/v2/valid/chain-v1-then-v2.jsonl") == ("unsupported_version", 5)
    assert cannot_determine("unsupported_version") and cannot_determine("unsupported_algorithm")


def test_ed25519_alone_after_2035_is_seal_missing() -> None:
    assert first_failure("testdata/deeds/v2/invalid/ed25519-alone-2035-v1/deed.json")[0] == "seal_missing"


def test_v1_deed_after_a_pq_key_is_active_is_seal_missing() -> None:
    raw = json.loads(repo(V2KEYS).read_text())
    x = next(k for k in raw["keys"] if k["alg"] == "SLH-DSA-SHA2-128s")
    x["alg"] = "ML-DSA-65"  # a spec-registered algorithm this verifier can't check: still required
    x["jwk"] = {"kty": "AKP", "alg": "ML-DSA-65", "pub": x["public_key"], "kid": x["id"], "use": "sig"}
    ks = parse_key_set(json.dumps(raw))
    v = Verifier(ks, keys_pinned=True)
    v.loose(repo("testdata/deeds/valid/01-action-verified-l2.json").read_bytes(), "deed")
    f = v.report().failures
    assert f and f[0].code == "seal_missing"
