package telemetry

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// MCPCaller allows the engine to query downstream MCP servers when configured.
type MCPCaller interface {
	Call(ctx context.Context, toolName string, arguments map[string]any) ([]byte, error)
}

type Config struct {
	BaseURL     string // Central gateway e.g. "https://observability.svc.plus"
	MetricsURL  string // VictoriaMetrics API URL
	LogsURL     string // VictoriaLogs API URL
	TracesURL   string // VictoriaTraces API URL
	GrafanaURL  string // Grafana API URL
	AuthToken   string // Bearer / Basic auth token
	CacheTTL    time.Duration
	HTTPTimeout time.Duration
}

type Engine struct {
	mu          sync.RWMutex
	cfg         Config
	httpClient  *http.Client
	mcpCaller   MCPCaller
	metadata    TopologyMetadata
	metadataExp time.Time
}

func NewEngine(cfg Config, mcpCaller MCPCaller, customHTTP ...*http.Client) *Engine {
	if cfg.BaseURL == "" {
		cfg.BaseURL = os.Getenv("OBSERVABILITY_BASE_URL")
		if cfg.BaseURL == "" {
			cfg.BaseURL = "https://observability.svc.plus"
		}
	}
	cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/")

	if cfg.MetricsURL == "" {
		cfg.MetricsURL = os.Getenv("VICTORIAMETRICS_URL")
		if cfg.MetricsURL == "" {
			cfg.MetricsURL = cfg.BaseURL + "/vmetrics"
		}
	}
	cfg.MetricsURL = strings.TrimRight(cfg.MetricsURL, "/")

	if cfg.LogsURL == "" {
		cfg.LogsURL = os.Getenv("VICTORIALOGS_URL")
		if cfg.LogsURL == "" {
			cfg.LogsURL = cfg.BaseURL + "/vlogs"
		}
	}
	cfg.LogsURL = strings.TrimRight(cfg.LogsURL, "/")

	if cfg.TracesURL == "" {
		cfg.TracesURL = os.Getenv("VICTORIATRACES_URL")
		if cfg.TracesURL == "" {
			cfg.TracesURL = cfg.BaseURL + "/vtraces"
		}
	}
	cfg.TracesURL = strings.TrimRight(cfg.TracesURL, "/")

	if cfg.GrafanaURL == "" {
		cfg.GrafanaURL = os.Getenv("GRAFANA_URL")
		if cfg.GrafanaURL == "" {
			cfg.GrafanaURL = cfg.BaseURL + "/grafana"
		}
	}
	cfg.GrafanaURL = strings.TrimRight(cfg.GrafanaURL, "/")

	if cfg.AuthToken == "" {
		cfg.AuthToken = os.Getenv("OBSERVABILITY_AUTH_TOKEN")
	}

	if cfg.CacheTTL <= 0 {
		cfg.CacheTTL = 30 * time.Second
	}
	if cfg.HTTPTimeout <= 0 {
		cfg.HTTPTimeout = 3 * time.Second
	}

	var client *http.Client
	if len(customHTTP) > 0 && customHTTP[0] != nil {
		client = customHTTP[0]
	} else {
		client = &http.Client{Timeout: cfg.HTTPTimeout}
	}

	return &Engine{
		cfg:        cfg,
		httpClient: client,
		mcpCaller:  mcpCaller,
	}
}

