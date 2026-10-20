"""Export bundles (spec/v1/BUNDLE_FORMAT.md, unchanged in edition 2): reading and the manifest."""

from __future__ import annotations

import gzip
import hashlib
import io
import re
import tarfile
import zlib
from dataclasses import dataclass, field
from typing import Any, BinaryIO, Iterator

from . import ed25519
from .canon import StrictError, jcs, parse_strict
from .deed import DIGEST, IDS, genesis
from .errors import VerifyError
from .keys import KeySet, b64url_decode, valid_time

NAMES = ["README.txt", "keys.json", "deeds.jsonl", "manifest.json", "manifest.seal.json"]
LIMITS = {
    "README.txt": 64 << 10,
    "keys.json": 1 << 20,
    "deeds.jsonl": 8 << 30,
    "manifest.json": 64 << 10,
    "manifest.seal.json": 4 << 10,
}
TAG_BUNDLE = b"deedglass/bundle/v1\n"
HEX64 = re.compile(r"[0-9a-f]{64}")


def invalid(detail: str) -> VerifyError:
    return VerifyError("bundle_invalid", detail=detail)


class _Counted(io.RawIOBase):
    """The decompressed stream, refused once it expands past the sum of the limits (a gzip bomb)."""

    def __init__(self, src: BinaryIO, cap: int) -> None:
        self.src, self.cap, self.n = src, cap, 0

    def readable(self) -> bool:
        return True

    def readinto(self, b: Any) -> int:
        try:
            data = self.src.read(len(b))
        except (OSError, EOFError, zlib.error) as e:
            raise invalid(f"This bundle is damaged ({e}).") from None
        self.n += len(data)
        if self.n > self.cap:
            raise invalid("This bundle expands past the size limits.")
        b[: len(data)] = data
        return len(data)


@dataclass
class Observed:
    count: int = 0
    first_seq: int | None = None
    last_seq: int | None = None
    first: str | None = None
    last: str | None = None
    first_prev_hash: str | None = None
    org: str | None = None
    kinds: dict[str, int] = field(default_factory=lambda: {"action": 0, "admin": 0, "amendment": 0})
    head_seq: int | None = None
    head_id: str | None = None
    head_hash: str | None = None


class BundleReader:
    """Reads entries strictly in order and never writes anything to disk."""

    def __init__(self, f: BinaryIO) -> None:
        cap = sum(LIMITS.values()) + 64 * 1024
        self._gz = gzip.GzipFile(fileobj=f, mode="rb")
        self._raw = io.BufferedReader(_Counted(self._gz, cap), 1 << 16)  # type: ignore[arg-type]
        try:
            self._tar = tarfile.open(fileobj=self._raw, mode="r|")
        except VerifyError:
            raise
        except (tarfile.TarError, OSError, EOFError, zlib.error) as e:
            raise invalid(f"This bundle is damaged ({e}).") from None
        self._it = iter(self._tar)
        self.files: dict[str, bytes] = {}
        self.sizes: dict[str, int] = {}
        self.sha: dict[str, str] = {}
        self.deed_lines = 0
        self.readme = self._small("README.txt")
        self.keys_bytes = self._small("keys.json")

    def _member(self, want: str | None) -> tarfile.TarInfo | None:
        """The next entry, which must be `want`; manifest.seal.json is optional; None wants nothing."""
        try:
            m = next(self._it, None)
        except VerifyError:
            raise
        except (tarfile.TarError, OSError, EOFError, zlib.error) as e:
            raise invalid(f"This bundle is damaged ({e}).") from None
        if m is None:
            if want in (None, "manifest.seal.json"):
                return None
            raise invalid(f"This bundle has no {want}.")
        name = m.name
        if "/" in name or "\\" in name or ".." in name or name.startswith("."):
            raise invalid(f'This bundle contains an entry named "{name}".')
        if name not in NAMES:
            raise invalid(f'This bundle contains an unexpected entry "{name}".')
        if not m.isreg():
            raise invalid(f"{name} in this bundle is not a regular file.")
        if name in self.sizes:
            raise invalid(f"This bundle contains {name} twice.")
        if want is None:
            raise invalid(f'This bundle contains a sixth entry "{name}".')
        if name != want:
            raise invalid(f"The files in this bundle are out of order ({name}).")
        if m.size > LIMITS[name]:
            raise invalid(f"{name} in this bundle is larger than its limit.")
        self.sizes[name] = m.size
        return m

    def _read(self, m: tarfile.TarInfo) -> bytes:
        try:
            fh = self._tar.extractfile(m)
            assert fh is not None
            data = fh.read()
        except VerifyError:
            raise
        except (tarfile.TarError, OSError, EOFError, zlib.error) as e:
            raise invalid(f"This bundle is damaged ({e}).") from None
        if len(data) != m.size:
            raise invalid(f"{m.name} in this bundle is cut short.")
        return data

    def _small(self, name: str) -> bytes:
        m = self._member(name)
        assert m is not None
        data = self._read(m)
        self.files[name] = data
        self.sha[name] = hashlib.sha256(data).hexdigest()
        return data

    def deeds(self) -> Iterator[bytes]:
        """Yields each line of deeds.jsonl without its newline."""
        m = self._member("deeds.jsonl")
        assert m is not None
        try:
            fh = self._tar.extractfile(m)
            assert fh is not None
            h = hashlib.sha256()
            n = 0
            while True:
                line = fh.readline(LIMITS["deeds.jsonl"])
                if not line:
                    break
                h.update(line)
                n += len(line)
                self.deed_lines += 1
                yield line[:-1] if line.endswith(b"\n") else line
        except VerifyError:
            raise
        except (tarfile.TarError, OSError, EOFError, zlib.error) as e:
            raise invalid(f"This bundle is damaged ({e}).") from None
        if n != m.size:
            raise invalid("deeds.jsonl in this bundle is cut short.")
        self.sha["deeds.jsonl"] = h.hexdigest()

    def finish(self) -> tuple[bytes, bytes | None]:
        """Reads manifest.json and the optional manifest.seal.json; nothing may follow."""
        m = self._member("manifest.json")
        assert m is not None
        manifest = self._read(m)
        s = self._member("manifest.seal.json")
        seal = None
        if s is not None:
            seal = self._read(s)
            self._member(None)
        return manifest, seal


