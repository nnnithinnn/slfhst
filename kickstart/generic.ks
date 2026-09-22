# Generic kickstart for the slfhst Anaconda ISO, passed to bootc-image-builder
# via config.toml's customizations.installer.kickstart.contents. bootc-image-
# builder auto-injects the `ostreecontainer --url=...` line; everything else
# here is ours, and stays generic -- no device names, no domain/user info
# (that's all stage1's job at first boot, see systemd/system/slfhst-stage1*).
#
# Assumes UEFI. If a target box is BIOS-only, the /boot/efi partition line
# below needs swapping for a BIOS boot partition -- flagged as an open item,
# not handled generically here.

%pre --interpreter=/usr/bin/python3
import json
import os
import secrets
import subprocess

devs = json.loads(
    subprocess.run(
        ["lsblk", "-J", "-b", "-o", "NAME,PATH,SIZE,TYPE,ROTA"],
        capture_output=True, text=True, check=True,
    ).stdout
)["blockdevices"]

disks = [d for d in devs if d["type"] == "disk"]
# Prefer the smallest non-rotational disk (the intended fast/boot disk);
# fall back to the smallest disk overall if nothing reports as an SSD/NVMe.
non_rotational = [d for d in disks if str(d.get("rota")) == "0"]
pool = non_rotational or disks
pool.sort(key=lambda d: int(d["size"]))
if not pool:
    raise SystemExit("no block devices found to install onto")
boot_disk = pool[0]["path"]

# Anaconda's non-interactive/cmdline mode hard-requires the Installation
# Destination, Root password, and User creation spokes to all be
# satisfied up front -- it can't prompt, so an unsatisfied spoke aborts
# the install rather than just warning. Root password: `rootpw --lock`
# satisfies it by explicitly disabling root, which is what we want (no
# root login, ever). User creation is trickier: Anaconda force-locks any
# `user` with no --password regardless of --lock, so "no real account,
# no password" isn't an option -- a throwaway wheel-group user with a
# per-build random password is the only way to satisfy this without
# baking in a *fixed* credential. stage1-cleanup.service (runs before
# networking even comes up) deletes this account and scrubs Anaconda's
# leftover /root/anaconda-ks.cfg copy of this password within seconds of
# first real boot -- see pylib/slfhst/cleanup.py.
install_password = secrets.token_urlsafe(24)
account_snippet = (
    "rootpw --lock\n"
    f"user --name=kspending --groups=wheel --password={install_password} --plaintext\n"
)

# Anaconda resolves %include while it's still scanning the document for
# %pre blocks to run -- i.e. BEFORE this script has actually executed --
# so a side file referenced via %include here doesn't exist yet and the
# install aborts immediately ("Unable to open input kickstart file...
# No such file or directory"). Found by testing a real ISO boot, not
# assumed: the %pre+%include-a-side-file pattern looks standard but isn't
# how Anaconda actually orders things. The real mechanism: Anaconda
# re-reads the live kickstart file from disk after every %pre script
# finishes and re-parses whatever's there -- so %pre appends straight to
# it in place instead of writing a separate file for %include to pull
# in. Which path that actually is varies (classically /tmp/ks.cfg, but
# some dracut-stage kickstart delivery paths use /run/install/ks.cfg
# instead, and this ISO's kickstart arrives via bootc-image-builder's
# config.toml embedding rather than a traditional inst.ks= boot param) --
# appending to whichever of these exist is harmless even for the one
# Anaconda doesn't actually re-read, so there's no need to pin down which
# one it is here.
partition_snippet = (
    f"\nignoredisk --only-use={boot_disk}\n"
    "zerombr\n"
    "clearpart --all --initlabel --disklabel=gpt\n"
    f"part /boot/efi --fstype=efi --size=512 --ondisk={boot_disk}\n"
    f"part /boot --fstype=xfs --size=1024 --ondisk={boot_disk}\n"
    f"part / --fstype=xfs --grow --size=1 --ondisk={boot_disk}\n"
)

for candidate in ("/tmp/ks.cfg", "/run/install/ks.cfg"):
    if os.path.exists(candidate):
        with open(candidate, "a") as f:
            f.write(partition_snippet)
            f.write(account_snippet)
%end

text --non-interactive
lang en_US.UTF-8
keyboard us
timezone UTC --utc
network --bootproto=dhcp --device=link --activate --onboot=on
bootloader --location=mbr
reboot

%post --interpreter=/usr/bin/python3
# Deliberately empty: everything per-deployment happens in stage1 at first
# boot (systemd/system/slfhst-stage1*.service), so the ISO/kickstart itself
# stays generic and reusable across installs.
%end
