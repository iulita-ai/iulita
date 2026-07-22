<template>
  <!-- Telegram -->
  <template v-if="channelType === 'telegram'">
    <n-form-item :label="t('channelConfig.credential')" :required="required">
      <n-select
        :value="selectedCredentialId"
        @update:value="v => emit('update:selectedCredentialId', v)"
        :options="credentialOptions"
        :placeholder="t('channelConfig.selectCredential')"
        :disabled="disabled"
      />
      <template #feedback>
        {{ t('channelConfig.credentialHelp') }}
      </template>
    </n-form-item>

    <n-form-item :label="t('channelConfig.allowedUserIds')">
      <n-dynamic-tags
        :value="tg.allowedIdTags"
        @update:value="onAllowedIdsChange"
        :disabled="disabled"
      />
      <template #feedback>
        {{ t('channelConfig.allowedUserIdsHelp') }}
      </template>
    </n-form-item>
    <n-form-item :label="t('channelConfig.debounceWindow')">
      <n-input
        :value="tg.debounce_window"
        @update:value="v => update('debounce_window', v)"
        placeholder="1.5s"
        :disabled="disabled"
        style="width: 140px"
      />
      <template #feedback>
        {{ t('channelConfig.debounceHelp') }}
      </template>
    </n-form-item>
    <n-form-item :label="t('channelConfig.rateLimit')">
      <n-input-number
        :value="tg.rate_limit"
        @update:value="v => update('rate_limit', v ?? 0)"
        :min="0"
        :disabled="disabled"
        style="width: 140px"
      />
      <template #feedback>
        {{ t('channelConfig.rateLimitHelp') }}
      </template>
    </n-form-item>
    <n-form-item :label="t('channelConfig.rateWindow')">
      <n-input
        :value="tg.rate_window"
        @update:value="v => update('rate_window', v)"
        placeholder="1m"
        :disabled="disabled"
        style="width: 140px"
      />
      <template #feedback>
        {{ t('channelConfig.rateWindowHelp') }}
      </template>
    </n-form-item>
  </template>

  <!-- Discord -->
  <template v-else-if="channelType === 'discord'">
    <n-form-item :label="t('channelConfig.credential')" :required="required">
      <n-select
        :value="selectedCredentialId"
        @update:value="v => emit('update:selectedCredentialId', v)"
        :options="credentialOptions"
        :placeholder="t('channelConfig.selectCredential')"
        :disabled="disabled"
      />
      <template #feedback>
        {{ t('channelConfig.credentialHelp') }}
      </template>
    </n-form-item>
    <n-form-item :label="t('channelConfig.allowedChannelIds')">
      <n-dynamic-tags
        :value="discord.allowedChannelIdTags"
        @update:value="onDiscordChannelIdsChange"
        :disabled="disabled"
      />
      <template #feedback>
        {{ t('channelConfig.allowedChannelIdsHelp') }}
      </template>
    </n-form-item>
  </template>

  <!-- Slack -->
  <template v-else-if="channelType === 'slack'">
    <n-form-item :label="t('channelConfig.credential')" :required="required">
      <n-select
        :value="selectedCredentialId"
        @update:value="v => emit('update:selectedCredentialId', v)"
        :options="credentialOptions"
        :placeholder="t('channelConfig.selectCredential')"
        :disabled="disabled"
      />
      <template #feedback>
        {{ t('channelConfig.slackBotTokenHelp') }}
      </template>
    </n-form-item>
    <n-form-item :label="t('channelConfig.slackAppToken')" :required="required">
      <n-input
        :value="slackCfg.app_token"
        @update:value="v => update('app_token', v)"
        placeholder="xapp-..."
        :disabled="disabled"
        type="password"
        show-password-on="click"
      />
      <template #feedback>
        {{ t('channelConfig.slackAppTokenHelp') }}
      </template>
    </n-form-item>
    <n-form-item :label="t('channelConfig.slackAllowedUserIds')">
      <n-dynamic-tags
        :value="slackCfg.allowedUserIdTags"
        @update:value="onSlackUserIdsChange"
        :disabled="disabled"
      />
      <template #feedback>
        {{ t('channelConfig.slackAllowedUserIdsHelp') }}
      </template>
    </n-form-item>
    <n-form-item :label="t('channelConfig.debounceWindow')">
      <n-input
        :value="slackCfg.debounce_window"
        @update:value="v => update('debounce_window', v)"
        placeholder="2s"
        :disabled="disabled"
        style="width: 140px"
      />
      <template #feedback>
        {{ t('channelConfig.debounceHelp') }}
      </template>
    </n-form-item>
    <n-form-item :label="t('channelConfig.rateLimit')">
      <n-input-number
        :value="slackCfg.rate_limit"
        @update:value="v => update('rate_limit', v ?? 0)"
        :min="0"
        :disabled="disabled"
        style="width: 140px"
      />
      <template #feedback>
        {{ t('channelConfig.rateLimitHelp') }}
      </template>
    </n-form-item>
    <n-form-item :label="t('channelConfig.rateWindow')">
      <n-input
        :value="slackCfg.rate_window"
        @update:value="v => update('rate_window', v)"
        placeholder="1m"
        :disabled="disabled"
        style="width: 140px"
      />
      <template #feedback>
        {{ t('channelConfig.rateWindowHelp') }}
      </template>
    </n-form-item>

    <n-divider>{{ t('channelConfig.slackPosting') }}</n-divider>
    <n-text :depth="3" style="display: block; margin-bottom: 12px; font-size: 12px;">
      {{ t('channelConfig.slackPostingHelp') }}
    </n-text>
    <n-form-item :label="t('channelConfig.slackWriteChannels')">
      <n-dynamic-tags :value="slackCfg.write_channels" @update:value="onWriteChannelsChange" :disabled="disabled" />
      <template #feedback>{{ t('channelConfig.slackWriteChannelsHelp') }}</template>
    </n-form-item>
    <n-form-item :label="t('channelConfig.slackWriteMode')">
      <n-radio-group :key="writeModeKey" :value="slackCfg.write_mode" @update:value="onWriteModeChange" :disabled="disabled">
        <n-space vertical>
          <n-radio value="off">{{ t('channelConfig.slackWriteOff') }}</n-radio>
          <n-radio value="draft">{{ t('channelConfig.slackWriteDraft') }}</n-radio>
          <n-radio value="auto">{{ t('channelConfig.slackWriteAuto') }}</n-radio>
        </n-space>
      </n-radio-group>
    </n-form-item>
    <template v-if="slackCfg.write_mode === 'auto'">
      <n-form-item :label="t('channelConfig.slackMaxPostsPerHour')">
        <n-input-number :value="slackCfg.max_posts_per_hour" @update:value="v => updateGuardrail('max_posts_per_hour', v ?? 0)" :min="1" :disabled="disabled" style="width: 140px" />
      </n-form-item>
      <n-form-item :label="t('channelConfig.slackQuietHours')">
        <n-space align="center">
          <n-input-number :value="slackCfg.quiet_start" @update:value="v => updateGuardrail('quiet_start', v ?? 0)" :min="0" :max="23" :disabled="disabled" style="width: 90px" />
          <span>–</span>
          <n-input-number :value="slackCfg.quiet_end" @update:value="v => updateGuardrail('quiet_end', v ?? 0)" :min="0" :max="23" :disabled="disabled" style="width: 90px" />
        </n-space>
        <template #feedback>{{ t('channelConfig.slackQuietHoursHelp') }}</template>
      </n-form-item>
    </template>
  </template>

  <!-- Web Chat -->
  <template v-else-if="channelType === 'web'">
    <n-form-item>
      <n-text :depth="3">{{ t('channelConfig.noConfigRequired') }}</n-text>
    </n-form-item>
  </template>

  <!-- Unsupported channel type -->
  <template v-else>
    <n-form-item :label="t('channelConfig.configJson')">
      <n-input
        :value="rawJson"
        @update:value="onRawJsonChange"
        type="textarea"
        :rows="4"
        :disabled="disabled"
        placeholder="{}"
      />
    </n-form-item>
  </template>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { NFormItem, NInput, NInputNumber, NDynamicTags, NText, NSelect, NDivider, NRadioGroup, NRadio, NSpace, useDialog } from 'naive-ui'
