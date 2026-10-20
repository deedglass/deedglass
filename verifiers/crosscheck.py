#!/usr/bin/env python3
"""TY-10: the independent verifiers must reach the same verdict as `deedglass verify`.

    python3 verifiers/crosscheck.py <deedglass binary> [--repo .]

For every release bundle in testdata/release-bundles (the 2046 test), a set of tampered copies of
each, and every deed vector file in testdata/deeds, it runs three verifiers and compares verdicts:

    deedglass verify <bundle> [--keys k] [--strict] --json        the Go reference
    python3 -m deedglass_verify <bundle> [--keys k] [--strict] --json
    node verifiers/js/deedglass-verify.mjs <bundle> [--keys k] [--strict] --json

A verdict is the result (valid, invalid, cannot_determine), the first failure's code, seq and
field, and the bundle's manifest and manifest seal status. One class is compared more loosely: a
damaged gzip stream (a flipped or missing compressed byte). Where a damaged deflate stream is
noticed depends on how far each reader has buffered (a CRC mismatch shows only at the end of the
member), so the three must all refuse it (a non-zero exit) but may name different first failures. Then both verifiers run every deed
vector against its expected.json. Exit 1 on any difference. Standard library only; the tampered
copies are written to a temporary directory and removed.
"""

from __future__ import annotations

import argparse
import gzip
import io
import json
import os
import subprocess
import sys
import tarfile
import tempfile
from typing import Any, Callable

# ---- tampered copies -----------------------------------------------------------------------------

Entries = list[tuple[str, bytes, int]]  # (name, data, type)


def read_entries(path: str) -> tuple[Entries, int]:
    with tarfile.open(path, "r:gz") as t:
        out: Entries = []
        mtime = 0
        for m in t:
            f = t.extractfile(m)
            out.append((m.name, f.read() if f else b"", ord(m.type)))
            mtime = int(m.mtime)
    return out, mtime


def write_bundle(entries: Entries, mtime: int, path: str) -> None:
    buf = io.BytesIO()
    with tarfile.open(fileobj=buf, mode="w", format=tarfile.USTAR_FORMAT) as t:
        for name, data, typ in entries:
            ti = tarfile.TarInfo(name)
            ti.size = len(data) if typ == ord(tarfile.REGTYPE) else 0
            ti.mtime, ti.mode, ti.type = mtime, 0o644, bytes([typ])
            if typ == ord(tarfile.SYMTYPE):
                ti.linkname = "keys.json"
            t.addfile(ti, io.BytesIO(data) if ti.size else None)
    with open(path, "wb") as f:
        f.write(gzip.compress(buf.getvalue(), mtime=0))


def _get(es: Entries, name: str) -> bytes:
    return next(d for n, d, _ in es if n == name)


def _set(es: Entries, name: str, data: bytes) -> Entries:
    return [(n, data if n == name else d, t) for n, d, t in es]


def _lines(es: Entries) -> list[bytes]:
    return _get(es, "deeds.jsonl").split(b"\n")[:-1]


def _with_lines(es: Entries, lines: list[bytes]) -> Entries:
    return _set(es, "deeds.jsonl", b"".join(ln + b"\n" for ln in lines))


def _flip_char(b: bytes, at: int) -> bytes:
    c = b[at:at + 1]
    return b[:at] + (b"B" if c != b"B" else b"C") + b[at + 1:]


REG = ord(tarfile.REGTYPE)