def _closed(o: Any, keys: list[str], what: str) -> dict[str, Any]:
    if not isinstance(o, dict) or set(o) != set(keys):
        raise invalid(f"The bundle manifest's {what} is not as the format describes.")
    return o


def _nat(v: Any) -> bool:
    return isinstance(v, int) and not isinstance(v, bool) and v >= 0


def check_manifest(raw: bytes, br: BundleReader, obs: Observed) -> dict[str, Any]:
    """Section 3 and the reader rules of section 7: canonical, closed, self-consistent, matching."""
    try:
        m = parse_strict(raw)
    except StrictError as e:
        raise invalid(f"The bundle manifest is not strict JSON ({e}).") from None
    if not isinstance(m, dict) or jcs(m) != raw:
        raise invalid("The bundle manifest is not in canonical form.")
    _closed(m, ["object", "v", "org", "issuer", "created_at", "generator", "range", "chain", "kinds", "files"], "top level")
    if m["object"] != "deed_bundle_manifest" or m["v"] != 1 or isinstance(m["v"], bool):
        raise invalid("The bundle manifest is not a v1 deed_bundle_manifest.")
    if not isinstance(m["org"], str) or not IDS["org"].fullmatch(m["org"]) or not isinstance(m["issuer"], str):
        raise invalid("The bundle manifest's org or issuer is not valid.")
    if not valid_time(m["created_at"]):
        raise invalid("The bundle manifest's created_at is not valid.")
    g = _closed(m["generator"], ["name", "version", "commit"], "generator")
    if not all(isinstance(g[k], str) for k in g):
        raise invalid("The bundle manifest's generator is not valid.")
    r = _closed(m["range"], ["first_seq", "last_seq", "count", "from", "to"], "range")
    c = _closed(m["chain"], ["anchor", "head"], "chain")
    a = _closed(c["anchor"], ["kind", "seq", "body_hash"], "anchor")
    h = _closed(c["head"], ["seq", "deed_id", "body_hash"], "head")
    k = _closed(m["kinds"], ["action", "admin", "amendment"], "kinds")
    f = _closed(m["files"], ["README.txt", "keys.json", "deeds.jsonl"], "files")
    fr = _closed(f["README.txt"], ["bytes", "sha256"], "files")
    fk = _closed(f["keys.json"], ["bytes", "sha256"], "files")
    fd = _closed(f["deeds.jsonl"], ["bytes", "sha256", "lines"], "files")
    for e in (fr, fk, fd):
        if not _nat(e["bytes"]) or not isinstance(e["sha256"], str) or not HEX64.fullmatch(e["sha256"]):
            raise invalid("The bundle manifest's file list is not valid.")
    if not _nat(fd["lines"]) or not all(_nat(k[x]) for x in k):
        raise invalid("The bundle manifest's counts are not valid.")
    if not all(_nat(r[x]) for x in ("first_seq", "last_seq", "count")) or not valid_time(r["from"]) or not valid_time(r["to"]):
        raise invalid("The bundle manifest's range is not valid.")
    if r["count"] != r["last_seq"] - r["first_seq"] + 1 or h["seq"] != r["last_seq"]:
        raise invalid("The bundle manifest's range does not add up.")
    if not isinstance(a["body_hash"], str) or not DIGEST.fullmatch(a["body_hash"]):
        raise invalid("The bundle manifest's anchor is not valid.")
    if r["first_seq"] == 1:
        if a["kind"] != "genesis" or a["seq"] != 0 or a["body_hash"] != genesis(m["org"]):
            raise invalid("The bundle manifest's anchor is not the org's genesis.")
    elif a["kind"] != "deed" or a["seq"] != r["first_seq"] - 1:
        raise invalid("The bundle manifest's anchor is not valid.")
    if sum(k.values()) != r["count"]:
        raise invalid("The bundle manifest's kinds do not add up.")

    # The files.
    for name, e in (("README.txt", fr), ("keys.json", fk), ("deeds.jsonl", fd)):
        if br.sizes.get(name) != e["bytes"] or br.sha.get(name) != e["sha256"]:
            raise invalid(f"{name} differs from the bundle manifest.")
    if br.deed_lines != fd["lines"]:
        raise invalid("deeds.jsonl holds another number of lines than the bundle manifest says.")

    # The deeds that verified.
    if (
        obs.count != r["count"]
        or obs.first_seq != r["first_seq"]
        or obs.last_seq != r["last_seq"]
        or obs.first != r["from"]
        or obs.last != r["to"]
        or obs.kinds != k
        or obs.org != m["org"]
        or obs.first_prev_hash != a["body_hash"]
        or (obs.head_seq, obs.head_id, obs.head_hash) != (h["seq"], h["deed_id"], h["body_hash"])
    ):
        raise invalid("The bundle manifest does not match the deeds in it.")
    return m


