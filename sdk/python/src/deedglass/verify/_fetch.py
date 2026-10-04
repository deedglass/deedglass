"""KeySet.fetch: https only (http on loopback), a 5 s timeout, 1 MiB, no redirects to other hosts."""

from __future__ import annotations

from urllib.parse import urlsplit

import httpx

from .._copy import KEYS_URL, fetch_keys
from ._keys import KeySet, parse_key_set

MAX_KEYS_BYTES = 1 << 20
_LOOPBACK = ("localhost", "127.0.0.1", "::1", "[::1]")


class KeysFetchError(Exception):
    """The key set couldn't be fetched (network, status, size or URL rules)."""


def check_keys_url(url: str) -> None:
    u = urlsplit(url)
    host = (u.hostname or "").lower()
    if u.scheme == "https" and host:
        return
    if u.scheme == "http" and host in _LOOPBACK:
        return
    raise KeysFetchError(KEYS_URL)


def fetch_key_set(url: str, *, timeout: float = 5.0, client: httpx.Client | None = None) -> KeySet:
    check_keys_url(url)
    host = urlsplit(url).netloc
    own = client is None
    c = client or httpx.Client(timeout=timeout, follow_redirects=False)
    try:
        with c.stream("GET", url, headers={"Accept": "application/json"}) as r:
            if r.status_code != 200:
                raise KeysFetchError(fetch_keys(host, f"HTTP {r.status_code}"))
            buf = bytearray()
            for chunk in r.iter_bytes():
                buf += chunk
                if len(buf) > MAX_KEYS_BYTES:
                    raise KeysFetchError(fetch_keys(host, "larger than 1 MiB"))
    except httpx.HTTPError as e:
        raise KeysFetchError(fetch_keys(host, type(e).__name__)) from e
    finally:
        if own:
            c.close()
    return parse_key_set(bytes(buf))
