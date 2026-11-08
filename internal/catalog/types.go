package catalog

// Item represents a single frontend route or backend microservice API in the catalog.
type Item struct {
	ID             string `json:"id"`
	Order          int    `json:"order"`
	Type           string `json:"type"` // "frontend" | "api"
	CategoryKey    string `json:"category_key"`
	CategoryName   string `json:"category_name"`
	CategoryEnName string `json:"category_en_name"`
	Service        string `json:"service"` // "Console" | "Accounts" | "Billing"
	ServiceDomain  string `json:"service_domain"`
	Path           string `json:"path"`
	Description    string `json:"description"`
	Method         string `json:"method"`
	ExpectedHTTP   int    `json:"expected_http"`
	ObservedHTTP   int    `json:"observed_http"`
	LatencyMs      int    `json:"latency_ms"`
	AuthPolicy     string `json:"auth_policy,omitempty"`
	Status         string `json:"status"` // "PASS" | "FAIL" | "NO DATA"
}

// Filter defines search criteria for the catalog.
type Filter struct {
	Category    string `json:"category"`    // "all" | "frontend" | "api"
	Service     string `json:"service"`     // "console" | "accounts" | "billing"
	Environment string `json:"environment"` // "sit" | "prod"
	Query       string `json:"query"`       // keyword matching path, description, category, service
}

// CategorySummary provides count aggregation per category.
type CategorySummary struct {
	Key    string `json:"key"`
	Name   string `json:"name"`
	EnName string `json:"en_name"`
	Type   string `json:"type"`
	Count  int    `json:"count"`
}

// Result is the returned response payload for infra.api_catalog.query.
type Result struct {
	Total       int               `json:"total"`
	Environment string            `json:"environment"`
	Categories  []CategorySummary `json:"categories"`
	Items       []Item            `json:"items"`
	LiveSynced  bool              `json:"live_synced"`
	Source      string            `json:"source"`
}
