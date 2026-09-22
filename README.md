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
  anywhere). Stage1/stage2 entrypoints and the `slfhst` CLI both import from
  here, so there is exactly one implementation of each check/action.
- `usr/libexec/slfhst/` -- thin per-unit entrypoint scripts (`stage1_*.py`,
  `stage2_*.py`), each just calling into `pylib/slfhst`.
- `usr/bin/slfhst` -- the ongoing ops CLI (`status`, `dnsbl`, `dns sync`,
  `cf-ips sync`, `update check|apply`, `check-all`).
- `usr/share/slfhst/templates/` -- Quadlet (`.container`/`.network`/`.volume`),
  the bundling `.target`, and plain config file (`museum.yaml`, `garage.toml`,
  Traefik's `dynamic.yml`) templates, rendered with `string.Template` at
  stage2 time.
- `systemd/system/` -- the stage1/stage2 oneshot chains, plus
  `slfhst-monitor.timer` (health/DNSBL/Cloudflare-IP refresh, anomaly-only
  email) and `slfhst-bootc-update.timer` (weekly `bootc upgrade --apply`).
- `kickstart/generic.ks` -- generic Anaconda kickstart (auto-detects the
  boot disk via a Python `%pre`, `ignoredisk`s everything else so the bulk
  disk is left for stage1 to claim).
- `scripts/` -- `build_image.py` (podman build), `build_iso.py` (qcow2 or
  anaconda-iso via bootc-image-builder), `check.py` (byte-compile + import
  every module, what CI runs first).
- `.github/workflows/` -- `ci.yml` (check + build on every PR),
  `publish.yml` (on merge to `main`, builds and pushes the bootc image to
  GHCR as `:stable` + a dated tag -- what `slfhst-bootc-update.timer` tracks),
  `release.yml` (on a `vX.Y.Z` tag or manual dispatch, builds+pushes a
  versioned image and cuts the Anaconda installer ISO as a GitHub Release
  asset -- see "Cutting a release" below).
- `renovate.json` -- bumps the bootc base image, pinned Quadlet template
  tags, and GitHub Actions versions via PR.

## Two independent update lanes

- **App containers** (Stalwart, Traefik, Ente, Vaultwarden, Garage,
  Bulwark): Quadlets use `AutoUpdate=registry`; `podman-auto-update.timer`
  re-pulls newer digests for the same tag daily. No CI round-trip needed.
- **OS + slfhst tooling itself**: Renovate PRs bump the Containerfile;
  `publish.yml` republishes the bootc image on merge; each node's
  `slfhst-bootc-update.timer` runs `bootc upgrade --apply` weekly.

## Build / verify loop

```
python3 scripts/check.py                 # fast: byte-compile + import check
python3 scripts/build_image.py           # podman build the appliance image
python3 scripts/build_iso.py --type qcow2   # fast local iteration
# boot the qcow2 in libvirt/qemu-kvm with a SECOND scratch disk attached
# (simulates the NVMe+SSD split), walk the stage1 console wizard, confirm
# stage2 brings services up, curl each subdomain + the raw mail ports.

python3 scripts/build_iso.py --type anaconda-iso   # only once qcow2 is verified
```

Reboot mid-stage1/stage2 to confirm the `ConditionPathExists` gating makes
both stages idempotent -- no re-prompt, no re-partition, services just
restart.

## Cutting a release

Tags are plain [semver](https://semver.org/): `vMAJOR.MINOR.PATCH`.
- **PATCH**: backward-compatible fixes (a stage1/stage2 bug, a quadlet
  template correction, a CI fix that doesn't change behavior).
- **MINOR**: backward-compatible additions (a new `slfhst` subcommand, a
  new service in the stack, a new supported deployment option).
- **MAJOR**: breaking changes to the first-boot flow, the on-disk layout,
  or anything that makes an existing deployment's state incompatible.

Still `0.x.y` (pre-1.0): the interface can still change; see Open items
below for what's not settled yet.

```
git tag v0.1.0 && git push origin v0.1.0
```

`release.yml` then builds the appliance image, pushes it to GHCR as
`ghcr.io/<owner>/slfhst:v0.1.0`, builds the Anaconda ISO from *that*
registry reference (not local podman storage -- see `scripts/build_iso.py`'s
docstring for why: GitHub Actions' podman is rootless by default, so a
locally-built image never lands where bootc-image-builder looks for it),
and publishes a GitHub Release for the tag. Can also be run manually via
`workflow_dispatch` without a new tag.

The ISO ships as a direct GitHub Release asset -- measured at ~942MB for a
real build, comfortably under GitHub's 2GiB single-asset limit.

That measurement is worth spelling out, since the ISO's structure isn't
obvious: mounting a real built ISO shows an Anaconda live installer
environment (`images/install.img`, ~1.13GB -- the actual Anaconda
installer UI, kernel, and its own temporary root filesystem, needed to
run the installer before our deployed OS exists) *and* our own appliance
image embedded directly (`container/blobs/...`, ~1.17GB), which sums to
~2.6GB on paper -- yet the real ISO file is under a gigabyte. The
difference is content-level deduplication (composefs/ostree's
content-addressed storage): our image and Anaconda's own live environment
are both built from overlapping AlmaLinux packages and share a large
amount of identical file content, so the actual unique bytes on disc are
far less than the sum of the two logical sizes. None of this is
`bootc-image-builder` or AlmaLinux doing anything wrong -- an Anaconda
live environment of roughly this size is standard for any Anaconda-based
installer, and it isn't configurable via anything `bootc-image-builder`
exposes. `scripts/publish_release.py` fails loudly rather than silently
splitting/compressing if a future build ever grows past the limit, so
that stays a visible decision.

## Image size

`scripts/build_image.py` builds with `--squash-all`, not cosmetically --
the Containerfile removes several base-image packages in a layer *after*
the base image's own layers, and without squashing those bytes are still
physically present in the image (OCI layers are additive; removal just
adds whiteout markers). `tsflags=nodocs` + `install_weak_deps=False` are
also set globally in `/etc/dnf/dnf.conf` for anything installed after
that point.

Removed (all confirmed zero dependents via `rpm -q --whatrequires`
before removal, image content measured via `podman save`, not
`podman images`/`podman inspect .Size` which were unreliable for a
squashed image on this podman version):

- an unused SSSD/Samba/AD-auth stack + `toolbox` + `libicu` (~100MB)
- `linux-firmware` (~700MB) -- explicit decision to drop it for this
  deployment target (VPS). Real-world opinion on whether that's safe in
  a virtualized environment is genuinely split and provider-dependent
  (most virtio paths need no firmware; some accelerated-NIC cloud paths
  might); if this image ever needs to target bare metal or an unusual
  hypervisor path, reverting the `linux-firmware` line in the
  Containerfile is the lever.

Net result: **~1.94GB -> ~1.75GB** (`podman save` tarball size).

## Open items (not blocking, tracked so they don't get lost)

- Exact current image refs/tags for Stalwart, Bulwark, Ente museum
  (published image vs. build-from-source), Ente web, Vaultwarden, Traefik,
  Garage -- the templates in `usr/share/slfhst/templates/` have reasonable
  defaults but haven't been pinned against each project's current docs.
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
