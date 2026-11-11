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
| **Infrastructure-MCP-Server** | 本地 Command + Args / Wrapper / Web REST & SSE | ✅ **通过** | **11 个工具** | 初始化握手成功，稳定暴露 `infra.cmdb.query`、`infra.cmdb.summary`、`infra.graph.query`、`infra.cli.exec`、`infra.api_catalog.query`、`infra.flagger.status`、`infra.telemetry.query`。支持标准 stdio 管道、MCP SSE 及 Web REST/SSE 实时流模式。 |
| **QMD MCP Server** | 本地 Wrapper 脚本 | ✅ **通过** | **23 个工具** | 初始化握手成功，工具集全量暴露。**检索状态说明**：当前基于本地 PG 15432 生效 **BM25 词法全文检索**（收录 193 篇白皮书）；外部向量 API Key 注入后方可激活稠密向量索引。 |
| **可观测性 4-MCP 端点**<br>(Grafana / Metrics / Logs / Traces) | 远程 Streamable HTTP | ❌ **未通过** | 0 | **协议不符（阻断）**：请求返回 HTTP 302 重定向至 Grafana 网页（`Content-Type: text/html`），非 MCP 协议响应。服务端 Caddy Ingress 尚未开启 MCP 路由代理，**目前暂勿在生产客户端挂载**。 |

---

## 避坑指南：为什么单服务配置必须带 `"mcpServers"`？

在 **Trae**、**Cherry Studio** 等国内 IDE/客户端的“添加/编辑 MCP 服务”弹窗中：
1. **根对象限制**：如果直接粘贴 `{ "url": "..." }` 或 `{ "command": "..." }`，底层解析器会报错：**`当前 JSON 配置中没有解析到 MCP 服务项`**。客户端底层严格要求以 `"mcpServers"` 作为根节点进行对象键名解析。
2. **单服务数量限制**：弹窗底层严格校验 **`Object.keys(mcpServers).length === 1`**。如果在同一个弹窗中粘贴了包含 `infrastructure` 和 `qmd` 两个服务的 JSON，会直接报错拦截：**`该配置被识别为多配置项`**。
3. **黄金规则**：**一次只添加一个服务！必须分别复制、分别添加！每个服务都严格包裹在独占的 `"mcpServers": { "<单个服务名>": { ... } }` 中！**

---

## 默认 SSH 审计 MCP（classfang/ssh-mcp-server）

仓库提供 [SSH auditor 客户端配置](docs/examples/ssh-auditor.mcp.json)，默认连接本机 `~/.ssh/config` 中名为 `auditor` 的主机别名，并固定使用 `@fangjunjie/ssh-mcp-server@1.10.0`。白名单只开放身份、系统资源、监听端口、指定 systemd 服务状态和对应服务日志查询；不允许通过命令执行接口运行任意 shell。

启用前，先在本机 SSH 配置中为 `auditor` 指向目标主机的独立 `auditor` 账户，并使用该审计账户专属密钥。仓库只保存不含主机地址和密钥的 MCP 配置，不保存或生成 SSH 凭据。审计账户必须由主机权限强制只读、不得有 sudo、容器运行时管理组或业务写权限；MCP 命令白名单不能代替远端操作系统的授权。

该 MCP 同时提供 SFTP 写入/上传等工具，项目没有提供关闭这些工具的配置开关。因此其安全边界依赖远端 `auditor` 账户权限：只能写入其自身受控审计工作目录，不得写入系统或业务位置。该配置面向需要审计身份的本地/受控客户端，不应作为公网上的共享 Infrastructure MCP 后端启用；不要把它加入服务端 `config.example.json` 并向所有网关用户暴露。

---

## 一、可观测性 4-MCP（远程 SSE 接入规范）

> [!NOTE]
> **服务端网关与只读加固进展**：
> 经实测排查，已在 [`playbooks` 仓库通过 PR #820](https://github.com/ai-workspace-infra/playbooks/pull/820) 完成服务端核心修复：
> 1. **前缀对齐**：为 `mcp-grafana` 容器配置 `--base-path /mcp/v1/grafana` 并修正 Caddy 为 `handle` 透传，彻底解决 Grok Bot、Cursor 等自动化 Agent 因相对路径丢失 `/mcp/v1/grafana` 前缀而被 302 拦截的问题；
> 2. **只读加固**：显式注入官方参数 `--disable-write` 与 `--disable-admin`，彻底剥离写工具（如 `create_datasource`、`install_plugin`、`alerting_rules_write`、`delete_snapshot` 等），守牢只读安全红线；
> 3. **传输协议定性**：确认为标准 **`sse`** 传输（非单端点 streamable-http）；
> 4. **凭据安全提醒**：生产环境中切勿在明文配置中直接写死 Basic 密码，建议通过 Vault 动态轮换并注入 Header。

### 1. `observability-grafana`（大盘、告警与面板只读检索）
```json
{
  "mcpServers": {
    "observability-grafana": {
      "type": "sse",
      "url": "https://mcp-server.svc.plus/v1/grafana/sse",
      "headers": {
        "Authorization": "Basic <vault-injected-base64-credentials>"
      }
    }
  }
}
```

### 2. `observability-metrics`（VictoriaMetrics TSDB 时序指标）
```json
{
  "mcpServers": {
    "observability-metrics": {
      "url": "https://mcp-server.svc.plus/v1/metrics/mcp"
    }
  }
}
```

### 3. `observability-logs`（VictoriaLogs 高基数日志检索）
```json
{
  "mcpServers": {
    "observability-logs": {
      "url": "https://mcp-server.svc.plus/v1/logs/mcp"
    }
  }
}
```

### 4. `observability-traces`（VictoriaTraces 分布式链路调用追踪）
```json
{
  "mcpServers": {
    "observability-traces": {
      "url": "https://mcp-server.svc.plus/v1/traces/mcp"
    }
  }
}
```

---

## 二、Infrastructure-MCP-Server（CMDB 对账与 15 依赖边图拓扑）

> 💡 **部署拓扑**：本服务已通过 GitOps + FluxCD 部署于 **`dev-k3s-v5`** 节点，分配独立域名 **`mcp-server.svc.plus`**，提供远程 SSE/HTTP 端点与本地 CLI STDIO 双重支持。

### 0. 远程独立域名接入（推荐云端 / 容器客户端使用）
```json
{
  "mcpServers": {
    "infrastructure": {
      "type": "sse",
      "url": "https://mcp-server.svc.plus/sse"
    }
  }
}
```

> [!TIP]
> **验证状态**：✅ **实测通过**。原生命令行、Wrapper 脚本以及 Web REST / SSE 多传输模式均已完成协议实测验证，成功初始化 11 个核心工具（涵盖 `infra.cmdb.query`、`infra.cmdb.summary`、`infra.graph.query`、`infra.cli.exec`、`infra.api_catalog.query`、`infra.flagger.status`、`infra.telemetry.query`）。零硬编码环境，全面对接 `observability.svc.plus` 动态三路遥测。

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
