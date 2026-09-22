# slfhst

A generic, self-hosted bootc appliance: rootless Podman (Quadlets) running
**Stalwart** (SMTP/IMAP/JMAP mail) + **Bulwark** (JMAP webmail),
**Ente museum** (Photos/Auth-TOTP/Locker, backed by Postgres + Garage), and
**Vaultwarden** -- fronted by **Traefik** with Cloudflare-issued TLS.

Full design rationale lives in the plan doc this repo was built from
(disk layout, the two-stage first-boot sequence, firewall/Cloudflare
model, the two independent update lanes). This README is the day-to-day
build/verify loop.

## Layout

- `Containerfile` -- the appliance image, `FROM quay.io/almalinuxorg/almalinux-bootc:latest`.
- `pylib/slfhst/` -- the shared Python package (stdlib only, no shell scripts
  anywhere). Installer/stage1 entrypoints and the `slfhst` CLI (which now
  also runs stage2 -- see "Install flow" below) all import from here, so
  there is exactly one implementation of each check/action.
- `usr/libexec/slfhst/` -- thin per-unit entrypoint scripts (`stage1_*.py`,
  `installer_deploy.py`), each just calling into `pylib/slfhst`.
- `usr/bin/slfhst` -- the ongoing ops CLI (`status`, `dnsbl`, `dns sync`,
  `cf-ips sync`, `update check|apply`, `check-all`, `bootstrap`).
- `usr/share/slfhst/templates/` -- Quadlet (`.container`/`.network`/`.volume`),
  the bundling `.target`, and plain config file (`museum.yaml`, `garage.toml`,
  Traefik's `dynamic.yml`) templates, rendered with `string.Template` when
  `slfhst bootstrap` runs.
- `systemd/system/` -- the stage1 oneshot chain, `slfhst-installer.service`
  (the live installer -- see "Install flow"), plus `slfhst-monitor.timer`
  (health/DNSBL/Cloudflare-IP refresh, anomaly-only email) and
  `slfhst-bootc-update.timer` (weekly `bootc upgrade --apply`). There's no
  automatic stage2 target any more -- `slfhst bootstrap` replaces it, run
  interactively over SSH once stage1 finishes.
- `Containerfile.installer` -- the live installer image (`FROM` the
  appliance image itself). Replaces Anaconda/kickstart entirely -- see that
  file's own comment for the full history of why (kickstart's `%pre`/
  `%include` ordering, non-interactive-mode mandatory-spoke validation, an
  `ostree.final-diffid` deploy failure, `autopart` spanning multiple disks:
  each only discoverable by burning a real install attempt). Built into a
  bootable ISO directly (mksquashfs + grub2-mkrescue, see
  `scripts/build_installer_iso.py`) rather than via bootc-image-builder --
  confirmed directly from that tool's own error output and source that it
  can't produce this kind of ISO at all (every type it supports goes
  through Anaconda in some form; a `bootc-generic-iso` type doesn't exist
  in it despite an earlier research pass claiming otherwise).
- `pylib/slfhst/installer_disks.py` / `installer_deploy.py` -- the
  installer's actual work: dynamic root+bulk disk selection (smallest
  non-rotational disk = root, largest = bulk -- genuinely dynamic now,
  unlike the old kickstart `%pre` attempt at the same thing, which never
  took effect), partition+format both, `bootc install to-filesystem` the
  embedded appliance image onto the root disk, reboot. No kexec -- real
  research found no supported way to kexec into a fresh bootc/ostree
  deployment (GRUB2+bootupd's boot-loader-spec-entry selection and
  deployment activation are genuine bootloader-time logic).
- `scripts/` -- `build_image.py` (podman build the appliance image),
  `build_installer_image.py` (podman build the installer image, embedding
  a copy of the appliance image), `build_iso.py` (qcow2 via
  bootc-image-builder -- local dev iteration only), `build_installer_iso.py`
  (the live installer ISO -- mksquashfs + grub2-mkrescue, no
  bootc-image-builder), `check.py` (byte-compile + import every module,
  what CI runs first).
- `.github/workflows/` -- `ci.yml` (check + build on every PR), `publish.yml`
  (on every merge to `main` -- or manual dispatch -- builds+pushes the
  appliance image to GHCR **and** builds+republishes the live installer ISO,
  together, every time -- see "Publishing" below).
- `renovate.json` -- bumps the bootc base image, pinned Quadlet template
  tags, and GitHub Actions versions via PR.

## Two independent update lanes

- **App containers** (Stalwart, Traefik, Ente, Vaultwarden, Garage,
  Bulwark): Quadlets use `AutoUpdate=registry`; `podman-auto-update.timer`
  re-pulls newer digests for the same tag daily. No CI round-trip needed.
