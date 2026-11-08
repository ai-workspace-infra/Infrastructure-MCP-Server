package catalog

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/ai-workspace-infra/Infrastructure-MCP-Server/internal/telemetry"
)

type Store struct {
	mu         sync.RWMutex
	items      []Item
	httpClient *http.Client
	portalURL  string
	telemetry  *telemetry.Engine
}

func NewStore(portalURL string, engine ...*telemetry.Engine) *Store {
	if portalURL == "" {
		portalURL = "https://console.svc.plus"
	}
	var tel *telemetry.Engine
	if len(engine) > 0 && engine[0] != nil {
		tel = engine[0]
	} else {
		tel = telemetry.NewEngine(telemetry.Config{}, nil)
	}

	return &Store{
		items: defaultCatalogItems(),
		httpClient: &http.Client{
			Timeout: 2 * time.Second,
		},
		portalURL: portalURL,
		telemetry: tel,
	}
}

func (s *Store) WithTelemetry(engine *telemetry.Engine) *Store {
	if engine != nil {
		s.telemetry = engine
	}
	return s
}

// Query filters items dynamically driven by discovered environments and real-time observability probes.
func (s *Store) Query(ctx context.Context, filter Filter) Result {
	s.mu.RLock()
	baseItems := make([]Item, len(s.items))
	copy(baseItems, s.items)
	s.mu.RUnlock()

	// Dynamically discover active environments
	topo, _ := s.telemetry.DiscoverTopology(ctx)

	env := strings.ToUpper(strings.TrimSpace(filter.Environment))
	if env == "" {
		if len(topo.Environments) > 0 {
			for _, e := range topo.Environments {
				if strings.Contains(strings.ToLower(e), "prod") {
					env = strings.ToUpper(e)
					break
				}
			}
			if env == "" {
				env = strings.ToUpper(topo.Environments[0])
			}
		} else {
			env = "PROD"
		}
	}

	liveSynced := false
	source := "Dynamic Catalog Registry"

	// 1. Try real-time probe queries from VictoriaMetrics for this environment
	probeSuccessMap, probeCodeMap, probeLatencyMap, err := s.fetchLiveProbeMetrics(ctx, strings.ToLower(env))
	if err == nil && len(probeSuccessMap) > 0 {
		for i := range baseItems {
			key := fmt.Sprintf("%s:%s", strings.ToLower(baseItems[i].Service), baseItems[i].Path)
			if pass, ok := probeSuccessMap[key]; ok {
				liveSynced = true
				if pass {
					baseItems[i].Status = "PASS"
				} else {
					baseItems[i].Status = "FAIL"
				}
			}
			if code, ok := probeCodeMap[key]; ok && code > 0 {
				baseItems[i].ObservedHTTP = code
			}
			if lat, ok := probeLatencyMap[key]; ok && lat > 0 {
				baseItems[i].LatencyMs = lat
			}
		}
		source = "VictoriaMetrics Live Probes (" + env + ")"
	}

	// 2. Try real-time sync with Web Console operations-matrix API if available
	if s.portalURL != "" {
		syncedItems, err := s.fetchLiveOperationsMatrix(ctx, env)
		if err == nil && len(syncedItems) > 0 {
			baseItems = syncedItems
			liveSynced = true
			source = "Live Web Console Operations Matrix Telemetry (" + s.portalURL + ")"
		}
	}

	targetCat := strings.ToLower(strings.TrimSpace(filter.Category))
	targetSvc := strings.ToLower(strings.TrimSpace(filter.Service))
	kw := strings.ToLower(strings.TrimSpace(filter.Query))

	var matched []Item
	catMap := make(map[string]*CategorySummary)

	for _, item := range baseItems {
		// Category filter (all, frontend, api)
		if targetCat != "" && targetCat != "all" {
			if targetCat == "backend" {
				targetCat = "api"
			}
			if item.Type != targetCat {
				continue
			}
		}

		// Service filter (console, accounts, billing)
		if targetSvc != "" {
			if !strings.EqualFold(item.Service, targetSvc) {
				continue
			}
		}

		// Keyword search matching path, description, category, service
		if kw != "" {
			text := strings.ToLower(item.Path + " " + item.Description + " " + item.CategoryName + " " + item.CategoryEnName + " " + item.Service + " " + item.Method)
			if !strings.Contains(text, kw) {
				continue
			}
		}

		// Adjust domain for non-prod environment
		itemCopy := item
		if !strings.Contains(strings.ToLower(env), "prod") {
			if !strings.Contains(itemCopy.ServiceDomain, strings.ToLower(env)) {
				itemCopy.ServiceDomain = itemCopy.ServiceDomain + " (" + strings.ToLower(env) + ")"
			}
		}

		matched = append(matched, itemCopy)

		// Aggregate category summary
		if summary, exists := catMap[item.CategoryKey]; exists {
			summary.Count++
		} else {
			catMap[item.CategoryKey] = &CategorySummary{
				Key:    item.CategoryKey,
				Name:   item.CategoryName,
				EnName: item.CategoryEnName,
				Type:   item.Type,
				Count:  1,
			}
		}
	}

	categories := make([]CategorySummary, 0, len(catMap))
	for _, cat := range catMap {
		categories = append(categories, *cat)
	}

	return Result{
		Total:       len(matched),
		Environment: env,
		Categories:  categories,
		Items:       matched,
		LiveSynced:  liveSynced,
		Source:      source,
	}
}

