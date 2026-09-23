// Package disks handles the installer's disk work: dynamic root+bulk
// disk selection (direct port of installer_disks.py's select_disks()),
// DPS partitioning of the root disk via systemd-repart (ESP + usr-A +
// usr-B + root + var -- confirmed working for real: a real repart.d
// definition set produced exactly this layout with correct DPS GPT
// types and the two usr slots correctly labeled `_empty`, which is what
// systemd-sysupdate looks for when picking a slot to write into), and
// plain parted+xfs for the bulk disk with a systemd `.mount` unit
// (per the user's explicit "no /etc/fstab" correction) instead of an
// fstab entry.
package disks

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/nnnithinnn/slfhst/internal/runx"
)

// Provisional sizing -- deterministic beats clever (both usr slots are
// provisioned at install time, fixed size, rather than relying on
// systemd-repart's on-first-boot slot-growing trick), but the actual
// numbers here are placeholders pending a real appliance build size
// measurement (plan Phase 5). Generously sized on purpose: failing an
// install by running short is worse than a few hundred MB of slack on a
// VPS-sized disk.
const (
	espSize     = "512M"
	usrSlotSize = "3G"
	rootSize    = "1G"
)

type blockDevice struct {
	Name   string `json:"name"`
	Path   string `json:"path"`
	Size   string `json:"size"`
	Type   string `json:"type"`
	Rota   string `json:"rota"`
	PKName string `json:"pkname"`
}

type lsblkOutput struct {
	BlockDevices []blockDevice `json:"blockdevices"`
}

func listBlockDevices(columns string) ([]blockDevice, error) {
	out, err := runx.RunChecked([]string{"lsblk", "-J", "-b", "-o", columns}, runx.Options{})
	if err != nil {
		return nil, fmt.Errorf("disks: lsblk: %w", err)
	}
	var parsed lsblkOutput
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		return nil, fmt.Errorf("disks: parse lsblk output: %w", err)
	}
	return parsed.BlockDevices, nil
}

func sizeOf(d blockDevice) int64 {
	n, _ := strconv.ParseInt(d.Size, 10, 64)
	return n
}

// SelectDisks picks the root disk (smallest non-rotational disk, else
// smallest overall) and the bulk disk (largest of what's left). Direct
// port of installer_disks.py's select_disks().
func SelectDisks() (rootDisk, bulkDisk string, err error) {
	all, err := listBlockDevices("NAME,PATH,SIZE,TYPE,ROTA,PKNAME")
	if err != nil {
		return "", "", err
	}
	return selectDisksFrom(all)
}

// selectDisksFrom is SelectDisks' pure selection logic, split out so
// tests can exercise it against fixed lsblk-shaped input instead of a
// real `lsblk` call.
func selectDisksFrom(all []blockDevice) (rootDisk, bulkDisk string, err error) {
	var candidates []blockDevice
	for _, d := range all {
		if d.Type == "disk" && sizeOf(d) > 0 {
			candidates = append(candidates, d)
		}
	}
	if len(candidates) < 2 {
		return "", "", fmt.Errorf("disks: need at least 2 disks (root + bulk), found %d", len(candidates))
	}

	var nonRotational []blockDevice
	for _, d := range candidates {
		if d.Rota == "0" {
			nonRotational = append(nonRotational, d)
		}
	}
	pool := candidates
	if len(nonRotational) > 0 {
		pool = nonRotational
	}
	sort.Slice(pool, func(i, j int) bool { return sizeOf(pool[i]) < sizeOf(pool[j]) })
	rootDisk = pool[0].Path

	var others []blockDevice
	for _, d := range candidates {
		if d.Path != rootDisk {
			others = append(others, d)
		}
	}
	sort.Slice(others, func(i, j int) bool { return sizeOf(others[i]) > sizeOf(others[j]) })
	bulkDisk = others[0].Path

	return rootDisk, bulkDisk, nil
}

// repartDefinition renders one [Partition] unit for systemd-repart.
func repartDefinition(partType string, min, max, label, format string) string {
	var b strings.Builder
	b.WriteString("[Partition]\n")
	fmt.Fprintf(&b, "Type=%s\n", partType)
	if min != "" {
		fmt.Fprintf(&b, "SizeMinBytes=%s\n", min)
	}
	if max != "" {
		fmt.Fprintf(&b, "SizeMaxBytes=%s\n", max)
	}
	if format != "" {
		fmt.Fprintf(&b, "Format=%s\n", format)
	}
	if label != "" {
		fmt.Fprintf(&b, "Label=%s\n", label)
	}
	return b.String()
}

