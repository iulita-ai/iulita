# 多智能体编排

Iulita 支持并行子智能体执行，用于分解复杂任务。LLM 根据任务复杂度自主决定何时使用编排。

## 概述

`orchestrate` 技能并行启动多个专业化子智能体。每个子智能体运行简化的代理循环，拥有自己的系统提示词、工具子集和可选的 LLM 提供商路由。结果被收集并以结构化 markdown 报告的形式返回给父助手。

## 架构

```
用户消息
    │
    ▼
助手（主代理循环）
    │ 决定需要编排
    │ 使用智能体规格调用 orchestrate 工具
    ▼
Orchestrate 技能
    │ 验证深度（最大 1）
    │ 从配置 + 输入覆盖构建 Budget
    ▼
编排器（通过 errgroup 并行执行）
    ├── Runner（agent_1: researcher）──→ LLM ←→ 工具
    ├── Runner（agent_2: analyst）   ──→ LLM ←→ 工具
    └── Runner（agent_3: planner）   ──→ LLM ←→ 工具
         │
         │ 共享原子令牌预算
         │ 每智能体超时 + 上下文
         │ 状态事件 → 通道
         ▼
    收集的 AgentResults
    │ 格式化为 markdown
    ▼
助手继续处理编排输出
```

## 智能体类型

| 类型 | 系统提示词重点 | 默认工具 | 路由提示 |
|------|--------------|----------|----------|
| `researcher` | 收集信息、搜索网络、结构化摘要 | `web_search`、`webfetch` | — |
| `analyst` | 识别模式、异常、关键洞察 | 全部 | — |
| `planner` | 将目标分解为有序步骤 | `datetime` | — |
| `coder` | 编写、审查、调试代码 | 全部 | — |
| `summarizer` | 将输入压缩为要点 | 全部 | `ollama` |
| `generic` | 通用 | 全部 | — |

## 预算系统

### 共享令牌预算

所有智能体共享单个 `atomic.Int64` 令牌计数器。每个智能体的 LLM 调用从共享池中扣除。

**设计为软上限**：多个智能体可能在任何一个扣除令牌之前并发通过预检查，导致预算被超出最多 `(N_agents - 1) * tokens_per_call`。这是有意为之的。

### 默认参数

| 参数 | 默认值 | 覆盖 |
|------|--------|------|
| 最大轮次（LLM 调用） | 10 | `Budget.MaxTurns` |
| 超时（实际时间） | 60 秒 | `Budget.Timeout` 或输入 `timeout` |
| 最大并行智能体 | 5 | `Budget.MaxAgents` 或配置 |
| 共享令牌预算 | 无限制 | `Budget.MaxTokens` 或输入 `max_tokens` |

## 深度控制

子智能体不能生成更多子智能体。通过两个级别强制执行：

1. **上下文深度键**：`WithDepth(ctx, DepthFrom(ctx)+1)` 设置在每个子智能体上下文中。
2. **工具过滤**：`buildTools()` 始终从子智能体工具列表中排除 `orchestrate` 工具。

`MaxDepth = 1` 表示：深度 0 = 父助手，深度 1 = 子智能体（最大值）。

## 安全性

### 审批过滤

子智能体绕过正常的审批流程，因此不能访问需要审批的技能：

- `shell_exec`（ApprovalManual）— 不可访问
- Docker 执行器（ApprovalPrompt）— 不可访问

### 工具白名单

每个智能体规格可以可选地声明显式工具白名单。如果未指定，使用智能体类型的默认工具。

## 状态事件协议

| 事件 | 时机 | 数据字段 |
|------|------|----------|
| `orchestration_started` | 启动智能体之前 | `agent_count` |
| `agent_started` | 每个智能体运行前 | `agent_id`、`agent_type` |
| `agent_progress` | 每个智能体每轮 LLM 后 | `agent_id`、`turn` |
| `agent_completed` | 每个智能体成功时 | `agent_id`、`tokens`、`duration_ms` |
| `agent_failed` | 每个智能体失败时 | `agent_id`、`error` |
| `orchestration_done` | 所有智能体完成后 | `success_count`、`total_tokens`、`duration_ms` |

完成后的事件使用 `context.Background()` 并设置 5 秒超时，以确保即使父上下文的截止时间已过也能送达。

## 配置

| 键 | 默认值 | 描述 |
|------|--------|------|
| `skills.orchestrate.enabled` | true | 启用/禁用 orchestrate 技能 |
| `skills.orchestrate.max_tokens` | 0（无限制） | 共享令牌预算上限 |
| `skills.orchestrate.max_agents` | 5 | 最大并行智能体数 |
| `skills.orchestrate.timeout` | 60s | 每智能体超时 |
| `skills.orchestrate.request_timeout` | 1h | 总体编排截止时间（最大 4h） |

所有配置键支持通过 `ConfigReloadable` 热重载。

## 前端

`AgentProgress.vue` 组件在聊天视图中实时显示智能体状态，包括类型图标、名称、状态和进度信息。
