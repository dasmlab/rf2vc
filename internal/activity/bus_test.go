package activity

import "testing"

func TestBusRingAndChannels(t *testing.T) {
	b := New(4)
	b.Emit(Inbound, "info", "GET", "/redfish/v1/", nil)
	b.Emit(Runtime, "info", "folder-scan", "start", map[string]any{"n": 1})
	b.Emit(Outbound, "dryrun", "InsertMedia", "would attach", nil)
	b.Emit(Runtime, "info", "folder-scan", "done", nil)
	b.Emit(Runtime, "info", "folder-scan", "overflow", nil) // drops oldest inbound

	all := b.Snapshot("", 50)
	if len(all) != 4 {
		t.Fatalf("want 4 events after overflow, got %d", len(all))
	}
	rt := b.Snapshot(Runtime, 10)
	if len(rt) != 3 {
		t.Fatalf("runtime want 3 got %d", len(rt))
	}
	out := b.Since(0, Outbound, 10)
	if len(out) != 1 || !out[0].DryRun {
		t.Fatalf("outbound dry-run event missing: %+v", out)
	}
}