// DiscoverTopology dynamically fetches environments, clusters, env-to-cluster mappings, and active services.
func (e *Engine) DiscoverTopology(ctx context.Context) (TopologyMetadata, error) {
	e.mu.RLock()
	if time.Now().Before(e.metadataExp) && len(e.metadata.Environments) > 0 {
		cached := e.metadata
		e.mu.RUnlock()
		return cached, nil
	}
	e.mu.RUnlock()

	e.mu.Lock()
	defer e.mu.Unlock()

	// Double check after lock
	if time.Now().Before(e.metadataExp) && len(e.metadata.Environments) > 0 {
		return e.metadata, nil
	}

	meta := TopologyMetadata{
		Environments:   make([]string, 0),
		Clusters:       make([]string, 0),
		EnvClusterMap:  make(map[string]string),
		Services:       make([]string, 0),
		LastDiscovered: time.Now().UTC(),
		Source:         "Dynamic VictoriaMetrics Discovery",
	}

	// 1. Discover environments from label values
	envs, err := e.queryLabelValues(ctx, "environment")
	if err == nil && len(envs) > 0 {
		meta.Environments = envs
	}

	// 2. Discover clusters from label values
	clusters, err := e.queryLabelValues(ctx, "cluster")
	if err == nil && len(clusters) > 0 {
		meta.Clusters = clusters
	}

	// 3. Discover env-to-cluster mapping from probe series
	series, err := e.QueryMetrics(ctx, "count by (environment, cluster) (probe_success)")
	if err == nil && len(series) > 0 {
		for _, s := range series {
			env := s.Metric["environment"]
			cl := s.Metric["cluster"]
			if env != "" && cl != "" {
				meta.EnvClusterMap[env] = cl
				if !containsString(meta.Environments, env) {
					meta.Environments = append(meta.Environments, env)
				}
				if !containsString(meta.Clusters, cl) {
					meta.Clusters = append(meta.Clusters, cl)
				}
			}
		}
	}

	// 4. Discover active services from probes and flagger metrics
	svcSeries, err := e.QueryMetrics(ctx, "count by (service) (probe_success)")
	if err == nil && len(svcSeries) > 0 {
		for _, s := range svcSeries {
			svc := s.Metric["service"]
			if svc != "" && !containsString(meta.Services, svc) {
				meta.Services = append(meta.Services, svc)
			}
		}
	}

	// Sort lists for stability
	sort.Strings(meta.Environments)
	sort.Strings(meta.Clusters)
	sort.Strings(meta.Services)

	// If discovery succeeded, update cache
	if len(meta.Environments) > 0 {
		e.metadata = meta
		e.metadataExp = time.Now().Add(e.cfg.CacheTTL)
		return meta, nil
	}

	// If remote query failed, fallback to dynamically inferred or fallback defaults
	if len(e.metadata.Environments) == 0 {
		meta.Environments = []string{"prod", "sit", "uat"}
		meta.Clusters = []string{"PROD-K3S-V6", "SIT-K3S-V6"}
		meta.EnvClusterMap = map[string]string{
			"prod": "PROD-K3S-V6",
			"sit":  "SIT-K3S-V6",
		}
		meta.Services = []string{"console", "accounts", "billing"}
		meta.Source = "Calibrated Dynamic Fallback Baseline"
	} else {
		meta = e.metadata
	}

	e.metadata = meta
	e.metadataExp = time.Now().Add(e.cfg.CacheTTL)
	return meta, nil
}

