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
- `kickstart/generic.ks` -- Anaconda kickstart. No `%pre` (see the file's
  own comment for why, after real installs falsified two `%pre`-based
  fixes). Targets `/dev/vda` explicitly (`ignoredisk --only-use=`) --
  hardcoded on purpose: `autopart --type=plain` alone doesn't guarantee
  single-disk use (also learned the hard way), pykickstart has no
  declarative disk-selection predicate, and `/dev/vda` is the standard
  virtio-blk convention on the real KVM/VPS targets this image is built
  for. Not fully generic across every possible disk-naming scheme any
  more -- see Open items. Stage1's `disks.py` discovers whichever disk
  *isn't* root at first boot and claims it as bulk storage, independent of
  whatever device name the kickstart used.
- `scripts/` -- `build_image.py` (podman build), `build_iso.py` (qcow2 or
  anaconda-iso via bootc-image-builder), `check.py` (byte-compile + import
  every module, what CI runs first).
- `.github/workflows/` -- `ci.yml` (check + build on every PR), `publish.yml`
  (on every merge to `main` -- or manual dispatch -- builds+pushes the bootc
  image to GHCR **and** builds+republishes the Anaconda installer ISO from
  that exact image, together, every time -- see "Publishing" below).
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

## Publishing

One workflow, one rule: **if we build something, we build everything.**
Every push to `main` (or a manual `workflow_dispatch`) builds the
appliance image *and* the Anaconda installer ISO from that exact image, in
the same run, and republishes both -- there's no separate, semver-tag-gated
"cut a release" step any more. That's deliberate: installs happen fully
offline (see the build/verify loop above), so an ISO embedding a stale
image isn't just outdated, it's the thing an offline install actually
boots -- keeping the image build and the ISO build in one run is what
guarantees they always match, rather than drifting apart between whenever
someone remembered to tag a release.

Five steps (`publish.yml`), no more: **1. Precheck** (`check.py`) ->
**2. Build image** (`podman build`, local only) -> **3. Build ISO**
(`bootc-image-builder`, fed the *local* image directly -- `localhost/
slfhst:latest`, no GHCR round-trip -- see `build_iso.py`'s docstring for
how a rootless-podman-built local image gets into bootc-image-builder's
required rootful storage) -> **4. Publish image** (push to GHCR as
`:stable` + a dated tag, what `slfhst-bootc-update.timer` on
already-running nodes tracks) -> **5. Publish ISO** (the single rolling
`latest` GitHub Release, replacing whatever ISO was there before --
`scripts/publish_release.py` deletes-then-recreates it each run, since
`gh release create` refuses to reuse an existing tag).

Building the ISO from the local image rather than a GHCR reference isn't
just simpler -- it means the ISO build has no network dependency on this
run's own GHCR push succeeding first, and can't end up embedding a
different image than the one that gets pushed, since both come from the
exact same local build.

Git tags aren't part of this any more -- tag something yourself
(`git tag v0.2.0 && git push origin v0.2.0`) if you want a bookkeeping
checkpoint, but CI doesn't react to it either way.

The ISO ships as a direct GitHub Release asset. Its size has varied
meaningfully between real builds so far -- ~942MB (v0.1.0, EL9) and
~1.63GB (v0.1.1, EL10 + more debloat, where the appliance image itself is
*smaller*) -- both comfortably under GitHub's 2GiB limit, but with enough
swing that it shouldn't be assumed stable. `scripts/publish_release.py`
fails loudly rather than silently splitting/compressing if a future
build ever actually exceeds the limit, so that stays a visible decision
if it happens rather than a silent break.

The ISO's structure, from mounting real builds: an Anaconda live
installer environment (`images/install.img` -- the actual installer UI,
kernel, and its own temporary root filesystem, needed to run the
installer before our deployed OS exists) plus our own appliance image
embedded directly (`container/blobs/...`). For v0.1.1 those two plus the
initrd/kernel/EFI boot images summed to almost exactly the real file
size (~1.6GB logical vs ~1.63GB actual) -- no surprises. For v0.1.0 the
same accounting summed to ~2.3GB logical against a ~942MB actual file, a
gap large enough that it's most likely sparse-file allocation in how
bootc-image-builder sizes the composefs/erofs images varying between
builds, not a stable, reproducible saving -- flagged here as an
open question rather than a confidently-explained mechanism, since a
second measurement contradicted the first attempt at explaining it.
None of this is `bootc-image-builder` or AlmaLinux doing anything wrong
either way -- an Anaconda live environment of roughly this size is
standard for any Anaconda-based installer, and its size isn't
configurable via anything `bootc-image-builder` exposes.

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

- `kickstart/generic.ks` hardcodes `ignoredisk --only-use=/dev/vda`
  (2026-09-22, corrected from an earlier, now-disproven claim that
  `autopart --type=plain` alone guaranteed single-disk use -- a real
  install showed it partitioning both disks). Confirmed correct for the
  real target, not just assumed: GreenCloudVPS always attaches the
  small/fast disk as `vda` first and the bulk disk as `vdb` on multi-disk
  plans. A different provider or a non-virtio device naming scheme
  (`nvme0n1`, ...) would need this line changed -- revisit only if/when
  that actually happens, rather than re-generalizing preemptively (the
  last three attempts at a fully generic version all failed against real
  installs).
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
