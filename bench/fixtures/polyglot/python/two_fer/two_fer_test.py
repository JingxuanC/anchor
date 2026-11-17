"""Acceptance tests for the two-fer exercise (pytest, plain asserts)."""

from two_fer import two_fer


def test_no_name():
    assert two_fer() == "One for you, one for me."


def test_single_name():
    assert two_fer("Alice") == "One for Alice, one for me."


def test_another_name():
    assert two_fer("Bob") == "One for Bob, one for me."
