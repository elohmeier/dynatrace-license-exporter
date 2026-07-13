package collector

import (
	"context"
	"errors"
	"testing"

	"github.com/elohmeier/dynatrace-license-exporter/internal/billing"
	"github.com/elohmeier/dynatrace-license-exporter/internal/config"
	"github.com/elohmeier/dynatrace-license-exporter/internal/dynatrace"
)

func TestKubernetesClusterEntityID(t *testing.T) {
	tests := []struct {
		name          string
		relationships []dynatrace.EntityReference
		want          string
	}{
		{name: "missing"},
		{name: "wrong entity type", relationships: []dynatrace.EntityReference{{ID: "HOST-EXAMPLE", Type: "HOST"}}},
		{name: "one cluster", relationships: []dynatrace.EntityReference{{ID: "KUBERNETES_CLUSTER-EXAMPLE", Type: "KUBERNETES_CLUSTER"}}, want: "KUBERNETES_CLUSTER-EXAMPLE"},
		{name: "trimmed ID", relationships: []dynatrace.EntityReference{{ID: " KUBERNETES_CLUSTER-EXAMPLE ", Type: "KUBERNETES_CLUSTER"}}, want: "KUBERNETES_CLUSTER-EXAMPLE"},
		{name: "duplicate relationship", relationships: []dynatrace.EntityReference{{ID: "KUBERNETES_CLUSTER-EXAMPLE", Type: "KUBERNETES_CLUSTER"}, {ID: "KUBERNETES_CLUSTER-EXAMPLE", Type: "KUBERNETES_CLUSTER"}}, want: "KUBERNETES_CLUSTER-EXAMPLE"},
		{name: "ambiguous clusters", relationships: []dynatrace.EntityReference{{ID: "KUBERNETES_CLUSTER-EXAMPLE-A", Type: "KUBERNETES_CLUSTER"}, {ID: "KUBERNETES_CLUSTER-EXAMPLE-B", Type: "KUBERNETES_CLUSTER"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			entity := dynatrace.Entity{ToRelationships: map[string][]dynatrace.EntityReference{"isClusterOfHost": tt.relationships}}
			if got := kubernetesClusterEntityID(entity); got != tt.want {
				t.Fatalf("kubernetesClusterEntityID() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestHostPlatformEnrichmentLifecycle(t *testing.T) {
	const (
		environmentID = "environment-example"
		hostID        = "HOST-EXAMPLE"
	)
	client := &fakeHostEntityClient{entities: []dynatrace.Entity{{
		EntityID: hostID,
		Type:     "HOST",
		Properties: map[string]any{
			"hostGroupName": " Synthetic Group A ",
			"networkZone":   " synthetic-zone ",
			"autoInjection": "DISABLED_MANUALLY",
		},
	}}}
	enricher := newHostNameEnricher([]HostTarget{{
		Environment: config.Environment{ID: environmentID, Name: "Example"},
		Client:      client,
	}}, nil)
	newSnapshot := func() *billing.Snapshot {
		return &billing.Snapshot{Environments: []billing.EnvironmentSnapshot{{
			ID: environmentID, Name: "Example", Hosts: []billing.HostSnapshot{{ID: hostID}},
		}}}
	}

	snapshot := newSnapshot()
	enricher.enrich(context.Background(), snapshot)
	got := snapshot.Environments[0].Hosts[0].Platform
	want := (billing.HostPlatformInfo{HostGroup: "Synthetic Group A", NetworkZone: "synthetic-zone", AutoInjection: "disabled_manually"})
	if got != want {
		t.Fatalf("platform = %+v, want %+v", got, want)
	}

	client.mu.Lock()
	client.entities[0].Properties = map[string]any{"hostGroupName": "Synthetic Group B"}
	client.mu.Unlock()
	snapshot = newSnapshot()
	enricher.enrich(context.Background(), snapshot)
	got = snapshot.Environments[0].Hosts[0].Platform
	want = billing.HostPlatformInfo{HostGroup: "Synthetic Group B"}
	if got != want {
		t.Fatalf("updated platform = %+v, want %+v", got, want)
	}

	client.mu.Lock()
	client.err = errors.New("synthetic entity API failure")
	client.mu.Unlock()
	snapshot = newSnapshot()
	enricher.enrich(context.Background(), snapshot)
	if got := snapshot.Environments[0].Hosts[0].Platform; got != want {
		t.Fatalf("retained platform = %+v, want %+v", got, want)
	}

	enricher.enrich(context.Background(), &billing.Snapshot{Environments: []billing.EnvironmentSnapshot{{ID: environmentID}}})
	if len(enricher.platformByHost) != 0 {
		t.Fatalf("platform cache size = %d, want 0", len(enricher.platformByHost))
	}
}
