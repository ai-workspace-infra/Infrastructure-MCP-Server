package flagger

// CanaryStatus represents the progressive release state for a single microservice.
type CanaryStatus struct {
	Service          string `json:"service"`           // "Console", "Accounts", "Billing"
	Workload         string `json:"workload"`          // e.g. "web-saas-console"
	Subdomain        string `json:"subdomain"`         // e.g. "console.svc.plus"
	Phase            string `json:"phase"`             // "Succeeded", "Progressing", "Failed", "Waiting"
	StatusCode       int    `json:"status_code"`       // 0: Progressing, 1: Succeeded, 2: Failed
	Weight           int    `json:"weight"`            // Candidate traffic weight (0-100)
	PrimaryPercent   int    `json:"primary_percent"`   // 100 - Weight
	CandidatePercent int    `json:"candidate_percent"` // Weight
	Message          string `json:"message,omitempty"`
}

// Filter defines query parameters for Flagger canary status inspection.
type Filter struct {
	Environment string `json:"environment"` // "sit" | "prod" | "all"
	Service     string `json:"service"`     // "console" | "accounts" | "billing" | workload
	Cluster     string `json:"cluster"`     // "PROD-K3S-V6" | "SIT-K3S-V6"
}

// Result is the returned response payload for infra.flagger.status.
type Result struct {
	Environment  string         `json:"environment"`
	Cluster      string         `json:"cluster"`
	Timestamp    string         `json:"timestamp"`
	GateDecision string         `json:"gate_decision"` // "ALLOWED" | "BLOCKED" | "PROGRESSING"
	Services     []CanaryStatus `json:"services"`
	LiveMetrics  bool           `json:"live_metrics"`
	DataSource   string         `json:"data_source"`
}
