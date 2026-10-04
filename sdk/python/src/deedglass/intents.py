"""Signed intents (S32 §3.7): a byte-for-byte port of S09's deedglass.intent/1 signer.

The intent is JCS bytes of at most 2,048; the signature covers ``"deedglass/intent/v1\\n" +
intent_hash``; the header is ``b64url(intent) "." b64url(signature)``. Needs the crypto extra.
"""

from __future__ import annotations

import hashlib
import os
import re
import time
from collections.abc import Callable
from dataclasses import dataclass
from typing import Any

from . import _copy
from ._args import args_digest, canonical_args
from ._errors import InvalidRequestError
from .verify._b64 import encode_b64url
from .verify._jcs import canonicalize

__all__ = ["IntentKey", "SignedIntent", "args_digest", "generate_intent_key", "intent_kid", "sign_intent"]

INTENT_VERSION = "deedglass.intent/1"
INTENT_DOMAIN = "deedglass/intent/v1\n"
MAX_INTENT_BYTES = 2048
DEFAULT_TTL_MS = 60_000
MAX_TTL_MS = 300_000
INTENT_TOOL = re.compile(r"^[a-z][a-z0-9-]{1,31}\.[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}\Z")
REF = re.compile(r"^[A-Za-z0-9._~:@/+=-]{1,256}\Z")
_ORG = re.compile(r"^org_[0-7][0-9a-hjkmnp-tv-z]{25}\Z")
_AGENT = re.compile(r"^agt_[0-7][0-9a-hjkmnp-tv-z]{25}\Z")
_NONCE = re.compile(r"^[0-9a-f]{32}\Z")


def _bad(msg: str, param: str) -> InvalidRequestError:
    return InvalidRequestError(msg, type="client_error", code="intent_invalid", param=param)


def _crypto() -> Any:
    try:
        from cryptography.hazmat.primitives import serialization
        from cryptography.hazmat.primitives.asymmetric import ed25519
    except ImportError:
        raise ImportError(_copy.SIGN_CRYPTO_MISSING) from None
    return serialization, ed25519


def intent_kid(public_key_raw: bytes) -> str:
    """The key id: hex of the first 16 bytes of SHA-256(raw public key)."""
    return hashlib.sha256(public_key_raw).digest()[:16].hex()


@dataclass(frozen=True)
class IntentKey:
    """An Ed25519 intent key: the PKCS#8 PEM ``deedglass agent intent keygen`` writes.

    ``org`` and ``agent`` are filled from ``whoami`` when left out. The private key never appears in
    ``repr``.
    """

    private_key_pem: str
    org: str | None = None
    agent: str | None = None
    kid: str | None = None

    def __repr__(self) -> str:
        return f"IntentKey(kid={self.kid!r}, org={self.org!r}, agent={self.agent!r})"

    def _load(self) -> tuple[Any, str]:
        serialization, ed25519 = _crypto()
        try:
            k = serialization.load_pem_private_key(self.private_key_pem.encode("ascii"), password=None)
        except (ValueError, TypeError, UnicodeEncodeError):
            raise _bad(_copy.INTENT_KEY_BAD, "intent_key") from None
        if not isinstance(k, ed25519.Ed25519PrivateKey):
            raise _bad(_copy.INTENT_KEY_BAD, "intent_key")
        pub = k.public_key().public_bytes(serialization.Encoding.Raw, serialization.PublicFormat.Raw)
        kid = intent_kid(pub)
        if self.kid and self.kid != kid:
            raise _bad(
                f"The intent key's kid is {kid}, not {self.kid}. Pass the matching kid, or leave it out.",
                "intent_key.kid",
            )
        return k, kid


@dataclass(frozen=True)
class SignedIntent:
    header: str
    intent_hash: str
    kid: str
    expires_at: int  # Unix milliseconds


def sign_intent(
    key: IntentKey,
    *,
    org: str,
    agent: str,
    tool: str,
    args: Any,
    on_behalf_of: str | None = None,
    session: str | None = None,
    ttl_ms: int = DEFAULT_TTL_MS,
    now: Callable[[], int] | None = None,
    nonce: str | None = None,
) -> SignedIntent:
    """Signs one intent. Refusals are InvalidRequestError (code intent_invalid, or big_int for args).

    ``now`` (Unix milliseconds) and ``nonce`` are injectable only for test vectors.
    """
    if not _ORG.match(org):
        raise _bad("org must be an org id like org_01k6h7kp6msmzrhdzwtcr3jnkp.", "org")
    if not _AGENT.match(agent):
        raise _bad("agent must be an agent id like agt_01k633rrmh3bcz2p39gg4vp46q.", "agent")
    if len(tool) > 200 or not INTENT_TOOL.match(tool):
        raise _bad(_copy.bad_intent_tool(tool), "tool")
    if on_behalf_of is not None and not REF.match(on_behalf_of):
        raise _bad(_copy.bad_ref("on_behalf_of"), "on_behalf_of")
    if session is not None and not REF.match(session):
        raise _bad(_copy.bad_ref("session"), "session")
    if isinstance(ttl_ms, bool) or not isinstance(ttl_ms, int) or ttl_ms <= 0 or ttl_ms > MAX_TTL_MS:
        raise _bad("ttl_ms must be 1 to 300,000 milliseconds.", "ttl_ms")
    n = nonce if nonce is not None else os.urandom(16).hex()
    if not _NONCE.match(n):
        raise _bad("nonce must be 32 lowercase hex characters (16 bytes).", "nonce")
    sk, kid = key._load()
    digest = args_digest(args)
    canonical_args(args)
    iat = int(now() if now else time.time() * 1000)
    intent: dict[str, Any] = {
        "v": INTENT_VERSION,
        "org": org,
        "agent": agent,
        "kid": kid,
        "tool": tool,
        "args_digest": digest,
        "nonce": n,
        "iat": iat,
        "exp": iat + ttl_ms,
    }
    if on_behalf_of is not None:
        intent["on_behalf_of"] = on_behalf_of
    if session is not None:
        intent["session"] = session
    b = canonicalize(intent).encode("utf-8")
    if len(b) > MAX_INTENT_BYTES:
        raise _bad(f"The intent is {len(b)} bytes; the limit is 2,048. Shorten on_behalf_of or session.", "intent")
    intent_hash = "sha256:" + hashlib.sha256(b).hexdigest()
    sig = sk.sign((INTENT_DOMAIN + intent_hash).encode("utf-8"))
    return SignedIntent(encode_b64url(b) + "." + encode_b64url(sig), intent_hash, kid, iat + ttl_ms)


def generate_intent_key() -> tuple[str, str, str]:
    """A new Ed25519 intent key: (PKCS#8 PEM to keep secret, the public key to register, its kid)."""
    serialization, ed25519 = _crypto()
    k = ed25519.Ed25519PrivateKey.generate()
    pem = k.private_bytes(serialization.Encoding.PEM, serialization.PrivateFormat.PKCS8, serialization.NoEncryption())
    pub = k.public_key().public_bytes(serialization.Encoding.Raw, serialization.PublicFormat.Raw)
    return pem.decode("ascii"), "ed25519:" + encode_b64url(pub), intent_kid(pub)
