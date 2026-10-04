"""The strict JSON profile, a port of Go's intent.ParseStrict / ParseStrictNonNegative.

Valid UTF-8, no duplicate keys (compared after unescaping), integers only with magnitude at most
2^53-1, no lone surrogates in ``\\u`` escapes, depth at most 16, nothing after the value. With
``non_negative`` (deed bodies and key sets) a minus sign is refused too, so there are no negatives
and no -0. :func:`json.loads` is never used on deed input: it accepts duplicate keys.
"""

from __future__ import annotations

from typing import Any, Union

from ._verror import VerifyError, malformed

JsonValue = Union[None, bool, int, str, list[Any], dict[str, Any]]

MAX_SAFE_INTEGER = 9007199254740991
MAX_DEPTH = 16


class StrictError(Exception):
    """A strict-profile failure: the same message and path as Go's intent.Error."""

    def __init__(self, msg: str, path: str) -> None:
        super().__init__(f"{msg} at {path}" if path else msg)
        self.msg = msg
        self.path = path


def decode_utf8(data: bytes) -> str:
    """Decodes bytes as strict UTF-8 (a BOM is kept, as Go keeps it)."""
    try:
        return data.decode("utf-8")
    except UnicodeDecodeError:
        raise StrictError("input is not valid UTF-8", "") from None


def _well_formed(s: str) -> bool:
    return not any(0xD800 <= ord(c) <= 0xDFFF for c in s)


def parse_strict_value(data: str | bytes, non_negative: bool = False) -> JsonValue:
    """Parses one JSON value under the strict profile. Raises StrictError."""
    if isinstance(data, bytes):
        text = decode_utf8(data)
    else:
        text = data
        if not _well_formed(text):
            raise StrictError("input is not valid UTF-8", "")
    p = _Parser(text, non_negative)
    p.ws()
    v = p.value("", 0)
    p.ws()
    if p.i != len(text):
        raise StrictError("trailing data after the top-level value", "")
    return v


def malformed_from(e: BaseException) -> VerifyError:
    if isinstance(e, StrictError):
        return malformed(f"{e.msg} at {e.path}" if e.path else e.msg)
    if isinstance(e, VerifyError):
        return e
    return malformed(str(e))


def parse_strict(data: str | bytes, *, non_negative: bool = False) -> JsonValue:
    """The public API: parses or raises VerifyError('malformed')."""
    try:
        return parse_strict_value(data, non_negative)
    except StrictError as e:
        raise malformed_from(e) from None


def _join(path: str, key: str) -> str:
    return key if path == "" else f"{path}.{key}"


_WS = frozenset(" \t\n\r")
_DIGITS = frozenset("0123456789")
_HEX = frozenset("0123456789abcdefABCDEF")
_ESC = {'"': '"', "\\": "\\", "/": "/", "b": "\b", "f": "\f", "n": "\n", "r": "\r", "t": "\t"}


