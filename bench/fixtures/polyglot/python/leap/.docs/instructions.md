# Leap year

A leap year has one extra day, 29 February.

Given a year, report whether it is a leap year. The rule is:

- a year that is divisible by 4 is a leap year;
- a year that is divisible by 100 is *not* a leap year;
- a year that is divisible by 400 *is* a leap year.

Implement `is_leap_year(year)` in `leap.py`. It returns `True` for a leap
year and `False` otherwise.

The visible acceptance tests are in `leap_test.py` and are run with:

```
python3 -m pytest -q leap_test.py
```