func (s *Store) fetchLiveProbeMetrics(ctx context.Context, env string) (map[string]bool, map[string]int, map[string]int, error) {
	querySuccess := fmt.Sprintf(`probe_success{environment="%s"}`, env)
	queryCode := fmt.Sprintf(`probe_http_status_code{environment="%s"}`, env)
	queryDuration := fmt.Sprintf(`probe_duration_seconds{environment="%s"}`, env)

	successVectors, err := s.telemetry.QueryMetrics(ctx, querySuccess)
	if err != nil || len(successVectors) == 0 {
		return nil, nil, nil, err
	}

	successMap := make(map[string]bool)
	for _, vec := range successVectors {
		svc := strings.ToLower(vec.Metric["service"])
		route := vec.Metric["route"]
		if svc != "" && route != "" {
			if fVal, ok := telemetry.ParseMetricFloat(vec.Value); ok {
				successMap[fmt.Sprintf("%s:%s", svc, route)] = (int(fVal) == 1)
			}
		}
	}

	codeMap := make(map[string]int)
	codeVectors, _ := s.telemetry.QueryMetrics(ctx, queryCode)
	for _, vec := range codeVectors {
		svc := strings.ToLower(vec.Metric["service"])
		route := vec.Metric["route"]
		if svc != "" && route != "" {
			if fVal, ok := telemetry.ParseMetricFloat(vec.Value); ok {
				codeMap[fmt.Sprintf("%s:%s", svc, route)] = int(fVal)
			}
		}
	}

	latencyMap := make(map[string]int)
	durVectors, _ := s.telemetry.QueryMetrics(ctx, queryDuration)
	for _, vec := range durVectors {
		svc := strings.ToLower(vec.Metric["service"])
		route := vec.Metric["route"]
		if svc != "" && route != "" {
			if fVal, ok := telemetry.ParseMetricFloat(vec.Value); ok {
				latencyMap[fmt.Sprintf("%s:%s", svc, route)] = int(fVal * 1000)
			}
		}
	}

	return successMap, codeMap, latencyMap, nil
}

