"""Run a check and expose its failure tail in public GitHub annotations."""
from collections import deque
import os
import subprocess
import sys


def main():
    if len(sys.argv) < 2:
        raise SystemExit("Usage: run_ci_check.py COMMAND [ARGS ...]")
    sys.stdout.reconfigure(encoding="utf-8", errors="replace")
    tail = deque(maxlen=150)
    child = subprocess.Popen(sys.argv[1:], stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
                             text=True, encoding="utf-8", errors="replace")
    for line in child.stdout:
        print(line, end="", flush=True)
        tail.append(line)
    code = child.wait()
    if code and os.environ.get("GITHUB_ACTIONS") == "true":
        message = ("Command: " + " ".join(sys.argv[1:]) + "\n" + "".join(tail)[-12000:])
        message = message.replace("%", "%25").replace("\r", "%0D").replace("\n", "%0A")
        print("::error title=Automated check failed::" + message, flush=True)
    return code


if __name__ == "__main__":
    raise SystemExit(main())
