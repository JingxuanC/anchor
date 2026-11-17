"""Acceptance tests for the leap-year exercise (pytest, plain asserts)."""

from leap import is_leap_year


def test_year_divisible_by_4_is_leap():
    assert is_leap_year(1996) is True


def test_common_year_is_not_leap():
    assert is_leap_year(2001) is False


def test_century_is_not_leap():
    assert is_leap_year(1900) is False


def test_year_divisible_by_400_is_leap():
    assert is_leap_year(2000) is True


def test_other_regular_leap_year():
    assert is_leap_year(2024) is True
