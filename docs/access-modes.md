# 智能体与客户端接入模式详解 (4 Access Modes)

Infrastructure MCP Server 支持业界最全面的 4 种智能体与客户端接入模式，满足从本地 IDE 到分布式云端多智能体系统的多场景需求。

---

## 接入模式全景对照表

| 接入模式 | 适用场景 | 访问入口 | 通信协议 | 响应格式 | 典型客户端 |
| :--- | :--- | :--- | :--- | :--- | :--- |
| **Mode 1: Web REST API** | Web SaaS 控制台、CI/CD 运维脚本、自动化流水线 | `GET/POST https://mcp.svc.plus/api/v1/...` | HTTP/1.1, HTTP/2 | JSON (`application/json`) | 浏览器前端、Shell 脚本、GitHub Actions、Curl |
| **Mode 2: MCP Tool Call** | 第三方 AI Agent 运行时环境 | `POST https://mcp.svc.plus/api/v1/tools/call` | HTTP POST | MCP Tool Result JSON | LangChain、AutoGPT、Dify、Coze、Agent SDK |
| **Mode 3: Server-Sent Events** | AI Agent Teams 多轮协同、运维态势大屏、状态监听 | `GET https://mcp.svc.plus/api/v1/events?env=sit` | SSE (text/event-stream) | SSE Events 流式事件 | 观测云 Obsy Agent Teams、监控推流机器人 |
| **Mode 4: 标准 JSON-RPC 2.0 stdio** | 桌面级开发者工具、深度本地上下文交互 | 进程标准输入输出 (stdin/stdout) | JSON-RPC 2.0 (NDJSON) | JSON-RPC Frame | Claude Desktop、Cursor、Trae、Antigravity |

---

## Mode 1: Web REST API

适合通过标准化 HTTP 请求快速获取基础设施状态、域名映射、拓扑与遥测数据。已内置 CORS 支持（`Access-Control-Allow-Origin: *`）。

### 核心 REST API 路由表

- `GET /healthz`：服务健康检查
- `GET /api/v1/tools`：查询当前暴露的 17 个工具元数据
- `GET /api/v1/gateway?env={sit|prod}`：查询网关路由、证书绑定与后端转发状态
- `GET /api/v1/domains`：查询平台全量域名清单及健康度
- `GET /api/v1/topology`：查询多集群计算节点、服务依赖与拓扑动态视图
- `GET /api/v1/graph?q={query}`：查询架构依赖图谱与连通子图
- `GET /api/v1/telemetry/metrics?query={promql}`：VictoriaMetrics 动态指标直查
- `GET /api/v1/telemetry/logs?query={logsql}`：VictoriaLogs 高基数日志直查
- `GET /api/v1/telemetry/traces?service={service}`：VictoriaTraces 链路 Span 直查
- `GET /api/v1/flagger?env={env}&cluster={cluster}`：Flagger 灰度发布进度与金丝雀门禁
- `GET /api/v1/catalog?env={env}&service={service}`：微服务三级路由与 API Catalog 目录
- `GET /api/v1/cmdb?env={env}`：多云计算资产与 CMDB 资源库存

---

## Mode 2: MCP 通用工具调用接口 (Generic Tool Call)

提供针对第三方 AI 智能体平台的通用工具调用端点，遵循 MCP Tool Call 规范。

### 调用规范
- **URL**: `https://mcp.svc.plus/api/v1/tools/call`
- **Method**: `POST`
- **Headers**: `Content-Type: application/json`
- **Body**:
  ```json
  {
    "name": "<tool_name>",
    "arguments": {
      "<arg_key>": "<arg_value>"
    }
  }
  ```

### 请求与响应示例
```bash
curl -s --resolve mcp.svc.plus:443:172.237.9.58 \
  -H "Content-Type: application/json" \
  -d '{"name": "infra.domains.query", "arguments": {"environment": "sit"}}' \
  "https://mcp.svc.plus/api/v1/tools/call"
```

响应：
```json
{
  "tool": "infra.domains.query",
  "result": {
    "content": [
      {
        "type": "text",
        "text": "{\"count\":10,\"domains\":[...]}"
      }
    ],
    "structuredContent": {
      "count": 10,
      "domains": [...]
    }
  }
}
```

---

## Mode 3: Server-Sent Events (SSE 实时事件推流)

专为 AI Agent Teams 多智能体协同设计。智能体只需发起一次持久长连接，即可按秒级接收基础设施集群、网关路由变化与金丝雀发布进度推送。

### 监听端点
- **URL**: `https://mcp.svc.plus/api/v1/events?env={sit|prod}`
- **标准 MCP SSE 端点**: `https://mcp.svc.plus/sse` 及 `/v1/sse`

### 事件推流规范
连接建立后首先触发 `connected` 握手事件，随后由后台调度引擎轮询下发：
1. `event: connected`：连接成功确认，回显监听环境与时间戳；
2. `event: flagger_status`：推送微服务当前金丝雀权重、步进与门禁判定；
3. `event: gateway_status`：推送网关路由状态、域名绑卡与 TLS 有效性；
4. `event: telemetry_ping`：定期下发可观测性系统存活心跳与指标摘要。

---

## Mode 4: 标准 JSON-RPC 2.0 stdio 管道

适用于本地运行的客户端直接与二进制通过标准输入输出（stdin/stdout）通信。

### 客户端配置示例

#### Cursor / Claude Desktop / Trae
```json
{
  "mcpServers": {
    "infrastructure": {
      "command": "/usr/local/bin/infrastructure-mcp-server",
      "args": ["-config", "/Users/username/.config/infrastructure-mcp-server/config.json"]
    }
  }
}
```

#### Antigravity 原生集成
可在工作区或全局 `mcp.json` 中配置该 stdio 管道，服务启动后直接完成 `initialize` 与 `tools/list` 握手，无需经过网络代理。

## Inbound authentication

When `MCP_SERVICE_TOKEN` is set, every HTTP path except `/healthz` and `/health`
requires `Authorization: Bearer <token>`. When it is unset the server logs a
warning at startup and accepts unauthenticated requests (front it with an
authenticating proxy in that case).

## Backend transports

- `streamable-http`: `Mcp-Session-Id` is stored after `initialize` and replayed; a 404 on a stale session triggers one re-initialize + retry. `text/event-stream` replies are parsed.
- `sse`: legacy HTTP+SSE (GET event stream, POST to the announced endpoint), e.g. mcp-grafana `/sse`.
- stdio: each request is bounded by `timeout_seconds` (default 30s); a hung child is killed and restarted on the next call.

Per-backend `allowed_tools` / `disabled_tools` (shell patterns, deny wins) filter both `tools/list` and `tools/call`.
SSH targets for the `ssh` backend are rendered from CMDB, see `cmdb-ssh-inventory.md`.
