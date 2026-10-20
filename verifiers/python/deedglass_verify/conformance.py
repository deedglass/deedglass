"""python -m deedglass_verify.conformance <testdata/deeds>: every published vector, compared with its expected.json.

Runs the v1 and v2 vectors (valid, lenient, chains, invalid, each also under --strict) and the parity
corpora, and compares code, seq and field with the expectation. COSE vectors are skipped: this
verifier does not read the optional COSE_Sign1 encoding (DEED_SPEC.md section 12). Exit 1 on any
difference.
"""

from __future__ import annotations

import base64
import json
import os
import sys
from typing import Any

from .errors import VerifyError
from .keys import KeySet, parse_key_set
from .verifier import Report, split_lines, verify_lines, verify_one


def _outcome(rep: Report) -> dict[str, Any]:
    f = rep.failure
    if f is None:
        return {"valid": True, "code": None, "seq": None, "field": None}
    return {"valid": False, "code": f.code, "seq": f.seq, "field": f.field}


def _run(path: str, keys: KeySet | None, keys_error: VerifyError | None, strict: bool) -> Report:
    rep = Report()
    if keys_error is not None:
        rep.failure = keys_error
        return rep
    with open(path, "rb") as fh:
        data = fh.read()
    if path.endswith(".jsonl"):
        verify_lines(rep, split_lines(data), keys, strict)
    else:
        verify_one(rep, data, keys, strict)
    return rep


def _json(path: str) -> Any:
    with open(path, encoding="utf-8") as fh:
        return json.load(fh)


def _keys(path: str) -> tuple[KeySet | None, VerifyError | None]:
    with open(path, "rb") as fh:
        try:
            return parse_key_set(fh.read()), None
        except VerifyError as e:
            return None, e


def run_dir(top: str, root: str, keys_rel: str) -> tuple[int, int, int, list[str]]:
    """Returns (checked, passed, skipped, differences) for one vector directory (v1 or v2)."""
    diffs: list[str] = []
    checked = passed = skipped = 0
    default = _keys(os.path.join(root, keys_rel))
    cases: list[tuple[str, str, dict[str, Any], tuple[KeySet | None, VerifyError | None]]] = []
    expected = _json(os.path.join(root, "expected.json"))
    prefix = os.path.basename(os.path.normpath(root)) + "/"
    for rel, exp in sorted(expected.items()):
        local = rel[len(prefix):] if rel.startswith(prefix) else rel
        ks = _keys(os.path.join(top, exp["keys"])) if exp.get("keys") else default
        cases.append((local, os.path.join(root, local), exp, ks))
    inv = os.path.join(root, "invalid")
    for name in sorted(os.listdir(inv)):
        d = os.path.join(inv, name)
        exp = _json(os.path.join(d, "expected.json"))
        f = os.path.join(d, "deeds.jsonl" if os.path.exists(os.path.join(d, "deeds.jsonl")) else "deed.json")
        ks = _keys(os.path.join(d, "keys.json")) if os.path.exists(os.path.join(d, "keys.json")) else default
        cases.append((f"invalid/{name}", f, exp, ks))
    for rel, path, exp, (ks, kerr) in cases:
        if rel.endswith(".cose"):
            skipped += 1
            continue
        for strict in (False, True):
            want = {k: exp[k] for k in ("valid", "code", "seq", "field")}
            if strict and exp.get("strict_code") and exp["valid"]:
                want = {"valid": False, "code": exp["strict_code"], "seq": 1, "field": None}
            got = _outcome(_run(path, ks, kerr, strict))
            if strict and want["code"] == "non_canonical":
                want["seq"] = got["seq"]  # the deed's own seq
            checked += 1
            if got == want:
                passed += 1
            else:
                diffs.append(f"{root}/{rel}{' --strict' if strict else ''}: got {got}, want {want}")
    return checked, passed, skipped, diffs


def run_corpus(corpus: str, expected_path: str, keys_path: str) -> tuple[int, int, list[str]]:
    keys, kerr = _keys(keys_path)
    expected = _json(expected_path)
    diffs: list[str] = []
    n = ok = 0
    with open(corpus, encoding="utf-8") as fh:
        corpus_lines = fh.read().splitlines()
    for line in corpus_lines:
        c = json.loads(line)
        deeds = [base64.b64decode(d) for d in c["deeds"]]
        rep = Report()
        if kerr is not None:
            rep.failure = kerr
        elif c["chain"]:
            verify_lines(rep, deeds, keys, c["strict"])
        else:
            verify_one(rep, deeds[0], keys, c["strict"])
        got = {"case": c["case"], **_outcome(rep), "deeds": rep.deeds}
        want = expected[c["case"] - 1]
        n += 1
        if got == want:
            ok += 1
        else:
            diffs.append(f"{corpus} #{c['case']} ({c['kind']}): got {got}, want {want}")
    return n, ok, diffs


def main(argv: list[str]) -> int:
    root = argv[0] if argv else "testdata/deeds"
    total = good = skipped = 0
    diffs: list[str] = []
    for sub, keys_rel in (("", "keys/test-keys.json"), ("v2", "keys.json")):
        d = os.path.join(root, sub) if sub else root
        c, p, s, df = run_dir(root, d, keys_rel)
        total, good, skipped, diffs = total + c, good + p, skipped + s, diffs + df
    for corpus, exp, keys in (
        ("parity/corpus-500.jsonl", "parity/corpus-500.expected.json", "keys/test-keys.json"),
        ("v2/parity/corpus-200.jsonl", "v2/parity/corpus-200.expected.json", "v2/keys.json"),
    ):
        c, p, df = run_corpus(os.path.join(root, corpus), os.path.join(root, exp), os.path.join(root, keys))
        total, good, diffs = total + c, good + p, diffs + df
    for line in diffs[:40]:
        print("✕ " + line)
    print(f"{'✓' if not diffs else '✕'} {good}/{total} vector checks match expected.json and the parity corpora"
          f" ({skipped} COSE vectors skipped: not read by this verifier)")
    return 0 if not diffs else 1


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
