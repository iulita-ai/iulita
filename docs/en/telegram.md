# Telegram: topics, cards and private actions

This guide describes the Telegram Bot API 10.4 integration in the current source tree. The changes are **unreleased** until included in a published binary or image; updating an older deployment is required. Platform availability is separate from Iulita configuration.

## Start here

1. Create a bot with [BotFather](https://t.me/BotFather), supply its token through the supported secret configuration, and restrict `telegram.allowed_ids` if the bot is personal.
2. Enable topics for the bot in BotFather if you want separate conversations. Iulita checks the bot's reported topic capability; it does not change BotFather settings for you.
3. Start Iulita and open a private conversation with the bot. Send `/topic` to list known topics, `/day` for your personal overview, or `/weather London 4` for a forecast.
4. Connect your own [Todoist](skills.md#todoist), [Google Workspace](skills.md#google-workspace) or [Craft](skills.md#craft) integration to populate the corresponding day sections. Unconnected or unavailable sources are labelled rather than presented as an empty successful result.

![English previews of weather, day and shared-result cards in light and dark themes](/screenshots/telegram/card-family-en.png)

*Illustrative browser previews of the current production templates with sample English data. These are not native Telegram screenshots. The shared-result preview represents the test-server feature described below.*

Individual previews: [weather, light](/screenshots/telegram/weather-light-en.png), [weather, dark](/screenshots/telegram/weather-dark-en.png), [day, light](/screenshots/telegram/day-light-en.png), [day, dark](/screenshots/telegram/day-dark-en.png), [shared result, light](/screenshots/telegram/public-light-en.png), [shared result, dark](/screenshots/telegram/public-dark-en.png).

## Commands

| Command | Result |
| --- | --- |
| `/topic` or `/topic list` | List topics already known to this Iulita instance and owner. |
| `/topic list 2` | Next numbered page of known topics, 20 per page. |
| `/topic new Project notes` | Preview and confirm an explicitly named new topic. Without a name, asks for one; `/cancel` cancels the prompt. |
| `/topic rename New name` | Rename the current topic after confirmation. |
| `/topic link` | Get a link to the current owned topic. |
| `/topic pause` | Pause Iulita processing and delivery in this topic. |
| `/topic resume` | Resume the current topic. From the root chat, use `/topic resume THREAD_ID`. |
| `/topic delete` | Confirm deletion of the topic and its bound local context/work. |
| `/topic bind` | Confirm the bot identity for a known legacy topic. From root, use `/topic bind THREAD_ID`. |
| `/topic recover OPERATION_ID` | Recover an interrupted creation from inside the created topic; follow the operation ID provided by Iulita. |
| `/topic help` | Show topic command help. |
| `/clear` | Clear only the current conversation's context. |
| `/compact` | Explicitly summarize context in the current private topic and report its new size. |
| `/day` | Open the personal day card. |
| `/day text` | Open the text/button version, including when the client cannot display widgets. |
| `/weather London 4` | Current weather when available and four forecast days. |
| `/weather text London 4` | Full human-readable text forecast. |
| `/weather` | Four days for the application fallback city, Irkutsk; no IP-based location guessing. |

The `/topic close` and `/topic reopen` compatibility aliases mean local pause/resume. They do not close or reopen a native private Telegram topic. A topic name is a single line of 1–128 characters and cannot contain a detected secret or payment identifier.

## Topic context

An ordinary message in the root/general private chat starts a new topic when Telegram topic mode is enabled. Commands are handled as commands; `/topic` lists existing known topics. Photos in the same album stay together, and the original message is copied into the created topic as the visible starting point.

Each topic has its own conversation history and pending interactions. Iulita loads the full persisted history of a verified private topic, rather than silently dropping everything before the last 50 messages. At the model's context limit it summarizes older context and keeps a bounded recent tail. This is a summary, not a promise to preserve every original word forever.

Automatic and `/compact` compression post an explicit notice after the summary has been saved. The notice reports estimated tokens before and after, message counts, and the model context window. Estimates are not provider-billed token counts. `/clear` clears the current context and attempts deletion of recent bot replies within Telegram's limits; it does not delete the topic, other topics, saved account facts, reminders or external tasks.

### Pause and delete have different effects

Pause leaves the native topic open and blocks new processing, deliveries and card actions there. It does not recall an external request already sent. Missed or suppressed reminders are not automatically replayed on resume; reschedule them explicitly.

Confirmed deletion first commits a local deletion marker and cleanup. It clears topic history, summaries and topic-derived context, expires local cards, cancels attached local reminders and queued/running work, disables attached background schedules, and cancels active assistant/prompt handling. Late writes and deliveries are fenced. It then deletes the native Telegram topic. If the native result is uncertain, local cleanup remains committed and reconciliation is pending; Iulita does not claim that the native deletion succeeded.

**External Todoist, Google and Craft tasks are preserved.** Unrelated conversations and account-wide saved facts/directives are not bulk erased. Already-dispatched external calls may still finish. Topic deletion is not an account-erasure command.

### Deleting a topic directly in Telegram

Iulita periodically reconciles known routes, also checking at startup and when listing topics. Detection is eventual, bounded and dependent on Telegram responses, not an instant deletion notification. Telegram does not provide a general deletion event or a complete bot-topic discovery API. Unknown errors, timeouts, throttling and permission failures do not authorize cleanup.

Deletion synchronization requires a route pinned to the bot's stable numeric identity. A legacy route with no proven bot identity remains readable but is excluded from automatic destructive cleanup until the owner confirms `/topic bind`. Renaming a bot or adding a username is not proof that another bot owns its historical routes. Binding preserves history itself, but enables deletion synchronization: if the now-bound native topic is already gone, the subsequent check can clear its context and cancel its bound local work. Review the exact confirmation before accepting.

## My day

The private card has Tasks, Reminders, Events and Jobs sections, pagination, refresh and details. Refresh reads the configured sources; it is not a promise of automatic live provider notifications. Cards expire after 24 hours and should then be reopened with `/day`. The saved profile timezone is used, with UTC as the day-view fallback.

| Source | Available behavior |
| --- | --- |
| Todoist | Read tasks; confirm completion or supported rescheduling. Recurring-task rescheduling is disabled. |
| Google Tasks | Read connected accounts/lists; confirm completion or date-only rescheduling. |
| Craft tasks | Read tasks; confirm completion or change the scheduled date. A separate deadline is preserved. |
| Google Calendar | Read event details; the day card does not edit calendar events. |
| Local reminders | Read reminders bound to the conversation and resolve/reschedule eligible delivery states. |
| Background jobs | Inspect persisted execution status, details and actual available result files. |

Job downloads are bounded, redacted `job-result.txt` exports of the stored successful run result; they are not links to arbitrary generated artifacts.

Changes require an exact owner-bound confirmation showing the source, item and intended change. Scheduling offers tomorrow at 09:00 or the current due date plus one day when applicable. For another time, enter RFC3339 with an explicit offset, such as `2026-12-01T18:30:00+03:00`. Past times are rejected. Google Tasks and Craft store dates only, so the UI explicitly states that the clock time is discarded; Todoist can preserve the selected time. Craft checks the item before and after the write, without claiming an atomic compare-and-swap guarantee.

If delivery or a provider mutation has an **unknown outcome**, do not repeat it. Iulita checks the pinned source when possible and exposes unresolved receipts in Jobs. Seeing the desired state does not prove which request caused it. Explicitly acknowledging uncertainty permits a new intent; it does not retroactively mark the old operation successful or automatically retry the old write.

In a group, `/day` offers a generic personal-response button. Its reply is private to the actor who pressed it. Open the bot privately and use `/day text` for actionable cards. Ordinary group messages cannot silently read personal memory or invoke personal tools.

## Weather cards

Weather works through ordinary language requests and `/weather`. The direct command accepts 1–16 days, defaults to four, and respects the disabled/enabled state of the weather skill. All actually returned days are retained across cards of up to four rows; a fallback provider's shorter horizon is labelled.

Cards show the location, current model conditions when supplied by Open-Meteo, forecast low/high values, available metrics, source and retrieval time. The fallback providers are forecast-only. Missing values are unavailable, while a measured/modelled zero remains zero. A daily high is never substituted for current temperature, and unknown conditions do not become a sunny illustration. There is no invented hourly forecast. Current values are upstream model data, not a claim of a local sensor observation.

Sun, clouds, precipitation, fog and night illustrations are embedded locally in the template. No external image/font request is needed. The model's synthesized answer remains a separately formatted companion. `/weather text CITY DAYS` provides the full text without model-facing instructions. Shared location pins use a descriptive location label rather than exposing coordinates in the card.

## Telegram capabilities and availability

The new day, weather and shared-result card UI supports English and Russian, with English as the fallback for other locales. This is narrower than the application's six-language interface.

The integration uses the typed `go-telegram/bot` SDK with Bot API 10.4 support. The following are distinct capabilities, not a claim that every new Bot API feature is enabled:

| Capability | Iulita behavior |
| --- | --- |
| Private widgets and widget requests | Personal day/weather cards; owner-bound actions and private responses. |
| Updating an existing widget | Day refresh changes message state after authorization, using durable delivery/recovery. The original HTML is immutable. |
| Additional bot usernames | Discover active usernames, accept addressed commands and use a verified preferred name in generated links. |
| Rich messages and rich quotes | Read bounded typed content; render supported headings, lists, tables, code and links. Native selected quotes require source/position verification in the same chat/topic; ordinary HTML is the fallback. |
| Personal reply to a callback | Deliver an actor's personal preview/confirmation privately without putting it in the group. |
| Inline/shared widgets | Implemented for Telegram's isolated test server; disabled in production. |
| Wallet/payment query fields | Not a wallet, transfer or payment feature. Unsupported query types do not authorize actions. |

[Telegram's availability table](https://core.telegram.org/bots/widgets#availability) currently permits widgets only in private bot chats in production. Groups, forwarding and inline widgets require the test server. Its [widget profile](https://core.telegram.org/bots/widgets#what-widgets-support) runs offline inside Telegram, not in a Mini App WebView. A client without widget support may display unsupported media and its caption; it cannot reliably be detected by the bot.

### Test-server public snapshots

Use a **separate test-server bot and account** with `telegram.test_environment = true`; this setting does not unlock inline widgets on a production bot. For that test bot, enable inline mode through BotFather’s `/setinline` command and set a query placeholder, as described in [Telegram’s inline setup](https://core.telegram.org/bots/inline). Then type `@YourTestBot calc 12 * 3` or `@YourTestBot time 2026-12-01T12:00:00+03:00 Europe/Berlin` in a test-server chat, replacing `YourTestBot` with its verified username. These calculator/time results are deterministic. Reply to a result with `/publish` to preview and confirm a bounded redacted public snapshot, then use the returned `share SNAPSHOT_ID` inline query. Job result cards can offer the same confirmed export. Snapshots expire after seven days. `/unpublish SNAPSHOT_ID` confirms withdrawal; already retained copies may remain. Shared-widget events resolve the snapshot, not the owner's private conversation.

Production inline answers remain gated off. Do not enable public export by bypassing this guard.

## Settings and upgrades

```toml
[telegram]
allowed_ids = [] # Restrict to your numeric Telegram user IDs for a personal bot.
day_widgets = true
day_actions = true
# Optional: names already active on this SAME bot, assigned through BotFather.
# aliases = ["ExampleAssistantBot"]
# preferred_username = "ExampleAssistantBot"
test_environment = false
```

An empty allowlist permits all users. Supply the token through the supported secret configuration, not a public example. `day_widgets` and `day_actions` default to true, including existing configurations that omit these keys. Explicit false values remain false. Persisted dashboard settings override defaults. Restart after changing either day setting.

- `day_widgets = false` disables both `/day` and `/day text`, as well as illustrated weather cards. `/weather` still returns its full text forecast, and `/weather text` remains available when the weather skill is enabled.
- `day_actions = false` makes day cards read-only. It does not disable unrelated skill operations or confirmations.
- Environment overrides are `IULITA_TELEGRAM_DAY_WIDGETS` and `IULITA_TELEGRAM_DAY_ACTIONS`.
- `aliases` does not register or purchase a username. All active names are discovered at startup against the bot's own numeric identity, including when `aliases` is omitted. Addressed commands such as `/day@OldActiveName` continue to work while that name remains active; `preferred_username` only chooses generated links. A failed verification keeps the canonical fallback rather than trusting another identity.
- Multiple Telegram instances can specify their own token, allowlist, debounce/rate settings, usernames and test environment. Day-widget/action switches are global and applied to each instance, not per-instance JSON options.

See [configuration](configuration.md#telegram) for the full settings reference.

## Operation and troubleshooting

For a read-only identity check, make the token available through the named environment variable and run from a source checkout:

```bash
go run ./cmd/telegram-readiness
# Separate test-server credentials only:
go run ./cmd/telegram-readiness -token-env TELEGRAM_TEST_TOKEN -test-environment
```

The tool reports public bot identity, active usernames, topic flags and alias lookup availability. It does not poll updates, send messages, alter BotFather settings or print the token. Identity checks do not prove rendering, permission to mutate tasks or successful delivery.

If topics are unavailable, verify their BotFather configuration and the reported capability. `/topic` lists only stored known topics, not everything the Telegram client has ever displayed. If a client shows **Widget stopped**, open a new card and try `/day text` or `/weather text CITY DAYS`; capture the client version when reporting it. Already-sent widget HTML cannot be replaced by a state update. Iulita falls back to text on eligible API failures, but cannot detect every client-side rendering failure.

### Privacy and publication

Telegram responses are buffered until final classification instead of exposing unchecked streamed fragments. Suspected credentials or payment details use recipient-specific ephemeral delivery, without bookmark/copy controls; failure produces a neutral notice, not an ordinary-message fallback containing the sensitive text. Callbacks, prompts and mutations retain their captured actor and topic ownership.

Detected payment entities, card/TON text and secrets are redacted before model/history/bookmark use where supported. This is best-effort text/entity handling, not a guarantee for OCR, binary attachments or obfuscated secrets. Model output is never executable widget HTML. Consult the [security guide](security.md#telegram-security).
