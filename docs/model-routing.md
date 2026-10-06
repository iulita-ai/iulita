# Model profiles and task routing

Administrators can configure DeepSeek and Z.ai in **Settings → Models**. Other
users do not fetch administrative model settings. Configuration encryption and
changing the initial administrator password are prerequisites for changes.

The catalog is a documentation snapshot dated 2026-10-05; it does not establish
account access or successful image recognition.

| Profile preset | API model | Documented input | Context | Default mode |
| --- | --- | --- | --- | --- |
| DeepSeek Flash | `deepseek-flash` | Text, images | 1,000,000 tokens | Thinking disabled |
| DeepSeek Pro | `deepseek-v4-pro` | Text | 1,000,000 tokens | Thinking disabled |
| GLM Flash | `glm-5.3-flash` | Text, images | 1,000,000 tokens | Thinking enabled, low effort |
| GLM | `glm-5.3` | Text | 1,000,000 tokens | Thinking enabled, high effort |

References: [DeepSeek models](https://api-docs.deepseek.com/quick_start/pricing/),
[GLM Flash](https://docs.z.ai/guides/vlm/glm-5.3-flash),
[GLM](https://docs.z.ai/guides/llm/glm-5.3).

Use the standard metered Z.ai API, `https://api.z.ai/api/paas/v4`; a Coding Plan
subscription does not establish entitlement to this endpoint. New DeepSeek and
Z.ai connection changes are restricted to their official HTTPS origins. These
clients use the application's configured proxy transport and reject redirects.
Existing trusted legacy connections can be retained during migration; their
connection editing remains in the legacy path before a Models draft is saved.

## Configure and verify

1. Add a connection and save the profiles **for verification**, initially without
   assigning them to live task roles. Keys are encrypted and never returned.
2. Explicitly run the text and tool checks for each new profile. Run the image
   check for the profile intended for the vision role. Checks incur provider
   usage; loading settings, refreshing status and saving drafts do not.
3. Assign everyday, complex, vision and background roles, then save the same
   draft again. Unchanged key generations and verification evidence survive
   this step. Identity changes require new checks; renaming a profile does not.
4. Apply the verified draft. The previous live key remains in use until this
   operation succeeds. First-time setup then offers completion and restart
   instructions because background services start on application boot.

Image checks use synthetic raster images with a random printed label and color,
including an image-only request and a two-image request. A successful text reply
alone does not establish vision support. Pro and full GLM reject image requests
before HTTP. JPEG, PNG, GIF and WebP are accepted within bounded size/pixel limits;
PDFs are not image inputs and remote image URLs are not accepted by these adapters.

Checks have a 90-second deadline and explicit cancellation. Status refresh cannot
start a second paid check. Reusing the same idempotency key recovers the same
operation; an expired key requires a new explicit request. Interrupted checks are
shown as uncertain after restart. Cancellation cannot promise that a provider
has stopped billing work it already accepted.

Z.ai can return HTTP 429 for account balance, product access and quota rejections
as well as transient rate limits. The dashboard explains recognized account
errors; retry and fallback do not repeat or switch providers on those rejections.
Only fixed error categories are retained, without upstream messages.

GLM tool responses may omit `reasoning_content` even with thinking enabled.
Tool continuation preserves the supplied reasoning exactly, including its
absence; it does not fabricate a block. DeepSeek thinking profiles still require
their reasoning replay contract. Requests rejected before HTTP are recorded as
known zero usage.

The vision check uses large raster labels and a strict image-only oracle: the
model must read an unseen label and rectangle color, then read two images in
order. A successful older check remains valid for the same profile and
connection. A check establishes synthetic conformance; it does not establish
accuracy for arbitrary documents or small text.


## Routing and migration

The existing router resolves explicit profile IDs separately from legacy hints.
A tool loop pins its policy revision and model profile. Profiles share a provider
connection while retaining independent model parameters. Jobs can select a
verified profile; clearing that selection returns to background routing. Planner
agents use the complex role; delegation, compression and other background calls
use the common router and credential admission boundary.

The old visible-only Claude-to-Haiku synthesis route is retained only for exact
preserved legacy profiles. It cannot move explicit profile overrides or opaque
reasoning between models. New profiles keep their model throughout a tool loop.

**Import current routing** prepares a draft preserving custom hint targets and
legacy light routing. Unchanged server-recognized Claude/OpenAI/Ollama profiles
can be labeled **existing configuration preserved**; that label does not claim
new live tests. Changing their model parameters or connection identity removes
that compatibility status. New DeepSeek/GLM profiles always need verification.
Managed Claude profiles use thinking disabled until signed reasoning replay is
implemented. Importing a legacy Claude configuration with extended thinking
therefore requires verification of the changed mode before activation.

DeepSeek thinking profiles remain experimental until their durable multi-turn
reasoning replay contract passes live conformance. GLM preserves reasoning only
within the current tool loop and clears cross-turn thinking. Automatic paid
classification is opt-in and requires current quality evidence. Assigning GLM to
the complex/planner role does not require enabling classification. Unverified
cross-model fallbacks are rejected.

An emergency key revoke suspends new calls immediately and attempts to cancel
admitted calls, including old pinned snapshots and legacy clients. Revoked keys
cannot be reintroduced under a new generation. An environment-owned connection
cannot be rotated through the dashboard; remove the environment override and
restart first. Explicit policy restoration prepares a retained settings revision
with **current** credentials and normal verification gates; it never restores
revoked keys or clears current provider bans.

## Usage and operations

Each adapter attempt records requested and served model, profile, operation,
policy revision, time, tokens and the price estimate used at that time. Private
prompts, images, tool arguments, reasoning and keys are excluded from this ledger
and from model probe evidence. Unknown usage or prices remain visibly unknown.
DeepSeek estimates currently use conservative peak rates throughout the day;
these are indicative estimates, not an invoice. Response caching is opt-in for
stateless requests with a complete identity; conversations and attachments bypass
it, and a cache hit adds no new token charge.

Before deployment, back up SQLite and the configuration encryption key using the
existing private backup process. The usage aggregation index now includes the
provider identity. Rolling back to an older binary requires restoring its matching
pre-upgrade database backup; old usage UPSERT statements are not compatible with
this migration. The local implementation does not deploy or change live provider
assignments automatically. Live conformance and end-to-end account checks remain
deployment prerequisites.

## Paid selection and backup profiles

Automatic selection uses a separate profile with at most 1024 output tokens (the GLM Flash Selector preset uses 512). It sends at most 500 Unicode characters of the current ambiguous text request, without conversation history or tools. Simple requests use Everyday; reasoning and creative work use Complex. Explicit roles, profile choices, route hints, attachments and tool continuations skip classification. A failed selector uses Everyday. The classifier deadline defaults to 5000 ms and supports 500–10000 ms.

In **Models → Task roles**, select the classifier profile, save for testing, and run its text and tool checks plus **Evaluate classifier**. Evaluation makes up to 12 paid synthetic calls in English and Russian. Activation requires at least 11 correct routes and all eight complex/creative cases correct. Evidence is server-owned and bound to the model, parameters, credential generation, prompt version and configured deadline; changing them requires another evaluation. A new failed evaluation invalidates the previous quality pass.

Each role can have up to two ordered backup profiles with current text/tool verification; vision backups additionally need a successful image check. Profiles with a smaller context window are skipped. A temporary availability failure (HTTP 429/500/502/503/504/529, recognized transport failure, or the attempt deadline) may switch profiles only before visible text or tool calls. Authentication, balance, input, configuration, context, cancellation and document failures do not switch. Once a profile is pinned or a tool round has started, it remains pinned. Opaque reasoning from a failed attempt is never transferred. Each real attempt is accounted for separately, including potentially billable failed attempts.

The per-attempt deadline applies when backups are configured, defaults to 60000 ms, and supports 1000–120000 ms. All attempts also share the caller deadline. Save and activate a tested policy explicitly; opening this page starts no paid requests.