- **OS + slfhst tooling itself**: Renovate PRs bump the Containerfile;
  `publish.yml` republishes the bootc image (and the ISO built from it,
  see "Publishing" below) on every merge; each already-running node's
  `slfhst-bootc-update.timer` runs `bootc upgrade --apply` weekly. A fresh
  offline install instead gets the current OS/tooling straight from
  whatever ISO it booted -- no post-install network round-trip needed for
  that part.

## Install flow

No Anaconda, no kickstart -- a small live installer does the whole job,
then hands off to the same first-boot flow this project always had:

```
1. Boot the installer ISO (console-only, no network, fully automatic --
   no prompts, since disk selection is dynamic now).
     -> installer_disks.py: pick root disk (smallest non-rotational,
        else smallest overall) and bulk disk (largest of what's left),
        partition+format both.
     -> installer_deploy.py: `bootc install to-filesystem` the appliance
        image (embedded in the ISO, no network needed) onto the root
        disk, then `systemctl reboot`.
   One reboot, full stop -- no kexec (see Containerfile.installer's
   comment for why that's not supported for a fresh bootc/ostree
   deployment).

2. Real OS, first boot -- stage1 (systemd/system/slfhst-stage1*.service,
   console-attached, automatic): claim the bulk disk (already partitioned
   by the installer, disks.py just mounts it), interactive network setup
   (DHCP or static, see netconf.py), the setup wizard (hostname/domain/
   admin user+password+optional pubkey/alert email/Cloudflare token/
   secrets), create the service+admin users, SSH hardening (password or
   key, both always require TOTP -- see totp.py), firewall.

3. Stage 2, run BY THE ADMIN over SSH once stage1 finishes:
   `slfhst bootstrap` -- renders quadlets, pulls+starts every container,
   bootstraps Garage/Postgres/Stalwart, syncs Cloudflare DNS, writes the
   MOTD. Not automatic any more (was a boot-time systemd target) -- this
   is where the admin actually watches it happen and can re-run it
   idempotently if anything needs fixing.
```

## Build / verify loop

```
python3 scripts/check.py                    # fast: byte-compile + import check
python3 scripts/build_image.py              # podman build the appliance image
python3 scripts/build_iso.py --type qcow2   # fast local iteration -- boots
                                             # the real appliance image directly,
                                             # no installer involved, for testing
                                             # stage1/slfhst bootstrap only
# boot the qcow2 in libvirt/qemu-kvm with a SECOND scratch disk attached
# (simulates the NVMe+SSD split), walk stage1, then SSH in and run
# `slfhst bootstrap`, confirm services come up, curl each subdomain + the
# raw mail ports.

python3 scripts/build_installer_image.py    # only once qcow2/stage1 verified
python3 scripts/build_installer_iso.py      # then the real installer ISO
# boot THIS in a VM with two differently-sized scratch disks attached --
# the one thing genuinely new and unverified in this design is dynamic
# disk selection landing on the right disk for each role. Confirm that
# directly before trusting it on real hardware. Also the actual x86_64
# BIOS+UEFI hybrid boot itself, if building on non-x86_64 hardware --
# see build_installer_iso.py's docstring for exactly what was and wasn't
# verified without one.
```

Reboot mid-stage1 to confirm the `ConditionPathExists` gating makes it
idempotent -- no re-prompt, no re-partition, services just restart.
`slfhst bootstrap` is naturally idempotent too (every module it calls is
already `require_done_or_exit("stage1")`-guarded) -- safe to re-run if
anything needs retrying.

## Publishing

