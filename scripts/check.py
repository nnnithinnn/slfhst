#!/usr/bin/python3
"""Fast local/CI check: byte-compile every slfhst .py file and make sure the
package + every module imports cleanly. Not a substitute for the real
qcow2/anaconda-iso verification loop in README.md, just a cheap first gate.
"""
import compileall
import sys
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(REPO_ROOT / "pylib"))

TARGETS = [
    REPO_ROOT / "pylib" / "slfhst",
    REPO_ROOT / "usr" / "libexec" / "slfhst",
    REPO_ROOT / "scripts",
]


def main() -> int:
    ok = True
    for target in TARGETS:
        if not compileall.compile_dir(str(target), quiet=1):
            ok = False
    if not compileall.compile_file(str(REPO_ROOT / "usr" / "bin" / "slfhst"), quiet=1):
        ok = False

    import importlib
    for name in ("common", "disks", "netconf", "wizard", "users", "totp",
                 "firewall", "quadlets", "backing", "dns_cf", "monitor",
                 "healthcheck", "deploy", "cli"):
        importlib.import_module(f"slfhst.{name}")

    return 0 if ok else 1


if __name__ == "__main__":
    sys.exit(main())
