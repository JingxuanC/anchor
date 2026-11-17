"""Correct two-fer implementation used by the offline mock agent."""


def two_fer(name="you"):
    """Return the one-line two-fer sentence for name."""
    return "One for {}, one for me.".format(name)
