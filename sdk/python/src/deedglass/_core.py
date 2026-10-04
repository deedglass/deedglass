"""The request core (S32 §3.2): pure request building, response parsing and the retry policy.

Every operation is a generator that yields ``httpx.Request`` (send it) or :class:`Sleep` (wait),
receives a :class:`Resp` (or has a transport error thrown into it), and returns its result. The
sync and async clients only drive these generators, so both send byte-identical requests.

Retried: connection errors, 408, 409 ``idempotency_key_in_use``, 429 ``rate_limited`` (never
``budget_exceeded``, which shares the status), 502, 503 and 504, with a backoff of 0.5 s × 2^n ±25%,
honouring ``Retry-After`` up to 10 s. A POST is retried only with the same Idempotency-Key, so the
server never runs a tool twice. Policy results are never retried.
"""

from __future__ import annotations

import json
import logging
import os
import platform
import random
import re
from collections.abc import Callable, Generator, Mapping
from dataclasses import dataclass, field
from email.utils import parsedate_to_datetime
from time import time as _now
from typing import Any, TypeVar, Union

import httpx

from . import _copy
from ._errors import APIConnectionError, APIError, APITimeoutError, AuthenticationError, DeedglassError, from_envelope
from ._types import Action
from ._version import API_VERSION, __version__

log = logging.getLogger("deedglass")

T = TypeVar("T")

API_KEY = re.compile(r"^dg_(live|test)_[0-9A-Za-z]{8}_[0-9A-Za-z]{32}\Z")
TOOL_NAME = re.compile(r"^[a-z][a-z0-9-]{1,31}\.[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}\Z")
REF = re.compile(r"^[A-Za-z0-9._~:@/+=-]{1,256}\Z")
CALL_ID = re.compile(r"^[\x21-\x7e]{1,128}\Z")
DEFAULT_BASE_URL = "http://127.0.0.1:8080"
MAX_BODY = 8 << 20
MAX_RETRY_AFTER = 10.0
RETRY_STATUSES = frozenset({408, 409, 429, 502, 503, 504})
_B32 = "0123456789abcdefghjkmnpqrstvwxyz"


@dataclass(frozen=True)
class Sleep:
    seconds: float


@dataclass(frozen=True)
class Resp:
    status: int
    headers: httpx.Headers
    body: bytes


class BodyTooLarge(Exception):
    """Thrown into an operation when the answer exceeded 8 MiB."""


@dataclass(frozen=True)
class Meta:
    request_id: str | None
    replayed: bool
    status: int


Step = Union[httpx.Request, Sleep]
Op = Generator[Step, Union[Resp, None], T]


def new_idempotency_key(rand: Callable[[int], bytes] = os.urandom) -> str:
    """'idk_' + 26 random lowercase Crockford base32 characters: one per logical call, reused by retries."""
    n = int.from_bytes(rand(17), "big")
    return "idk_" + "".join(_B32[(n >> (5 * i)) & 31] for i in range(26))


def mask_key(k: str) -> str:
    """'dg_test_ab12cd34_••••': the prefix only, never the secret."""
    m = re.match(r"^(dg_(?:live|test)_[0-9A-Za-z]{8}_)", k)
    return (m.group(1) if m else "dg_") + "••••"


def retry_after(h: httpx.Headers, now: float | None = None) -> float:
    v = h.get("retry-after")
    if not v:
        return 0.0
    try:
        return max(0.0, float(v))
    except ValueError:
        pass
    try:
        at = parsedate_to_datetime(v).timestamp()
    except (TypeError, ValueError):
        return 0.0
    return float(max(0.0, at - (now if now is not None else _now())))


def backoff(attempt: int, rand: Callable[[], float] = random.random) -> float:
    return 0.5 * (1 << attempt) * (0.75 + rand() * 0.5)


def user_agent(app_info: str | None = None) -> str:
    ua = f"deedglass-python/{__version__} (python {platform.python_version()}; httpx {httpx.__version__})"
    return f"{ua} {app_info}" if app_info else ua


