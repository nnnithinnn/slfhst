# slfhst appliance image
#
# Generic bootc image: everything here must work unmodified on any target box.
# Per-deployment values (hostname, domain, keys, secrets) are collected at first
# boot by the stage1 wizard (usr/libexec/slfhst/stage1_wizard.py), never baked
# in here.
# Pinned to the major version explicitly, not :latest -- :latest currently
# still tracks AlmaLinux 9. AlmaLinux 10 is smaller out of the box even
# before any of our own debloat work (measured: ~1.5GB vs ~1.75GB for an
# otherwise-identical build), ships Python 3.12 instead of 3.9, and has a
# later EOL. Verified compatible: repo file structure, EPEL availability,
# and every package/tool this Containerfile and pylib/slfhst depend on are
# all present (with two adjustments noted below where EL10 genuinely
# differs from EL9).
FROM quay.io/almalinuxorg/almalinux-bootc:10

# --- Use AlmaLinux's own repo servers, not the third-party mirrorlist ------
# The base image's almalinux-*.repo files default to mirrorlist=, which
# redirects through volunteer-run third-party mirrors -- flaky/rate-limited
# ones are a known, documented source of "everything suddenly fails to
# download" failures (see e.g. AlmaLinux forum threads on this). This
# matters beyond our own `dnf install` below: bootc-image-builder resolves
# and downloads the Anaconda ISO's package set using THIS image's own repo
# config, so a flaky mirror there can take down an entire release build.
# Each repo file already ships a matching, commented-out baseurl= pointing
# straight at AlmaLinux's own repo.almalinux.org -- just swap to it.
RUN sed -i \
        -e 's/^mirrorlist=/#mirrorlist=/' \
        -e 's/^# baseurl=/baseurl=/' \
        /etc/yum.repos.d/almalinux-*.repo

# --- Skip docs/weak-deps on everything we install from here on -------------
# Standard, well-established, essentially risk-free size win (--nodocs +
# install_weak_deps=False), applied globally so every dnf call below
# benefits without repeating the flags. Doesn't shrink what's already in
# the base layer -- see the explicit removals below for that.
RUN printf 'tsflags=nodocs\ninstall_weak_deps=False\n' >> /etc/dnf/dnf.conf

# --- Remove what we're replacing, and what's unused dead weight ------------
# NetworkManager/chrony: replaced by systemd-networkd/-resolved/-timesyncd
# (see usr/share/slfhst/templates/network + resolved.conf.d/quad9.conf).
#
# sssd*/samba*/libsmbclient/libwbclient: a full AD/domain-join + Samba
# client stack the base image ships enabled by default -- confirmed via
# `rpm -q --whatrequires` that nothing installed requires any of it (it's
# all top-level/orphaned). We don't do domain-joined or Samba auth; our
# own auth is SSH key + PAM TOTP (see totp.py). ~35MB direct.
#
# toolbox/libicu: also confirmed zero dependents. toolbox is an
# interactive debugging convenience irrelevant on a headless appliance;
# nothing here links against libicu. ~46MB combined.
#
# glibc-gconv-extra isn't listed explicitly -- it's an automatic orphan
# once samba-client-libs (its only requirer) is gone, and dnf removes it
# along with the rest in this one transaction. ~19MB.
#
# linux-firmware: ~700MB, over a third of the image, zero package
# dependents. Real-world opinion on stripping it in a virtualized/VPS
# environment is genuinely split and provider-dependent (most virtio
# paths need no firmware; some accelerated-NIC cloud paths might) --
# explicit decision made to drop it anyway for this deployment target.
# If this image ever needs to target bare metal or an unusual VPS
# hypervisor path, this is the line to revert.
#
# Two separate RUN steps deliberately: NetworkManager/chrony get `|| true`
# because whether they're present can drift with the base image, but the
# debloat removal below is NOT allowed to fail silently -- these are
# confirmed present with zero dependents, so any failure here means that
# assumption broke and the build should stop, not silently skip the cleanup.
RUN dnf -y remove NetworkManager NetworkManager-* chrony chrony-* || true
RUN dnf -y remove \
        sssd sssd-* samba-client-libs samba-common samba-common-libs \
        libsmbclient libwbclient toolbox libicu linux-firmware

