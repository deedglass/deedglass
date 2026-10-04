"""Every client-side sentence the SDK writes (S32 §5). Server messages pass through verbatim."""

from __future__ import annotations

import json

MISSING_API_KEY = (
    "No API key. Pass api_key=… or set DEEDGLASS_API_KEY. Create one with: deedglass agent keys create <agent>."
)
INVALID_API_KEY = "The API key isn't valid. It starts with dg_live_ or dg_test_. Create one in Console › Agents."
OUTAGE = "Deedglass isn't reachable, so this tool was not run. Check that deedglass serve is running."
NOTHING_RAN = "Nothing was run."
STOPPED = "This agent was stopped in Deedglass by an admin. Nothing was run."
TOO_LARGE = "The response was larger than 8 MiB."
BIG_INT = (
    "The arguments hold an integer larger than 2^53 − 1, which JSON numbers can't carry exactly. "
    'Send it as a string, like "9007199254740993".'
)
INTENT_KEY_BAD = (
    "The intent key must be a PKCS#8 PEM Ed25519 private key, like the one deedglass agent intent keygen writes."
)
CRYPTO_MISSING = 'Verifying deeds needs the crypto extra. Install it with: pip install "deedglass[crypto]"'
SIGN_CRYPTO_MISSING = 'Signing intents needs the crypto extra. Install it with: pip install "deedglass[crypto]"'
KEYS_URL = "Seal keys are fetched over https, or http only from localhost or 127.0.0.1."


def clip(s: str, n: int) -> str:
    return s[:n] + "…" if len(s) > n else s


def unreachable(base_url: str) -> str:
    return f"Can't reach Deedglass at {base_url}. Is deedglass serve running? Try: deedglass doctor."


def timeout(seconds: float, action_id: str | None = None) -> str:
    where = f"check {action_id} in Console › Actions" if action_id else "check Console › Actions"
    return f"Deedglass didn't answer within {round(seconds)} s. The action may still be running: {where}."


def bad_tool_name(name: str) -> str:
    return (
        f"{json.dumps(clip(name, 80), ensure_ascii=False)} isn't a Deedglass tool name. "
        'Use "connector.tool", for example "billing.refund_customer".'
    )


def bad_intent_tool(name: str) -> str:
    return (
        f"{json.dumps(clip(name, 80), ensure_ascii=False)} can't be signed: a signed intent's tool is a lowercase "
        'connector, a dot and a tool name, like "billing.refund_customer".'
    )


def bad_ref(name: str) -> str:
    return f"{name} must be 1 to 256 characters from A-Z a-z 0-9 . _ ~ : @ / + = -, for example usr_4821."


def args_invalid(why: str) -> str:
    return f"The arguments can't be sent: {why}."


def unreadable(status: int) -> str:
    return (
        f"Deedglass answered {status} with something the SDK couldn't read. "
        "Check that DEEDGLASS_SERVER points at a Deedglass server."
    )


def approval_waiting(approval_id: str) -> str:
    return f"Waiting for approval in Console › Approvals ({approval_id}). Try again after someone decides."


def approval_pending(approval_id: str, expires: str) -> str:
    return (
        f"Waiting for approval {approval_id}. It expires at {expires}. Nothing has run yet. "
        "Try again after a person approves it."
    )


def tool_execution(tool: str) -> str:
    return (
        f"{tool} raised after Deedglass authorized it. The deed records it as UNVERIFIABLE because it may have run. "
        "Pass failure_means='not_applied' only if the function fails before any side effect."
    )


def tool_failed_not_applied(tool: str) -> str:
    return (
        f"{tool} raised after Deedglass authorized it, before any side effect (failure_means='not_applied'). "
        "The deed records it as failed."
    )


def report_lost(action_id: str) -> str:
    return (
        f"Couldn't report the result of {action_id} to Deedglass after 5 attempts. "
        "Deedglass will seal it as UNVERIFIABLE (edge_lost)."
    )


def fetch_keys(host: str, why: str) -> str:
    return f"Couldn't fetch seal keys from {host} ({why})."


def adapter_missing(module: str, extra: str, framework: str) -> str:
    return f'deedglass.{module} needs {framework}. Install it with: pip install "deedglass[{extra}]"'
