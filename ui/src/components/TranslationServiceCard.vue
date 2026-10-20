<template>
  <n-card :title="t('translation.title')" size="small" embedded data-testid="translation-service">
    <n-space vertical :size="12">
      <n-text>{{ t('translation.description') }}</n-text>
      <n-text>{{ t('translation.connection') }}</n-text>
      <n-text :depth="3">{{ t('translation.sharedKey') }}</n-text>
      <n-text>{{ t('translation.mode') }}</n-text>
      <div role="status" aria-live="polite">
        <n-tag :type="state === 'ready' ? 'info' : 'warning'" :bordered="false">
          {{ t(`translation.states.${state}`) }}
        </n-tag>
      </div>
      <n-text v-if="state === 'ready'" :depth="3">{{ t('translation.notVerified') }}</n-text>
      <n-button v-if="admin" size="small" @click="$emit('manage-connection')">
        {{ t('translation.openModels') }}
      </n-button>
      <n-text v-else :depth="3">{{ t('translation.adminManaged') }}</n-text>
    </n-space>
  </n-card>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { NButton, NCard, NSpace, NTag, NText } from 'naive-ui'
import type { TranslationServiceStatus } from '../api'

const props = defineProps<{ serviceStatus?: TranslationServiceStatus; admin: boolean }>()
defineEmits<{ 'manage-connection': [] }>()
const { t } = useI18n()
// A missing status never implies that the provider is ready or has been checked.
const state = computed(() => props.serviceStatus?.state ?? 'runtime_unavailable')
</script>

<style scoped>
:deep(.n-tag) {
  max-width: 100%;
  height: auto;
  white-space: normal;
  padding-block: 4px;
}
</style>
