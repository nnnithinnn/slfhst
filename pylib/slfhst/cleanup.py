"""Stage1, runs first: remove the throwaway account the kickstart had to
create to satisfy Anaconda's non-interactive mode (it hard-requires an
unlocked root or an unlocked wheel-group user to exist -- a user with no
password is silently force-locked regardless of --lock, so "no real
account at all" isn't an option; see kickstart/generic.ks). Runs before
networking comes up so the account -- and its password, however briefly
live -- is gone before the box is reachable at all.
"""
from __future__ import annotations

from pathlib import Path

from .common import log, run

INSTALL_USER = "kspending"

# Anaconda leaves a copy of the kickstart it installed from on disk,
# including the static throwaway --plaintext password -- scrub it too,
# not just the account itself.
LEFTOVER_KICKSTART_COPIES = (
    Path("/root/anaconda-ks.cfg"),
    Path("/root/original-ks.cfg"),
)


def main() -> None:
    run(["userdel", "-r", INSTALL_USER], check=False)
    for path in LEFTOVER_KICKSTART_COPIES:
        path.unlink(missing_ok=True)
    log.info("throwaway kickstart install account removed")


if __name__ == "__main__":
    main()