import type { CredentialView } from '../api'

const { t } = useI18n()
// useDialog throws without an <n-dialog-provider> ancestor (e.g. in unit tests
// that mount this form in isolation). Degrade gracefully to a direct commit.
let dialog: ReturnType<typeof useDialog> | null = null
try {
  dialog = useDialog()
} catch {
  dialog = null
}

export interface TelegramConfig {
  token: string
  allowed_ids: number[]
  debounce_window: string
  rate_limit: number
  rate_window: string
}

export interface ChannelConfigFormProps {
  channelType: string
  modelValue: string // JSON string
  disabled?: boolean
  required?: boolean
  credentials?: CredentialView[]
  selectedCredentialId?: number | null
}

const props = withDefaults(defineProps<ChannelConfigFormProps>(), {
  disabled: false,
  required: false,
  credentials: () => [],
  selectedCredentialId: null,
})

const emit = defineEmits<{
  'update:modelValue': [value: string]
  'update:selectedCredentialId': [value: number | null]
}>()

const credentialOptions = computed(() =>
  (props.credentials ?? []).map(c => ({ label: `${c.name} (${c.type})`, value: c.id }))
)

function parseConfig(): Record<string, unknown> {
  if (!props.modelValue) return {}
  try {
    return JSON.parse(props.modelValue)
  } catch {
    return {}
  }
}

function emitUpdate(obj: Record<string, unknown>) {
  emit('update:modelValue', JSON.stringify(obj))
}