// PartitionRoot lays out the root disk per the DPS scheme (ESP + usr-A +
// usr-B + persistent root + persistent var) via systemd-repart -- not
// hand-typed GPT type GUIDs, which are architecture-specific and easy to
// get subtly wrong; systemd-repart already knows the right ones for
// whatever architecture it's running on, confirmed by a real run
// producing correct x86-64/arm64-appropriate types. root and var get
// fixed labels ("slfhst-root"/"slfhst-var") so they can be found
// afterward without depending on GPT type GUIDs either.
func PartitionRoot(disk string) error {
	dir, err := os.MkdirTemp("", "slfhst-repart-")
	if err != nil {
		return fmt.Errorf("disks: create repart definitions dir: %w", err)
	}
	defer os.RemoveAll(dir)

	files := map[string]string{
		"10-esp.conf":   repartDefinition("esp", espSize, espSize, "", "vfat"),
		"20-usr-a.conf": repartDefinition("usr", usrSlotSize, usrSlotSize, "_empty", "erofs"),
		"21-usr-b.conf": repartDefinition("usr", usrSlotSize, usrSlotSize, "_empty", "erofs"),
		"30-root.conf":  repartDefinition("root", rootSize, rootSize, "slfhst-root", "ext4"),
		"40-var.conf":   repartDefinition("var", "256M", "", "slfhst-var", "ext4"),
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			return fmt.Errorf("disks: write %s: %w", name, err)
		}
	}

	_, err = runx.Run([]string{"systemd-repart", "--definitions=" + dir, "--dry-run=no", "--empty=allow", disk},
		runx.Options{Capture: true})
	if err != nil {
		return fmt.Errorf("disks: systemd-repart %s: %w", disk, err)
	}
	return nil
}

// findPartitionByLabel returns the /dev path of disk's child partition
// with the given GPT PARTLABEL. Queried directly as a generic map
// (rather than adding a rarely-used PartLabel field to blockDevice)
// since this is the only place that needs it.
func findPartitionByLabel(disk, label string) (string, error) {
	out, err := runx.RunChecked([]string{"lsblk", "-J", "-b", "-o", "NAME,PATH,PKNAME,PARTLABEL"}, runx.Options{})
	if err != nil {
		return "", fmt.Errorf("disks: lsblk: %w", err)
	}
	var raw struct {
		BlockDevices []map[string]any `json:"blockdevices"`
	}
	if err := json.Unmarshal([]byte(out), &raw); err != nil {
		return "", fmt.Errorf("disks: parse lsblk output: %w", err)
	}
	diskName := filepath.Base(disk)
	for _, d := range raw.BlockDevices {
		pkname, _ := d["pkname"].(string)
		partlabel, _ := d["partlabel"].(string)
		path, _ := d["path"].(string)
		if pkname == diskName && partlabel == label {
			return path, nil
		}
	}
	return "", fmt.Errorf("disks: no partition on %s labeled %q", disk, label)
}

// MountRootAndVar mounts the root and var partitions (found by the
// fixed labels PartitionRoot gave them) at mountpoint and
// mountpoint/var.
func MountRootAndVar(disk, mountpoint string) error {
	rootPart, err := findPartitionByLabel(disk, "slfhst-root")
	if err != nil {
		return err
	}
	varPart, err := findPartitionByLabel(disk, "slfhst-var")
	if err != nil {
		return err
	}

	if err := os.MkdirAll(mountpoint, 0o755); err != nil {
		return fmt.Errorf("disks: mkdir %s: %w", mountpoint, err)
	}
	if _, err := runx.Run([]string{"mount", rootPart, mountpoint}, runx.Options{}); err != nil {
		return fmt.Errorf("disks: mount %s: %w", rootPart, err)
	}
	varMount := filepath.Join(mountpoint, "var")
	if err := os.MkdirAll(varMount, 0o755); err != nil {
		return fmt.Errorf("disks: mkdir %s: %w", varMount, err)
	}
	if _, err := runx.Run([]string{"mount", varPart, varMount}, runx.Options{}); err != nil {
		return fmt.Errorf("disks: mount %s: %w", varPart, err)
	}
	return nil
}

