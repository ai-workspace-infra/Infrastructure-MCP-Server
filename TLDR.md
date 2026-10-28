# MCP 客户端接入配置速查与落地指南 (TL;DR)

本指南针对各类 Code Agent（如 **Trae**、**Cursor**、**Claude Desktop**、**Windsurf**、**Cherry Studio**）以及可观测性智能体（如 **观测云 Obsy Agent Teams**）提供了标准、兼容性最佳的单服务与多服务 MCP 接入配置。

---

## 📌 核心服务接入矩阵全景

```mermaid
flowchart TD
    subgraph AgentLayer ["AI 智能体 / IDE 客户端层 (Obsy Agent Teams / Trae / Cursor)"]
        Obsy["观测云 Obsy Agent Teams (7x24h 自动化巡检)"]
        CodeAgent["Code Agent (Trae / Cursor / Claude Desktop / Codex)"]
    end

    subgraph MCPGateways ["三大 MCP 服务接入柱"]
        ObsyMCP["1. 可观测性 4-MCP (远程 Streamable HTTP)"]
        InfraMCP["2. Infrastructure-MCP-Server (CMDB & 15 边图拓扑)"]
        QMDMCP["3. QMD Memory Hub (193 篇白皮书 & PG 记忆)"]
    end

    subgraph Backends ["底层服务与数据源"]
        Stack["observability.svc.plus (VictoriaMetrics/Logs/Traces/Grafana)"]
        CMDB["SIT / UAT / PROD 多云资产 (Linode/GCP/AWS/Vultr/Cloudflare)"]
        PG["PostgreSQL :15432 (pgvector) + docs/reference"]
    end

    Obsy --> ObsyMCP & InfraMCP & QMDMCP
    CodeAgent --> ObsyMCP & InfraMCP & QMDMCP

    ObsyMCP --> Stack
    InfraMCP --> CMDB
    QMDMCP --> PG
```

---

## 📊 客户端实测验证状态矩阵 (Verification Status)

> [!IMPORTANT]
> 经实际测试与 MCP 协议握手抓包校验，仅当返回标准 JSON-RPC 2.0 / SSE 协议响应且成功完成 `initialize` / `tools/list` 握手时才视为可用。**仅有 HTTP 200/302 不代表 MCP 配置可用！**

| 配置组件 / 服务项 | 接入模式 | 实测结果 | 可用工具数 | 协议握手与就绪状态说明 |
| :--- | :--- | :---: | :---: | :--- |
| **Infrastructure-MCP-Server** | 本地 Command + Args / Wrapper | ✅ **通过** | **8 个工具** | 初始化握手成功，稳定暴露 `infra.cmdb.query`、`infra.cmdb.summary`、`infra.graph.query`、`infra.cli.exec` 等。 |
| **QMD MCP Server** | 本地 Wrapper 脚本 | ✅ **通过** | **23 个工具** | 初始化握手成功，工具集全量暴露。**检索状态说明**：当前基于本地 PG 15432 生效 **BM25 词法全文检索**（收录 193 篇白皮书）；外部向量 API Key 注入后方可激活稠密向量索引。 |
| **可观测性 4-MCP 端点**<br>(Grafana / Metrics / Logs / Traces) | 远程 Streamable HTTP | ❌ **未通过** | 0 | **协议不符（阻断）**：请求返回 HTTP 302 重定向至 Grafana 网页（`Content-Type: text/html`），非 MCP 协议响应。服务端 Caddy Ingress 尚未开启 MCP 路由代理，**目前暂勿在生产客户端挂载**。 |

---

## 避坑指南：为什么单服务配置必须带 `"mcpServers"`？

在 **Trae**、**Cherry Studio** 等国内 IDE/客户端的“编辑 MCP 服务”弹窗中：
* 如果直接粘贴 `{ "url": "..." }` 或 `{ "command": "..." }`，底层解析器会报错：**`当前 JSON 配置中没有解析到 MCP 服务项`**。
* **原因**：客户端底层严格要求以 `"mcpServers"` 作为根节点进行对象键名解析。
* **规则**：**无论配置单项还是多项，都必须严格包裹在 `"mcpServers": { "<服务名>": { ... } }` 中！**