One workflow, one rule: **if we build something, we build everything.**
Every push to `main` (or a manual `workflow_dispatch`) builds the
appliance image *and* the live installer ISO built from it, in the same
run, and republishes both -- there's no separate, semver-tag-gated
"cut a release" step any more. That's deliberate: the installer embeds the
appliance image and deploys it with no network involved (see "Install
flow" above), so an ISO embedding a stale image isn't just outdated, it's
the thing an offline install actually boots -- keeping the image build and
the ISO build in one run is what guarantees they always match, rather than
drifting apart between whenever someone remembered to tag a release.

Five steps (`publish.yml`), no more: **1. Precheck** (`check.py`) ->
**2. Build image** (`podman build` the appliance image, then the installer
image layered on top of it, both local only) -> **3. Build ISO**
(mksquashfs + grub2-mkrescue against the *local* installer image directly
-- no GHCR round-trip, no bootc-image-builder at all -- see
`build_installer_iso.py`'s docstring for why bootc-image-builder can't do
this) -> **4. Publish image** (the *appliance* image, not the installer
image, pushed to GHCR as `:stable` + a dated tag -- what
`slfhst-bootc-update.timer` on already-running nodes tracks) -> **5. Publish
ISO** (the single rolling `latest` GitHub Release, replacing whatever ISO
was there before -- `scripts/publish_release.py` deletes-then-recreates it
each run, since `gh release create` refuses to reuse an existing tag).

Building the ISO from the local installer image rather than a GHCR
reference isn't just simpler -- it means the ISO build has no network
dependency on this run's own GHCR push succeeding first, and can't end up
embedding a different appliance image than the one that gets pushed to
GHCR, since both come from the exact same local `build_image.py` run.

Git tags aren't part of this any more -- tag something yourself
(`git tag v0.2.0 && git push origin v0.2.0`) if you want a bookkeeping
checkpoint, but CI doesn't react to it either way.

The ISO ships as a direct GitHub Release asset. `scripts/publish_release.py`
fails loudly rather than silently splitting/compressing if a build ever
actually exceeds GitHub's 2GiB limit, so that stays a visible decision if
it happens rather than a silent break. Old Anaconda-based builds measured
~942MB-1.63GB, comfortable headroom. **The new live installer ISO is
~2.06GB (2,063,931,392 bytes) as of its first successful real build
(2026-09-22)** -- only ~80MB under the 2,147,483,648-byte limit, a real,
current risk rather than a distant hypothetical. Two things stack here
that didn't before: the appliance image is back to ~1.94GB unsquashed
(see "Image size" below -- `--squash-all` broke ostree deploys), and the
installer image embeds a *full second copy* of that same appliance image
as an oci-archive (`Containerfile.installer`) so `bootc install
to-filesystem` never needs network -- squashfs compression clearly isn't
reclaiming much against that combination. If the appliance image grows at
all from here, this needs attention before it fails a real release
outright -- e.g. compressing the embedded oci-archive harder, or
reconsidering whether it needs to be a full copy at all.

## Image size

`scripts/build_image.py` no longer squashes (`--squash-all` was removed
2026-09-22 -- it broke real installs, see that script's docstring:
squashing destroys the `ostree.final-diffid` label bootc's container-image
deploy needs, so it's a correctness requirement, not a style choice). The
Containerfile still removes several base-image packages in a layer *after*
the base image's own layers, but without squashing those bytes stay
physically present (OCI layers are additive; removal just adds whiteout
markers) -- there's no available fix for that the way there would be for a
package this Containerfile installs itself (do the install+removal in one
layer): these packages ship in the *base* image's own layers, which we
don't control. `tsflags=nodocs` + `install_weak_deps=False` are still set
globally in `/etc/dnf/dnf.conf` for anything installed after that point,
and still help.

Removed (all confirmed zero dependents via `rpm -q --whatrequires` before
removal) -- the whiteout markers still shrink what actually needs pulling
at runtime even though the bytes remain in the image itself:

- an unused SSSD/Samba/AD-auth stack + `toolbox` + `libicu` (~100MB)
- `linux-firmware` (~700MB) -- explicit decision to drop it for this
  deployment target (VPS). Real-world opinion on whether that's safe in
  a virtualized environment is genuinely split and provider-dependent
  (most virtio paths need no firmware; some accelerated-NIC cloud paths
  might); if this image ever needs to target bare metal or an unusual
  hypervisor path, reverting the `linux-firmware` line in the
  Containerfile is the lever.

Net result: back to **~1.94GB** (`podman save` tarball size) -- the
~1.75GB squashed figure from an earlier README revision no longer applies
and shouldn't be quoted; correctness (a bootc image that actually deploys)
took priority over that ~190MB reclaim. A real ostree-aware size-reduction
path (e.g. an `rpm-ostree compose`-style rebuild instead of layering atop
the published base image) would be the way to get it back, if ever worth
the added complexity -- not attempted here.

## Open items (not blocking, tracked so they don't get lost)

- **The installer redesign (2026-09-22) has never booted on real hardware
  or a VM** -- this is the biggest gap in the project right now, same
  caveat this section has carried since the very first architecture, just
  for a newer design. What HAS been verified, and how, matters here since
  earlier passes on this exact redesign got real things wrong (a
  nonexistent bootc-image-builder type, a dracut clevis-module chain that
  took two guesses to actually fix) before switching to "verify locally,
  don't guess" -- see individual files' comments and git history:
  - `Containerfile.installer`'s dracut invocation (module list, `--kver`,
    the three dracut.conf.d files it removes) was verified by actually
    building a real initramfs locally and confirming with `lsinitrd` that
    `dmsquash-live` is present.
  - The live-ISO mechanism itself (mksquashfs + grub2-mkrescue,
    `build_installer_iso.py`) was verified locally end-to-end against the
    base AlmaLinux bootc image -- a real ISO was built, and `xorriso -indev
    ... -find` confirmed `LiveOS/squashfs.img`/`boot/grub/grub.cfg`/
    `boot/vmlinuz`/`boot/initramfs.img` all land at the exact paths
    dmsquash-live's own source (not docs) expects.
  - x86_64 build confirmed on 2026-09-22 (CI, GitHub's `ubuntu-latest`
    runners): `grub2-pc`/`grub2-efi-x64`/`shim-x64` install and
    grub2-mkrescue produces a ~2.06GB ISO without error. **Build success is
    not boot success** -- nothing has actually booted this ISO yet (no
    KVM in this dev sandbox), so the BIOS+UEFI hybrid boot catalog itself,
    dmsquash-live actually finding and mounting the squashfs at real boot,
    and the installer unit actually running are all still unverified. That
    real boot test is the next real gap, not this build step.
  - `installer_deploy.py`'s `bootc install to-filesystem` flags
    (`--root-mount-spec`/`--boot-mount-spec`/`--replace`) -- confirmed
    against research, not a real `--help` output or a real run.
  - `pam-ssh-auth-info` (Containerfile, `totp.py`'s PAM stack) is built
    from unpinned `main` HEAD (no tagged releases upstream exist) and its
    `make install` module path isn't confirmed to land where PAM's
    default search path expects, though it did compile and install
    without error in a real local build -- pin to a specific reviewed
    commit SHA before relying on this for a real deployment. The
    password+TOTP / key+TOTP split itself needs a real login test of both
    paths, not just `sshd -t`/`sshd -T`.
  - Dynamic root/bulk disk selection in `installer_disks.py` needs a real
    two-disk boot to confirm it lands on the right disk for each role --
    the exact thing that made the old kickstart-era hardcoded `/dev/vda`
    necessary was never actually re-tested with dynamic selection in this
    installer context (a genuinely different, lower-risk environment than
    kickstart's `%pre`, but "lower-risk" isn't "verified").
- Image tags are now pinned (2026-09-22 pass): `stalwartlabs/stalwart:v0.16.20`,
  `vaultwarden/server:1.37.2`, `bulwarkmail/webmail:1.10.0`,
  `dxflrs/garage:v1.0.1`, `postgres:16-alpine`, `traefik:v3.3`. Ente's
  `ghcr.io/ente/server`/`ghcr.io/ente/web` are deliberately left on
  `:latest` -- upstream publishes no stable version tags, just a weekly
  rebuild, so `AutoUpdate=registry` is what actually tracks it (note the
  org rename from `ente-io`, GHCR doesn't redirect the old path).
- **Stalwart v0.16 removed TOML config files entirely** (JMAP objects in
  its datastore instead) and stopped creating plaintext STARTTLS
  submission(587)/imap(143) listeners by default. This forced a real
  rework, not just a tag bump: `backing.py::harden_stalwart()` now applies
  a declarative NDJSON plan via `stalwart-cli` (a separate image,
  `stalwartlabs/cli`, run as a one-shot container against Stalwart's JMAP
  API -- see that function's docstring), the firewall/quadlet port list
  dropped 587/143 in favor of implicit-TLS-only (465/993), and a pinned
  `STALWART_RECOVERY_ADMIN` credential (via the new
  `stalwart_admin_password` secret) replaces the old random one-time
  bootstrap password so automation has something stable to authenticate
  with. The `SystemSettings`/`Security`/`MtaInboundThrottle` field names in
  `_stalwart_hardening_plan()` are verified against Stalwart's actual Rust
  schema source, not guessed -- but **the plan has never been run against a
  live v0.16 instance**, so treat a real deploy's first `stalwart-cli
  apply` as the actual test. Per-listener `max-connections` (part of the
  old hardening file) was deliberately dropped rather than guessed at,
  since a wrong `NetworkListener` upsert risks blanking out an existing
  listener's `bind`/`protocol` instead of just capping its connections --
  revisit once the apply envelope is confirmed against a real instance.
- `museum.yaml.tmpl` is a best-effort scaffold -- validate every key against
  Ente's own `museum.yaml.sample` before a real deploy.
- Stalwart's DKIM public key isn't wired into `slfhst dns sync` yet
  (`dns_cf._stalwart_dkim_txt()` is a stub) -- add the `default._domainkey`
  TXT record manually until that's automated.
- `slfhst check-all`'s alert email currently assumes unauthenticated local
  SMTP submission on `127.0.0.1:2525`; Stalwart will likely need a real
  authenticated relay account provisioned for this to work in practice.
- Bulwark/Vaultwarden/Ente-web backend ports in `traefik-dynamic.yml.tmpl`
  are documented defaults, not yet confirmed against a live deploy.
