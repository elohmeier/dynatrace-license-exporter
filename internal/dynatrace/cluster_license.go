package dynatrace

import (
	"context"
	"net/url"
	"time"
)

const clusterLicensePath = "/api/cluster/v2/clusterLicense"

// ClusterLicense contains the non-sensitive contract and usage fields exposed
// by the Dynatrace Managed Cluster API. Account, contact, cluster, and license
// key fields are intentionally not modeled.
type ClusterLicense struct {
	LicenseExpirationTime time.Time    `json:"licenseExpirationTime"`
	LastBillingTime       time.Time    `json:"lastBillingTime"`
	UsageOfHostUnits      LicenseUsage `json:"usageOfHostUnits"`
	UsageOfDEMUnits       LicenseUsage `json:"usageOfDemUnits"`
	UsageOfDDUUnits       LicenseUsage `json:"usageOfDduUnits"`
}

// LicenseUsage is the quota state for one licensed product.
type LicenseUsage struct {
	Quota            float64 `json:"quota"`
	Usage            float64 `json:"usage"`
	UsagePercent     float64 `json:"usagePercent"`
	Remaining        float64 `json:"remaining"`
	RemainingPercent float64 `json:"remainingPercent"`
	UsageStatus      string  `json:"usageStatus"`
}

// ClusterLicense retrieves the current cluster-wide contract quota and billed
// usage summary.
func (c *Client) ClusterLicense(ctx context.Context) (ClusterLicense, error) {
	var response ClusterLicense
	if err := c.getJSON(ctx, "cluster_license", clusterLicensePath, url.Values{}, &response); err != nil {
		return ClusterLicense{}, err
	}
	return response, nil
}
