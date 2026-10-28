package cmdb

type Resource struct {
	ID          string         `json:"id"`
	Name        string         `json:"name"`
	Environment string         `json:"environment"` // sit, uat, prod, shared
	Provider    string         `json:"provider"`    // linode, gcp, aws, cloudflare, vultr
	Kind        string         `json:"kind"`        // compute, dns, service, database, network
	Endpoint    string         `json:"endpoint,omitempty"`
	IP          string         `json:"ip,omitempty"`
	Status      string         `json:"status"` // running, active, ready, standby, unknown
	Attributes  map[string]any `json:"attributes,omitempty"`
	Source      string         `json:"source"` // iac, gitops, pipeline, cli
}

type ServiceStatus struct {
	Name     string `json:"name"`
	Domain   string `json:"domain"`
	Kind     string `json:"kind"`
	Provider string `json:"provider"`
	Status   string `json:"status"`
}

type EnvironmentSummary struct {
	Environment   string          `json:"environment"`
	Tier          string          `json:"tier"`
	ResourceCount int             `json:"resource_count"`
	Providers     []string        `json:"providers"`
	Domains       []string        `json:"domains"`
	Services      []ServiceStatus `json:"services"`
	Deployment    string          `json:"deployment"` // selfhost, serverless, hybrid
	Status        string          `json:"status"`
}

type SyncReport struct {
	TotalSynced  int                   `json:"total_synced"`
	Environments map[string]int        `json:"environments"`
	Sources      []string              `json:"sources"`
	CLIs         map[string]bool       `json:"clis"`
	MCPAdapters  []string              `json:"mcp_adapters"`
	Summaries    []EnvironmentSummary  `json:"summaries"`
}
