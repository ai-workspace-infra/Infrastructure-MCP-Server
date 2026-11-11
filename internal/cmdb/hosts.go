package cmdb

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	// DefaultSSHUser is the read-only inspection account on every host.
	DefaultSSHUser = "auditor"
	// DefaultSSHVaultRef is the PROPOSED Vault KV v2 path holding the
	// inspection SSH private key (field ssh_private_key) and, where needed,
	// per-host addresses. It must be created by an operator; nothing in this
	// repository stores the secret itself.
	DefaultSSHVaultRef = "kv/data/CICD/infrastructure-mcp"

	envBaseDir   = "INFRA_CMDB_BASE_DIR"
	envHostsFile = "INFRA_CMDB_HOSTS_FILE"
	defaultBase  = "/Users/shenlan/workspaces/ai-workspace-infra"
)

// resolveBaseDir picks the CMDB data directory: explicit argument, then
// $INFRA_CMDB_BASE_DIR, then the historical developer default.
func resolveBaseDir(baseDir string) string {
	if baseDir != "" {
		return baseDir
	}
	if v := strings.TrimSpace(os.Getenv(envBaseDir)); v != "" {
		return v
	}
	return defaultBase
}

// defaultHostInventory is the declared inspection inventory (hostnames only).
// It mirrors the GitOps/IaC declarations and can be overridden or extended
// by an inventory file (see loadHostInventory).
func defaultHostInventory() []HostEntry {
	edge := []string{
		"jp-free-selfhost-prod-v2.svc.plus",
		"jp-xconnect-01.svc.plus",
		"jp-xconnect-02.svc.plus",
		"ph-xconnect.svc.plus",
		"sg-xconnect.svc.plus",
		"us-xconnect-01.svc.plus",
		"us-xconnect-los-angeles-custom-01.svc.plus",
		"us-xconnect.svc.plus",
	}
	hosts := []HostEntry{
		// sit-k3s-v6 temporarily holds several roles, including OPS: it runs the
		// mcp.svc.plus gateway and the ssh-mcp-server sidecar. To move OPS to a
		// dedicated host, drop "ops" here (or via hosts.json) and add it there.
		{Hostname: "sit-k3s-v6", Environment: "sit", Role: "k3s-node", Roles: []string{"sit", "ops"}, RolesTemporary: true, Provider: "linode", Cluster: "SIT-K3S-V6"},
		{Hostname: "prod-k3s-v6", Environment: "prod", Role: "k3s-node", Provider: "linode", Cluster: "PROD-K3S-V6"},
	}
	for _, h := range edge {
		hosts = append(hosts, HostEntry{Hostname: h, Environment: "prod", Role: "edge-agent", Provider: "unknown"})
	}
	return hosts
}

// loadHostInventory merges the default inventory with an optional JSON file
// ($INFRA_CMDB_HOSTS_FILE, else <baseDir>/cmdb/hosts.json). File entries
// override defaults by hostname and may add new hosts; an entry with
// "inspect": false removes the host from SSH inspection.
func loadHostInventory(baseDir string) ([]HostEntry, string, error) {
	hosts := defaultHostInventory()
	source := "gitops-seed"
	path := strings.TrimSpace(os.Getenv(envHostsFile))
	if path == "" {
		path = filepath.Join(baseDir, "cmdb", "hosts.json")
	}
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		if os.Getenv(envHostsFile) != "" {
			return hosts, source, fmt.Errorf("read %s: %w", path, err)
		}
		return hosts, source, nil
	}
	var fileHosts []HostEntry
	if err := json.Unmarshal(data, &fileHosts); err != nil {
		return hosts, source, fmt.Errorf("parse %s: %w", path, err)
	}
	index := map[string]int{}
	for i, h := range hosts {
		index[h.Hostname] = i
	}
	for _, h := range fileHosts {
		if strings.TrimSpace(h.Hostname) == "" {
			return hosts, source, fmt.Errorf("parse %s: host entry without hostname", path)
		}
		if i, ok := index[h.Hostname]; ok {
			hosts[i] = h
		} else {
			index[h.Hostname] = len(hosts)
			hosts = append(hosts, h)
		}
	}
	return hosts, "inventory-file", nil
}

func shortName(hostname string) string {
	name, _, _ := strings.Cut(hostname, ".")
	return name
}

// hostResource converts an inventory entry into a CMDB resource of kind "host".
func hostResource(h HostEntry, source string) Resource {
	env := strings.ToLower(strings.TrimSpace(h.Environment))
	if env == "" {
		env = "shared"
	}
	user := h.SSHUser
	if user == "" {
		user = DefaultSSHUser
	}
	vaultRef := h.VaultRef
	if vaultRef == "" {
		vaultRef = DefaultSSHVaultRef
	}
	port := h.SSHPort
	if port == 0 {
		port = 22
	}
	attrs := map[string]any{"role": h.Role, "hostname": h.Hostname}
	if len(h.Roles) > 0 {
		attrs["roles"] = h.Roles
		if h.RolesTemporary {
			attrs["roles_temporary"] = true
		}
	}
	if h.Cluster != "" {
		attrs["cluster"] = h.Cluster
	}
	r := Resource{
		ID:          fmt.Sprintf("host/%s/%s", env, shortName(h.Hostname)),
		Name:        h.Hostname,
		Environment: env,
		Provider:    h.Provider,
		Kind:        "host",
		Endpoint:    h.Hostname,
		Status:      "declared",
		Source:      source,
		Attributes:  attrs,
	}
	if h.Inspect == nil || *h.Inspect {
		r.SSH = &SSHAccess{
			Host:           h.Hostname,
			User:           user,
			Port:           port,
			ConnectionName: shortName(h.Hostname),
			VaultRef:       vaultRef,
		}
		attrs["ssh_target"] = user + "@" + h.Hostname
	}
	return r
}

// SSHTargets returns host resources that carry SSH access, optionally
// filtered by environment, sorted by ID.
func (s *Store) SSHTargets(environments ...string) []Resource {
	want := map[string]bool{}
	for _, e := range environments {
		if e = strings.ToLower(strings.TrimSpace(e)); e != "" {
			want[e] = true
		}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []Resource{}
	for _, r := range s.resources {
		if r.SSH == nil {
			continue
		}
		if len(want) > 0 && !want[r.Environment] {
			continue
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// HasRole reports whether a resource carries the given functional role,
// either as its primary attributes.role or inside attributes.roles.
func HasRole(r Resource, role string) bool {
	if role == "" {
		return true
	}
	if v, ok := r.Attributes["role"].(string); ok && strings.EqualFold(v, role) {
		return true
	}
	switch roles := r.Attributes["roles"].(type) {
	case []string:
		for _, v := range roles {
			if strings.EqualFold(v, role) {
				return true
			}
		}
	case []any:
		for _, v := range roles {
			if s, ok := v.(string); ok && strings.EqualFold(s, role) {
				return true
			}
		}
	}
	return false
}