func (s *Store) fetchLiveOperationsMatrix(ctx context.Context, env string) ([]Item, error) {
	reqURL := s.portalURL + "/api/telemetry/operations-matrix?env=" + env
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, http.ErrNotSupported
	}

	var payload struct {
		AllRoutesAndApis []struct {
			ID             string `json:"id"`
			Order          int    `json:"order"`
			Type           string `json:"type"`
			CategoryKey    string `json:"categoryKey"`
			CategoryName   string `json:"categoryName"`
			CategoryEnName string `json:"categoryEnName"`
			Service        string `json:"service"`
			ServiceDomain  string `json:"serviceDomain"`
			Path           string `json:"path"`
			Description    string `json:"description"`
			Method         string `json:"method"`
			ExpectedHTTP   int    `json:"expectedHttp"`
			ObservedHTTP   int    `json:"observedHttp"`
			LatencyMs      int    `json:"latencyMs"`
			Result         string `json:"result"`
		} `json:"allRoutesAndApis"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}

	if len(payload.AllRoutesAndApis) == 0 {
		return nil, nil
	}

	items := make([]Item, 0, len(payload.AllRoutesAndApis))
	for _, raw := range payload.AllRoutesAndApis {
		items = append(items, Item{
			ID:             raw.ID,
			Order:          raw.Order,
			Type:           raw.Type,
			CategoryKey:    raw.CategoryKey,
			CategoryName:   raw.CategoryName,
			CategoryEnName: raw.CategoryEnName,
			Service:        raw.Service,
			ServiceDomain:  raw.ServiceDomain,
			Path:           raw.Path,
			Description:    raw.Description,
			Method:         raw.Method,
			ExpectedHTTP:   raw.ExpectedHTTP,
			ObservedHTTP:   raw.ObservedHTTP,
			LatencyMs:      raw.LatencyMs,
			Status:         raw.Result,
		})
	}
	return items, nil
}

// defaultCatalogItems returns the complete standardized route & microservice API catalog.
func defaultCatalogItems() []Item {
	return []Item{
		// Frontend - fe-portal
		{
			ID: "fe-1", Order: 1, Type: "frontend",
			CategoryKey: "fe-portal", CategoryName: "产品、服务与门户", CategoryEnName: "Portal, Products & Services",
			Service: "Console", ServiceDomain: "console.svc.plus", Path: "/",
			Description: "云原生智能体协同平台首页与产品矩阵入口", Method: "GET",
			ExpectedHTTP: 200, ObservedHTTP: 200, LatencyMs: 33, AuthPolicy: "Public", Status: "PASS",
		},
		{
			ID: "fe-4", Order: 4, Type: "frontend",
			CategoryKey: "fe-portal", CategoryName: "产品、服务与门户", CategoryEnName: "Portal, Products & Services",
			Service: "Console", ServiceDomain: "console.svc.plus", Path: "/features",
			Description: "平台核心特性矩阵与跨云架构全景展示", Method: "GET",
			ExpectedHTTP: 200, ObservedHTTP: 200, LatencyMs: 42, AuthPolicy: "Public", Status: "PASS",
		},
		// Frontend - fe-auth
		{
			ID: "fe-8", Order: 8, Type: "frontend",
			CategoryKey: "fe-auth", CategoryName: "统一认证与访问管理", CategoryEnName: "Authentication & Security",
			Service: "Console", ServiceDomain: "console.svc.plus", Path: "/login",
			Description: "企业统一身份认证登入页面 (SSO / OIDC)", Method: "GET",
			ExpectedHTTP: 200, ObservedHTTP: 200, LatencyMs: 182, AuthPolicy: "Public", Status: "PASS",
		},
		{
			ID: "fe-9", Order: 9, Type: "frontend",
			CategoryKey: "fe-auth", CategoryName: "统一认证与访问管理", CategoryEnName: "Authentication & Security",
			Service: "Console", ServiceDomain: "console.svc.plus", Path: "/register",
			Description: "新租户组织入驻与管理员账号注册", Method: "GET",
			ExpectedHTTP: 200, ObservedHTTP: 200, LatencyMs: 145, AuthPolicy: "Public", Status: "PASS",
		},
		// Frontend - fe-agent
		{
			ID: "fe-14", Order: 14, Type: "frontend",
			CategoryKey: "fe-agent", CategoryName: "智能体与模型协同控制台", CategoryEnName: "Agent & LLM Workspaces",
			Service: "Console", ServiceDomain: "console.svc.plus", Path: "/panel/agent",
			Description: "智能体编排与工作流协同面板 (未登录 307 重定向至登录页)", Method: "GET",
			ExpectedHTTP: 307, ObservedHTTP: 307, LatencyMs: 246, AuthPolicy: "Session Cookie", Status: "PASS",
		},
		{
			ID: "fe-15", Order: 15, Type: "frontend",
			CategoryKey: "fe-agent", CategoryName: "智能体与模型协同控制台", CategoryEnName: "Agent & LLM Workspaces",
			Service: "Console", ServiceDomain: "console.svc.plus", Path: "/panel/workspace",
			Description: "多租户会话与跨集群工作区协同控制台", Method: "GET",
			ExpectedHTTP: 200, ObservedHTTP: 200, LatencyMs: 88, AuthPolicy: "Session Cookie", Status: "PASS",
		},
		// Frontend - fe-billing
		{
			ID: "fe-20", Order: 20, Type: "frontend",
			CategoryKey: "fe-billing", CategoryName: "商业账单、订阅与支付中心", CategoryEnName: "Billing, Plans & Orders",
			Service: "Billing", ServiceDomain: "billing.svc.plus", Path: "/panel/billing",
			Description: "企业订阅方案计划、按量抵扣与资源配额中心", Method: "GET",
			ExpectedHTTP: 200, ObservedHTTP: 200, LatencyMs: 78, AuthPolicy: "Session Cookie", Status: "PASS",
		},
		{
			ID: "fe-21", Order: 21, Type: "frontend",
			CategoryKey: "fe-billing", CategoryName: "商业账单、订阅与支付中心", CategoryEnName: "Billing, Plans & Orders",
			Service: "Billing", ServiceDomain: "billing.svc.plus", Path: "/pricing",
			Description: "商业版本订阅对比与阶梯定价透明展示页", Method: "GET",
			ExpectedHTTP: 200, ObservedHTTP: 200, LatencyMs: 45, AuthPolicy: "Public", Status: "PASS",
		},
		// Frontend - fe-ops
		{
			ID: "fe-25", Order: 25, Type: "frontend",
			CategoryKey: "fe-ops", CategoryName: "运维控制台与服务管理", CategoryEnName: "Operations, Observability & Mesh",
			Service: "Console", ServiceDomain: "console.svc.plus", Path: "/panel/operations",
			Description: "高保真运维看板与全量路由 API 渐进式发布矩阵", Method: "GET",
			ExpectedHTTP: 200, ObservedHTTP: 200, LatencyMs: 65, AuthPolicy: "Session Cookie", Status: "PASS",
		},

		// Backend APIs - be-portal-core
		{
			ID: "be-1", Order: 31, Type: "api",
			CategoryKey: "be-portal-core", CategoryName: "Web Portal 核心与系统探针", CategoryEnName: "Portal Core & Probes",
			Service: "Console", ServiceDomain: "console.svc.plus", Path: "/healthz",
			Description: "Web Portal 服务存活与内核探针", Method: "GET",
			ExpectedHTTP: 200, ObservedHTTP: 200, LatencyMs: 38, AuthPolicy: "Public", Status: "PASS",
		},
		{
			ID: "be-2", Order: 32, Type: "api",
			CategoryKey: "be-portal-core", CategoryName: "Web Portal 核心与系统探针", CategoryEnName: "Portal Core & Probes",
			Service: "Console", ServiceDomain: "console.svc.plus", Path: "/readyz",
			Description: "Web Portal 服务就绪与上游依赖健康度探针", Method: "GET",
			ExpectedHTTP: 200, ObservedHTTP: 200, LatencyMs: 40, AuthPolicy: "Public", Status: "PASS",
		},
		// Backend APIs - be-portal-ops
		{
			ID: "be-5", Order: 35, Type: "api",
			CategoryKey: "be-portal-ops", CategoryName: "运维监控与遥测聚合", CategoryEnName: "Operations & Telemetry APIs",
			Service: "Console", ServiceDomain: "console.svc.plus", Path: "/api/telemetry/operations-matrix",
			Description: "运维矩阵与渐进式发布全量遥测动态聚合接口", Method: "GET",
			ExpectedHTTP: 200, ObservedHTTP: 200, LatencyMs: 55, AuthPolicy: "Session Cookie / Bearer Token", Status: "PASS",
		},
		// Backend APIs - be-accounts-auth
		{
			ID: "be-8", Order: 38, Type: "api",
			CategoryKey: "be-accounts-auth", CategoryName: "用户认证、会话与多因素安全", CategoryEnName: "Auth, Session & MFA APIs",
			Service: "Accounts", ServiceDomain: "accounts.svc.plus", Path: "/healthz",
			Description: "Accounts 认证微服务存活探针", Method: "GET",
			ExpectedHTTP: 200, ObservedHTTP: 200, LatencyMs: 41, AuthPolicy: "Public", Status: "PASS",
		},
		{
			ID: "be-9", Order: 39, Type: "api",
			CategoryKey: "be-accounts-auth", CategoryName: "用户认证、会话与多因素安全", CategoryEnName: "Auth, Session & MFA APIs",
			Service: "Accounts", ServiceDomain: "accounts.svc.plus", Path: "/readyz",
			Description: "Accounts 认证微服务数据库就绪探针", Method: "GET",
			ExpectedHTTP: 200, ObservedHTTP: 200, LatencyMs: 57, AuthPolicy: "Public", Status: "PASS",
		},
		{
			ID: "be-10", Order: 40, Type: "api",
			CategoryKey: "be-accounts-auth", CategoryName: "用户认证、会话与多因素安全", CategoryEnName: "Auth, Session & MFA APIs",
			Service: "Accounts", ServiceDomain: "accounts.svc.plus", Path: "/api/ping",
			Description: "Accounts 微服务链路连通性心跳接口", Method: "GET",
			ExpectedHTTP: 200, ObservedHTTP: 200, LatencyMs: 64, AuthPolicy: "Public", Status: "PASS",
		},
		{
			ID: "be-11", Order: 41, Type: "api",
			CategoryKey: "be-accounts-auth", CategoryName: "用户认证、会话与多因素安全", CategoryEnName: "Auth, Session & MFA APIs",
			Service: "Accounts", ServiceDomain: "accounts.svc.plus", Path: "/api/auth/session",
			Description: "当前上下文用户凭证与会话状态校验", Method: "GET",
			ExpectedHTTP: 200, ObservedHTTP: 200, LatencyMs: 62, AuthPolicy: "Session Cookie", Status: "PASS",
		},
		// Backend APIs - be-billing-plan
		{
			ID: "be-20", Order: 50, Type: "api",
			CategoryKey: "be-billing-plan", CategoryName: "计费订阅计划与定价策略", CategoryEnName: "Plans & Subscription Templates",
			Service: "Billing", ServiceDomain: "billing.svc.plus", Path: "/healthz",
			Description: "Billing 账单微服务存活探针", Method: "GET",
			ExpectedHTTP: 200, ObservedHTTP: 200, LatencyMs: 35, AuthPolicy: "Public", Status: "PASS",
		},
		{
			ID: "be-21", Order: 51, Type: "api",
			CategoryKey: "be-billing-plan", CategoryName: "计费订阅计划与定价策略", CategoryEnName: "Plans & Subscription Templates",
			Service: "Billing", ServiceDomain: "billing.svc.plus", Path: "/readyz",
			Description: "Billing 账单微服务支付网关连通性探针", Method: "GET",
			ExpectedHTTP: 200, ObservedHTTP: 200, LatencyMs: 48, AuthPolicy: "Public", Status: "PASS",
		},
		{
			ID: "be-22", Order: 52, Type: "api",
			CategoryKey: "be-billing-plan", CategoryName: "计费订阅计划与定价策略", CategoryEnName: "Plans & Subscription Templates",
			Service: "Billing", ServiceDomain: "billing.svc.plus", Path: "/api/ping",
			Description: "Billing 账单微服务心跳探活接口", Method: "GET",
			ExpectedHTTP: 200, ObservedHTTP: 200, LatencyMs: 71, AuthPolicy: "Public", Status: "PASS",
		},
		{
			ID: "be-23", Order: 53, Type: "api",
			CategoryKey: "be-billing-plan", CategoryName: "计费订阅计划与定价策略", CategoryEnName: "Plans & Subscription Templates",
			Service: "Billing", ServiceDomain: "billing.svc.plus", Path: "/api/billing/plans",
			Description: "获取全量商业订阅套餐、席位单价与特性权益清单", Method: "GET",
			ExpectedHTTP: 200, ObservedHTTP: 200, LatencyMs: 85, AuthPolicy: "Public / Bearer Token", Status: "PASS",
		},
		// Backend APIs - be-billing-order
		{
			ID: "be-25", Order: 55, Type: "api",
			CategoryKey: "be-billing-order", CategoryName: "账单交易订单与支付凭证", CategoryEnName: "Orders & Invoices",
			Service: "Billing", ServiceDomain: "billing.svc.plus", Path: "/api/billing/orders",
			Description: "查询组织租户历史订单与账单开票明细", Method: "GET",
			ExpectedHTTP: 200, ObservedHTTP: 200, LatencyMs: 110, AuthPolicy: "Session Cookie / Bearer Token", Status: "PASS",
		},
	}
}
