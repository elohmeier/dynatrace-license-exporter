package collector

import (
	"context"
	"log/slog"
	"strings"

	"github.com/elohmeier/dynatrace-license-exporter/internal/billing"
	"github.com/elohmeier/dynatrace-license-exporter/internal/config"
	"github.com/elohmeier/dynatrace-license-exporter/internal/dynatrace"
)

// HostEntityClient is the environment API operation used for host-name enrichment.
type HostEntityClient interface {
	Entities(ctx context.Context, environmentID string, entityIDs []string) ([]dynatrace.Entity, error)
}

// HostTarget binds an environment configuration to its Entity API client.
type HostTarget struct {
	Environment config.Environment
	Client      HostEntityClient
}

type hostNameEnricher struct {
	targets map[string]HostEntityClient
	names   map[string]string
	logger  *slog.Logger
}

func newHostNameEnricher(targets []HostTarget, logger *slog.Logger) *hostNameEnricher {
	clients := make(map[string]HostEntityClient, len(targets))
	for _, target := range targets {
		if target.Environment.ID != "" && target.Client != nil {
			clients[target.Environment.ID] = target.Client
		}
	}
	return &hostNameEnricher{
		targets: clients,
		names:   make(map[string]string),
		logger:  logger,
	}
}

func (e *hostNameEnricher) enrich(ctx context.Context, snapshot *billing.Snapshot) {
	if e == nil || snapshot == nil {
		return
	}
	activeNames := make(map[string]bool)
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
					name := strings.TrimSpace(entity.DisplayName)
					if requested[entity.EntityID] && name != "" {
						e.names[hostNameKey(environment.ID, entity.EntityID)] = name
					}
				}
			}
		}
		for hostIndex := range environment.Hosts {
			host := &environment.Hosts[hostIndex]
			key := hostNameKey(environment.ID, host.ID)
			activeNames[key] = true
			if name := e.names[key]; name != "" {
				host.Name = name
			}
			if strings.TrimSpace(host.Name) == "" {
				host.Name = host.ID
			}
		}
	}
	for key := range e.names {
		if !activeNames[key] {
			delete(e.names, key)
		}
	}
}

func hostNameKey(environmentID, hostID string) string {
	return environmentID + "\x00" + hostID
}
