# Two-fer

"Two-fer" is a very small exercise about the one-line phrase:

> One for X, one for me.

Implement `two_fer(name="you")` in `two_fer.py`. With no argument (or with an
empty name) it returns `"One for you, one for me."`. With a name it returns
`"One for <name>, one for me."`.

The visible acceptance tests are in `two_fer_test.py` and are run with:

```
python3 -m pytest -q two_fer_test.py
```