# --- Second debloat pass: desktop/hardware/NFS tooling irrelevant on a
# headless VPS appliance. All confirmed via `rpm -q --whatrequires` (see
# git history for the exact command output):
#
#   udisks2/libudisks2 + its libblockdev-* plugin cluster (fs/mdraid/crypto/
#     swap/part/loop/utils) + libbytesize: zero dependents. udisks2 is a
#     D-Bus disk-automounting daemon for desktop environments; we partition
#     and mount disks ourselves in stage1 (disks.py).
#   mdadm: its only requirer, libblockdev-mdraid, is removed above in this
#     same transaction.
#   man-db + groff-base: zero dependents (groff-base's only requirer is
#     man-db). tsflags=nodocs already means no new man pages get installed;
#     this drops the indexing daemon itself.
#   sg3_utils + sg3_utils-libs: zero dependents. SCSI generic device tools,
#     not needed for virtio/NVMe disks.
#   nfs-utils + libnfsidmap + rpcbind + gssproxy + quota + quota-nls: zero
#     dependents on nfs-utils (the rest are nfs-utils's own dependencies,
#     cascading away with it). We don't mount NFS or enforce disk quotas.
#   criu + criu-libs: zero dependents once paired (criu-libs requires
#     criu). Container checkpoint/restore, a podman feature unused here.
#   adcli + adcli-selinux: zero dependents once paired (adcli-selinux
#     requires adcli). AD-domain-join client, leftover from the sssd/samba
#     removal above -- these are separately-named, not caught by that glob.
#   libsss_certmap/libsss_idmap/libsss_nss_idmap/libsss_sudo/libipa_hbac:
#     zero dependents, same leftover-from-sssd situation as adcli.
#   stalld: zero dependents. Real-time scheduling starvation-avoidance
#     daemon; nothing here needs custom RT scheduling.
#   flatpak-session-helper: zero dependents. Flatpak sandboxing, meaningless
#     without a desktop session.
#   avahi-libs: zero dependents. mDNS/Bonjour; no avahi-daemon is installed
#     to use it in the first place.
#   nano: redundant with vim-minimal (also in the base image) for the one
#     use case that matters here -- emergency console editing.
#
# Deliberately NOT touching lvm2/device-mapper/cryptsetup/clevis/tpm2 --
# /usr/lib/dracut/dracut.conf.d/30-bootc-standard.conf explicitly enables
# dracut's lvm and crypt modules, which need these tools present to build
# a working initramfs. Also not touching os-prober (grub2-tools requires
# it) or irqbalance (genuinely useful on a multi-core VPS).
#
# Also NOT touching fwupd/fwupd-plugin-flashrom/flashrom/libjcat/dmidecode
# on this (AlmaLinux 10) base, unlike the EL9 build this started on: fwupd
# itself provides the virtual `dbxtool` capability here, which the
# *protected* shim-aa64 (UEFI Secure Boot bootloader) package requires --
# confirmed by actually testing the removal, which broke the build with
# "Problem: The operation would result in broken dependencies for the
# following protected packages: shim-aa64". This dependency didn't exist
# on EL9 (fwupd had zero dependents there) -- a good example of why this
# whole removal list is verified per-base-image, not assumed to transfer.
#
# Also NOT touching exim, tried and reverted: fail2ban hard-requires
# fail2ban-sendmail, which hard-requires /usr/sbin/sendmail, and exim is
# EL10's provider for that virtual capability (confirmed via `dnf install`
# actually pulling it in during the next RUN step even after removal was
# attempted here, since at *this* point in the build it isn't installed
# yet to remove -- it only appears once fail2ban gets installed below).
# It's disabled by default and only 5MB; its own EPEL package just doesn't
# declare /var/log/exim + /var/spool/exim/* via tmpfiles.d, which is what
# usr/lib/tmpfiles.d/slfhst.conf's exim entries paper over (see that file).
RUN dnf -y remove \
        udisks2 libudisks2 libblockdev libblockdev-utils libblockdev-fs \
        libblockdev-mdraid libblockdev-crypto libblockdev-swap \
        libblockdev-part libblockdev-loop libbytesize mdadm \
        man-db groff-base \
        sg3_utils sg3_utils-libs \
        nfs-utils libnfsidmap rpcbind gssproxy quota quota-nls \
        criu criu-libs \
        adcli adcli-selinux \
        libsss_certmap libsss_idmap libsss_nss_idmap libsss_sudo libipa_hbac \
        stalld flatpak-session-helper avahi-libs nano