class _Parser:
    def __init__(self, s: str, non_neg: bool) -> None:
        self.s = s
        self.n = len(s)
        self.i = 0
        self.non_neg = non_neg

    def ws(self) -> None:
        s, n = self.s, self.n
        while self.i < n and s[self.i] in _WS:
            self.i += 1

    def value(self, path: str, depth: int) -> JsonValue:
        if self.i >= self.n:
            raise StrictError("unexpected end of input", path)
        c = self.s[self.i]
        if c == "{":
            return self.obj(path, depth + 1)
        if c == "[":
            return self.arr(path, depth + 1)
        if c == '"':
            return self.str_(path)
        if c == "-" or c in _DIGITS:
            return self.number(path)
        if c == "t":
            return self.literal(path, "true", True)
        if c == "f":
            return self.literal(path, "false", False)
        if c == "n":
            return self.literal(path, "null", None)
        raise StrictError(f"unexpected character {_go_quote(c)}", path)

    def literal(self, path: str, lit: str, v: JsonValue) -> JsonValue:
        if self.s[self.i : self.i + len(lit)] != lit:
            raise StrictError("invalid literal", path)
        self.i += len(lit)
        return v

    def obj(self, path: str, depth: int) -> dict[str, Any]:
        if depth > MAX_DEPTH:
            raise StrictError("nesting too deep", path)
        self.i += 1
        m: dict[str, Any] = {}
        self.ws()
        if self.i < self.n and self.s[self.i] == "}":
            self.i += 1
            return m
        while True:
            self.ws()
            if self.i >= self.n or self.s[self.i] != '"':
                raise StrictError("expected object key", path)
            k = self.str_(path)
            kp = _join(path, k)
            if k in m:
                raise StrictError("duplicate object key", kp)
            self.ws()
            if self.i >= self.n or self.s[self.i] != ":":
                raise StrictError("expected ':'", kp)
            self.i += 1
            self.ws()
            m[k] = self.value(kp, depth)
            self.ws()
            if self.i >= self.n:
                raise StrictError("unterminated object", path)
            c = self.s[self.i]
            if c == ",":
                self.i += 1
            elif c == "}":
                self.i += 1
                return m
            else:
                raise StrictError("expected ',' or '}'", path)

    def arr(self, path: str, depth: int) -> list[Any]:
        if depth > MAX_DEPTH:
            raise StrictError("nesting too deep", path)
        self.i += 1
        a: list[Any] = []
        self.ws()
        if self.i < self.n and self.s[self.i] == "]":
            self.i += 1
            return a
        while True:
            self.ws()
            a.append(self.value(f"{path}[{len(a)}]", depth))
            self.ws()
            if self.i >= self.n:
                raise StrictError("unterminated array", path)
            c = self.s[self.i]
            if c == ",":
                self.i += 1
            elif c == "]":
                self.i += 1
                return a
            else:
                raise StrictError("expected ',' or ']'", path)

    def str_(self, path: str) -> str:
        s, n = self.s, self.n
        self.i += 1
        out: list[str] = []
        run = self.i
        while True:
            if self.i >= n:
                raise StrictError("unterminated string", path)
            c = s[self.i]
            if c == '"':
                out.append(s[run : self.i])
                self.i += 1
                return "".join(out)
            if ord(c) < 0x20:
                raise StrictError("unescaped control character in string", path)
            if c == "\\":
                out.append(s[run : self.i])
                out.append(self.escape(path))
                run = self.i
                continue
            self.i += 1

    def escape(self, path: str) -> str:
        s = self.s
        if self.i + 1 >= self.n:
            raise StrictError("truncated escape", path)
        e = s[self.i + 1]
        self.i += 2
        if e in _ESC:
            return _ESC[e]
        if e == "u":
            r = self.hex4()
            if r < 0:
                raise StrictError("invalid \\u escape", path)
            if 0xD800 <= r < 0xDC00:
                if s[self.i : self.i + 2] != "\\u":
                    raise StrictError("high surrogate not followed by a low surrogate", path)
                self.i += 2
                r2 = self.hex4()
                if r2 < 0:
                    raise StrictError("invalid \\u escape", path)
                if r2 < 0xDC00 or r2 > 0xDFFF:
                    raise StrictError("high surrogate not followed by a low surrogate", path)
                return chr(0x10000 + ((r - 0xD800) << 10) + (r2 - 0xDC00))
            if 0xDC00 <= r <= 0xDFFF:
                raise StrictError("lone low surrogate", path)
            return chr(r)
        raise StrictError(f"invalid escape \\{e}", path)

    def hex4(self) -> int:
        h = self.s[self.i : self.i + 4]
        if len(h) < 4 or any(c not in _HEX for c in h):
            return -1
        self.i += 4
        return int(h, 16)

    def number(self, path: str) -> int:
        s, n = self.s, self.n
        neg = False
        if s[self.i] == "-":
            if self.non_neg:
                raise StrictError("negative numbers and -0 are not allowed", path)
            neg = True
            self.i += 1
        if self.i >= n or s[self.i] not in _DIGITS:
            raise StrictError("invalid number", path)
        start = self.i
        if s[self.i] == "0":
            self.i += 1
            if self.i < n and s[self.i] in _DIGITS:
                raise StrictError("leading zero in number", path)
        else:
            while self.i < n and s[self.i] in _DIGITS:
                self.i += 1
        if self.i < n and s[self.i] in ".eE":
            raise StrictError("only integers are allowed (no fraction or exponent)", path)
        d = s[start : self.i]
        if len(d) > 16:
            raise StrictError("integer magnitude exceeds 2^53-1", path)
        v = int(d)
        if v > MAX_SAFE_INTEGER:
            raise StrictError("integer magnitude exceeds 2^53-1", path)
        return -v if neg else v


def _go_quote(c: str) -> str:
    """Go's %q of the byte a parser stopped on (ASCII; a non-ASCII lead byte as Latin-1)."""
    o = ord(c)
    named = {7: "a", 8: "b", 12: "f", 10: "n", 13: "r", 9: "t", 11: "v"}
    if c == "'":
        return "'\\''"
    if c == "\\":
        return "'\\\\'"
    if 0x20 <= o < 0x7F:
        return f"'{c}'"
    if o == 0x7F:
        return "'\\x7f'"
    if o < 0x80:
        return f"'\\{named[o]}'" if o in named else f"'\\x{o:02x}'"
    return f"'{chr(c.encode('utf-8')[0])}'"
