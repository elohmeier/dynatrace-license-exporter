package dynatrace

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	maxEntityPages          = 100
	maxEntitySelectorLength = 2000
	entityPageSize          = 100
)

// MetricDatum is one dimension tuple returned by a Metrics API query.
type MetricDatum struct {
	MetricID   string
	Dimensions map[string]string
	Value      float64
}

type metricPage struct {
	NextPageKey string         `json:"nextPageKey"`
	Result      []metricResult `json:"result"`
}

type metricResult struct {
	MetricID string       `json:"metricId"`
	Data     []metricData `json:"data"`
}

type metricData struct {
	DimensionMap map[string]string `json:"dimensionMap"`
	Values       []*float64        `json:"values"`
}

// Entity contains metadata used to enrich environment-backed series.
type Entity struct {
	EntityID        string         `json:"entityId"`
	Type            string         `json:"type"`
	DisplayName     string         `json:"displayName"`
	Tags            []EntityTag    `json:"tags"`
	ManagementZones []EntityZone   `json:"managementZones"`
	Properties      map[string]any `json:"properties"`
}

// EntityTag is a Dynatrace entity tag.
type EntityTag struct {
	Context string `json:"context"`
	Key     string `json:"key"`
	Value   string `json:"value"`
}

// EntityZone is a Dynatrace management zone reference.
type EntityZone struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type entitiesResponse struct {
	NextPageKey string   `json:"nextPageKey"`
	Entities    []Entity `json:"entities"`
}

// QueryMetric queries one environment billing metric over a fixed window.
func (c *Client) QueryMetric(ctx context.Context, environmentID, selector string, from, to time.Time) ([]MetricDatum, error) {
	if !to.After(from) {
		return nil, fmt.Errorf("metric query end must be after start")
	}
	query := url.Values{
		"metricSelector": []string{selector},
		"from":           []string{strconv.FormatInt(from.UnixMilli(), 10)},
		"to":             []string{strconv.FormatInt(to.UnixMilli(), 10)},
		"resolution":     []string{"Inf"},
	}
	var rows []MetricDatum
	for pageNumber := 0; pageNumber < 100; pageNumber++ {
		var page metricPage
		path := "/e/" + url.PathEscape(environmentID) + "/api/v2/metrics/query"
		if err := c.getJSON(ctx, "metrics_query", path, query, &page); err != nil {
			return nil, err
		}
		for _, result := range page.Result {
			for _, data := range result.Data {
				for _, value := range data.Values {
					if value != nil {
						rows = append(rows, MetricDatum{MetricID: result.MetricID, Dimensions: data.DimensionMap, Value: *value})
						break
					}
				}
			}
		}
		if page.NextPageKey == "" {
			return rows, nil
		}
		query = url.Values{"nextPageKey": []string{page.NextPageKey}}
	}
	return nil, fmt.Errorf("metrics query exceeded 100 pages")
}

// Entity fetches metadata for one environment entity ID.
func (c *Client) Entity(ctx context.Context, environmentID, entityID string) (*Entity, error) {
	selector := fmt.Sprintf("entityId(%s)", strconv.Quote(entityID))
	query := url.Values{
		"entitySelector": []string{selector},
		"fields":         []string{"properties,tags,managementZones"},
	}
	path := "/e/" + url.PathEscape(environmentID) + "/api/v2/entities"
	var response entitiesResponse
	if err := c.getJSON(ctx, "entities", path, query, &response); err != nil {
		return nil, err
	}
	if len(response.Entities) == 0 {
		return nil, nil
	}
	return &response.Entities[0], nil
}

// Entities fetches the basic metadata for a bounded set of environment entity IDs.
// Dynatrace limits entity selector strings to 2,000 characters, so IDs are
// de-duplicated, sorted, and split across as many paginated requests as needed.
func (c *Client) Entities(ctx context.Context, environmentID string, entityIDs []string) ([]Entity, error) {
	batches, err := entityIDBatches(entityIDs)
	if err != nil {
		return nil, err
	}
	path := "/e/" + url.PathEscape(environmentID) + "/api/v2/entities"
	var entities []Entity
	for _, batch := range batches {
		query := url.Values{
			"entitySelector": []string{entityIDSelector(batch)},
			"pageSize":       []string{strconv.Itoa(entityPageSize)},
		}
		for pageNumber := 0; pageNumber < maxEntityPages; pageNumber++ {
			var response entitiesResponse
			if err := c.getJSON(ctx, "entities", path, query, &response); err != nil {
				return nil, err
			}
			entities = append(entities, response.Entities...)
			if response.NextPageKey == "" {
				break
			}
			if pageNumber == maxEntityPages-1 {
				return nil, fmt.Errorf("entities query exceeded %d pages", maxEntityPages)
			}
			query = url.Values{"nextPageKey": []string{response.NextPageKey}}
		}
	}
	return entities, nil
}

func entityIDBatches(entityIDs []string) ([][]string, error) {
	unique := make(map[string]bool, len(entityIDs))
	ids := make([]string, 0, len(entityIDs))
	for _, id := range entityIDs {
		id = strings.TrimSpace(id)
		if id == "" {
			return nil, fmt.Errorf("entity ID must not be empty")
		}
		if !unique[id] {
			unique[id] = true
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	var batches [][]string
	for _, id := range ids {
		if len(entityIDSelector([]string{id})) > maxEntitySelectorLength {
			return nil, fmt.Errorf("entity ID %q exceeds selector length limit", id)
		}
		if len(batches) == 0 || len(entityIDSelector(append(append([]string{}, batches[len(batches)-1]...), id))) > maxEntitySelectorLength {
			batches = append(batches, []string{id})
			continue
		}
		batches[len(batches)-1] = append(batches[len(batches)-1], id)
	}
	return batches, nil
}

func entityIDSelector(entityIDs []string) string {
	quoted := make([]string, len(entityIDs))
	for i, id := range entityIDs {
		quoted[i] = strconv.Quote(id)
	}
	return "entityId(" + strings.Join(quoted, ",") + ")"
}

func (c *Client) getJSON(ctx context.Context, endpoint, path string, query url.Values, target any) error {
	requestURL := *c.baseURL
	requestURL.Path = strings.TrimRight(c.baseURL.Path, "/") + path
	requestURL.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL.String(), nil)
	if err != nil {
		return fmt.Errorf("create %s request: %w", endpoint, err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Api-Token "+c.token)
	if c.userAgent != "" {
		req.Header.Set("User-Agent", c.userAgent)
	}
	started := time.Now()
	resp, err := c.httpClient.Do(req)
	if err != nil {
		c.metrics.observe(endpoint, "error", time.Since(started).Seconds())
		return fmt.Errorf("%s request: %w", endpoint, err)
	}
	defer resp.Body.Close()
	c.metrics.observe(endpoint, strconv.Itoa(resp.StatusCode), time.Since(started).Seconds())
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		message, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("%s API returned HTTP %d: %s", endpoint, resp.StatusCode, strings.TrimSpace(string(message)))
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, c.maxDownloadBytes+1))
	if err != nil {
		return fmt.Errorf("read %s response: %w", endpoint, err)
	}
	if int64(len(data)) > c.maxDownloadBytes {
		return fmt.Errorf("%s response exceeds limit %d", endpoint, c.maxDownloadBytes)
	}
	if err := json.Unmarshal(data, target); err != nil {
		return fmt.Errorf("decode %s response: %w", endpoint, err)
	}
	return nil
}
