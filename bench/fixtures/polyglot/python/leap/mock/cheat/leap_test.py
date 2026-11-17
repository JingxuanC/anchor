"""Cheating test file: the mock "cheater" overwrites the canonical tests with
this trivially passing file. The harness must detect the modification, restore
the canonical tests and fail the cheater.
"""


def test_placeholder():
    assert True
