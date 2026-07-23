<template>
  <n-spin :show="loading">
    <n-space vertical :size="24">
      <n-page-header :title="t('slackActivity.title')" :subtitle="t('slackActivity.subtitle')">
        <template #extra>
          <n-button size="small" @click="fetchData">{{ t('common.refresh') }}</n-button>
        </template>
      </n-page-header>

      <n-alert type="info" :show-icon="true">
        {{ t('slackActivity.privacyNote') }}
      </n-alert>

      <n-card :title="t('slackActivity.recent')">
        <n-empty v-if="rows.length === 0" :description="t('slackActivity.noData')" />
        <n-data-table
          v-else
          :columns="columns"
          :data="rows"
          :bordered="true"
          :pagination="{ pageSize: 25 }"
          :row-key="(row: SlackActivityEntry) => row.id"
          size="small"
        />
      </n-card>
    </n-space>
  </n-spin>
</template>

<script setup lang="ts">
import { ref, computed, h, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { useMessage, NButton } from 'naive-ui'
import {
  NSpace, NCard, NDataTable, NPageHeader, NSpin, NEmpty, NTag, NAlert,
} from 'naive-ui'
import type { DataTableColumns } from 'naive-ui'
import { api } from '../api'
import type { SlackActivityEntry } from '../api'

const { t } = useI18n()
const message = useMessage()
const loading = ref(true)
const rows = ref<SlackActivityEntry[]>([])

function formatTime(iso: string): string {
  if (!iso) return '-'
  const d = new Date(iso)
  if (isNaN(d.getTime())) return '-'
  return d.toLocaleString()
}

// Detail is metadata-only (server-whitelisted) — never message text or queries.
// Post rows carry `channel`/`decision` (no `mode`), so show the channel too — "a
// post to which channel was blocked" is the core audit question.
function formatDetail(detail: Record<string, unknown>): string {
  if (!detail || typeof detail !== 'object') return '-'
  const parts: string[] = []
  for (const key of ['mode', 'outcome', 'decision', 'channel', 'result_count', 'text_len']) {
    if (detail[key] !== undefined && detail[key] !== null && detail[key] !== '') {
      parts.push(`${key}=${detail[key]}`)
    }
  }
  return parts.length ? parts.join(' · ') : '-'
}

const columns = computed<DataTableColumns<SlackActivityEntry>>(() => [
  { title: t('slackActivity.time'), key: 'created_at', width: 200, render: (row) => formatTime(row.created_at) },
  { title: t('slackActivity.action'), key: 'action', width: 200 },
  {
    title: t('slackActivity.result'),
    key: 'success',
    width: 110,
    render: (row) => h(
      NTag,
      { type: row.success ? 'success' : 'error', size: 'small', bordered: false },
      { default: () => (row.success ? t('slackActivity.ok') : t('slackActivity.failed')) },
    ),
  },
  { title: t('slackActivity.detail'), key: 'detail', render: (row) => formatDetail(row.detail) },
])

async function fetchData() {
  loading.value = true
  try {
    rows.value = (await api.getSlackActivity(200)) || []
  } catch (e: any) {
    message.error(e.message || t('slackActivity.loadFailed'))
  } finally {
    loading.value = false
  }
}

onMounted(fetchData)
</script>
