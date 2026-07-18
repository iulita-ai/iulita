<template>
  <n-card :title="t('import.progressTitle')" size="small" role="status" aria-live="polite">
    <template #header-extra>
      <n-button v-if="done || failed" size="tiny" quaternary @click="$emit('close')">
        {{ t('import.dismiss') }}
      </n-button>
    </template>
    <n-space vertical :size="12">
      <n-steps :current="stepIndex" :status="failed ? 'error' : 'process'" size="small">
        <n-step :title="t('import.phaseMemories')" />
        <n-step :title="t('import.phaseConversations')" />
        <n-step :title="t('import.phaseEmbedding')" />
      </n-steps>

      <div>
        <n-space align="center" :size="8">
          <n-spin v-if="indeterminate" :size="14" />
          <n-text depth="3" style="font-size: 13px">{{ phaseLabel }}</n-text>
        </n-space>
        <n-progress
          v-if="!indeterminate"
          type="line"
          :percentage="pct"
          :processing="!done && !failed"
          :status="failed ? 'error' : 'default'"
          :indicator-placement="'inside'"
        />
      </div>

      <n-space :size="20" style="font-size: 13px">
        <span v-if="stored != null">{{ t('import.stored') }}: {{ stored }}</span>
        <span v-if="skipped != null && skipped > 0">{{ t('import.skipped') }}: {{ skipped }}</span>
      </n-space>
    </n-space>
  </n-card>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { NCard, NButton, NSpace, NSteps, NStep, NText, NProgress, NSpin } from 'naive-ui'

interface ProgressPayload {
  job_id: string
  phase: string
  done?: number
  total?: number
  stored?: number
  skipped?: number
  // done payload uses these field names:
  messages_stored?: number
  messages_skipped?: number
}

const props = defineProps<{ payload: ProgressPayload; done?: boolean; failed?: boolean }>()
defineEmits<{ (e: 'close'): void }>()
const { t } = useI18n()

const p = computed(() => props.payload)
const done = computed(() => props.done === true)
const failed = computed(() => props.failed === true)

// stored/skipped come from progress payloads (stored/skipped) or the done payload
// (messages_stored/messages_skipped).
const stored = computed(() => p.value.stored ?? p.value.messages_stored ?? null)
const skipped = computed(() => p.value.skipped ?? p.value.messages_skipped ?? null)

const stepIndex = computed(() => {
  if (done.value) return 4
  switch (p.value.phase) {
    case 'conversations':
      return 2
    case 'embedding':
      return 3
    default: // starting | memories
      return 1
  }
})

// True when the phase has no known total (conversations stream): show an animated
// spinner + growing count instead of a static 0% line bar that reads as stalled.
const indeterminate = computed(() => !done.value && !failed.value && !((p.value.total ?? 0) > 0))

const pct = computed(() => {
  if (done.value) return 100
  const total = p.value.total ?? 0
  const doneN = p.value.done ?? 0
  if (total > 0) return Math.min(100, Math.round((doneN / total) * 100))
  return 0
})

const phaseLabel = computed(() => {
  if (failed.value) return t('import.phaseFailed')
  if (done.value) return t('import.phaseDone')
  const phase = p.value.phase || 'starting'
  const label = t(`import.phaseLabel_${phase}`)
  const doneN = p.value.done ?? 0
  if (phase === 'embedding' || phase === 'conversations') return `${label} (${doneN})`
  return label
})
</script>
