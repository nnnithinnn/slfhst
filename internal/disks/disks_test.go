package disks

import (
	"strings"
	"testing"
)

func TestSelectDisksFromPrefersSmallestNonRotational(t *testing.T) {
	all := []blockDevice{
		{Path: "/dev/vda", Type: "disk", Size: "10737418240", Rota: "1"},  // 10G, HDD
		{Path: "/dev/vdb", Type: "disk", Size: "21474836480", Rota: "0"},  // 20G, SSD -- root (smaller of the SSDs)
		{Path: "/dev/vdc", Type: "disk", Size: "107374182400", Rota: "0"}, // 100G, SSD -- bulk (largest of what's left)
	}
	root, bulk, err := selectDisksFrom(all)
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
		{Path: "/dev/vda", Type: "disk", Size: "10737418240", Rota: "1"},
		{Path: "/dev/vdb", Type: "disk", Size: "107374182400", Rota: "1"},
	}
	root, bulk, err := selectDisksFrom(all)
	if err != nil {
		t.Fatalf("selectDisksFrom: %v", err)
	}
	if root != "/dev/vda" || bulk != "/dev/vdb" {
		t.Errorf("root=%q bulk=%q, want vda/vdb", root, bulk)
	}
}

func TestSelectDisksFromIgnoresNonDiskEntries(t *testing.T) {
	all := []blockDevice{
		{Path: "/dev/vda", Type: "disk", Size: "10737418240", Rota: "0"},
		{Path: "/dev/vda1", Type: "part", Size: "1073741824", Rota: "0"}, // a partition, not a disk
		{Path: "/dev/vdb", Type: "disk", Size: "21474836480", Rota: "0"},
	}
	root, bulk, err := selectDisksFrom(all)
	if err != nil {
		t.Fatalf("selectDisksFrom: %v", err)
	}
	if root != "/dev/vda" || bulk != "/dev/vdb" {
		t.Errorf("root=%q bulk=%q, want vda/vdb (partition entry should be ignored)", root, bulk)
	}
}

func TestSelectDisksFromRequiresTwoDisks(t *testing.T) {
	all := []blockDevice{{Path: "/dev/vda", Type: "disk", Size: "10737418240", Rota: "0"}}
	if _, _, err := selectDisksFrom(all); err == nil {
		t.Fatal("expected an error with only one disk")
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
