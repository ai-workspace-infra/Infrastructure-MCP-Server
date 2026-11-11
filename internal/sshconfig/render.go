// Package sshconfig renders a classfang/ssh-mcp-server --config-file from
// CMDB host resources, so the SSH inspection inventory is never maintained
// by hand. Secrets are not rendered from the CMDB: the private key path (or
// SSH agent socket) comes from the operator's environment, typically a file
// materialised from the Vault path recorded in each host's vault_ref.
package sshconfig

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/ai-workspace-infra/Infrastructure-MCP-Server/internal/cmdb"
)

// Policy holds the per-connection safety settings understood by
// ssh-mcp-server (field names match its config-file keys).
type Policy struct {
	CommandWhitelist    []string `json:"commandWhitelist"`
	CommandBlacklist    []string `json:"commandBlacklist"`
	AllowedRemotePaths  []string `json:"allowedRemotePaths"`
	Pty                 bool     `json:"pty"`
	CommandTimeoutMs    int      `json:"commandTimeoutMs"`
	ConnectionTimeoutMs int      `json:"connectionTimeoutMs"`
	MaxOutputBytes      int      `json:"maxOutputBytes"`
}

// DefaultPolicy is the read-only inspection policy. ssh-mcp-server rejects
// any command containing ; & | ` $() > < or newlines once a whitelist is set,
// so every allowed command is a single pipe-free invocation.
func DefaultPolicy() Policy {
	return Policy{
		CommandWhitelist: []string{
			`^uptime$`,
			`^free(?: -[mhg])?$`,
			`^df(?: -[hTiP]+)*(?: [A-Za-z0-9_./-]+)?$`,
			`^systemctl status [A-Za-z0-9_.@-]+(?: --no-pager)?(?: -l)?$`,
			`^systemctl is-active [A-Za-z0-9_.@-]+$`,
			`^systemctl show [A-Za-z0-9_.@-]+(?: -p [A-Za-z]+)+$`,
			`^journalctl -u [A-Za-z0-9_.@-]+ -n [0-9]{1,4} --no-pager(?: --since -?[0-9]+[mh])?(?: --grep [A-Za-z0-9_]+)?$`,
			`^ss -ln[tu]p?$`,
			`^vector validate(?: --no-environment)?(?: /etc/vector/vector\.toml)?$`,
			`^grep -cF '\[sinks\.(?:prometheus_remote|victorialogs)\.auth\]' /etc/vector/vector\.toml$`,
			`^kubectl get (?:nodes|pods|pv|pvc|events|deploy|svc|endpoints|canaries)(?: -A| -n [a-z0-9-]+)?(?: -o wide)?(?: --field-selector=[A-Za-z.=!,-]+)?$`,
			`^kubectl describe (?:node|pod|pv|pvc|canary) [A-Za-z0-9.-]+(?: -n [a-z0-9-]+)?$`,
			`^kubectl logs -n kube-system [a-z0-9-]+ --tail=[0-9]{1,3}$`,
			`^crictl ps(?: -a)?$`,
		},
		CommandBlacklist: []string{
			`[sS][eE][cC][rR][eE][tT]`,
			`^\s*sudo\b`, `^\s*su\b`, `^\s*cat\b`, `^\s*rm\b`, `^\s*mv\b`, `^\s*cp\b`,
			`^\s*(?:reboot|shutdown|halt|poweroff|kill|pkill|killall)\b`,
			`^\s*systemctl (?:start|stop|restart|reload|enable|disable|mask|edit|set-property|daemon-reload)\b`,
			`^\s*kubectl (?:delete|apply|edit|patch|scale|rollout|drain|cordon|uncordon|label|annotate|exec|cp|create|replace|run)\b`,
			`^\s*(?:curl|wget|tee|dd|chmod|chown)\b`,
		},
		AllowedRemotePaths:  []string{"/tmp/ssh-mcp-sandbox"},
		Pty:                 false,
		CommandTimeoutMs:    15000,
		ConnectionTimeoutMs: 10000,
		MaxOutputBytes:      262144,
	}
}

