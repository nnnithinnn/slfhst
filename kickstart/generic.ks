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
# source nominally does. This drops %pre entirely:
#
# - Partitioning: no more %pre + lsblk-based dynamic disk selection.
#   `ignoredisk --only-use=/dev/vda` + `autopart --type=plain` instead.
#   /dev/vda is hardcoded on purpose, not an oversight: a real install's
#   own log showed `autopart --type=plain` WITHOUT ignoredisk partitioning
#   BOTH disks (vda and vdb) -- `--type=plain` only controls the partition
#   scheme (plain vs LVM), it never guaranteed single-disk use, and
#   pykickstart has no declarative "pick the smallest/non-rotational disk"
#   predicate to fall back on. With %pre confirmed non-functional in this
#   pipeline (two real failures) and no generic alternative, /dev/vda is
#   the pragmatic call: it's the standard virtio-blk device-naming
#   convention on KVM/VPS hosts (confirmed by this same real install's own
#   log) and matches every real deployment target this image is actually
#   built for. If a target box ever doesn't use virtio-blk naming, this is
#   the line to change -- flagged, not hidden.
# - Accounts: still zero real credentials baked in (all per-deployment
#   account setup is stage1's job, see systemd/system/slfhst-stage1*), but
#   the throwaway account Anaconda's non-interactive mode requires to exist
#   is static kickstart text instead of %pre-generated -- it's deleted by
#   stage1-cleanup.service before networking even comes up, so a fixed
#   placeholder password is fine.
# - network --activate stays (below): the OS deploy itself is already
#   fully local/offline (`ostree container image deploy` reads the image
#   embedded in the ISO, not a network pull -- confirmed by a real
#   install's own log), but bootc-image-builder's own reference kickstart
#   always includes this line and sets NetworkOnBoot=true itself when
#   generating the base kickstart it prepends to ours -- removing it would
#   deviate from the only proven-working pattern for a risk (this line
#   supposedly requiring network) that isn't what's actually failed here.

text --non-interactive
lang en_US.UTF-8
keyboard us
timezone UTC --utc
network --bootproto=dhcp --device=link --activate --onboot=on

zerombr
clearpart --all --initlabel --disklabel=gpt
ignoredisk --only-use=/dev/vda
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
