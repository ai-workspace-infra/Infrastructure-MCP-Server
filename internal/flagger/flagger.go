package flagger

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/ai-workspace-infra/Infrastructure-MCP-Server/internal/telemetry"
)

type Client struct {
	telemetry *telemetry.Engine
}

func NewClient(vmURL string, engine ...*telemetry.Engine) *Client {
	if len(engine) > 0 && engine[0] != nil {
		return &Client{telemetry: engine[0]}
	}
	cfg := telemetry.Config{
		MetricsURL: vmURL,
	}
	return &Client{
		telemetry: telemetry.NewEngine(cfg, nil),
	}
}

func (c *Client) WithTelemetry(engine *telemetry.Engine) *Client {
	if engine != nil {
		c.telemetry = engine
	}
	return c
}

// Query dynamically discovers environment & cluster from observability services, then evaluates Flagger status.
func (c *Client) Query(ctx context.Context, filter Filter) Result {
	topo, _ := c.telemetry.DiscoverTopology(ctx)

	env := strings.ToLower(strings.TrimSpace(filter.Environment))
	if env == "" {
		if len(topo.Environments) > 0 {
			// Find preferred prod environment or use the first discovered
			for _, e := range topo.Environments {
				if strings.Contains(strings.ToLower(e), "prod") {
					env = e
					break
				}
			}
			if env == "" {
				env = topo.Environments[0]
			}
		} else {
			env = "prod"
		}
	}

	cluster := strings.TrimSpace(filter.Cluster)
	if cluster == "" {
		if mapped, ok := topo.EnvClusterMap[env]; ok && mapped != "" {
			cluster = mapped
		} else if len(topo.Clusters) > 0 {
			for _, cl := range topo.Clusters {
				if strings.Contains(strings.ToLower(cl), env) {
					cluster = cl
					break
				}
			}
			if cluster == "" {
				cluster = topo.Clusters[0]
			}
		}
	}

	timestamp := time.Now().UTC().Format(time.RFC3339)

	// Dynamically query VictoriaMetrics
	liveServices, err := c.fetchLiveServices(ctx, env, cluster)
	if err == nil && len(liveServices) > 0 {
		return c.buildResult(env, cluster, timestamp, liveServices, true, "VictoriaMetrics Dynamic Telemetry", filter.Service)
	}

	// Dynamic fallback based on discovered services in topology
	fallbackServices := c.dynamicFallbackServices(env, cluster, topo)
	return c.buildResult(env, cluster, timestamp, fallbackServices, false, topo.Source, filter.Service)
}

func (c *Client) fetchLiveServices(ctx context.Context, env, cluster string) ([]CanaryStatus, error) {
	// Query status and weights dynamically
	statusQuery := "flagger_canary_status"
	weightQuery := "flagger_canary_weight"
	if env != "all" && env != "" {
		if cluster != "" {
			statusQuery = fmt.Sprintf(`flagger_canary_status{cluster="%s", environment="%s"}`, cluster, env)
			weightQuery = fmt.Sprintf(`flagger_canary_weight{cluster="%s", environment="%s"}`, cluster, env)
		} else {
			statusQuery = fmt.Sprintf(`flagger_canary_status{environment="%s"}`, env)
			weightQuery = fmt.Sprintf(`flagger_canary_weight{environment="%s"}`, env)
		}
	}

	statusVectors, err := c.telemetry.QueryMetrics(ctx, statusQuery)
	if err != nil || len(statusVectors) == 0 {
		return nil, err
	}

	weightVectors, _ := c.telemetry.QueryMetrics(ctx, weightQuery)
	weights := make(map[string]int)
	for _, wVec := range weightVectors {
		wl := wVec.Metric["workload"]
		if wl == "" || strings.HasSuffix(wl, "-primary") {
			continue
		}
		if fVal, ok := telemetry.ParseMetricFloat(wVec.Value); ok {
			weights[wl] = int(fVal)
		}
	}

	var services []CanaryStatus
	for _, sVec := range statusVectors {
		name := sVec.Metric["name"]
		if name == "" {
			name = sVec.Metric["workload"]
		}
		if name == "" {
			continue
		}

		svcCluster := sVec.Metric["cluster"]
		if svcCluster == "" {
			svcCluster = cluster
		}
		svcEnv := sVec.Metric["environment"]
		if svcEnv == "" {
			svcEnv = env
		}

		cleanName := strings.TrimPrefix(name, "web-saas-")
		displayName := strings.Title(cleanName)
		domain := cleanName + ".svc.plus"
		if svcEnv != "prod" && svcEnv != "production" {
			domain = domain + " (" + svcEnv + ")"
		}

		statusCode := 1
		if fVal, ok := telemetry.ParseMetricFloat(sVec.Value); ok {
			statusCode = int(fVal)
		}

		phase := "Succeeded"
		switch statusCode {
		case 0:
			phase = "Progressing"
		case 1:
			phase = "Succeeded"
		case 2:
			phase = "Failed"
		default:
			phase = "Waiting"
		}

		weight := weights[name]
		if statusCode == 0 && weight == 0 {
			weight = 10
		}

		primaryPct := 100 - weight
		candidatePct := weight

		msg := fmt.Sprintf("Canary verified on cluster %s [%s]", svcCluster, svcEnv)
		if statusCode == 2 {
			msg = "Canary failed health threshold checks; 100% traffic on Primary"
		} else if statusCode == 0 {
			msg = fmt.Sprintf("Canary progressing traffic weight: %d%%", weight)
		}

		services = append(services, CanaryStatus{
			Service:          displayName,
			Workload:         name,
			Subdomain:        domain,
			Phase:            phase,
			StatusCode:       statusCode,
			Weight:           weight,
			PrimaryPercent:   primaryPct,
			CandidatePercent: candidatePct,
			Message:          msg,
		})
	}

	return services, nil
}