// Telegram-specific computed
const tg = computed(() => {
  const cfg = parseConfig() as Partial<TelegramConfig>
  return {
    token: cfg.token ?? '',
    allowed_ids: cfg.allowed_ids ?? [],
    allowedIdTags: (cfg.allowed_ids ?? []).map(String),
    debounce_window: cfg.debounce_window ?? '1.5s',
    rate_limit: cfg.rate_limit ?? 0,
    rate_window: cfg.rate_window ?? '1m',
  }
})

function update(field: string, value: unknown) {
  const cfg = parseConfig()
  cfg[field] = value
  emitUpdate(cfg)
}

function onAllowedIdsChange(tags: string[]) {
  const ids = tags.map(Number).filter(n => !isNaN(n) && n > 0)
  update('allowed_ids', ids)
}

// Slack-specific computed
interface SlackGuardrails { max_posts_per_hour?: number; quiet_hours?: [number, number] }
const slackCfg = computed(() => {
  const cfg = parseConfig() as {
    bot_token?: string; app_token?: string; allowed_user_ids?: string[]; debounce_window?: string
    rate_limit?: number; rate_window?: string
    write_channels?: string[]; write_mode?: string; guardrails?: SlackGuardrails
  }
  const g = cfg.guardrails ?? {}
  return {
    bot_token: cfg.bot_token ?? '',
    app_token: cfg.app_token ?? '',
    allowed_user_ids: cfg.allowed_user_ids ?? [],
    allowedUserIdTags: cfg.allowed_user_ids ?? [],
    debounce_window: cfg.debounce_window ?? '2s',
    rate_limit: cfg.rate_limit ?? 0,
    rate_window: cfg.rate_window ?? '1m',
    write_channels: cfg.write_channels ?? [],
    write_mode: cfg.write_mode ?? 'off',
    max_posts_per_hour: g.max_posts_per_hour ?? 0,
    quiet_start: g.quiet_hours?.[0] ?? 0,
    quiet_end: g.quiet_hours?.[1] ?? 0,
  }
})

function onSlackUserIdsChange(tags: string[]) {
  update('allowed_user_ids', tags)
}

function onWriteChannelsChange(tags: string[]) {
  update('write_channels', tags)
}

// The write-mode radio is a controlled component driven by slackCfg.write_mode.
// When the user picks "auto" we intercept with a confirm dialog and do NOT commit
// yet — but the native <input> has already visually moved to "auto". If the user
// cancels, no reactive dep changes, so Vue never re-renders the radio back. Bumping
// this key force-remounts the group so it snaps to the true (unchanged) value.
const writeModeKey = ref(0)

// Selecting "auto" is a deliberate, confirmed choice (posts publicly without review).
function onWriteModeChange(mode: string) {
  if (mode === 'auto' && dialog) {
    const revert = () => { writeModeKey.value++ }
    dialog.warning({
      title: t('channelConfig.slackWriteAuto'),
      content: t('channelConfig.slackWriteAutoWarn'),
      positiveText: t('common.confirm'),
      negativeText: t('common.cancel'),
      onPositiveClick: commitAuto,
      onNegativeClick: revert,
      onClose: revert,
      onMaskClick: revert,
      onEsc: revert,
    })
    return
  }
  update('write_mode', mode)
}

// Committing "auto" also seeds a safe default rate cap (min 1) so the user doesn't
// land on an invalid guardrail state that save-validation would immediately reject.
function commitAuto() {
  const cfg = parseConfig() as { guardrails?: SlackGuardrails; [k: string]: unknown }
  cfg.write_mode = 'auto'
  const g: SlackGuardrails = cfg.guardrails ?? {}
  if (!(typeof g.max_posts_per_hour === 'number' && g.max_posts_per_hour > 0)) {
    g.max_posts_per_hour = 1
  }
  if (!Array.isArray(g.quiet_hours)) g.quiet_hours = [0, 0]
  cfg.guardrails = g
  emitUpdate(cfg)
}

function updateGuardrail(field: 'max_posts_per_hour' | 'quiet_start' | 'quiet_end', value: number) {
  const g = { max_posts_per_hour: slackCfg.value.max_posts_per_hour, quiet_hours: [slackCfg.value.quiet_start, slackCfg.value.quiet_end] as [number, number] }
  if (field === 'max_posts_per_hour') g.max_posts_per_hour = value
  else if (field === 'quiet_start') g.quiet_hours[0] = value
  else g.quiet_hours[1] = value
  update('guardrails', g)
}

// Discord-specific computed
const discord = computed(() => {
  const cfg = parseConfig() as { token?: string; allowed_channel_ids?: string[] }
  return {
    token: cfg.token ?? '',
    allowed_channel_ids: cfg.allowed_channel_ids ?? [],
    allowedChannelIdTags: cfg.allowed_channel_ids ?? [],
  }
})

function onDiscordChannelIdsChange(tags: string[]) {
  update('allowed_channel_ids', tags)
}

// Raw JSON fallback for unsupported types
const rawJson = computed(() => props.modelValue || '{}')

function onRawJsonChange(val: string) {
  emit('update:modelValue', val)
}
</script>