# --- Third debloat pass: AlmaLinux 10 splits firmware into many per-vendor
# packages instead of EL9's one monolithic linux-firmware -- removing
# linux-firmware above doesn't touch these, they're entirely separate
# packages. Same rationale as the linux-firmware decision above (explicit
# call for this VPS deployment target): a VPS has no NVIDIA/AMD/Intel GPU,
# no WiFi chipset (atheros/mt7xxx/brcmfmac/realtek/tiwilink/nxpwireless),
# and no need for the audio codec firmware either. amd-ucode-firmware is
# CPU microcode, which a VM guest doesn't load itself anyway (the host
# hypervisor owns that). linux-firmware-whence is just a license/attribution
# index for firmware blobs we no longer ship. All confirmed zero
# dependents via `rpm -q --whatrequires` (linux-firmware-whence is
# required only by these same firmware packages, so it cascades away
# cleanly in this one transaction). ~257MB.
#
# sos (sysreport/diagnostic collection) and btrfs-progs (we use XFS only,
# see disks.py): also confirmed zero dependents. ~11MB combined.
RUN dnf -y remove \
        nvidia-gpu-firmware atheros-firmware amd-gpu-firmware \
        mt7xxx-firmware brcmfmac-firmware intel-gpu-firmware \
        realtek-firmware tiwilink-firmware intel-audio-firmware \
        cirrus-audio-firmware nxpwireless-firmware amd-ucode-firmware \
        linux-firmware-whence \
        sos btrfs-progs

# --- Base tooling ------------------------------------------------------------
# python3 ships in the AlmaLinux bootc base already -- everything under
# usr/libexec/slfhst/ and usr/bin/slfhst is Python (stdlib only, no pip
# installs in the image), never shell, per project convention.
#
# parted and systemd-container are explicit, not implicit: disks.py needs
# parted/partprobe (stage1 bulk-disk partitioning) and quadlets.py/deploy.py
# need machinectl (`machinectl shell svc@ ...`, systemd-container's
# binary) -- neither ships in the base image by default. Found by actually
# testing each tool's presence rather than assuming it, which also caught
# that machinectl was silently missing from the EL9 build all along (never
# exercised in a real boot until now) -- not an EL10-specific gap.
RUN dnf -y install epel-release && \
    dnf -y install \
        podman \
        systemd-networkd \
        systemd-resolved \
        systemd-timesyncd \
        systemd-container \
        firewalld \
        fail2ban \
        google-authenticator \
        qrencode \
        jq \
        curl \
        bind-utils \
        openssl \
        parted \
    && dnf clean all

# --- Rootless port publish for privileged ports (25/465/587/143/993/8443) ---
# We do NOT lower net.ipv4.ip_unprivileged_port_start; firewalld forwards each
# privileged port to a fixed unprivileged container-side port instead
# (see pylib/slfhst/firewall.py).

# --- Bake in the shared `slfhst` python package (common.py, disks.py, ...) --
# Installed via a .pth file rather than a version-pinned site-packages path,
# so it keeps importing fine across python3 minor version bumps.
COPY pylib/slfhst/ /usr/share/slfhst/pylib/slfhst/
RUN python3 -c "\
import pathlib, site; \
candidates = [pathlib.Path(p) for p in site.getsitepackages()]; \
target = next((p for p in candidates if p.is_dir()), candidates[0]); \
target.mkdir(parents=True, exist_ok=True); \
(target / 'slfhst.pth').write_text('/usr/share/slfhst/pylib\n')" && \
    python3 -c "import slfhst"

# --- Bake in stage1/stage2 units, scripts, quadlet templates, and the CLI ---
COPY systemd/system/ /usr/lib/systemd/system/
COPY usr/lib/tmpfiles.d/ /usr/lib/tmpfiles.d/
COPY usr/libexec/slfhst/ /usr/libexec/slfhst/
COPY usr/share/slfhst/templates/ /usr/share/slfhst/templates/
COPY usr/bin/slfhst /usr/bin/slfhst

RUN chmod 0755 /usr/libexec/slfhst/*.py /usr/bin/slfhst && \
    mkdir -p /etc/slfhst && \
    systemctl enable \
        systemd-networkd.service \
        systemd-resolved.service \
        systemd-timesyncd.service \
        firewalld.service \
        podman-auto-update.timer \
        slfhst-stage1.target \
        slfhst-stage2.target \
        slfhst-monitor.timer \
        slfhst-bootc-update.timer && \
    rm -rf /var/cache/dnf /var/cache/libdnf5 /var/lib/dnf \
           /var/cache/ldconfig/aux-cache \
           /var/log/dnf* /var/log/hawkey.log /run/*
# /etc/resolv.conf is left alone at build time (it's a busy bind-mount
# during `podman build` anyway) -- netconf.py points it at the resolved
# stub for real, at first boot.
#
# The rm -rf above clears build-time-only cruft (dnf cache/history/logs,
# stray /run content from package postinstall scripts) that `bootc container
# lint` correctly flags -- bootc images should carry frontend-clean /var and
# /run, populated at real boot time, not build time.

LABEL org.opencontainers.image.title="slfhst" \
      org.opencontainers.image.description="Generic self-hosted appliance: Stalwart+Bulwark, Ente/museum, Vaultwarden"
