package cmdb

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/ai-workspace-infra/Infrastructure-MCP-Server/internal/graph"
)

type Store struct {
	mu        sync.RWMutex
	resources map[string]Resource
	graph     *graph.Store
	baseDir   string
}

func NewStore(graphStore *graph.Store, baseDir string) *Store {
	if baseDir == "" {
		baseDir = "/Users/shenlan/workspaces/ai-workspace-infra"
	}
	s := &Store{
		resources: make(map[string]Resource),
		graph:     graphStore,
		baseDir:   baseDir,
	}
	// Initial baseline sync
	_, _ = s.Sync(context.Background())
	return s
}

func (s *Store) Sync(ctx context.Context) (*SyncReport, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	report := &SyncReport{
		Environments: make(map[string]int),
		Sources:      []string{"iac", "gitops", "pipelines"},
		CLIs:         make(map[string]bool),
		MCPAdapters:  []string{"github", "cloudflare"},
	}

	// 1. Check local Cloud CLIs
	for _, cliName := range []string{"linode-cli", "aws", "gcloud", "gh"} {
		_, err := exec.LookPath(cliName)
		report.CLIs[cliName] = (err == nil)
	}

	// 2. Ingest canonical SIT resources
	s.ingestResourceLocked(Resource{
		ID:          "sit/vps/all-in-one",
		Name:        "sit-vps-node",
		Environment: "sit",
		Provider:    "vultr",
		Kind:        "compute",
		IP:          "standby-on-pr",
		Status:      "standby",
		Source:      "iac",
		Attributes: map[string]any{
			"plan":         "vc2-2c-4gb",
			"declaration":  "sit/all-in-one.yaml",
			"tfstate":      "sit/vultr-vps/platform-ops-toolkit/all-in-one.tfstate",
			"routing_mode": "100% selfhost",
		},
	})
	s.ingestResourceLocked(Resource{
		ID:          "sit/dns/console-sit",
		Name:        "console-sit.onwalk.net",
		Environment: "sit",
		Provider:    "cloudflare",
		Kind:        "dns",
		Endpoint:    "https://console-sit.onwalk.net",
		Status:      "dns_only",
		Source:      "gitops",
		Attributes: map[string]any{
			"target": "console-selfhost-sit.onwalk.net",
			"ttl":    60,
		},
	})
	s.ingestResourceLocked(Resource{
		ID:          "sit/dns/accounts-sit",
		Name:        "accounts-sit.onwalk.net",
		Environment: "sit",
		Provider:    "cloudflare",
		Kind:        "dns",
		Endpoint:    "https://accounts-sit.onwalk.net",
		Status:      "dns_only",
		Source:      "gitops",
		Attributes: map[string]any{
			"target": "accounts-selfhost-sit.onwalk.net",
			"ttl":    60,
		},
	})

	// 3. Ingest canonical UAT resources
	s.ingestResourceLocked(Resource{
		ID:          "uat/linode/open-platform",
		Name:        "open-platform-node",
		Environment: "uat",
		Provider:    "linode",
		Kind:        "compute",
		IP:          "45.79.175.119",
		Status:      "running",
		Source:      "iac",
		Attributes: map[string]any{
			"plan":        "g6-standard-2 (2C4G)",
			"services":    []string{"observability", "vault", "iam"},
			"declaration": "terraform-hcl-standard/akamai-cloud/envs/uat/open-platform",
		},
	})
	s.ingestResourceLocked(Resource{
		ID:          "uat/vps/web-saas",
		Name:        "web-saas-uat-node",
		Environment: "uat",
		Provider:    "vultr",
		Kind:        "compute",
		IP:          "167.179.105.137",
		Status:      "running",
		Source:      "pipeline",
		Attributes: map[string]any{
			"services":    []string{"console", "accounts", "billing"},
			"declaration": "uat/web-saas-uat.yaml",
		},
	})
	s.ingestResourceLocked(Resource{
		ID:          "uat/dns/console-uat",
		Name:        "console-uat.onwalk.net",
		Environment: "uat",
		Provider:    "cloudflare",
		Kind:        "dns",
		Endpoint:    "https://console-uat.onwalk.net",
		Status:      "active",
		Source:      "gitops",
		Attributes: map[string]any{
			"target": "console-selfhost-uat.onwalk.net",
			"http":   "200 OK",
		},
	})
	s.ingestResourceLocked(Resource{
		ID:          "uat/dns/accounts-uat",
		Name:        "accounts-uat.onwalk.net",
		Environment: "uat",
		Provider:    "cloudflare",
		Kind:        "dns",
		Endpoint:    "https://accounts-uat.onwalk.net",
		Status:      "active",
		Source:      "gitops",
	})
	s.ingestResourceLocked(Resource{
		ID:          "uat/gcp/cloud-run",
		Name:        "uat-serverless-fallback",
		Environment: "uat",
		Provider:    "gcp",
		Kind:        "service",
		Status:      "ready",
		Source:      "pipeline",
		Attributes: map[string]any{
			"region": "asia-east1",
			"scale":  "0-to-1",
		},
	})

	// 4. Ingest canonical PROD resources
	s.ingestResourceLocked(Resource{
		ID:          "prod/gcp/console",
		Name:        "console-prod-host",
		Environment: "prod",
		Provider:    "gcp",
		Kind:        "compute",
		IP:          "34.81.174.112",
		Status:      "running",
		Source:      "iac",
		Attributes: map[string]any{
			"os_login": true,
			"service":  "console.svc.plus",
		},
	})
	s.ingestResourceLocked(Resource{
		ID:          "prod/vultr/jp-k3s",
		Name:        "jp-k3s-vultr-node",
		Environment: "prod",
		Provider:    "vultr",
		Kind:        "compute",
		IP:          "167.179.110.129",
		Status:      "running",
		Source:      "iac",
		Attributes: map[string]any{
			"cluster": "k3s-prod",
		},
	})
	s.ingestResourceLocked(Resource{
		ID:          "prod/aws/tky-proxy",
		Name:        "tky-proxy-host",
		Environment: "prod",
		Provider:    "aws",
		Kind:        "compute",
		IP:          "43.207.194.92",
		Status:      "running",
		Source:      "iac",
		Attributes: map[string]any{
			"region": "ap-northeast-1",
			"role":   "agent_proxy / xray_exporter",
		},
	})
	s.ingestResourceLocked(Resource{
		ID:          "prod/dns/console-prod",
		Name:        "console.svc.plus",
		Environment: "prod",
		Provider:    "cloudflare",
		Kind:        "dns",
		Endpoint:    "https://console.svc.plus",
		Status:      "active",
		Source:      "gitops",
		Attributes: map[string]any{
			"http": "200 OK",
		},
	})
	s.ingestResourceLocked(Resource{
		ID:          "prod/dns/observability-prod",
		Name:        "observability.svc.plus",
		Environment: "prod",
		Provider:    "cloudflare",
		Kind:        "dns",
		Endpoint:    "https://observability.svc.plus",
		Status:      "active",
		Source:      "gitops",
		Attributes: map[string]any{
			"http": "200 OK",
			"mcp":  "4-mcp active",
		},
	})
	s.ingestResourceLocked(Resource{
		ID:          "prod/dns/vault-prod",
		Name:        "vault.svc.plus",
		Environment: "prod",
		Provider:    "cloudflare",
		Kind:        "dns",
		Endpoint:    "https://vault.svc.plus",
		Status:      "active",
		Source:      "gitops",
		Attributes: map[string]any{
			"http": "200 OK",
		},
	})
	s.ingestResourceLocked(Resource{
		ID:          "prod/dns/root-prod",
		Name:        "svc.plus",
		Environment: "prod",
		Provider:    "cloudflare",
		Kind:        "dns",
		Endpoint:    "https://svc.plus",
		Status:      "active",
		Source:      "gitops",
		Attributes: map[string]any{
			"http": "200 OK",
		},
	})

	// 5. Ingest existing pipeline cmdb.json if found
	candidates := []string{
		filepath.Join(s.baseDir, "platform-ops-toolkit", "cmdb.json"),
		filepath.Join(s.baseDir, "toolkit-serverless-refactor", "cmdb.json"),
	}
	for _, cand := range candidates {
		if data, err := os.ReadFile(cand); err == nil {
			var rawMap map[string]map[string]any
			if json.Unmarshal(data, &rawMap) == nil {
				for hostname, hostInfo := range rawMap {
					resID := fmt.Sprintf("cmdb/%s", hostname)
					ip, _ := hostInfo["ip"].(string)
					s.ingestResourceLocked(Resource{
						ID:          resID,
						Name:        hostname,
						Environment: "uat",
						Provider:    "vultr",
						Kind:        "compute",
						IP:          ip,
						Status:      "running",
						Source:      "pipeline",
						Attributes:  hostInfo,
					})
				}
			}
		}
	}

	for _, r := range s.resources {
		report.Environments[r.Environment]++
	}
	report.TotalSynced = len(s.resources)
	report.Summaries = s.summaryLocked()

	return report, nil
}

