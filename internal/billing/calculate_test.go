package billing

import (
	"math"
	"testing"
)

func TestFullStackHostUnits(t *testing.T) {
	giB := int64(bytesPerGiB)
	tests := []struct {
		name   string
		memory int64
		want   float64
	}{
		{"missing", 0, 0},
		{"one_gib", giB, 0.1},
		{"four_gib", 4 * giB, 0.25},
		{"eight_gib", 8 * giB, 0.5},
		{"sixteen_gib", 16 * giB, 1},
		{"seventeen_gib", 17 * giB, 2},
		{"thirty_two_gib", 32 * giB, 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := FullStackHostUnits(tt.memory); got != tt.want {
				t.Fatalf("FullStackHostUnits(%d) = %v, want %v", tt.memory, got, tt.want)
			}
		})
	}
}

func TestEstimatedHostUnits(t *testing.T) {
	host := HostUsage{HostMemoryBytes: 64 * bytesPerGiB, InfrastructureOnly: true}
	if got := EstimatedHostUnits(host); got != 1 {
		t.Fatalf("infrastructure cap = %v, want 1", got)
	}
	host = HostUsage{PassMemoryLimit: 8 * bytesPerGiB}
	if got := EstimatedHostUnits(host); got != 0.5 {
		t.Fatalf("pass memory fallback = %v, want 0.5", got)
	}
}

func TestEstimatedDEMUnits(t *testing.T) {
	if got := EstimatedSyntheticDEMUnits(1, 20); got != 2 {
		t.Fatalf("HTTP monitor DEM = %v, want 2", got)
	}
	if got := EstimatedSyntheticDEMUnits(2, 20); got != 20 {
		t.Fatalf("browser monitor DEM = %v, want 20", got)
	}
	if got := EstimatedSyntheticDEMUnits(99, 20); got != 20 {
		t.Fatalf("unknown monitor DEM = %v, want 20", got)
	}
	entry := EnvironmentEntry{Visits: 8, MobileSessions: 4, SessionReplays: 2, MobileSessionReplays: 1, TotalRUMUserPropertiesUsed: 10}
	if got := EstimatedRUMDEMUnits(entry); math.Abs(got-6.1) > 1e-9 {
		t.Fatalf("RUM DEM = %v, want 6.1", got)
	}
}

func TestCalculateSnapshotValidationAndUnknownMonitor(t *testing.T) {
	if _, err := CalculateSnapshot(Document{}, nil); err == nil {
		t.Fatal("invalid interval unexpectedly succeeded")
	}
	doc := Document{
		TimeFrameStart: 1,
		TimeFrameEnd:   2,
		EnvironmentBillingEntries: []EnvironmentEntry{{
			EnvironmentUUID:       "environment-example",
			SyntheticBillingUsage: []SyntheticUsage{{MonitorTypeID: 99, TestID: "test"}},
		}},
	}
	snapshot, err := CalculateSnapshot(doc, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := snapshot.Environments[0].Synthetic[0].MonitorType; got != "unknown_99" {
		t.Fatalf("monitor type = %q", got)
	}
	doc.EnvironmentBillingEntries[0].EnvironmentUUID = ""
	if _, err := CalculateSnapshot(doc, nil); err == nil {
		t.Fatal("missing environment UUID unexpectedly succeeded")
	}
}
