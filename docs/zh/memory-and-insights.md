# 记忆与洞察

## 设计理念

大多数 AI 助手要么在会话之间遗忘所有内容，要么从训练数据中产生幻觉「记忆」。Iulita 采用了根本不同的方法：

- **仅显式存储** — 仅当你要求时才存储事实（"记住这个..."）
- **经过验证的数据** — 每个事实都可以追溯到具体的用户请求
- **交叉引用洞察** — 通过分析你的实际事实发现模式
- **时间衰减** — 较旧的记忆自然失去相关性，除非你访问它们
- **混合检索** — FTS5 全文搜索结合 ONNX 向量嵌入

## 事实

### 存储事实（Remember）

当你说"记住我的狗叫 Max"时，会发生以下过程：

1. **触发检测** — 助手检测到记忆关键词（"remember"）并强制调用 `remember` 工具
2. **重复检查** — 使用前 3 个词搜索现有事实以检测近似重复
3. **SQLite INSERT** — 事实以 `user_id`、`content`、`source_type=user`、时间戳保存
4. **FTS5 索引** — `AFTER INSERT` 触发器自动将事实添加到 `facts_fts` 全文索引
5. **向量嵌入** — 后台协程生成 ONNX 嵌入（384 维 all-MiniLM-L6-v2）并存储到 `fact_vectors`

```
domain.Fact {
    ID             int64
    ChatID         string     // 来源通道（"123456789"、"console"、"web:uuid"）
    UserID         string     // iulita UUID — 跨所有通道共享
    Content        string     // 事实文本
    SourceType     string     // "user"（显式）或 "system"（自动提取）
    CreatedAt      time.Time
    LastAccessedAt time.Time  // 每次回忆时重置
    AccessCount    int        // 每次回忆时递增
}
```

### 回忆事实（Recall）

当你问"我的狗叫什么名字？"时：

1. **用户范围搜索** — 首先尝试 `SearchFactsByUser(userID, query, limit)` 进行跨通道事实搜索
2. **FTS5 匹配** — `SELECT * FROM facts WHERE id IN (SELECT rowid FROM facts_fts WHERE facts_fts MATCH ?)`
3. **过采样** — 获取 `limit * 3` 个候选结果（最少 20 个）用于下游重排序
4. **时间衰减** — 每个候选项评分：`decay = exp(-ln(2) / halfLife * ageDays) * (1 + log(1 + accessCount))`
5. **MMR 重排序** — 最大边际相关性减少结果集中的近似重复
6. **强化** — 每个返回的事实 `access_count++` 并更新 `last_accessed_at = now`

助手还会对每条消息（不仅是显式回忆）执行**混合搜索**：

```
1. 清理查询（去除 FTS 运算符、停用词，限制 5 个关键词）
2. 通过 ONNX 嵌入生成查询向量
3. FTS 结果：基于位置的评分（1 - i/(n+1)）
4. 向量结果：与所有存储向量的余弦相似度
5. 组合：(1-vectorWeight)*ftsScore + vectorWeight*vecScore
6. 两组结果的并集，按组合分数排序
```

### 遗忘事实

`forget` 工具按 ID 删除事实。FTS 触发器（`facts_ad`）自动从全文索引中移除。`fact_vectors` 上的 `ON DELETE CASCADE` 移除嵌入。

## 书签（快速保存）

除了基于聊天的"记住"流程外，用户还可以一键将任何助手回复加入书签。

### 工作原理

1. **Telegram**：每个助手回复（包括多分块消息）下方显示 💾 内联键盘按钮
2. **WebChat**：悬停在助手消息上时显示 💾 图标
3. 点击按钮**立即**将完整回复保存为 `source_type="bookmark"` 的事实
4. 后台调度器任务（`bookmark.refine`）将内容发送给 LLM 进行摘要
5. 如果 LLM 产生了明显更短的版本（<原始长度的 90%），则更新事实内容

### 书签 vs 记住

| 方面 | 书签（💾 按钮） | 记住（"记住这个..."） |
|------|----------------|---------------------|
| 触发方式 | 按钮点击 | 聊天消息 |
| 内容 | 完整助手回复 | LLM 提取的关键事实 |
| 速度 | 即时（<5ms） | 2-5 秒（LLM 调用） |
| 令牌成本 | 延迟（后台精炼） | 即时 |
| 来源类型 | `bookmark` | `user` |
| 重复检查 | 无（原样保存） | FTS 前 3 词检查 |

