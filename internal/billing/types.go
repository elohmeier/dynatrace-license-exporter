package billing

import (
	"encoding/json"
	"fmt"
	"time"
)

// Identifier accepts identifiers encoded as either JSON strings or numbers.
// Dynatrace archive versions have used both representations.
type Identifier string

// UnmarshalJSON implements json.Unmarshaler.
func (i *Identifier) UnmarshalJSON(data []byte) error {
	if string(data) == "null" {
		*i = ""
		return nil
	}
	if len(data) > 0 && data[0] == '"' {
		var value string
		if err := json.Unmarshal(data, &value); err != nil {
			return err
		}
		*i = Identifier(value)
		return nil
	}
	var value json.Number
	if err := json.Unmarshal(data, &value); err != nil {
		return fmt.Errorf("identifier must be a string or number: %w", err)
	}
	*i = Identifier(value.String())
	return nil
}

// Document is one billing interval from the nested Dynatrace license archive.
type Document struct {
	TimeFrameStart            int64              `json:"timeFrameStart"`
	TimeFrameEnd              int64              `json:"timeFrameEnd"`
	EnvironmentBillingEntries []EnvironmentEntry `json:"environmentBillingEntries"`
}

// EnvironmentEntry contains raw license inputs for one environment and interval.
type EnvironmentEntry struct {
	EnvironmentUUID            string           `json:"environmentUuid"`
	HostUsages                 []HostUsage      `json:"hostUsages"`
	SyntheticBillingUsage      []SyntheticUsage `json:"syntheticBillingUsage"`
	DavisDataUnits             []DavisDataUnit  `json:"davisDataUnits"`
	Visits                     float64          `json:"visits"`
	MobileSessions             float64          `json:"mobileSessions"`
	SessionReplays             float64          `json:"sessionReplays"`
	MobileSessionReplays       float64          `json:"mobileSessionReplays"`
	TotalRUMUserPropertiesUsed float64          `json:"totalRUMUserPropertiesUsed"`
}

// HostUsage contains the billing-relevant properties of one monitored host.
type HostUsage struct {
	OSIId               Identifier `json:"osiId"`
	HostName            string     `json:"hostName"`
	HostCategory        string     `json:"hostCategory"`
	HostMemoryBytes     int64      `json:"hostMemoryBytes"`
	PassMemoryLimit     int64      `json:"passMemoryLimit"`
	InfrastructureOnly  bool       `json:"infrastructureOnly"`
	PaaS                bool       `json:"paas"`
	HasContainers       bool       `json:"hasContainers"`
	PremiumLogAnalytics bool       `json:"premiumLogAnalytics"`
}

// SyntheticUsage contains executions for one synthetic monitor.
type SyntheticUsage struct {
	MonitorTypeID     int        `json:"monitorTypeId"`
	TestID            Identifier `json:"testId"`
	PublicExecutions  float64    `json:"publicExecutions"`
	PrivateExecutions float64    `json:"privateExecutions"`
}

// DavisDataUnit contains usage for one DDU pool.
type DavisDataUnit struct {
	Pool  string  `json:"pool"`
	Total float64 `json:"total"`
}

// Snapshot is a calculated, immutable view of one settled billing interval.
type Snapshot struct {
	PeriodStart  time.Time
	PeriodEnd    time.Time
	Environments []EnvironmentSnapshot
}

// EnvironmentSnapshot contains calculated values for one environment.
type EnvironmentSnapshot struct {
	ID                   string
	Name                 string
	Hosts                []HostSnapshot
	Synthetic            []SyntheticSnapshot
	HostUnitsByMode      map[string]float64
	HostCountByMode      map[string]float64
	DEMBySource          map[string]float64
	RUMUsageByKind       map[string]float64
	DavisDataUnitsByPool map[string]float64
}

// HostSnapshot contains calculated current billing values for one host.
type HostSnapshot struct {
	ID                  string
	Name                string
	Category            string
	MonitoringMode      string
	PaaS                bool
	HasContainers       bool
	PremiumLogAnalytics bool
	MemoryBytes         int64
	EstimatedHostUnits  float64
	KubernetesCluster   KubernetesClusterInfo
}

// KubernetesClusterInfo identifies the Dynatrace Kubernetes cluster related to a host.
type KubernetesClusterInfo struct {
	EntityID     string
	Name         string
	Distribution string
}

// SyntheticSnapshot contains execution and estimated DEM data for one monitor.
type SyntheticSnapshot struct {
	TestID            string
	MonitorType       string
	PublicExecutions  float64
	PrivateExecutions float64
	EstimatedDEMUnits float64
}
