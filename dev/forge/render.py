"""Render a Sortie workflow template: replace each ${LIVE_*}, ${DEV_*} or ${ORTUS_NOVA_*}
placeholder with the environment variable of that name, and write the result
with mode 0600 (it holds tokens). A placeholder with no variable is an error.

Usage: render.py <template> <output>
"""

import os
import re
import sys

template, output = sys.argv[1:3]
text = open(template).read()
missing = sorted({m for m in re.findall(r"\$\{((?:LIVE|DEV|ORTUS_NOVA)_[A-Z_]+)\}", text) if m not in os.environ})
if missing:
    sys.exit(f"render.py: not set: {', '.join(missing)}")
out = re.sub(r"\$\{((?:LIVE|DEV|ORTUS_NOVA)_[A-Z_]+)\}", lambda m: os.environ[m.group(1)], text)
fd = os.open(output, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
with os.fdopen(fd, "w") as f:
    f.write(out)
