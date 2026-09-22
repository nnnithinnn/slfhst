# Generic kickstart for the slfhst Anaconda ISO, passed to bootc-image-builder
# via config.toml's customizations.installer.kickstart.contents. bootc-image-
# builder auto-injects the `ostreecontainer --url=...` line; everything else
# here is ours.
#
# Reassessed from scratch (2026-09-22) after two real installs falsified two
# separate %pre-based fixes in a row -- both assumed Anaconda re-parses a
# kickstart file %pre mutates in place, which real testing showed had zero
# effect (neither the dynamic partitioning nor the account lines a %pre
# script wrote ever took effect), even though that's what Anaconda's own
# source nominally does. Rather than ship a third unverified variant of the
# same mechanism, this drops %pre entirely:
#
# - Partitioning: no more %pre + lsblk-based dynamic disk selection. Instead
#   `autopart --type=plain` (matches bootc-image-builder's own verified
#   reference kickstart for customizations.installer.kickstart.contents --
#   no %pre, no ignoredisk, nothing custom). --type=plain forces traditional
#   (non-LVM) partitions, which can't span multiple disks -- so on the real
#   two-disk target (fast + bulk) this can only land on one disk, whichever
#   Anaconda's own candidate-disk logic picks, without risking silently
#   spanning both. Which physical disk that ends up being doesn't matter:
#   stage1's disks.py already finds the ACTUAL root disk dynamically via
#   `findmnt -no SOURCE /` post-install and treats whatever else exists as
#   the bulk disk -- it was never told which disk to expect, so it needed no
#   changes here. Also resolves the old "assumes UEFI" open item as a side
#   effect: autopart creates whatever boot partition the detected firmware
#   (BIOS or UEFI) actually needs, instead of a hand-written /boot/efi line.
# - Accounts: still zero real credentials baked in (all per-deployment
#   account setup is stage1's job, see systemd/system/slfhst-stage1*), but
#   the throwaway account Anaconda's non-interactive mode requires to exist
#   is now static kickstart text instead of %pre-generated -- it's deleted
#   by stage1-cleanup.service before networking even comes up, so a fixed
#   placeholder password is fine; there's nothing left to gain by involving
#   the same unverified mechanism that just failed twice for a value whose
#   secrecy doesn't actually matter.

text --non-interactive
lang en_US.UTF-8
keyboard us
timezone UTC --utc
network --bootproto=dhcp --device=link --activate --onboot=on

zerombr
clearpart --all --initlabel --disklabel=gpt
autopart --type=plain --noswap

# Anaconda's non-interactive/cmdline mode hard-requires the Root password
# and User creation spokes to be satisfied up front -- it can't prompt, so
# leaving both unset (our original intent) aborts the install rather than
# just warning. rootpw --lock keeps root disabled, which is what we want.
# Anaconda force-locks any `user` with no --password regardless of --lock,
# so a real (if throwaway, if static) password is unavoidable here --
# stage1-cleanup.service deletes this account and scrubs Anaconda's
# leftover /root/anaconda-ks.cfg copy of it before networking comes up.
rootpw --lock
user --name=kspending --groups=wheel --password=slfhst-install-throwaway --plaintext

reboot

%post --interpreter=/usr/bin/python3
# Deliberately empty: everything per-deployment happens in stage1 at first
# boot (systemd/system/slfhst-stage1*.service), so the ISO/kickstart itself
# stays generic and reusable across installs.
%end
