"""Core install must work without langchain-core present."""

from __future__ import annotations

import importlib
import importlib.util
import sys

import pytest

from kaptanto import KaptantoStream
from kaptanto.langchain import as_tool


def test_kaptanto_import_does_not_pull_langchain() -> None:
    """Importing the public package must not import langchain_core."""
    before = {m for m in sys.modules if m.startswith("langchain")}
    importlib.reload(importlib.import_module("kaptanto"))
    after = {m for m in sys.modules if m.startswith("langchain")}
    assert after == before


def test_langchain_module_imports_lazily() -> None:
    """``kaptanto.langchain`` loads without requiring langchain-core at import time."""
    mod = importlib.import_module("kaptanto.langchain")
    assert hasattr(mod, "as_tool")
    # The module namespace must not bind langchain_core at import time.
    assert "langchain_core" not in mod.__dict__
    assert "StructuredTool" not in mod.__dict__


def test_as_tool_raises_when_langchain_absent() -> None:
    """Calling as_tool without langchain-core must raise a clear ImportError.

    Skipped when langchain-core is installed (full matrix). The core-only CI
    job runs this assertion with langchain absent.
    """
    if importlib.util.find_spec("langchain_core") is not None:
        pytest.skip("langchain_core installed; ImportError path covered by core-only CI")

    stream = KaptantoStream("http://127.0.0.1:9/events", consumer="t")
    try:
        with pytest.raises(ImportError, match="langchain-core"):
            as_tool(stream)
    finally:
        stream.close()