---

## 一、可观测性 4-MCP（远程 Streamable HTTP，待服务端网关就绪）

> [!WARNING]
> **当前验证状态**：❌ **未通过协议验证（服务端未开放 MCP 路由）**
> 经实测，`https://observability.svc.plus/mcp/v1/{grafana,metrics,logs,traces}/mcp` 目前返回 HTTP 302 重定向至 Grafana 网页（`text/html`），未返回合法 MCP 协议。服务端 Ansible 角色中 `observability_mcp_enabled` 默认为 `false`。
> **待生产服务端网关完成路由代理上线前，请勿在客户端中启用这 4 个远程端点，以免引起握手超时或 HTML 解析报错。** 以下仅作为服务上线后的标准契约参考。

### 1. `observability-grafana`（大盘、告警与面板）
```json
{
  "mcpServers": {
    "observability-grafana": {
      "url": "https://observability.svc.plus/mcp/v1/grafana/mcp"
    }
  }
}
```

### 2. `observability-metrics`（VictoriaMetrics TSDB 时序指标）
```json
{
  "mcpServers": {
    "observability-metrics": {
      "url": "https://observability.svc.plus/mcp/v1/metrics/mcp"
    }
  }
}
```

### 3. `observability-logs`（VictoriaLogs 高基数日志检索）
```json
{
  "mcpServers": {
    "observability-logs": {
      "url": "https://observability.svc.plus/mcp/v1/logs/mcp"
    }
  }
}
```

### 4. `observability-traces`（VictoriaTraces 分布式链路调用追踪）
```json
{
  "mcpServers": {
    "observability-traces": {
      "url": "https://observability.svc.plus/mcp/v1/traces/mcp"
    }
  }
}
```

---

## 二、Infrastructure-MCP-Server（CMDB 对账与 15 依赖边图拓扑）

> [!TIP]
> **验证状态**：✅ **实测通过**。原生命令行与 Wrapper 两种模式均已完成 MCP 握手，成功初始化 8 个核心工具（`infra.cmdb.query`、`infra.cmdb.summary`、`infra.graph.query`、`infra.cli.exec` 等）。

> 赋予 Agent 查询 SIT/UAT/PROD 资产现状、提取 `service/observability-gateway` 依赖拓扑及受控安全执行只读 Cloud CLI 的能力。

### 方案 A：标准原生配置模式（首选推荐）
```json
{
  "mcpServers": {
    "infrastructure": {
      "args": [
        "-config",
        "/Users/shenlan/.config/infrastructure-mcp-server/config.json"
      ],
      "command": "/Users/shenlan/.local/bin/infrastructure-mcp-server"
    }
  }
}
```
*表单模式填写*：
* **类型 (Type)**：`STDIO`
* **命令 (Command)**：`/Users/shenlan/.local/bin/infrastructure-mcp-server`
* **参数 (Args)**：
  * `-config`
  * `/Users/shenlan/.config/infrastructure-mcp-server/config.json`
* **环境变量 (Env)**：留空

### 方案 B：极简 0 参数 Wrapper 模式（备选方案，防客户端丢参数）
```json
{
  "mcpServers": {
    "infrastructure": {
      "command": "/Users/shenlan/.local/bin/run-infrastructure-mcp.sh"
    }
  }
}
```
*表单模式填写*：
* **类型 (Type)**：`STDIO`
* **命令 (Command)**：`/Users/shenlan/.local/bin/run-infrastructure-mcp.sh`
* **参数与环境变量**：留空即可。

---

## 三、QMD MCP Server（193 篇白皮书 & 历史故障 SOP 记忆）

> [!TIP]
> **验证状态**：✅ **实测通过**。本地 Wrapper 脚本已成功完成 MCP 协议握手，初始化列出 23 个完整工具集。
> **检索能力技术说明**：基于本地 PostgreSQL 15432，当前已全面生效 **BM25 词法倒排全文检索**（收录 193 篇白皮书与 SOP 文档）。由于当前环境未注入外部 Embedding 模型 API Key，系统未建立稠密向量索引；待后续注入模型 API Key 后，可无缝激活稠密向量检索。