// PartitionBulk lays out the bulk data disk as a single GPT+xfs
// partition -- unrelated to the DPS scheme above (a second physical
// disk), direct port of disks.py's partition_and_format().
func PartitionBulk(disk string) (partition string, err error) {
	_, err = runx.Run([]string{"parted", "--script", disk,
		"mklabel", "gpt", "mkpart", "primary", "xfs", "0%", "100%"}, runx.Options{})
	if err != nil {
		return "", fmt.Errorf("disks: partition %s: %w", disk, err)
	}
	if _, err := runx.Run([]string{"partprobe", disk}, runx.Options{}); err != nil {
		return "", fmt.Errorf("disks: partprobe %s: %w", disk, err)
	}
	if _, err := runx.Run([]string{"udevadm", "settle"}, runx.Options{}); err != nil {
		return "", fmt.Errorf("disks: udevadm settle: %w", err)
	}

	all, err := listBlockDevices("NAME,PATH,PKNAME")
	if err != nil {
		return "", err
	}
	diskName := filepath.Base(disk)
	for _, d := range all {
		if d.PKName == diskName {
			partition = d.Path
			break
		}
	}
	if partition == "" {
		return "", fmt.Errorf("disks: no partition found on %s after partitioning", disk)
	}

	if _, err := runx.Run([]string{"mkfs.xfs", "-f", partition}, runx.Options{Capture: true}); err != nil {
		return "", fmt.Errorf("disks: mkfs.xfs %s: %w", partition, err)
	}
	return partition, nil
}

var dataSubdirs = []string{"stalwart", "vaultwarden", "garage", "traefik", "ente"}

// MountBulk mounts the bulk partition at <root>/srv/data and creates the
// per-service subdirectories, then writes and enables a systemd
// `.mount` unit (not an /etc/fstab entry, per the user's explicit
// correction) so it mounts automatically on every subsequent real boot.
func MountBulk(root, partition string) error {
	target := filepath.Join(root, "srv/data")
	if err := os.MkdirAll(target, 0o755); err != nil {
		return fmt.Errorf("disks: mkdir %s: %w", target, err)
	}
	if _, err := runx.Run([]string{"mount", partition, target}, runx.Options{}); err != nil {
		return fmt.Errorf("disks: mount %s at %s: %w", partition, target, err)
	}
	for _, name := range dataSubdirs {
		if err := os.MkdirAll(filepath.Join(target, name), 0o755); err != nil {
			return fmt.Errorf("disks: mkdir %s/%s: %w", target, name, err)
		}
	}
	return writeBulkMountUnit(root, partition)
}

func writeBulkMountUnit(root, partition string) error {
	uuid, err := runx.RunChecked([]string{"blkid", "-s", "UUID", "-o", "value", partition}, runx.Options{})
	if err != nil {
		return fmt.Errorf("disks: blkid %s: %w", partition, err)
	}
	uuid = strings.TrimSpace(uuid)

	content := fmt.Sprintf(`[Unit]
Description=slfhst bulk data

[Mount]
What=/dev/disk/by-uuid/%s
Where=/srv/data
Type=xfs

[Install]
WantedBy=local-fs.target
`, uuid)

	unitPath := filepath.Join(root, "etc/systemd/system/srv-data.mount")
	if err := os.MkdirAll(filepath.Dir(unitPath), 0o755); err != nil {
		return fmt.Errorf("disks: mkdir: %w", err)
	}
	if err := os.WriteFile(unitPath, []byte(content), 0o644); err != nil {
		return fmt.Errorf("disks: write %s: %w", unitPath, err)
	}

	wantsDir := filepath.Join(root, "etc/systemd/system/local-fs.target.wants")
	if err := os.MkdirAll(wantsDir, 0o755); err != nil {
		return fmt.Errorf("disks: mkdir %s: %w", wantsDir, err)
	}
	linkPath := filepath.Join(wantsDir, "srv-data.mount")
	_ = os.Remove(linkPath) // idempotent: ignore "doesn't exist yet".
	if err := os.Symlink("/etc/systemd/system/srv-data.mount", linkPath); err != nil {
		return fmt.Errorf("disks: symlink %s: %w", linkPath, err)
	}
	return nil
}