@dataclass
class Core:
    """Everything a request needs except the transport."""

    api_key: str
    base_url: str = DEFAULT_BASE_URL
    timeout: float = 30.0
    max_retries: int = 2
    api_version: str = API_VERSION
    on_behalf_of: str | None = None
    session: str | None = None
    app_info: str | None = None
    rand: Callable[[], float] = field(default=random.random, repr=False)

    @classmethod
    def create(
        cls,
        api_key: str | None,
        base_url: str | None,
        timeout: float,
        max_retries: int,
        api_version: str,
        on_behalf_of: str | None,
        session: str | None,
        app_info: str | None,
    ) -> Core:
        key = api_key if api_key is not None else os.environ.get("DEEDGLASS_API_KEY", "")
        if not key:
            raise AuthenticationError(_copy.MISSING_API_KEY, type="authentication_error", code="invalid_api_key")
        if not API_KEY.match(key):
            raise AuthenticationError(_copy.INVALID_API_KEY, type="authentication_error", code="invalid_api_key")
        url = base_url or os.environ.get("DEEDGLASS_SERVER") or DEFAULT_BASE_URL
        for name, v in (("on_behalf_of", on_behalf_of), ("session", session)):
            if v is not None and not REF.match(v):
                raise DeedglassError(_copy.bad_ref(name), code="validation_failed", param=name)
        return cls(key, url.rstrip("/"), timeout, max_retries, api_version, on_behalf_of, session, app_info)

    def __repr__(self) -> str:
        return f"Core(base_url={self.base_url!r}, api_key={mask_key(self.api_key)!r})"

    # --- building ---

    def build(
        self,
        method: str,
        path: str,
        *,
        query: Mapping[str, Any] | None = None,
        body: Any = None,
        headers: Mapping[str, str | None] | None = None,
        idempotency_key: str | None = None,
        read_timeout: float | None = None,
    ) -> httpx.Request:
        h: dict[str, str] = {
            "Authorization": "Bearer " + self.api_key,
            "Deedglass-Version": self.api_version,
            "User-Agent": user_agent(self.app_info),
            "Accept": "application/json",
        }
        if method == "POST":
            h["Idempotency-Key"] = idempotency_key or new_idempotency_key()
        if self.on_behalf_of:
            h["Deedglass-On-Behalf-Of"] = self.on_behalf_of
        if self.session:
            h["Deedglass-Session"] = self.session
        for k, v in (headers or {}).items():
            if v is not None:
                h[k] = v
        content: bytes | None = None
        if body is not None:
            content = json.dumps(body, separators=(",", ":"), ensure_ascii=False, allow_nan=False).encode("utf-8")
            h["Content-Type"] = "application/json"
        params: list[tuple[str, str | int | float | bool | None]] = []
        for k, v in (query or {}).items():
            if v is None:
                continue
            for x in v if isinstance(v, (list, tuple)) else [v]:
                params.append((k, "true" if x is True else "false" if x is False else str(x)))
        read = read_timeout if read_timeout is not None else self.timeout
        t = httpx.Timeout(read, connect=min(5.0, read))
        return httpx.Request(
            method,
            self.base_url + path,
            params=params,
            headers=h,
            content=content,
            extensions={"timeout": t.as_dict()},
        )

    # --- the operation ---

    def request(
        self,
        method: str,
        path: str,
        *,
        query: Mapping[str, Any] | None = None,
        body: Any = None,
        headers: Mapping[str, str | None] | None = None,
        idempotency_key: str | None = None,
        read_timeout: float | None = None,
        action_id: str | None = None,
        raw: bool = False,
    ) -> Op[tuple[Any, Meta]]:
        """One API call with retries. Returns (parsed JSON or bytes, Meta); raises typed errors."""
        req = self.build(
            method,
            path,
            query=query,
            body=body,
            headers=headers,
            idempotency_key=idempotency_key,
            read_timeout=read_timeout,
        )
        rid_seen: str | None = None
        attempt = 0
        while True:
            try:
                resp = yield req
            except httpx.TimeoutException as e:
                if attempt < self.max_retries and method == "GET":
                    yield Sleep(backoff(attempt, self.rand))
                    attempt += 1
                    continue
                read = read_timeout if read_timeout is not None else self.timeout
                raise APITimeoutError(
                    _copy.timeout(read, action_id), code="timeout", action_id=action_id, request_id=rid_seen
                ) from e
            except httpx.TransportError as e:
                if attempt < self.max_retries:
                    log.debug("deedglass: retrying %s %s after %s", method, path, type(e).__name__)
                    yield Sleep(backoff(attempt, self.rand))
                    attempt += 1
                    continue
                raise APIConnectionError(_copy.unreachable(self.base_url), code="connection_error") from e
            except BodyTooLarge:
                raise APIError(_copy.TOO_LARGE, type="api_error", code="response_too_large") from None
            assert resp is not None
            rid = resp.headers.get("deedglass-request-id")
            rid_seen = rid or rid_seen
            meta = Meta(rid, resp.headers.get("deedglass-idempotent-replayed") == "true", resp.status)
            if 200 <= resp.status < 300:
                if raw:
                    return resp.body, meta
                try:
                    data = json.loads(resp.body) if resp.body else None
                except (ValueError, UnicodeDecodeError):
                    data = None
                if not isinstance(data, (dict, list)):
                    raise APIError(
                        _copy.unreadable(resp.status),
                        type="api_error",
                        code="unreadable_response",
                        status=resp.status,
                        request_id=rid,
                    )
                return data, meta
            err = self._error(resp, rid)
            retryable = resp.status in RETRY_STATUSES and attempt < self.max_retries
            if resp.status == 409:
                retryable = retryable and err.code == "idempotency_key_in_use"
            if resp.status == 429:
                retryable = retryable and err.code == "rate_limited"
            if not retryable:
                raise err
            wait = backoff(attempt, self.rand)
            ra = retry_after(resp.headers)
            if ra > 0:
                wait = min(ra, MAX_RETRY_AFTER)
            log.debug("deedglass: retrying %s %s after %s (%s)", method, path, resp.status, rid)
            yield Sleep(wait)
            attempt += 1

    def _error(self, resp: Resp, rid: str | None) -> DeedglassError:
        ra = retry_after(resp.headers)
        try:
            parsed = json.loads(resp.body) if resp.body else None
        except (ValueError, UnicodeDecodeError):
            parsed = None
        if not isinstance(parsed, dict) or not isinstance(parsed.get("error"), dict):
            return from_envelope(resp.status, {"error": {"message": _copy.unreadable(resp.status)}}, rid, ra)
        a = parsed["error"].get("action")
        action = Action.from_json(a, request_id=rid) if isinstance(a, dict) else None
        return from_envelope(resp.status, parsed, rid, ra, action)


