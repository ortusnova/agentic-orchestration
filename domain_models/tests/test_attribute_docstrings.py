import importlib.util
from pathlib import Path

_spec = importlib.util.spec_from_file_location(
    "attribute_docstrings", Path(__file__).resolve().parents[1] / "tools" / "attribute_docstrings.py"
)
tool = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(tool)

CRAMPED = '''class Shape:
    """A shape."""
    first: int
    """The first."""
    second: int
    """The second."""
    # About the third.
    third: int
    fourth: int
    """The fourth."""
'''

SPACED = '''class Shape:
    """A shape."""

    first: int
    """The first."""

    second: int
    """The second."""

    # About the third.
    third: int

    fourth: int
    """The fourth."""
'''


def test_cramped_attributes_are_reported():
    assert tool.missing_blank_lines(CRAMPED) == [2, 4, 6, 8]


def test_fix_spaces_them_out():
    assert tool.fix(CRAMPED) == SPACED


def test_spaced_attributes_pass():
    assert tool.missing_blank_lines(SPACED) == []


def test_undocumented_attributes_may_sit_together():
    assert tool.missing_blank_lines("class A:\n    x: int\n    y: int\n") == []


def test_the_first_and_last_attribute_need_no_outer_blank_line():
    assert tool.missing_blank_lines('class A:\n    x: int\n    """X."""\n') == []


def test_the_package_follows_the_rule():
    src = Path(__file__).resolve().parents[1] / "src"
    assert tool.main([str(src)]) == 0