### 后台精炼

`bookmark.refine` 调度器任务：
- **能力**：`llm,storage`
- **最大尝试次数**：2
- **运行后删除**：是（一次性）
- 从书签回复中提取 1-3 个简洁句子
- 如果 LLM 返回空结果或精炼版本不够短，则跳过更新
- 优雅处理已删除的事实（用户可能在精炼前已删除）

### 安全性

- **Telegram**：验证 callback 发送者与消息接收者一致（`tgUserID` 检查）
- **WebChat**：消息缓存存储所有者 `chatID`；保存前验证所有权

## 时间衰减

事实和洞察使用指数（放射性）衰减随时间减弱：

```
decay_factor = exp(-ln(2) / halfLifeDays * ageDays)
```

| 距上次访问天数 | 半衰期 = 30 天 | 半衰期 = 90 天 |
|----------------|----------------|----------------|
| 0 | 1.00 | 1.00 |
| 15 | 0.71 | 0.89 |
| 30 | 0.50 | 0.79 |
| 60 | 0.25 | 0.63 |
| 90 | 0.13 | 0.50 |

**关键设计选择：**
- **事实**从 `last_accessed_at` 开始衰减 — 每次回忆重置时钟
- **洞察**从 `created_at` 开始衰减 — 它们有固定的生命期
- **访问增强**：`1 + log(1 + accessCount)` — 被访问 100 次的事实获得 4.6 倍增强
- **默认半衰期**：30 天（可通过 `skills.memory.half_life_days` 配置）

## MMR 重排序

在时间衰减评分之后，最大边际相关性防止近似重复结果：

```
MMR(item) = lambda * relevance_score - (1 - lambda) * max_similarity_to_selected
```

- `lambda = 1.0` → 纯相关性，无多样性
- `lambda = 0.7` → 推荐值：偏好相关性但惩罚近似重复
- `lambda = 0.0` → 纯多样性

相似度通过词令牌的 Jaccard 相似度衡量（无需 ONNX 嵌入器的零依赖近似方法）。

**配置**：`skills.memory.mmr_lambda`（默认 0，禁用）。设置为 0.7 以获得最佳效果。

## 洞察

洞察是由后台调度器发现的、AI 生成的事实间交叉引用。

### 生成管道

洞察生成任务每 24 小时运行一次（可配置）：

```
1. 加载用户的所有事实
2. 检查最少事实数量（默认 20）
3. 构建 TF-IDF 向量
   - 分词：小写化、去标点、过滤停用词
   - 生成二元组（相邻词对）
   - 计算 TF-IDF 分数
4. K-means++ 聚类
   - k = sqrt(numFacts / 3)
   - 余弦距离度量
   - 最多 20 次迭代
5. 采样跨簇对
   - 每次运行最多 6 对
   - 跳过已覆盖的事实对
6. 对每一对：
   a. 发送给 LLM："从这两个簇生成创造性洞察"
   b. 通过单独的 LLM 调用评估质量（1-5 分）
   c. 如果质量 >= 阈值则存储
```

### 洞察生命周期

```
domain.Insight {
    ID             int64
    ChatID         string
    UserID         string
    Content        string     // 洞察文本
    FactIDs        string     // 逗号分隔的源事实 ID
    Quality        int        // 1-5 LLM 质量评分
    AccessCount    int
    LastAccessedAt time.Time
    CreatedAt      time.Time
    ExpiresAt      *time.Time // 默认：创建 + 30 天
}
```

- **创建** — 由后台调度器在聚类和 LLM 合成之后创建
- **呈现** — 在上下文相关时出现在助手的系统提示词中（混合搜索）
- **强化** — 被访问时更新（访问计数和最后访问时间）
- **提升** — 通过 `promote_insight` 技能（延长或移除过期时间）
- **驳回** — 通过 `dismiss_insight` 技能（立即删除）
- **过期** — 清理任务每小时运行一次，移除超过 `expires_at` 的洞察

### 洞察质量评分

生成洞察后，第二次 LLM 调用对其评分：

```
系统提示词："请为以下洞察的新颖性和实用性评分，范围 1-5 分。"
用户消息：[洞察文本]
响应：单个数字 1-5
```

如果 `quality_threshold > 0` 且分数低于阈值，洞察将被丢弃。这防止低质量洞察占用记忆空间。

## 嵌入

### ONNX 提供商

Iulita 使用纯 Go ONNX 运行时（`knights-analytics/hugot`）在本地生成嵌入 — 无需外部 API 调用。

