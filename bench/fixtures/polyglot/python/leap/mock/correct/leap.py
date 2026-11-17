"""Correct leap-year implementation used by the offline mock agent."""


def is_leap_year(year):
    """Return True when year is a leap year, False otherwise."""
    if year % 4 != 0:
        return False
    if year % 100 != 0:
        return True
    return year % 400 == 0