# name -> (entries -> entries); raw-byte tampering is handled separately.
TAMPER: dict[str, Callable[[Entries], Entries]] = {
    "deed-edited": lambda es: _with_lines(es, _edit_first_verified(_lines(es))),
    "deed-seal-flipped": lambda es: _with_lines(es, [_flip_char(ln, ln.index(b'"seal":"ed25519:') + 20) if i == 4 else ln for i, ln in enumerate(_lines(es))]),
    "last-deed-removed": lambda es: _with_lines(es, _lines(es)[:-1]),
    "middle-deed-removed": lambda es: _with_lines(es, [ln for i, ln in enumerate(_lines(es)) if i != 5]),
    "deeds-swapped": lambda es: _with_lines(es, (lambda L: L[:3] + [L[4], L[3]] + L[5:])(_lines(es))),
    "deed-duplicated": lambda es: _with_lines(es, (lambda L: L[:4] + [L[3]] + L[4:])(_lines(es))),
    "blank-line": lambda es: _set(es, "deeds.jsonl", _get(es, "deeds.jsonl") + b"\n"),
    "manifest-seal-removed": lambda es: [e for e in es if e[0] != "manifest.seal.json"],
    "manifest-seal-flipped": lambda es: _set(es, "manifest.seal.json", _flip_char(_get(es, "manifest.seal.json"), _get(es, "manifest.seal.json").index(b"ed25519:") + 12)),
    "manifest-hash-in-seal-changed": lambda es: _set(es, "manifest.seal.json", _flip_hex_in_seal(_get(es, "manifest.seal.json"))),
    "manifest-pretty": lambda es: _set(es, "manifest.json", json.dumps(json.loads(_get(es, "manifest.json")), indent=2).encode()),
    "manifest-head-changed-unsealed": lambda es: [e for e in _set(es, "manifest.json", _bump_head(_get(es, "manifest.json"))) if e[0] != "manifest.seal.json"],
    "readme-edited": lambda es: _set(es, "README.txt", _get(es, "README.txt").replace(b"Deedglass", b"Deedglas5", 1)),
    "keys-edited": lambda es: _set(es, "keys.json", _get(es, "keys.json") + b" "),
    "entry-traversal": lambda es: es + [("../../.ssh/authorized_keys", b"x", REG)],
    "entry-extra-file": lambda es: es + [("notes.txt", b"x", REG)],
    "entries-reordered": lambda es: [es[1], es[0]] + es[2:],
    "entry-duplicated": lambda es: es[:3] + [es[2]] + es[3:],
    "manifest-missing": lambda es: [e for e in es if e[0] not in ("manifest.json", "manifest.seal.json")],
    "readme-symlink": lambda es: [("README.txt", b"", ord(tarfile.SYMTYPE))] + es[1:],
}


def _edit_first_verified(lines: list[bytes]) -> list[bytes]:
    i = next(i for i, ln in enumerate(lines) if b'"verdict":"VERIFIED"' in ln)
    return lines[:i] + [lines[i].replace(b'"verdict":"VERIFIED"', b'"verdict":"MISMATCH"', 1)] + lines[i + 1:]


def _flip_hex_in_seal(raw: bytes) -> bytes:
    i = raw.index(b'"manifest_hash":"sha256:') + len(b'"manifest_hash":"sha256:')
    c = raw[i:i + 1]
    return raw[:i] + (b"0" if c != b"0" else b"1") + raw[i + 1:]


def _bump_head(raw: bytes) -> bytes:
    m = json.loads(raw)
    h = m["chain"]["head"]["body_hash"]
    m["chain"]["head"]["body_hash"] = h[:-1] + ("0" if h[-1] != "0" else "1")
    return json.dumps(m, separators=(",", ":"), sort_keys=True, ensure_ascii=False).encode()


Case = tuple[str, str, "str | None", bool, bool]