- **模型**：`KnightsAnalytics/all-MiniLM-L6-v2` — 句子变换器，384 维
- **运行时**：纯 Go（无 CGo，无共享库）
- **线程安全**：由 `sync.Mutex` 保护（hugot 管道不是线程安全的）
- **模型缓存**：首次下载到 `~/.local/share/iulita/models/`，后续运行复用

### 向量存储

嵌入以二进制 BLOB 形式存储在 SQLite 中：

- **编码**：每个 `float32` → 4 字节小端序，打包为 `[]byte`
- **384 维** → 每个向量 1536 字节
- **表**：`fact_vectors`（fact_id 主键）、`insight_vectors`（insight_id 主键）
- **级联删除**：移除事实/洞察会自动移除其向量

### 嵌入缓存

`embedding_cache` 表防止对相同文本重复计算嵌入：

- **键**：输入文本的 SHA-256 哈希
- **LRU 淘汰**：仅保留最近访问的 N 个条目（默认 10,000）
- **使用者**：包装 ONNX 的 `CachedEmbeddingProvider`

### 混合搜索算法

```python
# 伪代码
def hybrid_search(query, user_id, limit):
    # 1. FTS5 结果（过采样）
    fts_results = FTS_MATCH(query, limit * 2)
    fts_scores = {r.id: 1 - i/(len+1) for i, r in enumerate(fts_results)}

    # 2. 向量相似度
    query_vec = onnx.embed(query)
    all_vecs = load_all_vectors(user_id)
    vec_scores = {id: cosine_similarity(query_vec, vec) for id, vec in all_vecs}

    # 3. 组合
    all_ids = set(fts_scores) | set(vec_scores)
    combined = {}
    for id in all_ids:
        fts = fts_scores.get(id, 0)
        vec = vec_scores.get(id, 0)
        combined[id] = (1 - vectorWeight) * fts + vectorWeight * vec

    # 4. Top-N
    return sorted(combined, key=combined.get, reverse=True)[:limit]
```

**配置**：`skills.memory.vector_weight`（默认 0，仅 FTS）。设置为 0.3-0.5 以启用混合搜索。

## 助手循环中的记忆

每条消息都会触发记忆注入到系统提示词中：

1. **最近事实**（最多 20 条）：从数据库加载，应用衰减 + MMR，格式化为 `## Remembered Facts`
2. **相关洞察**（最多 5 条）：使用消息文本进行混合搜索，格式化为 `## Insights`
3. **用户画像**（技术事实）：按类别分组的行为元数据，格式化为 `## User Profile`
4. **用户指令**：持久化的自定义指令，格式化为 `## User Directives`

此上下文出现在**动态系统提示词**中（每条消息，Claude 不缓存）。

## 记忆导出/导入

### 导出

```go
memory.ExportFacts(ctx, store, chatID) // → Markdown 字符串
memory.ExportAllFacts(ctx, store, dir) // → 每个聊天一个 .md 文件
```

格式：
```markdown
## Fact 42
The user prefers dark mode in all IDEs.

## Fact 43
User's favorite programming language is Go.
```

### 导入

```go
memory.ImportFacts(ctx, store, chatID, markdownContent)
```

解析 markdown，创建新事实（原始 ID 被丢弃 — 由 SQLite 自增分配新 ID）。每个导入的事实会自动生成嵌入。

## 配置参考

| 参数 | 默认值 | 描述 |
|------|--------|------|
| `skills.memory.half_life_days` | 30 | 时间衰减半衰期；0 = 禁用 |
| `skills.memory.mmr_lambda` | 0 | MMR 多样性（0 = 禁用，推荐 0.7） |
| `skills.memory.vector_weight` | 0 | 混合搜索混合度（0 = 仅 FTS，0.5 = 平衡） |
| `skills.insights.min_facts` | 20 | 触发洞察生成的最少事实数 |
| `skills.insights.max_pairs` | 6 | 每次生成运行的最大跨簇对数 |
| `skills.insights.ttl` | 720h | 洞察过期 TTL（30 天） |
| `skills.insights.interval` | 24h | 洞察生成频率 |
| `skills.insights.quality_threshold` | 0 | 最低质量分数（0 = 接受所有） |
| `embedding.model` | `KnightsAnalytics/all-MiniLM-L6-v2` | ONNX 模型名称 |
| `embedding.enabled` | true | 启用 ONNX 嵌入 |
