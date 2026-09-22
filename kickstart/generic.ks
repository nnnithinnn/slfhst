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

# Anaconda resolves %include while it's still scanning the document for
# %pre blocks to run -- i.e. BEFORE this script has actually executed --
# so a side file referenced via %include here doesn't exist yet and the
# install aborts immediately ("Unable to open input kickstart file...
# No such file or directory"). Found by testing a real ISO boot, not
# assumed: the %pre+%include-a-side-file pattern looks standard but isn't
# how Anaconda actually orders things. The real mechanism: Anaconda
# re-reads /tmp/ks.cfg (the live kickstart file itself) from disk after
# every %pre script finishes, and re-parses whatever's there -- so %pre
# appends straight to the kickstart file in place instead of writing a
# separate file for %include to pull in.
with open("/tmp/ks.cfg", "a") as f:
    f.write(f"\nignoredisk --only-use={boot_disk}\n")
    f.write("zerombr\n")
    f.write("clearpart --all --initlabel --disklabel=gpt\n")
    f.write(f"part /boot/efi --fstype=efi --size=512 --ondisk={boot_disk}\n")
    f.write(f"part /boot --fstype=xfs --size=1024 --ondisk={boot_disk}\n")
    f.write(f"part / --fstype=xfs --grow --size=1 --ondisk={boot_disk}\n")
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
