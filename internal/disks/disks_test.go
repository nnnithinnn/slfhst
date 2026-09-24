package disks

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestParseLsblkOutputAcceptsBothSizeAndRotaShapes is a regression test
// for a real bug found by actually running `slfhst install` end to end
// against a real Fedora 44 container (not caught by any of the other
// tests here, which all construct blockDevice literals directly rather
// than through JSON): older util-linux quotes lsblk -J's SIZE/ROTA
// fields as strings, but Fedora 44's util-linux 2.41.5 -- the same
// version the real appliance/installer ship -- emits raw JSON number/
// boolean instead. This broke SelectDisks, the very first thing
// `slfhst install` does, unconditionally, on the real target.
func TestParseLsblkOutputAcceptsBothSizeAndRotaShapes(t *testing.T) {
	for name, raw := range map[string]string{
		"quoted (older util-linux)": `{"blockdevices":[{"name":"vda","path":"/dev/vda","size":"10737418240","type":"disk","rota":"0","pkname":null}]}`,
		"raw (util-linux 2.41.5)":   `{"blockdevices":[{"name":"vda","path":"/dev/vda","size":10737418240,"type":"disk","rota":false,"pkname":null}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			var parsed lsblkOutput
			if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
				t.Fatalf("json.Unmarshal: %v", err)
			}
			if len(parsed.BlockDevices) != 1 {
				t.Fatalf("got %d block devices, want 1", len(parsed.BlockDevices))
			}
			d := parsed.BlockDevices[0]
			if sizeOf(d) != 10737418240 {
				t.Errorf("sizeOf = %d, want 10737418240", sizeOf(d))
			}
			if bool(d.Rota) {
				t.Errorf("Rota = true, want false")
			}
		})
	}
}

func TestSelectDisksFromPrefersSmallestNonRotational(t *testing.T) {
	all := []blockDevice{
		{Path: "/dev/vda", Type: "disk", Size: "10737418240", Rota: true},   // 10G, HDD
		{Path: "/dev/vdb", Type: "disk", Size: "21474836480", Rota: false},  // 20G, SSD -- root (smaller of the SSDs)
		{Path: "/dev/vdc", Type: "disk", Size: "107374182400", Rota: false}, // 100G, SSD -- bulk (largest of what's left)
	}
	root, bulk, err := selectDisksFrom(all, "")
	if err != nil {
		t.Fatalf("selectDisksFrom: %v", err)
	}
	if root != "/dev/vdb" {
		t.Errorf("root = %q, want /dev/vdb (smallest non-rotational)", root)
	}
	if bulk != "/dev/vdc" {
		t.Errorf("bulk = %q, want /dev/vdc (largest of what's left)", bulk)
	}
}

func TestSelectDisksFromFallsBackToSmallestOverall(t *testing.T) {
	// No non-rotational disks at all -- falls back to smallest overall
	// for root, largest of the rest for bulk.
	all := []blockDevice{
		{Path: "/dev/vda", Type: "disk", Size: "10737418240", Rota: true},
		{Path: "/dev/vdb", Type: "disk", Size: "107374182400", Rota: true},
	}
	root, bulk, err := selectDisksFrom(all, "")
	if err != nil {
		t.Fatalf("selectDisksFrom: %v", err)
	}
	if root != "/dev/vda" || bulk != "/dev/vdb" {
		t.Errorf("root=%q bulk=%q, want vda/vdb", root, bulk)
	}
}

func TestSelectDisksFromIgnoresNonDiskEntries(t *testing.T) {
	all := []blockDevice{
		{Path: "/dev/vda", Type: "disk", Size: "10737418240", Rota: false},
		{Path: "/dev/vda1", Type: "part", Size: "1073741824", Rota: false}, // a partition, not a disk
		{Path: "/dev/vdb", Type: "disk", Size: "21474836480", Rota: false},
	}
	root, bulk, err := selectDisksFrom(all, "")
	if err != nil {
		t.Fatalf("selectDisksFrom: %v", err)
	}
	if root != "/dev/vda" || bulk != "/dev/vdb" {
		t.Errorf("root=%q bulk=%q, want vda/vdb (partition entry should be ignored)", root, bulk)
	}
}

func TestSelectDisksFromRequiresTwoDisks(t *testing.T) {
	all := []blockDevice{{Path: "/dev/vda", Type: "disk", Size: "10737418240", Rota: false}}
	if _, _, err := selectDisksFrom(all, ""); err == nil {
		t.Fatal("expected an error with only one disk")
	}
}

// TestSelectDisksFromExcludesBootDevice is a regression test for a real,
// install-destroying bug found by direct user report on real hardware:
// the installer's own boot media (a virtual/USB CD attached by an
// IPMI/BMC-style remote console) is commonly reported by lsblk as
// TYPE="disk", not "rom" -- exactly the shape the candidate filter looks
// for. Since boot media is often the smallest device present, it was
// getting picked as the ROOT disk and would have been partitioned over.
func TestSelectDisksFromExcludesBootDevice(t *testing.T) {
	all := []blockDevice{
		{Name: "sr0", Path: "/dev/sr0", Type: "disk", Size: "734003200", Rota: false},    // boot ISO, ~700M, smallest -- must be excluded
		{Name: "vda", Path: "/dev/vda", Type: "disk", Size: "21474836480", Rota: false},  // 20G, SSD -- real root
		{Name: "vdb", Path: "/dev/vdb", Type: "disk", Size: "107374182400", Rota: false}, // 100G, SSD -- real bulk
	}
	root, bulk, err := selectDisksFrom(all, "sr0")
	if err != nil {
		t.Fatalf("selectDisksFrom: %v", err)
	}
	if root != "/dev/vda" {
		t.Errorf("root = %q, want /dev/vda (boot device sr0 must be excluded despite being smallest)", root)
	}
	if bulk != "/dev/vdb" {
		t.Errorf("bulk = %q, want /dev/vdb", bulk)
	}
}

// TestSelectDisksFromExcludesBootDeviceWhenOnlyTwoDisksRemain confirms
// exclusion still leaves an error (not a silent 2-disk selection that
// includes the boot device) when only the boot device plus one real disk
// are present -- excluding the boot device must reduce the candidate
// pool the same way a too-small disk count does.
func TestSelectDisksFromExcludesBootDeviceWhenOnlyTwoDisksRemain(t *testing.T) {
	all := []blockDevice{
		{Name: "sr0", Path: "/dev/sr0", Type: "disk", Size: "734003200", Rota: false},
		{Name: "vda", Path: "/dev/vda", Type: "disk", Size: "21474836480", Rota: false},
	}
	if _, _, err := selectDisksFrom(all, "sr0"); err == nil {
		t.Fatal("expected an error: only one real disk remains once the boot device is excluded")
	}
}

// TestEnsureFHSSymlinks is a regression test for a real, severe bug
// found by actually running the installer end to end: nothing in this
// project's split root/usr DPS design ever created these symlinks, so
// `chroot <target> sshd -t` failed with a misleading "No such file or
// directory" for a real, present sshd binary -- its dynamic linker
// couldn't be found via the absolute /lib/... path with no /lib symlink
// at the new root. Would have failed identically on the real deployed
// appliance's first actual boot.
func TestEnsureFHSSymlinks(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{"usr/bin", "usr/lib"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// No usr/lib64 or usr/sbin -- confirms the "skip if the usr side
	// doesn't exist" behavior for architectures/builds that lack them.

	if err := EnsureFHSSymlinks(root); err != nil {
		t.Fatalf("EnsureFHSSymlinks: %v", err)
	}

	for name, want := range map[string]string{"bin": "usr/bin", "lib": "usr/lib"} {
		got, err := os.Readlink(filepath.Join(root, name))
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if got != want {
			t.Errorf("%s -> %q, want %q", name, got, want)
		}
	}
	for _, name := range []string{"lib64", "sbin"} {
		if _, err := os.Lstat(filepath.Join(root, name)); err == nil {
			t.Errorf("%s: expected no symlink when usr/%s doesn't exist", name, name)
		}
	}

	// Idempotent: calling again with the symlinks already present must
	// not error (matches other installer steps' re-run safety).
	if err := EnsureFHSSymlinks(root); err != nil {
		t.Fatalf("EnsureFHSSymlinks (second call): %v", err)
	}
}

func TestRepartDefinitionRendersAllFields(t *testing.T) {
	got := repartDefinition("usr", "3G", "3G", "_empty", "erofs")
	for _, want := range []string{"[Partition]", "Type=usr", "SizeMinBytes=3G", "SizeMaxBytes=3G", "Format=erofs", "Label=_empty"} {
		if !strings.Contains(got, want) {
			t.Errorf("repartDefinition output missing %q:\n%s", want, got)
		}
	}
}

func TestRepartDefinitionOmitsEmptyFields(t *testing.T) {
	// var partition: only a minimum size, no max (grows to fill the rest).
	got := repartDefinition("var", "256M", "", "slfhst-var", "ext4")
	if strings.Contains(got, "SizeMaxBytes") {
		t.Errorf("expected no SizeMaxBytes line when max is empty:\n%s", got)
	}
}
