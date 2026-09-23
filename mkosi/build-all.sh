#!/bin/bash
# Orchestrates the whole appliance -> installer -> ISO pipeline in one
# shot, run inside a single privileged Fedora container in CI (see
# publish.yml) -- keeps all three mkosi/build-iso.sh steps sharing one
# filesystem, avoiding cross-container artifact handoff entirely.
#
# Expects to run as root inside a Fedora container with `mkosi`,
# `xorriso`, `grub2-tools-extra` available (installed by the caller, or
# by this script itself -- see publish.yml), the repo checked out at the
# working directory this script's own directory is two levels under, and
# the `slfhst` Go binary already built (on the host, before entering the
# container -- no Go toolchain needed in here) at
# mkosi/appliance/mkosi.extra/usr/bin/slfhst and
# mkosi/installer/mkosi.extra/usr/bin/slfhst.
set -euo pipefail

VERSION="${1:?usage: build-all.sh <version> <output-dir>}"
OUTDIR="${2:?usage: build-all.sh <version> <output-dir>}"

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

for f in mkosi/appliance/mkosi.extra/usr/bin/slfhst mkosi/installer/mkosi.extra/usr/bin/slfhst; do
    if [ ! -x "$f" ]; then
        echo "build-all.sh: $f missing or not executable -- build the Go binary before running this" >&2
        exit 1
    fi
done

echo "=== 1/4: appliance build (version $VERSION) ==="
( cd mkosi/appliance && mkosi --image-version="$VERSION" -f build )

USR_RAW="$(compgen -G "mkosi/appliance/slfhst_${VERSION}.usr-*.raw" | head -n1)"
UKI_EFI="mkosi/appliance/slfhst_${VERSION}.efi"
if [ -z "$USR_RAW" ] || [ ! -e "$UKI_EFI" ]; then
    echo "build-all.sh: expected split artifacts not found (usr_raw=$USR_RAW uki=$UKI_EFI)" >&2
    exit 1
fi

echo "=== 2/4: embedding appliance artifacts into the installer ==="
mkdir -p mkosi/installer/mkosi.extra/usr/share/slfhst/appliance
cp "$USR_RAW" mkosi/installer/mkosi.extra/usr/share/slfhst/appliance/usr.raw
cp "$UKI_EFI" mkosi/installer/mkosi.extra/usr/share/slfhst/appliance/uki.efi
printf '%s' "$VERSION" > mkosi/installer/mkosi.extra/usr/share/slfhst/appliance/VERSION

echo "=== 3/4: installer build ==="
( cd mkosi/installer && mkosi -f build )

echo "=== 4/4: ISO assembly ==="
mkdir -p "$OUTDIR"
mkosi/installer/build-iso.sh mkosi/installer/slfhst-installer "$OUTDIR/slfhst-installer.iso"

cp "$USR_RAW" "$OUTDIR/slfhst_${VERSION}.usr.raw"
cp "$UKI_EFI" "$OUTDIR/slfhst_${VERSION}.efi"
( cd "$OUTDIR" && sha256sum -- *.raw *.efi *.iso > SHA256SUMS )

echo "=== done: $OUTDIR ==="
ls -la "$OUTDIR"
