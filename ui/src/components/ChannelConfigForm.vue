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
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { NFormItem, NInput, NInputNumber, NDynamicTags, NText, NSelect } from 'naive-ui'
import type { CredentialView } from '../api'

const { t } = useI18n()

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