def check_manifest_seal(raw: bytes, manifest_raw: bytes, created_at: str, keys: KeySet | None) -> str:
    """Section 4: returns the sealing key id, or raises bundle_invalid / bundle_seal_invalid."""
    try:
        s = parse_strict(raw)
    except StrictError as e:
        raise invalid(f"The bundle manifest's seal is not strict JSON ({e}).") from None
    if not isinstance(s, dict) or jcs(s) != raw or set(s) != {"manifest_hash", "object", "seal", "seal_key_id"}:
        raise invalid("The bundle manifest's seal is not as the format describes.")
    if s["object"] != "deed_bundle_seal" or not isinstance(s["seal_key_id"], str) or not IDS["sk"].fullmatch(s["seal_key_id"]):
        raise invalid("The bundle manifest's seal is not as the format describes.")
    if not isinstance(s["seal"], str) or not re.fullmatch(r"ed25519:[A-Za-z0-9_-]{86}", s["seal"]):
        raise invalid("The bundle manifest's seal is not as the format describes.")
    if not isinstance(s["manifest_hash"], str) or not DIGEST.fullmatch(s["manifest_hash"]):
        raise invalid("The bundle manifest's seal is not as the format describes.")
    kid = s["seal_key_id"]
    bad = VerifyError("bundle_seal_invalid", key=kid)
    if s["manifest_hash"] != "sha256:" + hashlib.sha256(manifest_raw).hexdigest():
        raise bad
    key = keys.get(kid) if keys is not None else None
    if key is None or key.alg != "Ed25519" or key.revoked or not key.active_at(created_at):
        raise bad
    sig = b64url_decode(s["seal"][len("ed25519:"):])
    if sig is None or not ed25519.verify(key.public_key, TAG_BUNDLE + s["manifest_hash"].encode(), sig):
        raise bad
    return kid
