package collector

import (
	"context"
	"log/slog"
	"strings"

	"github.com/elohmeier/dynatrace-license-exporter/internal/billing"
	"github.com/elohmeier/dynatrace-license-exporter/internal/config"
	"github.com/elohmeier/dynatrace-license-exporter/internal/dynatrace"
)

// HostEntityClient is the environment API surface used for host metadata enrichment.
type HostEntityClient interface {
	Entities(ctx context.Context, environmentID string, entityIDs []string) ([]dynatrace.Entity, error)
	KubernetesClusters(ctx context.Context, environmentID string, entityIDs []string) ([]dynatrace.Entity, error)
}

// HostTarget binds an environment configuration to its Entity API client.
type HostTarget struct {
	Environment config.Environment
	Client      HostEntityClient
}

type hostNameEnricher struct {
	targets        map[string]HostEntityClient
	names          map[string]string
	clusterByHost  map[string]string
	clusterDetails map[string]billing.KubernetesClusterInfo
	logger         *slog.Logger
}

func newHostNameEnricher(targets []HostTarget, logger *slog.Logger) *hostNameEnricher {
	clients := make(map[string]HostEntityClient, len(targets))
	for _, target := range targets {
		if target.Environment.ID != "" && target.Client != nil {
			clients[target.Environment.ID] = target.Client
		}
	}
	return &hostNameEnricher{
		targets:        clients,
		names:          make(map[string]string),
		clusterByHost:  make(map[string]string),
		clusterDetails: make(map[string]billing.KubernetesClusterInfo),
		logger:         logger,
	}
}

func (e *hostNameEnricher) enrich(ctx context.Context, snapshot *billing.Snapshot) {
	if e == nil || snapshot == nil {
		return
	}
	activeHosts := make(map[string]bool)
	activeClusters := make(map[string]bool)
	for environmentIndex := range snapshot.Environments {
		environment := &snapshot.Environments[environmentIndex]
		requested := make(map[string]bool, len(environment.Hosts))
		ids := make([]string, 0, len(environment.Hosts))
		for _, host := range environment.Hosts {
			if !requested[host.ID] {
				requested[host.ID] = true
				ids = append(ids, host.ID)
			}
		}
		if client := e.targets[environment.ID]; client != nil && len(ids) > 0 {
			entities, err := client.Entities(ctx, environment.ID, ids)
			if err != nil {
				if e.logger != nil {
					e.logger.Warn("host name enrichment failed", "environment_id", environment.ID, "err", err)
				}
			} else {
				for _, entity := range entities {
					if !requested[entity.EntityID] {
						continue
					}
					key := entityKey(environment.ID, entity.EntityID)
					name := strings.TrimSpace(entity.DisplayName)
					if name != "" {
						e.names[key] = name
					}
					if clusterID := kubernetesClusterEntityID(entity); clusterID != "" {
						e.clusterByHost[key] = clusterID
					} else {
						delete(e.clusterByHost, key)
					}
				}
			}

			clusterIDs := make(map[string]bool)
			for _, host := range environment.Hosts {
				if clusterID := e.clusterByHost[entityKey(environment.ID, host.ID)]; clusterID != "" {
					clusterIDs[clusterID] = true
				}
			}
			if len(clusterIDs) > 0 {
				clusters, clusterErr := client.KubernetesClusters(ctx, environment.ID, sortedKeys(clusterIDs))
				if clusterErr != nil {
					if e.logger != nil {
						e.logger.Warn("host Kubernetes cluster enrichment failed", "environment_id", environment.ID, "err", clusterErr)
					}
				} else {
					for _, cluster := range clusters {
						if !clusterIDs[cluster.EntityID] || cluster.Type != "KUBERNETES_CLUSTER" {
							continue
						}
						name := strings.TrimSpace(cluster.DisplayName)
						if name == "" {
							name = cluster.EntityID
						}
						e.clusterDetails[entityKey(environment.ID, cluster.EntityID)] = billing.KubernetesClusterInfo{
							EntityID:     cluster.EntityID,
							Name:         name,
							Distribution: stringProperty(cluster.Properties, "kubernetesDistribution"),
						}
					}
				}
			}
		}
		for hostIndex := range environment.Hosts {
			host := &environment.Hosts[hostIndex]
			key := entityKey(environment.ID, host.ID)
			activeHosts[key] = true
			if name := e.names[key]; name != "" {
				host.Name = name
			}
			if strings.TrimSpace(host.Name) == "" {
				host.Name = host.ID
			}
			if clusterID := e.clusterByHost[key]; clusterID != "" {
				clusterKey := entityKey(environment.ID, clusterID)
				activeClusters[clusterKey] = true
				if cluster, ok := e.clusterDetails[clusterKey]; ok {
					host.KubernetesCluster = cluster
				}
			}
		}
	}
	for key := range e.names {
		if !activeHosts[key] {
			delete(e.names, key)
		}
	}
	for key := range e.clusterByHost {
		if !activeHosts[key] {
			delete(e.clusterByHost, key)
		}
	}
	for key := range e.clusterDetails {
		if !activeClusters[key] {
			delete(e.clusterDetails, key)
		}
	}
}

func kubernetesClusterEntityID(entity dynatrace.Entity) string {
	ids := make(map[string]bool)
	for _, relationship := range entity.ToRelationships["isClusterOfHost"] {
		id := strings.TrimSpace(relationship.ID)
		if id != "" && relationship.Type == "KUBERNETES_CLUSTER" {
			ids[id] = true
		}
	}
	if len(ids) != 1 {
		return ""
	}
	for id := range ids {
		return id
	}
	return ""
}

func stringProperty(properties map[string]any, key string) string {
	value, _ := properties[key].(string)
	return strings.TrimSpace(value)
}

func entityKey(environmentID, entityID string) string {
	return environmentID + "\x00" + entityID
}
