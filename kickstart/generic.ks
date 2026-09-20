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

with open("/tmp/part-include.ks", "w") as f:
    f.write(f"ignoredisk --only-use={boot_disk}\n")
    f.write("zerombr\n")
    f.write("clearpart --all --initlabel --disklabel=gpt\n")
    f.write(f"part /boot/efi --fstype=efi --size=512 --ondisk={boot_disk}\n")
    f.write(f"part /boot --fstype=xfs --size=1024 --ondisk={boot_disk}\n")
    f.write(f"part / --fstype=xfs --grow --size=1 --ondisk={boot_disk}\n")
%end

%include /tmp/part-include.ks

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
