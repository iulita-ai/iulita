# Telegram

[Telegram 完整指南（英文）](../en/telegram.md)介绍尚未发布的源代码功能：私聊话题、日程与天气卡片、附加用户名、引用和个人回调回复。

配置令牌及 `telegram.allowed_ids`，并在 BotFather 中启用话题。根聊天中的普通消息创建新话题；`/topic` 列出已知话题。`/clear` 与 `/compact` 仅处理当前上下文。`/topic pause` 暂停 Iulita，不关闭 Telegram 话题；`/topic delete` 经确认清理关联本地工作，不删除外部任务。

使用 `/day`、`/day text`、`/weather London 4` 或 `/weather text London 4`。`telegram.day_widgets` 与 `telegram.day_actions` 默认启用，旧配置缺少这些键时升级后也启用；显式 false 保留。修改后需要重启，操作需要所有者确认。

回复缓冲至最终隐私检查，不发布未经检查的流式片段。敏感内容私密发送失败时，不会退回普通消息发送。消息合并隔离用户和话题。生产环境仅支持私聊组件；inline/shared 需要 Telegram 测试服务器。语音、Rich 引用、配置、可用性与恢复详见英文指南。

![English previews of day, weather and shared-result cards](/screenshots/telegram/card-family-en.png)

*Illustrative browser previews of production templates with sample English data, not native Telegram captures. Shared-result widgets require Telegram's test server.*
