package dynatrace

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const licenseConsumptionPath = "/api/cluster/v2/license/consumption"

// Config configures a read-only Dynatrace Managed cluster client.
type Config struct {
	BaseURL            string
	Token              string
	ConnectAddress     string
	Timeout            time.Duration
	InsecureSkipVerify bool
	CAFile             string
	UserAgent          string
	MaxDownloadBytes   int64
	Metrics            *Metrics
}

// Client calls the Dynatrace cluster API.
type Client struct {
	baseURL          *url.URL
	token            string
	httpClient       *http.Client
	userAgent        string
	maxDownloadBytes int64
	metrics          *Metrics
}

// NewClient validates config and builds the HTTP transport.
func NewClient(cfg Config) (*Client, error) {
	baseURL, err := url.Parse(strings.TrimSpace(cfg.BaseURL))
	if err != nil {
		return nil, fmt.Errorf("parse Dynatrace URL: %w", err)
	}
	if baseURL.Scheme != "https" && baseURL.Scheme != "http" {
		return nil, fmt.Errorf("Dynatrace URL must use http or https")
	}
	if baseURL.Host == "" {
		return nil, fmt.Errorf("Dynatrace URL must contain a host")
	}
	if baseURL.RawQuery != "" || baseURL.Fragment != "" {
		return nil, fmt.Errorf("Dynatrace URL must not contain a query or fragment")
	}
	baseURL.Path = strings.TrimRight(baseURL.Path, "/")
	if strings.TrimSpace(cfg.Token) == "" {
		return nil, fmt.Errorf("Dynatrace API token is required")
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 2 * time.Minute
	}
	if cfg.MaxDownloadBytes <= 0 {
		cfg.MaxDownloadBytes = 64 << 20
	}

	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: cfg.InsecureSkipVerify} // #nosec G402 -- explicit operator option
	if cfg.CAFile != "" {
		pem, err := os.ReadFile(cfg.CAFile)
		if err != nil {
			return nil, fmt.Errorf("read CA file: %w", err)
		}
		roots, err := x509.SystemCertPool()
		if err != nil || roots == nil {
			roots = x509.NewCertPool()
		}
		if !roots.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("CA file contains no certificates")
		}
		tlsConfig.RootCAs = roots
	}

	dialer := &net.Dialer{Timeout: cfg.Timeout, KeepAlive: 30 * time.Second}
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           dialer.DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          20,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: time.Second,
		TLSClientConfig:       tlsConfig,
	}
	if cfg.ConnectAddress != "" {
		if _, _, err := net.SplitHostPort(cfg.ConnectAddress); err != nil {
			return nil, fmt.Errorf("connect address must be host:port: %w", err)
		}
		originalAddress := baseURL.Host
		if _, _, err := net.SplitHostPort(originalAddress); err != nil {
			port := "80"
			if baseURL.Scheme == "https" {
				port = "443"
			}
			originalAddress = net.JoinHostPort(baseURL.Hostname(), port)
		}
		connectAddress := cfg.ConnectAddress
		// An explicit dial target represents a direct tunnel and must not be
		// replaced by an environment-configured HTTP proxy.
		transport.Proxy = nil
		transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
			if address == originalAddress {
				address = connectAddress
			}
			return dialer.DialContext(ctx, network, address)
		}
	}

	return &Client{
		baseURL:          baseURL,
		token:            strings.TrimSpace(cfg.Token),
		httpClient:       &http.Client{Transport: transport, Timeout: cfg.Timeout},
		userAgent:        cfg.UserAgent,
		maxDownloadBytes: cfg.MaxDownloadBytes,
		metrics:          cfg.Metrics,
	}, nil
}

// LicenseConsumption downloads a bounded billing archive for [start, end].
func (c *Client) LicenseConsumption(ctx context.Context, start, end time.Time) ([]byte, error) {
	if !end.After(start) {
		return nil, fmt.Errorf("billing end must be after start")
	}
	requestURL := *c.baseURL
	requestURL.Path = strings.TrimRight(c.baseURL.Path, "/") + licenseConsumptionPath
	query := requestURL.Query()
	query.Set("startTs", strconv.FormatInt(start.UnixMilli(), 10))
	query.Set("endTs", strconv.FormatInt(end.UnixMilli(), 10))
	requestURL.RawQuery = query.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("create license consumption request: %w", err)
	}
	req.Header.Set("Accept", "application/octet-stream")
	req.Header.Set("Authorization", "Api-Token "+c.token)
	if c.userAgent != "" {
		req.Header.Set("User-Agent", c.userAgent)
	}

	started := time.Now()
	resp, err := c.httpClient.Do(req)
	if err != nil {
		c.metrics.observe("license_consumption", "error", time.Since(started).Seconds())
		return nil, fmt.Errorf("download license consumption archive: %w", err)
	}
	defer resp.Body.Close()
	c.metrics.observe("license_consumption", strconv.Itoa(resp.StatusCode), time.Since(started).Seconds())
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		message, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("license consumption API returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(message)))
	}
	if resp.ContentLength > c.maxDownloadBytes {
		return nil, fmt.Errorf("license archive content length %d exceeds limit %d", resp.ContentLength, c.maxDownloadBytes)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, c.maxDownloadBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read license consumption archive: %w", err)
	}
	if int64(len(data)) > c.maxDownloadBytes {
		return nil, fmt.Errorf("license archive exceeds download limit %d", c.maxDownloadBytes)
	}
	return data, nil
}
