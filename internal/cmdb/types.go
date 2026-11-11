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
	// SSH describes how an inspection agent may reach the resource. Only
	// non-secret data lives here; the key (and the address, when the
	// hostname is an ssh_config alias) is resolved from VaultRef.
	SSH *SSHAccess `json:"ssh,omitempty"`
}

// SSHAccess is the non-secret SSH reachability record of a host resource.
type SSHAccess struct {
	Host           string `json:"host"`            // hostname or ~/.ssh/config alias, never an IP in git
	User           string `json:"user"`            // read-only account, e.g. auditor
	Port           int    `json:"port,omitempty"`  // default 22
	ConnectionName string `json:"connection_name"` // ssh-mcp-server connectionName
	VaultRef       string `json:"vault_ref"`       // Vault KV v2 path holding the key / address
}

// HostEntry is one record of the host inventory (seed or inventory file).
type HostEntry struct {
	Hostname    string `json:"hostname"`
	Environment string `json:"environment"` // sit, uat, prod
	Role        string `json:"role"`        // k3s-node, edge-agent, ...
	Provider    string `json:"provider"`
	Cluster     string `json:"cluster,omitempty"`
	SSHUser     string `json:"ssh_user,omitempty"`
	SSHPort     int    `json:"ssh_port,omitempty"`
	VaultRef    string `json:"vault_ref,omitempty"`
	Inspect     *bool  `json:"inspect,omitempty"` // default true
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
	TotalSynced  int                  `json:"total_synced"`
	Environments map[string]int       `json:"environments"`
	Sources      []string             `json:"sources"`
	CLIs         map[string]bool      `json:"clis"`
	MCPAdapters  []string             `json:"mcp_adapters"`
	Summaries    []EnvironmentSummary `json:"summaries"`
	Errors       []string             `json:"errors,omitempty"`
}