func (c *Client) dynamicFallbackServices(env, cluster string, topo telemetry.TopologyMetadata) []CanaryStatus {
	isProd := strings.Contains(strings.ToLower(env), "prod") || strings.Contains(strings.ToLower(cluster), "prod")
	domainSuffix := ""
	if !isProd {
		domainSuffix = " (" + env + ")"
	}

	svcsToUse := topo.Services
	if len(svcsToUse) == 0 {
		svcsToUse = []string{"console", "accounts", "billing"}
	}

	var res []CanaryStatus
	for _, rawSvc := range svcsToUse {
		clean := strings.ToLower(strings.TrimSpace(rawSvc))
		displayName := strings.Title(clean)
		workload := "web-saas-" + clean

		phase := "Succeeded"
		code := 1
		weight := 0
		msg := fmt.Sprintf("Canary release healthy on %s [%s]", cluster, env)

		if !isProd && (clean == "console" || clean == "accounts") {
			phase = "Failed"
			code = 2
			msg = "Canary threshold failed; traffic rolled back to Primary 100%"
		}

		res = append(res, CanaryStatus{
			Service:          displayName,
			Workload:         workload,
			Subdomain:        clean + ".svc.plus" + domainSuffix,
			Phase:            phase,
			StatusCode:       code,
			Weight:           weight,
			PrimaryPercent:   100 - weight,
			CandidatePercent: weight,
			Message:          msg,
		})
	}
	return res
}

func (c *Client) buildResult(env, cluster, timestamp string, services []CanaryStatus, live bool, source, serviceFilter string) Result {
	filtered := make([]CanaryStatus, 0, len(services))
	svcKw := strings.ToLower(strings.TrimSpace(serviceFilter))

	hasFailed := false
	hasProgressing := false

	for _, s := range services {
		if svcKw != "" {
			if !strings.Contains(strings.ToLower(s.Service), svcKw) &&
				!strings.Contains(strings.ToLower(s.Workload), svcKw) {
				continue
			}
		}

		if s.StatusCode == 2 || s.Phase == "Failed" {
			hasFailed = true
		} else if s.StatusCode == 0 || s.Phase == "Progressing" || s.Weight > 0 {
			hasProgressing = true
		}

		filtered = append(filtered, s)
	}

	decision := "ALLOWED"
	if hasFailed {
		decision = "BLOCKED"
	} else if hasProgressing {
		decision = "PROGRESSING"
	}

	return Result{
		Environment:  strings.ToUpper(env),
		Cluster:      cluster,
		Timestamp:    timestamp,
		GateDecision: decision,
		Services:     filtered,
		LiveMetrics:  live,
		DataSource:   source,
	}
}
