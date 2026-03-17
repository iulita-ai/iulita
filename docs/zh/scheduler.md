# 调度器

调度器是一个双组件系统：**协调器**按计划产生任务，**工作器**认领并执行任务。两者都使用 SQLite 作为任务队列。

## 架构

```
Scheduler（协调器）
    │ 每 30 秒轮询
    │ 根据 scheduler_states 检查任务计时
    │
    ├── InsightJob（24 小时）→ insight.generate 任务
    ├── InsightCleanupJob（1 小时）→ insight.cleanup 任务
    ├── TechFactsJob（6 小时）→ techfact.analyze 任务
    ├── HeartbeatJob（6 小时）→ heartbeat.check 任务
    ├── RemindersJob（30 秒）→ reminder.fire 任务
    ├── AgentJobsJob（30 秒）→ agent.job 任务
    └── TodoSyncJob（每小时 cron）→ todo.sync 任务
           │
           ▼
    tasks 表（SQLite）
           │
           ▼
Worker（工作器）
    │ 每 5 秒轮询
    │ 原子认领任务
    │ 分发到已注册的处理器
    │
    ├── InsightGenerateHandler
    ├── InsightCleanupHandler
    ├── TechFactAnalyzeHandler
    ├── HeartbeatHandler
    ├── ReminderFireHandler
    ├── AgentJobHandler
    ├── RefineBookmarkHandler
    └── TodoSyncHandler
```

## 协调器

### 任务定义

```go
type JobDefinition struct {
    Name        string
    Interval    time.Duration
    CronExpr    string           // 标准 cron（5 字段）
    Timezone    string           // cron 的 IANA 时区
    Enabled     bool
    CreateTasks func(ctx) ([]domain.Task, error)
}
```

每个任务声明固定 `Interval` 或 `CronExpr`。Cron 使用 `robfig/cron/v3`，支持时区。

### 调度循环

1. **预热**：首次启动时，`NextRun = now + 1 分钟`（宽限期）
2. 每 30 秒**轮询**：
   - 维护：回收过期任务（运行 > 5 分钟），删除旧任务（> 7 天）
   - 对于每个已启用的任务：如果 `now >= state.NextRun`，调用 `CreateTasks`
   - 通过 `CreateTaskIfNotExists`（按 `UniqueKey` 幂等）插入任务
   - 更新状态：`LastRun = now`，`NextRun = computeNextRun()`

### 手动触发

`TriggerJob(name)`：
- 查找命名任务
- 调用 `CreateTasks`，`Priority = 1`（高优先级）
- 立即插入任务
- **不**更新调度状态（下次常规运行仍会发生）

可通过仪表盘使用：`POST /api/schedulers/:name/trigger`

## 工作器

### 任务认领

```
每 5 秒：
    对每个可用并发槽：
        ClaimTask(ctx, workerID, capabilities)  // 原子 SQLite 事务
        如果认领到任务：
            go executeTask(task)
        否则：
            break  // 没有更多可用任务
```

`workerID = hostname-pid`（每个进程唯一）。

### 基于能力的路由

任务将所需能力声明为逗号分隔的字符串（如 `"llm,storage"`）。工作器的能力列表必须是其超集。

**本地工作器能力**：`["storage", "llm", "telegram"]`

**远程工作器**：任意能力集，通过 `Scheduler.WorkerToken` 认证。

### 任务生命周期

```
pending → claimed（由工作器）→ running → completed / failed
```

- `ClaimTask`：事务中原子 SELECT + UPDATE
- `StartTask`：设置状态为 `running`，记录开始时间
- `CompleteTask`：存储结果，发布 `TaskCompleted` 事件
- `FailTask`：存储错误，发布 `TaskFailed` 事件

### 远程工作器 API

对于分布式部署，仪表盘暴露 REST API：

| 端点 | 方法 | 描述 |
|------|------|------|
| `/api/tasks/` | GET | 列出任务 |
| `/api/tasks/counts` | GET | 按状态计数 |
| `/api/tasks/claim` | POST | 认领任务 |
| `/api/tasks/:id/start` | POST | 标记为运行中 |
| `/api/tasks/:id/complete` | POST | 以结果完成 |
| `/api/tasks/:id/fail` | POST | 以错误失败 |

