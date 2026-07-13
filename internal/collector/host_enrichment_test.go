package collector

import (
	"testing"

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
