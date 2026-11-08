package telemetry

import "time"

// MetricVector represents a Prometheus-compatible instant vector query result.
type MetricVector struct {
	Metric map[string]string `json:"metric"`
	Value  []any             `json:"value"` // [unixTimestamp, stringValue]
}

// LogRecord represents a structured log entry from VictoriaLogs.
type LogRecord struct {
	Time    time.Time         `json:"time"`
	Stream  string            `json:"stream,omitempty"`
	Message string            `json:"message"`
	Fields  map[string]string `json:"fields,omitempty"`
}

// TraceSpan represents a distributed trace span from VictoriaTraces.
type TraceSpan struct {
	TraceID      string            `json:"trace_id"`
	SpanID       string            `json:"span_id"`
	ParentSpanID string            `json:"parent_span_id,omitempty"`
	ServiceName  string            `json:"service_name"`
	Operation    string            `json:"operation"`
	DurationMs   float64           `json:"duration_ms"`
	StatusCode   string            `json:"status_code"`
	Tags         map[string]string `json:"tags,omitempty"`
}

// GrafanaDatasource represents a configured datasource in Grafana.
type GrafanaDatasource struct {
	ID        int    `json:"id"`
	UID       string `json:"uid"`
	Name      string `json:"name"`
	Type      string `json:"type"`
	URL       string `json:"url"`
	IsDefault bool   `json:"is_default"`
}

// TopologyMetadata represents dynamic infrastructure topology discovered from observability.
type TopologyMetadata struct {
	Environments   []string          `json:"environments"`
	Clusters       []string          `json:"clusters"`
	EnvClusterMap  map[string]string `json:"env_cluster_map"`
	Services       []string          `json:"services"`
	LastDiscovered time.Time         `json:"last_discovered"`
	Source         string            `json:"source"`
}
