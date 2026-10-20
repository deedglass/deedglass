"""Verification failures and their sentences (DEED_SPEC.md section 11.3)."""

from __future__ import annotations

from typing import Any

#: Codes that mean "cannot decide" (exit 2), not "invalid" (exit 1).
CANNOT_DETERMINE = {"keys_missing", "unsupported_version", "unsupported_algorithm"}

MESSAGES: dict[str, tuple[str, str]] = {
    "malformed": ("Deed {seq} is not valid strict JSON ({detail}).", "Export it again from the issuer; do not edit deeds by hand."),
    "unsupported_version": ("Deed {seq} uses deed format v{v}, which this verifier can't read.", "Use a verifier that reads that format."),
    "schema_invalid": ("Deed {seq} has an invalid field: {field}.", "Export it again from the issuer."),
    "id_mismatch": ("Deed {seq}'s id does not match its body.", "Export it again from the issuer."),
    "body_hash_mismatch": ("Deed {seq} was changed after it was sealed.", "Get a fresh export from the issuer and compare deed {seq}."),
    "seal_key_mismatch": ("Deed {seq} names two different seal keys.", "Treat this deed as altered."),
    "seal_key_unknown": ("Deed {seq} names seal key {key}, which is not in the key set.", "Fetch the issuer's keys: --keys <issuer>/.well-known/deedglass/keys.json"),
    "seal_key_rejected": ("The key set contains an unsafe key ({key}).", "Do not use this key set. Fetch it again from the issuer."),
    "seal_key_not_valid_at": ("Deed {seq} was sealed at {issued_at}, outside seal key {key}'s active window.", "Treat this deed as altered, or check you have the issuer's full key set."),
    "seal_key_revoked": ("Deed {seq} was sealed by revoked key {key}.", "Ask the issuer which deeds they re-sealed after the revocation."),
    "seal_invalid": ("Deed {seq}'s seal does not match its contents.", "Treat this deed as altered."),
    "non_canonical": ("Deed {seq} is not in canonical form (--strict).", "Re-export it; canonical bytes come straight from the Ledger."),
    "keys_missing": ("Cannot check deed {seq}: no seal keys were given.", "Add --keys <keys.json>. Issuers publish them at /.well-known/deedglass/keys.json"),
    "bundle_invalid": ("{detail} Nothing was extracted.", "Do not use this file. Ask the issuer for a new export."),
    "bundle_seal_invalid": ("The bundle manifest's seal does not match the manifest.", "Treat this bundle as altered. Ask the issuer for a new export."),
    "chain_genesis_mismatch": ("Deed 1 does not start this org's chain.", "Check the org id; the export may belong to another org."),
    "chain_seq_gap": ("Deed {want} is missing. The chain jumps from {prev} to {seq}.", "A deed was removed or the export is incomplete. Ask the issuer for seq {prev}-{seq}."),
    "chain_seq_out_of_order": ("Deed {seq} appears out of order (after {prev}).", "Sort by seq, or export again."),
    "chain_prev_hash_mismatch": ("Deed {seq} does not link to deed {prev}.", "A deed before it was changed or replaced. Get a fresh export."),
    "chain_org_mismatch": ("Deed {seq} belongs to {org}, not {want_org}.", "Verify each org's deeds separately."),
    "chain_time_regression": ("Deed {seq} was sealed before deed {prev}.", "Treat the chain as altered from deed {seq}."),
    "unsupported_algorithm": ("Deed {seq} is sealed with an algorithm this verifier can't check ({detail}).", "Check it with a verifier that supports that algorithm."),
    "seal_missing": ("Deed {seq} is missing a seal it must carry ({detail}).", "Treat this deed as altered, or ask the issuer for the deed as it was sealed."),
}


class VerifyError(Exception):
    """One verification failure: a section 11 code and what it names."""

    def __init__(self, code: str, **kw: Any) -> None:
        super().__init__(code)
        self.code = code
        self.seq: int | None = kw.pop("seq", None)
        self.field: str | None = kw.pop("field", None)
        self.extra: dict[str, Any] = kw

    def message(self) -> tuple[str, str]:
        text, nxt = MESSAGES[self.code]
        vals = {"seq": self.seq, "field": self.field, **self.extra}
        if self.code == "chain_seq_gap" and "prev" not in vals:
            text = "The deeds start at deed {seq}, not deed 1."
            nxt = "A deed was removed or the export is incomplete."
        if self.seq is None:
            text = text.replace("Deed {seq}'s", "The deed's").replace("Deed {seq}", "The deed").replace("deed {seq}", "the deed")

        class _D(dict[str, Any]):
            def __missing__(self, k: str) -> str:
                return "?"

        return text.format_map(_D(vals)), nxt.format_map(_D(vals))
