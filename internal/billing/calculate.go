package billing

import (
	"fmt"
	"math"
	"sort"
	"time"
)

const bytesPerGiB = 1024 * 1024 * 1024

// FullStackHostUnits estimates host units from configured host memory.
func FullStackHostUnits(memoryBytes int64) float64 {
	gib := float64(memoryBytes) / bytesPerGiB
	switch {
	case gib <= 0:
		return 0
	case gib <= 1.6:
		return 0.10
	case gib <= 4:
		return 0.25
	case gib <= 8:
		return 0.50
	case gib <= 16:
		return 1
	default:
		return math.Ceil(gib / 16)
	}
}

// EstimatedHostUnits estimates billed host units for the host monitoring mode.
func EstimatedHostUnits(host HostUsage) float64 {
	memory := host.HostMemoryBytes
	if memory == 0 {
		memory = host.PassMemoryLimit
	}
	fullStack := FullStackHostUnits(memory)
	if host.InfrastructureOnly {
		return math.Min(fullStack*0.3, 1)
	}
	return fullStack
}

// EstimatedSyntheticDEMUnits estimates DEM consumption from monitor executions.
func EstimatedSyntheticDEMUnits(monitorTypeID int, executions float64) float64 {
	switch monitorTypeID {
	case 1:
		return executions * 0.1
	case 2:
		return executions
	default:
		return executions
	}
}

// EstimatedRUMDEMUnits estimates DEM consumption from RUM inputs.
func EstimatedRUMDEMUnits(entry EnvironmentEntry) float64 {
	return entry.Visits*0.25 +
		entry.MobileSessions*0.25 +
		entry.SessionReplays +
		entry.MobileSessionReplays +
		entry.TotalRUMUserPropertiesUsed*0.01
}

// CalculateSnapshot converts a valid billing document into an immutable snapshot.
func CalculateSnapshot(doc Document, environmentNames map[string]string) (*Snapshot, error) {
	if doc.TimeFrameStart <= 0 || doc.TimeFrameEnd <= doc.TimeFrameStart {
		return nil, fmt.Errorf("invalid billing interval %d..%d", doc.TimeFrameStart, doc.TimeFrameEnd)
	}
	snapshot := &Snapshot{
		PeriodStart: time.UnixMilli(doc.TimeFrameStart).UTC(),
		PeriodEnd:   time.UnixMilli(doc.TimeFrameEnd).UTC(),
	}
	for _, entry := range doc.EnvironmentBillingEntries {
		if entry.EnvironmentUUID == "" {
			return nil, fmt.Errorf("billing entry has no environment UUID")
		}
		name := environmentNames[entry.EnvironmentUUID]
		if name == "" {
			name = entry.EnvironmentUUID
		}
		env := EnvironmentSnapshot{
			ID:              entry.EnvironmentUUID,
			Name:            name,
			HostUnitsByMode: map[string]float64{"full_stack": 0, "infrastructure": 0},
			HostCountByMode: map[string]float64{"full_stack": 0, "infrastructure": 0},
			DEMBySource:     map[string]float64{"synthetic": 0, "rum": EstimatedRUMDEMUnits(entry)},
			RUMUsageByKind: map[string]float64{
				"visits":                 entry.Visits,
				"mobile_sessions":        entry.MobileSessions,
				"session_replays":        entry.SessionReplays,
				"mobile_session_replays": entry.MobileSessionReplays,
				"user_properties":        entry.TotalRUMUserPropertiesUsed,
			},
			DavisDataUnitsByPool: make(map[string]float64),
		}
		for _, host := range entry.HostUsages {
			mode := "full_stack"
			if host.InfrastructureOnly {
				mode = "infrastructure"
			}
			memory := host.HostMemoryBytes
			if memory == 0 {
				memory = host.PassMemoryLimit
			}
			units := EstimatedHostUnits(host)
			env.HostUnitsByMode[mode] += units
			env.HostCountByMode[mode]++
			env.Hosts = append(env.Hosts, HostSnapshot{
				ID:                  string(host.OSIId),
				Name:                host.HostName,
				Category:            host.HostCategory,
				MonitoringMode:      mode,
				PaaS:                host.PaaS,
				HasContainers:       host.HasContainers,
				PremiumLogAnalytics: host.PremiumLogAnalytics,
				MemoryBytes:         memory,
				EstimatedHostUnits:  units,
			})
		}
		for _, synthetic := range entry.SyntheticBillingUsage {
			monitorType := monitorTypeName(synthetic.MonitorTypeID)
			executions := synthetic.PublicExecutions + synthetic.PrivateExecutions
			units := EstimatedSyntheticDEMUnits(synthetic.MonitorTypeID, executions)
			env.DEMBySource["synthetic"] += units
			env.Synthetic = append(env.Synthetic, SyntheticSnapshot{
				TestID:            string(synthetic.TestID),
				MonitorType:       monitorType,
				PublicExecutions:  synthetic.PublicExecutions,
				PrivateExecutions: synthetic.PrivateExecutions,
				EstimatedDEMUnits: units,
			})
		}
		for _, ddu := range entry.DavisDataUnits {
			if ddu.Pool != "" {
				env.DavisDataUnitsByPool[ddu.Pool] += ddu.Total
			}
		}
		sort.Slice(env.Hosts, func(i, j int) bool { return env.Hosts[i].ID < env.Hosts[j].ID })
		sort.Slice(env.Synthetic, func(i, j int) bool { return env.Synthetic[i].TestID < env.Synthetic[j].TestID })
		snapshot.Environments = append(snapshot.Environments, env)
	}
	sort.Slice(snapshot.Environments, func(i, j int) bool {
		return snapshot.Environments[i].ID < snapshot.Environments[j].ID
	})
	return snapshot, nil
}

func monitorTypeName(id int) string {
	switch id {
	case 1:
		return "http"
	case 2:
		return "browser"
	default:
		return fmt.Sprintf("unknown_%d", id)
	}
}
