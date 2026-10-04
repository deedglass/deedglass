"""The shared vectors: every testdata/deeds case, the 500-deed parity corpus and the JCS pairs (S32 §3.7)."""

from __future__ import annotations

import base64
import json
from typing import Any

import pytest

from deedglass.verify import KeySet, Verifier, VerifyError, canonicalize, parse_key_set, split_lines

from .conftest import repo

KEYS = "testdata/deeds/keys/test-keys.json"


def outcome(rep: Any) -> dict[str, Any]:
    f = rep.failures[0] if rep.failures else None
    if f is None:
        return {"valid": True, "code": None, "seq": None, "field": None}
    return {"valid": False, "code": f.code, "seq": f.seq, "field": f.field}


def run(path: str, keys_path: str, strict: bool) -> dict[str, Any]:
    try:
        ks = parse_key_set(repo(keys_path).read_bytes())
    except VerifyError as e:
        return {"valid": False, "code": e.code, "seq": e.seq or None, "field": e.field or None}
    v = Verifier(ks, strict=strict, keys_pinned=True)
    data = repo(path).read_bytes()
    if path.endswith(".jsonl"):
        v.chain(split_lines(data), path)
    else:
        v.loose(data, path)
    return outcome(v.report())


EXPECTED = json.loads(repo("testdata/deeds/expected.json").read_text())


@pytest.mark.parametrize("path", [p for p in EXPECTED if not p.endswith(".cose")])
def test_expected(path: str) -> None:
    want = EXPECTED[path]
    got = run("testdata/deeds/" + path, KEYS, False)
    assert got == {k: want[k] for k in ("valid", "code", "seq", "field")}
    if want["strict_code"]:
        assert run("testdata/deeds/" + path, KEYS, True)["code"] == want["strict_code"]


INVALID = sorted(p.name for p in repo("testdata/deeds/invalid").iterdir())


def test_invalid_count() -> None:
    assert len(INVALID) == 26


@pytest.mark.parametrize("name", INVALID)
def test_invalid(name: str) -> None:
    base = repo("testdata/deeds/invalid") / name
    want = json.loads((base / "expected.json").read_text())
    f = "deed.json" if (base / "deed.json").exists() else "deeds.jsonl"
    keys = f"testdata/deeds/invalid/{name}/keys.json" if (base / "keys.json").exists() else KEYS
    got = run(f"testdata/deeds/invalid/{name}/{f}", keys, False)
    assert got == {"valid": False, "code": want["code"], "seq": want["seq"], "field": want["field"]}


def test_parity_corpus_500() -> None:
    """Python matches Go on all 500 cases: the same valid, code, seq, field and deed count."""
    cases = [json.loads(line) for line in repo("testdata/deeds/parity/corpus-500.jsonl").read_text().splitlines()]
    expected = json.loads(repo("testdata/deeds/parity/corpus-500.expected.json").read_text())
    assert len(cases) == 500
    ks = KeySet.from_json(repo(KEYS).read_bytes())
    diffs = []
    for c in cases:
        deeds = [base64.b64decode(d) for d in c["deeds"]]
        v = Verifier(ks, strict=c["strict"], keys_pinned=True)
        if c["chain"]:
            v.chain(deeds, "corpus")
        else:
            v.loose(deeds[0], "corpus")
        rep = v.report()
        got = {"case": c["case"], **outcome(rep), "deeds": rep.deeds}
        if got != expected[c["case"] - 1]:
            diffs.append((c["case"], c["kind"], got, expected[c["case"] - 1]))
    assert diffs == []


@pytest.mark.parametrize("name", sorted(p.name for p in repo("testdata/jcs/input").iterdir()))
def test_jcs_pairs(name: str) -> None:
    src = json.loads(repo(f"testdata/jcs/input/{name}").read_text())
    want = repo(f"testdata/jcs/output/{name}").read_bytes().decode("utf-8")
    assert canonicalize(src) == want


def test_genesis_20_report() -> None:
    ks = parse_key_set(repo(KEYS).read_bytes())
    v = Verifier(ks, keys_pinned=True)
    v.chain(split_lines(repo("testdata/deeds/chains/genesis-20.jsonl").read_bytes()))
    r = v.report()
    assert (r.result, r.intact, r.chain_status) == ("valid", 20, "intact")
    assert r.to_dict()["object"] == "verification_report"
    assert [w["code"] for w in r.warnings] == ["test_keys"]
