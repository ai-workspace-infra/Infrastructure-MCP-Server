# 基础设施与可观测性全维度查询能力规范

Infrastructure MCP Server 内置了 17 个专为基础设施工程与可观测性打造的 MCP 工具，覆盖多云 CMDB、多集群资源拓扑、安全网关、灰度发布及三位一体遥测（指标、日志、链路）。

---

## 1. 工具清单全景总览

| 工具名称 (Tool Name) | 核心功能分类 | 核心入参 | 数据源 / 后端提供方 |
| :--- | :--- | :--- | :--- |
| `infra.gateway.query` | 网关与域名路由 | `environment`, `domain`, `gateway` | Traefik IngressRoute, Caddy Ingress |
| `infra.domains.query` | 平台域名全生命周期 | `environment`, `query` | 统一域名注册表 (Unified Domain Registry) |
| `infra.topology.query` | 多集群资源拓扑 | `environment`, `cluster` | VictoriaMetrics 动态服务发现 + CMDB 资产库 |
| `infra.graph.query` | 环境依赖关系图谱 | `query` (节点名或空) | 15 边环境依赖关系有向图 (In-Memory Graph) |
| `infra.metrics.query` | TSDB 时序指标查询 | `query` (PromQL/MetricsQL) | VictoriaMetrics (:8428) |
| `infra.logs.query` | 高基数结构化日志 | `query` (LogsQL), `limit` | VictoriaLogs (:9428) |
| `infra.traces.query` | 分布式链路追踪 | `service`, `limit` | VictoriaTraces (:10428) |
| `infra.flagger.status` | 灰度发布与金丝雀门禁 | `service`, `namespace`, `environment` | Flagger Canary CRD & Metrics Analysis |
| `infra.api_catalog.query` | 微服务 API Catalog 目录 | `service`, `environment` | 统一三级路由目录白皮书标准 |
| `infra.cmdb.query` | 多云资产检索 | `provider`, `status`, `environment` | 多云主机与容器清单 (CMDB Store) |
| `infra.cmdb.summary` | 多云资产汇总统计 | `environment` | 资产统计聚合引擎 |
| `infra.cmdb.sync` | CMDB 与云平台对账 | `source` | 资产对账调度器 |
| `infra.observability.status` | 可观测性组件健康总览 | 无 | Victoria 全家桶 + Grafana 存活探测 |
| `infra.health` | 网关自检与健康度 | 无 | 本地运行时探针 |
| `infra.backends` | 下游 MCP 适配器状态 | 无 | 下游进程/HTTP 客户端连接池 |
| `infra.cli.exec` | 受控运维只读命令 | `command`, `args` | 白名单安全执行沙箱 |
| `infra.telemetry.query` | 遥测综合聚合检索 | `query`, `type` | 遥测路由分配器 |

---

## 2. 重点工具使用指南与数据结构

### 2.1 网关与域名查询 (`infra.gateway.query`, `infra.domains.query`)
提供平台对外所有微服务暴露的域名、TLS 证书状态、Ingress 类型（Traefik / Caddy）以及后端转发 Target 的完整清单。
- 支持环境过滤：`sit`、`prod`、`shared`。
- 自动化标识主入口 `https://mcp.svc.plus` 及兼容域名 `mcp.sit.onwalk.net`。

### 2.2 动态多集群资源拓扑 (`infra.topology.query`)
绝不采用静态配置文件，而是通过实时对接 VictoriaMetrics TSDB 中的动态 series（`job`、`env`、`instance`）自动推导平台拓扑：
- **纳管集群**：`PROD-K3S-V6`（172.237.24.232）与 `SIT-K3S-V6`（172.237.9.58）。
- **微服务依赖拓扑**：Console -> Accounts (认证)、Console -> Billing (计费)、Accounts -> PostgreSQL (数据持久化)、Flagger -> VictoriaMetrics (指标对账)。

### 2.3 动态遥测直查 (`infra.metrics.query`, `infra.logs.query`, `infra.traces.query`)
智能体可以直接下发 PromQL/LogsQL/TraceQL 表达式进行精确诊断：
- **指标**：`infra.metrics.query` 入参 `{"query": "probe_success"}` 或 `{"query": "sum(rate(http_requests_total[5m]))"}`。
- **日志**：`infra.logs.query` 入参 `{"query": "service:console AND level:error"}`。
- **链路**：`infra.traces.query` 入参 `{"service": "web-saas-console"}`。

### 2.4 金丝雀灰度门禁状态 (`infra.flagger.status`)
提供部署运维智能体用于决策是否推进或回滚金丝雀发布的依据：
- 暴露 Canary 阶段（`Initialized`、`Progressing`、`Waiting`、`Promoting`、`Succeeded`、`Failed`）。
- 实时暴露当前流量切分权重（如权重 `10%` -> `20%`）及错误率判定门禁。
