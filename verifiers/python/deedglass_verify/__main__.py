"""python -m deedglass_verify <bundle.tar.gz | deeds.jsonl | deed.json> [--keys keys.json] [--strict] [--json]

Exit 0 when everything verifies, 1 on a failure, 2 when it cannot decide (DEED_SPEC.md section 11.3).
"""

from __future__ import annotations

import argparse
import json
import sys

from .errors import VerifyError
from .keys import KeySet, parse_key_set
from .verifier import Report, split_lines, verify_bundle, verify_lines, verify_one


def run(path: str, keys_path: str | None, strict: bool) -> Report:
    rep = Report(needs_pinned=strict)
    keys: KeySet | None = None
    if keys_path is not None:
        with open(keys_path, "rb") as kf:
            try:
                keys = parse_key_set(kf.read())
            except VerifyError as e:
                rep.failure = e
                return rep
        rep.keys_pinned = True
    with open(path, "rb") as f:
        head = f.read(2)
        f.seek(0)
        if head == b"\x1f\x8b":
            keys = verify_bundle(rep, f, keys, strict)
        elif path.endswith(".jsonl"):
            verify_lines(rep, split_lines(f.read()), keys, strict)
        else:
            data = f.read()
            lines = split_lines(data.strip())
            if len(lines) > 1 and all(ln.strip().startswith(b"{") and ln.strip().endswith(b"}") for ln in lines[:2]):
                verify_lines(rep, lines, keys, strict)
            else:
                verify_one(rep, data, keys, strict)
    if keys is not None and keys.has_test_keys:
        rep.warnings.append({"code": "test_keys", "message": "These deeds were sealed by Deedglass's published test keys. They mean nothing outside tests."})
    return rep


def human(rep: Report, path: str) -> str:
    out = []
    if rep.failure is None and rep.result == "cannot_determine":
        out.append(f"○ {rep.intact} deeds verified against keys from the bundle itself; --strict needs the issuer's keys (--keys).")
    elif rep.failure is None:
        span = f" · {rep.frm[:10]} → {rep.to[:10]}" if rep.frm and rep.to else ""
        chain = " · chain intact" if rep.chain == "intact" else ""
        out.append(f"✓ {rep.intact} deed{'s' if rep.intact != 1 else ''}{chain} · {len(rep.keys_used)} seal key{'s' if len(rep.keys_used) != 1 else ''}{span}")
    else:
        msg, nxt = rep.failure.message()
        out.append(f"✕ {msg}")
        out.append(f"  next       {nxt}")
    b = rep.bundle
    if b and b["manifest"] == "ok":
        out.append("✓ Bundle manifest matches" + (f" · sealed by {b['sealed_by']}" if b.get("sealed_by") else ""))
    for w in rep.warnings:
        out.append(f"○ {w['message']}")
    return "\n".join(out)


def main(argv: list[str] | None = None) -> int:
    ap = argparse.ArgumentParser(prog="deedglass_verify", description="Check Deedglass deeds and export bundles offline, from the spec alone.")
    ap.add_argument("path", help="an export bundle (.tar.gz), a .jsonl chain, or one deed (.json)")
    ap.add_argument("--keys", help="the issuer's keys.json (a bundle's own keys are used when omitted)")
    ap.add_argument("--strict", action="store_true", help="also require canonical bytes (non_canonical)")
    ap.add_argument("--json", action="store_true", help="print the verification report as JSON")
    a = ap.parse_args(argv)
    try:
        rep = run(a.path, a.keys, a.strict)
    except OSError as e:
        print(f"✕ Can't read {e.filename}: {e.strerror}", file=sys.stderr)
        return 2
    print(json.dumps(rep.to_json(), indent=2, ensure_ascii=False) if a.json else human(rep, a.path))
    return rep.exit_code


if __name__ == "__main__":
    sys.exit(main())