// LoadPolicy reads a policy JSON file; an empty path returns DefaultPolicy.
func LoadPolicy(path string) (Policy, error) {
	if path == "" {
		return DefaultPolicy(), nil
	}
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return Policy{}, err
	}
	p := DefaultPolicy()
	if err := json.Unmarshal(data, &p); err != nil {
		return Policy{}, fmt.Errorf("parse %s: %w", path, err)
	}
	if len(p.CommandWhitelist) == 0 {
		return Policy{}, errors.New("policy must define a non-empty commandWhitelist")
	}
	return p, nil
}

// Options select which credentials the rendered connections reference.
type Options struct {
	PrivateKeyPath string // path to a key file materialised from Vault
	AgentSocket    string // SSH agent socket (alternative to PrivateKeyPath)
	// HostMap optionally maps a CMDB hostname or connection name to the
	// address to dial (e.g. for short names like sit-k3s-v6). It is loaded at
	// render time from a Vault-sourced file, so addresses never enter git or
	// the CMDB; only the rendered runtime file contains them.
	HostMap map[string]string
}

// LoadHostMap reads a JSON object {"<hostname or connection name>": "<address>"}.
// An empty path returns nil.
func LoadHostMap(path string) (map[string]string, error) {
	if path == "" {
		return nil, nil
	}
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return nil, err
	}
	m := map[string]string{}
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parse host map %s: %w", path, err)
	}
	return m, nil
}

// Connection is one ssh-mcp-server connection entry.
type Connection struct {
	Name       string `json:"name"`
	Host       string `json:"host"`
	Port       int    `json:"port"`
	Username   string `json:"username"`
	PrivateKey string `json:"privateKey,omitempty"`
	Agent      string `json:"agent,omitempty"`
	Policy
}

// Render builds connections for every resource carrying SSH access. It
// returns the distinct Vault references the operator must resolve.
func Render(resources []cmdb.Resource, policy Policy, opts Options) ([]Connection, []string, error) {
	if opts.PrivateKeyPath == "" && opts.AgentSocket == "" {
		return nil, nil, errors.New("no SSH credential: set a private key path (materialised from Vault) or an SSH agent socket")
	}
	if len(policy.CommandWhitelist) == 0 {
		return nil, nil, errors.New("refusing to render without a command whitelist")
	}
	conns := []Connection{}
	refs := map[string]bool{}
	seen := map[string]string{}
	for _, r := range resources {
		if r.SSH == nil || r.SSH.Host == "" {
			continue
		}
		name := r.SSH.ConnectionName
		if name == "" {
			name = r.SSH.Host
		}
		if prev, dup := seen[name]; dup {
			return nil, nil, fmt.Errorf("duplicate connection name %q (%s and %s)", name, prev, r.ID)
		}
		seen[name] = r.ID
		port := r.SSH.Port
		if port == 0 {
			port = 22
		}
		user := r.SSH.User
		if user == "" {
			user = cmdb.DefaultSSHUser
		}
		host := r.SSH.Host
		if addr, ok := opts.HostMap[r.SSH.Host]; ok && addr != "" {
			host = addr
		} else if addr, ok := opts.HostMap[name]; ok && addr != "" {
			host = addr
		}
		c := Connection{Name: name, Host: host, Port: port, Username: user, Policy: policy}
		if opts.PrivateKeyPath != "" {
			c.PrivateKey = opts.PrivateKeyPath
		} else {
			c.Agent = opts.AgentSocket
		}
		conns = append(conns, c)
		if r.SSH.VaultRef != "" {
			refs[r.SSH.VaultRef] = true
		}
	}
	if len(conns) == 0 {
		return nil, nil, errors.New("no CMDB host resources with SSH access matched")
	}
	sort.Slice(conns, func(i, j int) bool { return conns[i].Name < conns[j].Name })
	vaultRefs := make([]string, 0, len(refs))
	for ref := range refs {
		vaultRefs = append(vaultRefs, ref)
	}
	sort.Strings(vaultRefs)
	return conns, vaultRefs, nil
}

// WriteFile writes connections as JSON with 0600 permissions.
func WriteFile(path string, conns []Connection) error {
	data, err := json.MarshalIndent(conns, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o600)
}