def make_cases(repo: str, tmp: str) -> list[Case]:
    """(label, bundle path, keys path or None, strict, damaged stream) for every bundle and tampered copy."""
    rb = os.path.join(repo, "testdata", "release-bundles")
    versions = sorted((d for d in os.listdir(rb) if os.path.isdir(os.path.join(rb, d))), key=lambda v: tuple(map(int, v.split("."))))
    cases: list[Case] = []
    for v in versions:
        bundle = os.path.join(rb, v, "bundle.tar.gz")
        keys = os.path.join(rb, v, "keys.json")
        cases += [
            (f"v{v}", bundle, keys, False, False),
            (f"v{v} --strict", bundle, keys, True, False),
            (f"v{v} (bundle's own keys)", bundle, None, False, False),
            (f"v{v} (bundle's own keys) --strict", bundle, None, True, False),
        ]
        raw = open(bundle, "rb").read()
        for label, data in (
            ("gzip-byte-flipped", raw[:200] + bytes([raw[200] ^ 0x01]) + raw[201:]),
            ("gzip-truncated", raw[: len(raw) // 2]),
        ):
            p = os.path.join(tmp, f"{v}-{label}.tar.gz")
            open(p, "wb").write(data)
            cases.append((f"v{v} {label}", p, keys, True, True))
        es, mtime = read_entries(bundle)
        for label, fn in TAMPER.items():
            p = os.path.join(tmp, f"{v}-{label}.tar.gz")
            write_bundle(fn(es), mtime, p)
            cases.append((f"v{v} {label}", p, keys, True, False))
        # A key set without the key that seals deeds 11 and 12 and the manifest.
        ks = json.loads(open(keys, "rb").read())
        ks["keys"] = [k for k in ks["keys"] if k["status"] != "active"]
        p = os.path.join(tmp, f"{v}-keys-without-b.json")
        open(p, "w").write(json.dumps(ks))
        cases.append((f"v{v} keys without the active key", bundle, p, True, False))
    cases += stamped_cases(repo, tmp)
    return cases


def stamped_cases(repo: str, tmp: str) -> list[Case]:
    """TY-03: a bundle with timestamps.json (testdata/timestamps). The independent verifiers check
    the file against the manifest but skip the timestamps themselves, so only bundle-level verdicts
    are compared; deedglass verify alone checks tokens and inclusion proofs (cmd/deedglass tests)."""
    bundle = os.path.join(repo, "testdata", "timestamps", "stamped.tar.gz")
    keys = os.path.join(repo, "testdata", "deeds", "keys", "test-keys.json")
    cases: list[Case] = [
        ("timestamps stamped", bundle, keys, False, False),
        ("timestamps stamped --strict", bundle, keys, True, False),
        ("timestamps stamped (bundle's own keys)", bundle, None, False, False),
    ]
    es, mtime = read_entries(bundle)
    for label, fn in {
        "timestamps-edited": lambda es: _set(es, "timestamps.json", _get(es, "timestamps.json") + b" "),
        "timestamps-removed": lambda es: [e for e in es if e[0] != "timestamps.json"],
        "timestamps-after-deeds": lambda es: [e for e in es if e[0] != "timestamps.json"][:3] + [e for e in es if e[0] == "timestamps.json"] + [e for e in es if e[0] != "timestamps.json"][3:],
        "timestamps-twice": lambda es: es[:3] + [es[2]] + es[3:],
        "timestamps-unlisted-unsealed": lambda es: [e for e in _set(es, "manifest.json", _unlist_timestamps(_get(es, "manifest.json"))) if e[0] != "manifest.seal.json"],
    }.items():
        p = os.path.join(tmp, f"stamped-{label}.tar.gz")
        write_bundle(fn(es), mtime, p)
        cases.append((f"timestamps {label}", p, keys, True, False))
    return cases


def _unlist_timestamps(raw: bytes) -> bytes:
    m = json.loads(raw)
    del m["files"]["timestamps.json"]
    return json.dumps(m, separators=(",", ":"), sort_keys=True, ensure_ascii=False).encode()


def vector_cases(repo: str) -> list[Case]:
    """Every deed vector file (not COSE) with its key set, plain and --strict."""
    root = os.path.join(repo, "testdata", "deeds")
    cases: list[Case] = []
    for sub, default in (("", "keys/test-keys.json"), ("v2", "keys.json")):
        d = os.path.join(root, sub)
        exp = json.load(open(os.path.join(d, "expected.json")))
        for rel, e in sorted(exp.items()):
            local = rel[len(sub) + 1:] if sub and rel.startswith(sub + "/") else rel
            if local.endswith(".cose"):
                continue
            keys = os.path.join(root, e["keys"]) if e.get("keys") else os.path.join(d, default)
            for strict in (False, True):
                cases.append((f"{os.path.join(sub, local)}{' --strict' if strict else ''}", os.path.join(d, local), keys, strict, False))
        for name in sorted(os.listdir(os.path.join(d, "invalid"))):
            v = os.path.join(d, "invalid", name)
            f = os.path.join(v, "deeds.jsonl") if os.path.exists(os.path.join(v, "deeds.jsonl")) else os.path.join(v, "deed.json")
            keys = os.path.join(v, "keys.json") if os.path.exists(os.path.join(v, "keys.json")) else os.path.join(d, default)
            cases.append((os.path.join(sub, "invalid", name), f, keys, False, False))
    return cases


# ---- running the verifiers -------------------------------------------------------------------------


def verdict(cmd: list[str], cwd: str | None = None, env: dict[str, str] | None = None) -> dict[str, Any]:
    p = subprocess.run(cmd, capture_output=True, text=True, cwd=cwd, env=env, timeout=300)
    try:
        r = json.loads(p.stdout)
    except json.JSONDecodeError:
        try:  # deedglass's error envelope (for example a key set it refuses) on stderr
            e = json.loads(p.stderr)["error"]
        except (json.JSONDecodeError, KeyError, TypeError):
            return {"exit": p.returncode, "error": (p.stderr or p.stdout).strip().splitlines()[-1:] or ["no output"]}
        res = {1: "invalid", 2: "cannot_determine"}.get(p.returncode, "error")
        return {"exit": p.returncode, "result": res, "code": e.get("code"), "seq": None, "field": None, "manifest": None, "manifest_seal": None}
    f = r["failures"][0] if r.get("failures") else {}
    b = r.get("bundle") or {}
    return {
        "exit": p.returncode,
        "result": r["result"],
        "code": f.get("code"),
        "seq": f.get("seq"),
        "field": f.get("field"),
        "manifest": b.get("manifest"),
        "manifest_seal": b.get("manifest_seal"),
    }


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("deedglass", help="the deedglass binary to compare with")
    ap.add_argument("--repo", default=os.path.join(os.path.dirname(os.path.abspath(__file__)), ".."))
    a = ap.parse_args()
    repo = os.path.abspath(a.repo)
    py_env = dict(os.environ, PYTHONPATH=os.path.join(repo, "verifiers", "python"))
    js = os.path.join(repo, "verifiers", "js", "deedglass-verify.mjs")
    diffs = 0
    with tempfile.TemporaryDirectory() as tmp:
        cases = make_cases(repo, tmp)
        nb = len(cases)
        cases += vector_cases(repo)
        for label, bundle, keys, strict, damaged in cases:
            extra = (["--keys", keys] if keys else []) + (["--strict"] if strict else []) + ["--json"]
            go = verdict([os.path.abspath(a.deedglass), "verify", bundle, *extra])
            py = verdict([sys.executable, "-m", "deedglass_verify", bundle, *extra], env=py_env)
            nd = verdict(["node", js, bundle, *extra])
            if damaged:
                same = go["exit"] != 0 and py["exit"] != 0 and nd["exit"] != 0
            else:
                same = go == py == nd
            diffs += not same
            mark = "✓" if same else "✕"
            what = go.get("code") or go.get("result") or "deedglass: " + str(go.get("error"))[:60]
            if damaged and same:
                what = f"refused by all three ({go.get('code') or 'exit ' + str(go['exit'])} / {py.get('code')} / {nd.get('code')})"
            print(f"{mark} {label:56} {what}")
            if not same:
                print(f"    deedglass  {go}\n    python     {py}\n    js         {nd}")
    print()
    for name, cmd, env in (
        ("python vectors", [sys.executable, "-m", "deedglass_verify.conformance", os.path.join(repo, "testdata", "deeds")], py_env),
        ("js vectors", ["node", js, "--vectors", os.path.join(repo, "testdata", "deeds")], None),
    ):
        p = subprocess.run(cmd, capture_output=True, text=True, env=env, timeout=600)
        print(p.stdout.strip().splitlines()[-1] if p.stdout.strip() else f"✕ {name}: {p.stderr.strip()}")
        if p.returncode != 0:
            print(p.stdout)
            diffs += 1
    n = len(cases)
    print(f"\n{'✓' if diffs == 0 else '✕'} {n - min(diffs, n)}/{n} cases agree across deedglass verify, the Python verifier and the JS verifier"
          f" ({nb} release-bundle cases, {n - nb} deed-vector files)")
    return 0 if diffs == 0 else 1


if __name__ == "__main__":
    sys.exit(main())