func (s *Store) ingestResourceLocked(r Resource) {
	s.resources[r.ID] = r
	if s.graph != nil {
		s.graph.UpsertNode(graph.Node{
			ID:   r.ID,
			Kind: r.Kind,
			Name: r.Name,
			Attributes: map[string]any{
				"environment": r.Environment,
				"provider":    r.Provider,
				"status":      r.Status,
				"ip":          r.IP,
				"endpoint":    r.Endpoint,
				"source":      r.Source,
			},
		})
		s.graph.UpsertEdge(graph.Edge{
			From: fmt.Sprintf("env/%s", r.Environment),
			To:   r.ID,
			Type: "hosts_resource",
		})
	}
}

func (s *Store) Resources(env, provider, kind, status string) []Resource {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var result []Resource
	for _, r := range s.resources {
		if env != "" && !strings.EqualFold(r.Environment, env) {
			continue
		}
		if provider != "" && !strings.EqualFold(r.Provider, provider) {
			continue
		}
		if kind != "" && !strings.EqualFold(r.Kind, kind) {
			continue
		}
		if status != "" && !strings.EqualFold(r.Status, status) {
			continue
		}
		result = append(result, r)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	if result == nil {
		result = []Resource{}
	}
	return result
}

func (s *Store) Summary() []EnvironmentSummary {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.summaryLocked()
}

func (s *Store) summaryLocked() []EnvironmentSummary {
	envMap := make(map[string]*EnvironmentSummary)
	envMap["sit"] = &EnvironmentSummary{
		Environment: "sit",
		Tier:        "Integration Testing",
		Deployment:  "100% selfhost (on-demand PR / standby)",
		Status:      "standby",
	}
	envMap["uat"] = &EnvironmentSummary{
		Environment: "uat",
		Tier:        "User Acceptance Testing",
		Deployment:  "Hybrid (Akamai Linode open-platform + Vultr + GCP Fallback)",
		Status:      "healthy",
	}
	envMap["prod"] = &EnvironmentSummary{
		Environment: "prod",
		Tier:        "Production",
		Deployment:  "Multi-Cloud (GCP + Akamai + AWS Tokyo + Cloudflare Edge)",
		Status:      "healthy",
	}

	providerSet := make(map[string]map[string]bool)
	domainSet := make(map[string]map[string]bool)

	for _, r := range s.resources {
		summary, ok := envMap[r.Environment]
		if !ok {
			continue
		}
		summary.ResourceCount++
		if providerSet[r.Environment] == nil {
			providerSet[r.Environment] = make(map[string]bool)
		}
		if r.Provider != "" {
			providerSet[r.Environment][r.Provider] = true
		}
		if domainSet[r.Environment] == nil {
			domainSet[r.Environment] = make(map[string]bool)
		}
		if r.Kind == "dns" || strings.Contains(r.Name, ".") {
			domainSet[r.Environment][r.Name] = true
		}

		summary.Services = append(summary.Services, ServiceStatus{
			Name:     r.Name,
			Domain:   r.Endpoint,
			Kind:     r.Kind,
			Provider: r.Provider,
			Status:   r.Status,
		})
	}

	orderedEnvs := []string{"sit", "uat", "prod"}
	var list []EnvironmentSummary
	for _, env := range orderedEnvs {
		if sum, ok := envMap[env]; ok {
			for p := range providerSet[env] {
				sum.Providers = append(sum.Providers, p)
			}
			sort.Strings(sum.Providers)
			for d := range domainSet[env] {
				sum.Domains = append(sum.Domains, d)
			}
			sort.Strings(sum.Domains)
			list = append(list, *sum)
		}
	}
	return list
}
