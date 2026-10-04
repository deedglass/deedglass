"""make py-examples as a test: every example against a running stack (DEEDGLASS_PY_DEMO=1)."""

from __future__ import annotations

import os
import subprocess

import pytest

from .conftest import repo


@pytest.mark.demo
@pytest.mark.skipif(os.environ.get("DEEDGLASS_PY_DEMO") != "1", reason="set DEEDGLASS_PY_DEMO=1 with a running stack")
def test_examples_against_the_stack() -> None:
    out = subprocess.run([str(repo("scripts/py_examples.sh"))], capture_output=True, text=True, check=False)
    assert out.returncode == 0, out.stdout + out.stderr
    assert out.stdout.count("L2 · VERIFIED") >= 3
