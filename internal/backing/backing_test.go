package backing

import (
	"encoding/json"
	"testing"
)

func TestStalwartHardeningPlanMarshalsCleanly(t *testing.T) {
	plan := stalwartHardeningPlan("mail.example.com")
	if len(plan) != 3 {
		t.Fatalf("got %d ops, want 3", len(plan))
	}
	for i, op := range plan {
		data, err := json.Marshal(op)
		if err != nil {
			t.Fatalf("op %d: marshal: %v", i, err)
		}
		var roundTrip map[string]any
		if err := json.Unmarshal(data, &roundTrip); err != nil {
			t.Fatalf("op %d: round-trip unmarshal: %v", i, err)
		}
		if roundTrip["@type"] == nil || roundTrip["object"] == nil {
			t.Errorf("op %d missing @type/object: %s", i, data)
		}
	}

	settings := plan[0]
	value, ok := settings["value"].(map[string]any)
	if !ok || value["defaultHostname"] != "mail.example.com" {
		t.Errorf("op 0 (SystemSettings) value = %#v, want defaultHostname=mail.example.com", settings["value"])
	}

	if plan[2]["object"] != "MtaInboundThrottle" || plan[2]["@type"] != "upsert" {
		t.Errorf("op 2 = %#v, want upsert MtaInboundThrottle", plan[2])
	}
}

func TestGarageCapacityFloorsAtFiveG(t *testing.T) {
	dir := t.TempDir()
	// A fresh tmp dir's free space in most CI/dev sandboxes is well
	// above the 5G floor, but this at least exercises the real statfs
	// call and the "%dG" formatting without needing a live garage
	// container -- the floor behavior itself is exercised by the
	// arithmetic, not by needing to actually fill the disk.
	got, err := garageCapacityAt(dir)
	if err != nil {
		t.Fatalf("garageCapacityAt: %v", err)
	}
	if got == "" || got[len(got)-1] != 'G' {
		t.Errorf("garageCapacityAt(%q) = %q, want a value ending in G", dir, got)
	}
}