通过静态 Bearer 令牌（`scheduler.worker_token`）认证。

## 内置任务

### 洞察生成（`insights`）

- **间隔**：24 小时（可通过 `skills.insights.interval` 配置）
- **任务类型**：`insight.generate`
- **能力**：`llm,storage`
- **条件**：聊天/用户必须有 >= `minFacts`（默认 20）个事实

**处理器管道：**
1. 加载用户的所有事实
2. 构建 TF-IDF 向量（分词、二元组、TF-IDF 分数）
3. K-means++ 聚类：`k = sqrt(numFacts / 3)`，余弦距离，20 次迭代
4. 采样最多 6 个跨簇对（跳过已覆盖的对）
5. 对每一对：LLM 生成洞察 + 评分质量（1-5）
6. 存储质量 >= 阈值的洞察

### 洞察清理（`insight_cleanup`）

- **间隔**：1 小时
- **任务类型**：`insight.cleanup`
- **能力**：`storage`

删除 `expires_at < now` 的洞察。默认 TTL 为 30 天。

### 技术事实分析（`techfacts`）

- **间隔**：6 小时（可配置）
- **任务类型**：`techfact.analyze`
- **能力**：`llm,storage`
- **条件**：10 条以上消息，其中 5 条以上来自用户

**处理器**：将用户消息发送给 LLM，请求结构化 JSON：`[{category, key, value, confidence}]`。类别包括话题、沟通风格和行为模式。Upsert 到 `tech_facts` 表。

### 心跳（`heartbeat`）

- **间隔**：6 小时（可配置）
- **任务类型**：`heartbeat.check`
- **能力**：`llm,storage,telegram`

**处理器**：收集最近的事实、洞察和待处理提醒。询问 LLM 是否需要签到消息。如果响应不是 `HEARTBEAT_OK`，则将消息发送给用户。

### 提醒（`reminders`）

- **间隔**：30 秒
- **任务类型**：`reminder.fire`
- **能力**：`telegram,storage`

**处理器**：格式化带本地时间的提醒，通过 `MessageSender` 发送，标记为已触发。

### 代理任务（`agent_jobs`）

- **间隔**：30 秒
- **任务类型**：`agent.job`
- **能力**：`llm`

轮询 `GetDueAgentJobs(now)` 获取用户定义的定时 LLM 任务。在执行前立即更新 `next_run`（防止重复）。

**处理器**：使用用户定义的提示词调用 `provider.Complete`。可选将结果投递到配置的聊天。

### 书签精炼（`bookmark.refine`）

- **触发**：按需（由 `bookmark.Service.Save` 创建）
- **任务类型**：`bookmark.refine`
- **能力**：`llm,storage`
- **最大尝试次数**：2
- **运行后删除**：是

**处理器**：接收 `{fact_id, content, chat_id, user_id}`。使用摘要提示词调用 LLM 提取 1-3 个简洁句子。如果精炼版本明显更短（<原始长度的 90%），则更新事实内容。优雅处理已删除的事实。

**非定时任务** — 任务在用户点击书签按钮时按需创建。工作器在下一个轮询周期（每 5 秒）中拾取它们。

### 待办同步（`todo_sync`）

- **Cron**：`0 * * * *`（每小时）
- **任务类型**：`todo.sync`
- **能力**：`storage`

**处理器**：遍历所有可用的 `TodoProvider` 实例（Todoist、Google Tasks、Craft）。对每个：`FetchAll` → upsert 到 `todo_items` → 删除过期条目。

## 代理任务（用户定义）

用户可以通过仪表盘创建定时 LLM 任务：

```json
{
  "name": "Daily Summary",
  "prompt": "Summarize my recent facts and insights",
  "cron_expr": "0 9 * * *",
  "delivery_chat_id": "123456789"
}
```

字段：
- `name` — 显示名称
- `prompt` — 要执行的 LLM 提示词
- `cron_expr` 或 `interval` — 调度设置
- `delivery_chat_id` — 发送结果的目标（可选）

通过仪表盘管理：`GET/POST/PUT/DELETE /api/agent-jobs/`
