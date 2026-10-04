"""Hypothesis property tests (S32 §6): JCS is idempotent; the strict parser only ever refuses with VerifyError."""

from __future__ import annotations

import json
from typing import Any

from hypothesis import given, settings
from hypothesis import strategies as st

from deedglass.verify import VerifyError, canonicalize, parse_strict
from deedglass.verify._jcs import es_number

json_values = st.recursive(
    st.none()
    | st.booleans()
    | st.integers(min_value=-(2**53 - 1), max_value=2**53 - 1)
    | st.floats(allow_nan=False, allow_infinity=False)
    | st.text(),
    lambda inner: st.lists(inner, max_size=5) | st.dictionaries(st.text(), inner, max_size=5),
    max_leaves=25,
)


@settings(max_examples=400, deadline=None)
@given(json_values)
def test_jcs_idempotent(x: Any) -> None:
    c = canonicalize(x)
    assert canonicalize(json.loads(c)) == c


@settings(max_examples=400, deadline=None)
@given(st.floats(allow_nan=False, allow_infinity=False))
def test_es_number_round_trips(f: float) -> None:
    assert float(es_number(f)) == f or (f == 0 and es_number(f) == "0")


@settings(max_examples=600, deadline=None)
@given(st.binary(max_size=300) | st.text(max_size=300).map(lambda s: s.encode("utf-8", "surrogatepass")))
def test_strict_parser_only_refuses(b: bytes) -> None:
    try:
        parse_strict(b)
    except VerifyError as e:
        assert e.code == "malformed"


@settings(max_examples=300, deadline=None)
@given(json_values.filter(lambda v: isinstance(v, (dict, list))))
def test_strict_accepts_integer_json(x: Any) -> None:
    def no_floats(v: Any) -> bool:
        if isinstance(v, float):
            return False
        if isinstance(v, dict):
            return all(no_floats(y) for y in v.values())
        if isinstance(v, list):
            return all(no_floats(y) for y in v)
        return True

    if no_floats(x):
        assert parse_strict(canonicalize(x)) == x