> 赋予 Agent 在遇到疑难故障或架构决策时，自动检索本地 193 篇工程白皮书、历史故障排查手册与 SOP 方案的能力。

### 方案 A：极简 0 参数 Wrapper 模式（彻底解决客户端不读 `env` 导致连不上 PG 15432 的问题）
```json
{
  "mcpServers": {
    "qmd": {
      "command": "/Users/shenlan/.local/bin/run-qmd-mcp.sh"
    }
  }
}
```
*表单模式填写*：
* **类型**：`STDIO`
* **命令**：`/Users/shenlan/.local/bin/run-qmd-mcp.sh`
* **参数与环境变量**：留空即可（Wrapper 内部已封装好 Node 绝对路径、PostgreSQL 连接串与缓存路径）。

### 方案 B：标准原生配置模式
```json
{
  "mcpServers": {
    "qmd": {
      "command": "/Users/shenlan/.local/devtools/node-v24.20.0-darwin-arm64/bin/node",
      "args": [
        "/Users/shenlan/workspaces/ai-workspace-lab/qmd/dist/cli/qmd.js",
        "mcp"
      ],
      "env": {
        "QMD_BACKEND": "pg",
        "QMD_PG_URL": "postgres://postgres:096fdebb5352ca34c01cf26ca0354ce1@127.0.0.1:15432/qmd?sslmode=disable",
        "PATH": "/Users/shenlan/.local/devtools/node-v24.20.0-darwin-arm64/bin:/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin"
      }
    }
  }
}
```

---

## 四、全量合并配置（供支持批量导入的客户端直接粘贴）

### 1. 即刻可用配置（首选推荐，已通过实测验证）

> [!NOTE]
> 该配置包含已实测通过的 **Infrastructure（标准原生带参数模式）** 与 **QMD（免参数 Wrapper 模式）**，开箱即用，无任何网络阻断。

```json
{
  "mcpServers": {
    "infrastructure": {
      "args": [
        "-config",
        "/Users/shenlan/.config/infrastructure-mcp-server/config.json"
      ],
      "command": "/Users/shenlan/.local/bin/infrastructure-mcp-server"
    },
    "qmd": {
      "command": "/Users/shenlan/.local/bin/run-qmd-mcp.sh"
    }
  }
}
```

> 💡 *若客户端对参数解析异常，Infrastructure 可同样使用 Wrapper 模式*：
> ```json
> {
>   "mcpServers": {
>     "infrastructure": {
>       "command": "/Users/shenlan/.local/bin/run-infrastructure-mcp.sh"
>     },
>     "qmd": {
>       "command": "/Users/shenlan/.local/bin/run-qmd-mcp.sh"
>     }
>   }
> }
> ```

### 2. 全量未来配置（含待服务端网关就绪的 4-MCP）

> [!WARNING]
> ⚠️ **请注意**：由于当前 `observability.svc.plus` 服务端 Caddy 网关尚未放行 `/mcp/v1/*` 路由（返回 HTTP 302 重定向至 Grafana 网页），**请待服务端网关启用 `observability_mcp_enabled: true` 后再追加以下 4-MCP 项**，否则可能导致 IDE 启动时报错或请求卡死。

```json
{
  "mcpServers": {
    "infrastructure": {
      "args": [
        "-config",
        "/Users/shenlan/.config/infrastructure-mcp-server/config.json"
      ],
      "command": "/Users/shenlan/.local/bin/infrastructure-mcp-server"
    },
    "qmd": {
      "command": "/Users/shenlan/.local/bin/run-qmd-mcp.sh"
    },
    "observability-grafana": {
      "url": "https://observability.svc.plus/mcp/v1/grafana/mcp"
    },
    "observability-metrics": {
      "url": "https://observability.svc.plus/mcp/v1/metrics/mcp"
    },
    "observability-logs": {
      "url": "https://observability.svc.plus/mcp/v1/logs/mcp"
    },
    "observability-traces": {
      "url": "https://observability.svc.plus/mcp/v1/traces/mcp"
    }
  }
}
```
