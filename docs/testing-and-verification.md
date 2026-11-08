# 外部调用测试与实盘验证用例 (Testing & Verification)

本指南汇总了针对 **`https://mcp.svc.plus`** 的实机联调测试用例、curl 脚本及验证结果。

---

## 1. 验证前置准备与网络拓扑

- **外部入口域名**：`https://mcp.svc.plus`
- **目标宿主机 IP**：`172.237.9.58`（SIT-K3S-V6 宿主机，Traefik 监听 `:443`）
- **TLS 证书规范**：已由 Let's Encrypt 签发（CN=svc.plus, SAN 包含 `*.svc.plus` 和 `svc.plus`），全局标准受信任，curl 发起请求无需添加 `-k` 或 `--insecure`。
- **协议兼容建议**：为避免跨公网长距离链路中部分客户端 HTTP/2 连接复用延迟，使用 curl 测试时建议显式指定 `--http1.1`。

---

## 2. 自动化实盘测试脚本与输出

### 用例 1：服务根目录与四模式元数据自省
```bash
curl -s --resolve mcp.svc.plus:443:172.237.9.58 "https://mcp.svc.plus/" | jq .
```
**实测响应片段**：
```json
{
  "cluster": "SIT-K3S-V6",
  "entrypoint": "https://mcp.svc.plus",
  "host_ip": "172.237.9.58",
  "service": "infrastructure-mcp-server",
  "status": "healthy",
  "version": "0.3.0",
  "downstream_adapters": {
    "grafana_mcp": "http://mcp-grafana.platform-mcp-sit:8000/v1/grafana/sse",
    "victoriametrics": "http://observability-observability-stack-victoria-metrics.observability-sit:8428",
    "victorialogs": "http://observability-observability-stack-victoria-logs.observability-sit:9428",
    "victoriatraces": "http://observability-observability-stack-victoria-traces.observability-sit:10428"
  },
  "modes": {
    "mode_1_web_rest_api": { "description": "Universal HTTP REST APIs..." },
    "mode_2_mcp_tool_call": { "endpoint": "/api/v1/tools/call", "method": "POST" },
    "mode_3_sse_stream": { "endpoint": "/api/v1/events?env=sit" },
    "mode_4_stdio": { "binary": "infrastructure-mcp-server" }
  }
}
```

---

### 用例 2：Mode 1 网关与 Ingress 路由查询
```bash
curl -s --resolve mcp.svc.plus:443:172.237.9.58 "https://mcp.svc.plus/api/v1/gateway?env=sit" | jq .structuredContent
```
**实测验证重点**：
- 确认 `traefik-ingress` 与 `caddy-ingress` 均处于 `running` 状态。
- 确认包含主路由 `sit-mcp-svc-plus-routes`，域名绑定 `mcp.svc.plus` 与 `mcp-sit.svc.plus`，证书为 `shared-observability-tls`。

---

### 用例 3：Mode 1 统一域名注册表查询
```bash
curl -s --resolve mcp.svc.plus:443:172.237.9.58 "https://mcp.svc.plus/api/v1/domains" | jq .structuredContent
```
**实测验证重点**：
- 平台全量 10 个核心域名均返回 `status: PASS`。
- 覆盖 `mcp.svc.plus`、`observability.svc.plus`、`console.svc.plus`、`accounts.svc.plus`、`billing.svc.plus`、`vault.svc.plus` 及兼容别名。

---

### 用例 4：Mode 1 动态资源与集群拓扑查询
```bash
curl -s --resolve mcp.svc.plus:443:172.237.9.58 "https://mcp.svc.plus/api/v1/topology" | jq .structuredContent
```
**实测验证重点**：
- 动态拉取 VictoriaMetrics series，实时呈现 `environments` 与 `clusters`。
- 呈现 `PROD-K3S-V6` (172.237.24.232) 与 `SIT-K3S-V6` (172.237.9.58) 负载与上下游依赖关系。

---

### 用例 5：Mode 2 通用 MCP 工具调用 (`POST /api/v1/tools/call`)
```bash
curl --http1.1 -s --resolve mcp.svc.plus:443:172.237.9.58 -X POST \
  "https://mcp.svc.plus/api/v1/tools/call" \
  -H "Content-Type: application/json" \
  -d '{"name": "infra.metrics.query", "arguments": {"query": "probe_success"}}' | jq .
```
**实测验证重点**：
- 返回符合 MCP Tool Result 规范的 JSON 结构体，包含 `content` 数组与 `structuredContent` 对象。

---

### 用例 6：Mode 3 Server-Sent Events 流式长连接
```bash
curl --http1.1 -N -s --max-time 10 --resolve mcp.svc.plus:443:172.237.9.58 \
  "https://mcp.svc.plus/api/v1/events?env=sit"
```
**实测推流片段**：
```text
event: connected
data: {"status":"connected","environment":"sit","endpoint":"https://mcp.svc.plus/api/v1/events","timestamp":"2026-11-08T13:11:14Z"}

event: flagger_status
data: {"service":"web-saas-billing","status":"Succeeded","weight":100}

event: gateway_status
data: {"primary_fqdn":"mcp.svc.plus","tls_valid":true,"route_count":4}
```

---

### 用例 7：Mode 4 本地 stdio 标准输入输出
本地直接执行二进制或进入容器内测试：
```bash
echo '{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}' | ./infrastructure-mcp-server
```
返回完整的 17 个工具 Schema 定义 JSON-RPC 响应。
