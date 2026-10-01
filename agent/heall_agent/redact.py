"""Removes credentials from text before it is sent to the model.

Test logs and source files can hold tokens. The model never needs them, so
anything that looks like one is replaced before it leaves the machine.
"""

from __future__ import annotations

import re

_PATTERNS = [
    # Provider tokens with a recognisable prefix.
    re.compile(r"\b(?:gsk|sk|pk|rk)[-_][A-Za-z0-9_\-]{20,}"),
    re.compile(r"\bgh[pousr]_[A-Za-z0-9]{30,}"),
    re.compile(r"\bgithub_pat_[A-Za-z0-9_]{30,}"),
    re.compile(r"\bxox[abprs]-[A-Za-z0-9\-]{10,}"),
    re.compile(r"\bAKIA[0-9A-Z]{16}\b"),
    re.compile(r"\beyJ[A-Za-z0-9_\-]{10,}\.[A-Za-z0-9_\-]{10,}\.[A-Za-z0-9_\-]{10,}"),
    re.compile(r"-----BEGIN [A-Z ]*PRIVATE KEY-----.*?-----END [A-Z ]*PRIVATE KEY-----", re.S),
]

_SECRET_NAME = r"(?:secret|token|passw(?:or)?d|api[_-]?key|access[_-]?key|private[_-]?key)"

# A quoted literal assigned to a name that says it is a secret. Only string
# literals are touched: redacting `token = makeToken()` would corrupt the
# source the model has to read and edit.
_QUOTED = re.compile(
    rf"""(?ix)
    \b([a-z0-9_.\-]*{_SECRET_NAME}[a-z0-9_]*["']?\s*[:=]\s*)
    (["'`])([^"'`\s]{{8,}})\2
    """
)
# An environment-style line: UPPER_CASE_NAME=value.
_ENV = re.compile(rf"(?m)^(\s*(?:export\s+)?[A-Z0-9_]*{_SECRET_NAME.upper()}[A-Z0-9_]*=)(\S{{8,}})$")
_BEARER = re.compile(r"(?i)\b(authorization\s*:\s*(?:bearer|basic)\s+)\S{8,}")
_URL_CREDENTIALS = re.compile(r"(?i)\b([a-z][a-z0-9+.\-]*://[^\s:/@]+:)[^\s@/]{3,}@")

MASK = "[REDACTED]"


def redact(text: str) -> str:
    for pattern in _PATTERNS:
        text = pattern.sub(MASK, text)
    text = _QUOTED.sub(lambda m: f"{m.group(1)}{m.group(2)}{MASK}{m.group(2)}", text)
    text = _ENV.sub(lambda m: m.group(1) + MASK, text)
    text = _BEARER.sub(lambda m: m.group(1) + MASK, text)
    return _URL_CREDENTIALS.sub(lambda m: f"{m.group(1)}{MASK}@", text)