// QueryMetrics executes a Prometheus/MetricsQL query against VictoriaMetrics or via MCP backend.
func (e *Engine) QueryMetrics(ctx context.Context, promQL string) ([]MetricVector, error) {
	// 1. If an MCP caller is configured, try querying via downstream MCP server first
	if e.mcpCaller != nil {
		resBytes, err := e.mcpCaller.Call(ctx, "observability-metrics.query", map[string]any{"query": promQL})
		if err == nil && len(resBytes) > 0 {
			var parsed []MetricVector
			if json.Unmarshal(resBytes, &parsed) == nil && len(parsed) > 0 {
				return parsed, nil
			}
		}
	}

	// 2. Direct HTTP query to VictoriaMetrics
	queryURL := fmt.Sprintf("%s/api/v1/query?query=%s", e.cfg.MetricsURL, url.QueryEscape(promQL))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, queryURL, nil)
	if err != nil {
		return nil, err
	}
	e.applyAuth(req)

	resp, err := e.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("metrics query status: %d", resp.StatusCode)
	}

	var res struct {
		Status string `json:"status"`
		Data   struct {
			ResultType string         `json:"resultType"`
			Result     []MetricVector `json:"result"`
		} `json:"data"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return nil, err
	}
	return res.Data.Result, nil
}

// QueryLogs executes a LogsQL query against VictoriaLogs.
func (e *Engine) QueryLogs(ctx context.Context, logsQL string, limit int) ([]LogRecord, error) {
	if limit <= 0 {
		limit = 50
	}

	// 1. Try MCP caller
	if e.mcpCaller != nil {
		resBytes, err := e.mcpCaller.Call(ctx, "observability-logs.query", map[string]any{"query": logsQL, "limit": limit})
		if err == nil && len(resBytes) > 0 {
			var logs []LogRecord
			if json.Unmarshal(resBytes, &logs) == nil {
				return logs, nil
			}
		}
	}

	// 2. Direct HTTP query to VictoriaLogs
	queryURL := fmt.Sprintf("%s/select/logsql/query?query=%s&limit=%d", e.cfg.LogsURL, url.QueryEscape(logsQL), limit)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, queryURL, nil)
	if err != nil {
		return nil, err
	}
	e.applyAuth(req)

	resp, err := e.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("logs query status: %d", resp.StatusCode)
	}

	var records []LogRecord
	dec := json.NewDecoder(resp.Body)
	for {
		var raw map[string]any
		if err := dec.Decode(&raw); err != nil {
			if err == io.EOF {
				break
			}
			return nil, err
		}

		msg, _ := raw["_msg"].(string)
		if msg == "" {
			msg, _ = raw["message"].(string)
		}
		stream, _ := raw["_stream"].(string)

		timeVal := time.Now()
		if tStr, ok := raw["_time"].(string); ok {
			if t, err := time.Parse(time.RFC3339Nano, tStr); err == nil {
				timeVal = t
			}
		}

		fields := make(map[string]string)
		for k, v := range raw {
			if !strings.HasPrefix(k, "_") {
				fields[k] = fmt.Sprint(v)
			}
		}

		records = append(records, LogRecord{
			Time:    timeVal,
			Stream:  stream,
			Message: msg,
			Fields:  fields,
		})
	}

	return records, nil
}

// QueryTraces retrieves trace information from VictoriaTraces.
func (e *Engine) QueryTraces(ctx context.Context, service string) ([]TraceSpan, error) {
	// 1. Try MCP caller
	if e.mcpCaller != nil {
		resBytes, err := e.mcpCaller.Call(ctx, "observability-traces.query", map[string]any{"service": service})
		if err == nil && len(resBytes) > 0 {
			var spans []TraceSpan
			if json.Unmarshal(resBytes, &spans) == nil {
				return spans, nil
			}
		}
	}

	// 2. Direct HTTP query to VictoriaTraces
	queryURL := fmt.Sprintf("%s/api/traces?service=%s", e.cfg.TracesURL, url.QueryEscape(service))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, queryURL, nil)
	if err != nil {
		return nil, err
	}
	e.applyAuth(req)

	resp, err := e.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("traces query status: %d", resp.StatusCode)
	}

	var spans []TraceSpan
	if err := json.NewDecoder(resp.Body).Decode(&spans); err != nil {
		return nil, err
	}
	return spans, nil
}

// QueryGrafanaDatasources retrieves configured data sources from Grafana.
func (e *Engine) QueryGrafanaDatasources(ctx context.Context) ([]GrafanaDatasource, error) {
	// 1. Try MCP caller
	if e.mcpCaller != nil {
		resBytes, err := e.mcpCaller.Call(ctx, "observability-grafana.list_datasources", nil)
		if err == nil && len(resBytes) > 0 {
			var ds []GrafanaDatasource
			if json.Unmarshal(resBytes, &ds) == nil {
				return ds, nil
			}
		}
	}

	// 2. Direct HTTP to Grafana API
	queryURL := fmt.Sprintf("%s/api/datasources", e.cfg.GrafanaURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, queryURL, nil)
	if err != nil {
		return nil, err
	}
	e.applyAuth(req)

	resp, err := e.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("grafana datasources status: %d", resp.StatusCode)
	}

	var ds []GrafanaDatasource
	if err := json.NewDecoder(resp.Body).Decode(&ds); err != nil {
		return nil, err
	}
	return ds, nil
}

func (e *Engine) queryLabelValues(ctx context.Context, labelName string) ([]string, error) {
	queryURL := fmt.Sprintf("%s/api/v1/label/%s/values", e.cfg.MetricsURL, url.PathEscape(labelName))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, queryURL, nil)
	if err != nil {
		return nil, err
	}
	e.applyAuth(req)

	resp, err := e.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("label query status: %d", resp.StatusCode)
	}

	var res struct {
		Status string   `json:"status"`
		Data   []string `json:"data"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return nil, err
	}

	var filtered []string
	for _, val := range res.Data {
		v := strings.TrimSpace(val)
		if v != "" && !strings.HasPrefix(v, "$") {
			filtered = append(filtered, v)
		}
	}
	return filtered, nil
}

func (e *Engine) applyAuth(req *http.Request) {
	if e.cfg.AuthToken != "" {
		if strings.HasPrefix(e.cfg.AuthToken, "Basic ") || strings.HasPrefix(e.cfg.AuthToken, "Bearer ") {
			req.Header.Set("Authorization", e.cfg.AuthToken)
		} else {
			req.Header.Set("Authorization", "Bearer "+e.cfg.AuthToken)
		}
	}
}

func containsString(list []string, target string) bool {
	for _, item := range list {
		if strings.EqualFold(item, target) {
			return true
		}
	}
	return false
}

// Helper to parse metric float value.
func ParseMetricFloat(v []any) (float64, bool) {
	if len(v) < 2 {
		return 0, false
	}
	switch val := v[1].(type) {
	case string:
		f, err := strconv.ParseFloat(val, 64)
		return f, err == nil
	case float64:
		return val, true
	case int:
		return float64(val), true
	default:
		return 0, false
	}
}
