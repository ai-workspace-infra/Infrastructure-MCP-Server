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

## 避坑指南：为什么单服务配置必须带 `"mcpServers"`？

在 **Trae**、**Cherry Studio** 等国内 IDE/客户端的“编辑 MCP 服务”弹窗中：
* 如果直接粘贴 `{ "url": "..." }` 或 `{ "command": "..." }`，底层解析器会报错：**`当前 JSON 配置中没有解析到 MCP 服务项`**。
* **原因**：客户端底层严格要求以 `"mcpServers"` 作为根节点进行对象键名解析。
* **规则**：**无论配置单项还是多项，都必须严格包裹在 `"mcpServers": { "<服务名>": { ... } }` 中！**

---

## 一、可观测性 4-MCP（远程 Streamable HTTP 直连）

> 适用于 Obsy Agent Teams 或任意无本机执行环境的 Agent，直接调用集中式监控集群，支持 MetricsQL、LogsQL 与链路追踪。

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

> 赋予 Agent 查询 SIT/UAT/PROD 资产现状、提取 `service/observability-gateway` 依赖拓扑及受控安全执行只读 Cloud CLI 的能力。

### 方案 A：极简 0 参数 Wrapper 模式（国内客户端最稳妥，防丢参数/环境变量）
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
* **类型**：`STDIO`
* **命令**：`/Users/shenlan/.local/bin/run-infrastructure-mcp.sh`
* **参数与环境变量**：留空即可。

### 方案 B：标准原生配置模式
```json
{
  "mcpServers": {
    "infrastructure": {
      "command": "/Users/shenlan/.local/bin/infrastructure-mcp-server",
      "args": [
        "-config",
        "/Users/shenlan/.config/infrastructure-mcp-server/config.json"
      ],
      "env": {
        "PATH": "/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin"
      }
    }
  }
}
```

---

## 三、QMD MCP Server（193 篇白皮书 & 历史故障 SOP 记忆）

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

```json
{
  "mcpServers": {
    "infrastructure": {
      "command": "/Users/shenlan/.local/bin/run-infrastructure-mcp.sh"
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