def run_sync(client: httpx.Client, op: Op[T], sleep: Callable[[float], None]) -> T:
    """Drives an operation over a sync httpx client."""
    try:
        step = next(op)
        while True:
            if isinstance(step, Sleep):
                sleep(step.seconds)
                step = op.send(None)
                continue
            try:
                r = _send_sync(client, step)
            except (httpx.TransportError, BodyTooLarge) as e:
                step = op.throw(e)
                continue
            step = op.send(r)
    except StopIteration as stop:
        return stop.value  # type: ignore[no-any-return]


def _send_sync(client: httpx.Client, req: httpx.Request) -> Resp:
    r = client.send(req, stream=True, follow_redirects=False)
    try:
        buf = bytearray()
        for chunk in r.iter_bytes():
            buf += chunk
            if len(buf) > MAX_BODY:
                raise BodyTooLarge
        return Resp(r.status_code, r.headers, bytes(buf))
    finally:
        r.close()


async def run_async(client: httpx.AsyncClient, op: Op[T], sleep: Callable[[float], Any]) -> T:
    """Drives an operation over an async httpx client."""
    try:
        step = next(op)
        while True:
            if isinstance(step, Sleep):
                await sleep(step.seconds)
                step = op.send(None)
                continue
            try:
                r = await _send_async(client, step)
            except (httpx.TransportError, BodyTooLarge) as e:
                step = op.throw(e)
                continue
            step = op.send(r)
    except StopIteration as stop:
        return stop.value  # type: ignore[no-any-return]


async def _send_async(client: httpx.AsyncClient, req: httpx.Request) -> Resp:
    r = await client.send(req, stream=True, follow_redirects=False)
    try:
        buf = bytearray()
        async for chunk in r.aiter_bytes():
            buf += chunk
            if len(buf) > MAX_BODY:
                raise BodyTooLarge
        return Resp(r.status_code, r.headers, bytes(buf))
    finally:
        await r.aclose()
