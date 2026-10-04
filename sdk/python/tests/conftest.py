from __future__ import annotations

import pathlib

import pytest

REPO = pathlib.Path(__file__).resolve().parents[3]


def repo(rel: str) -> pathlib.Path:
    return REPO / rel


@pytest.fixture
def repo_root() -> pathlib.Path:
    return REPO


@pytest.fixture(params=["sync", "async"])
def kind(request: pytest.FixtureRequest) -> str:
    return str(request.param)
