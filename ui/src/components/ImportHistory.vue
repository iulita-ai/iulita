<template>
  <n-card :title="t('import.historyTitle')" size="small">
    <template #header-extra>
      <n-button size="small" quaternary :loading="loading" @click="$emit('refresh')">
        {{ t('common.refresh') }}
      </n-button>
    </template>
    <n-data-table
      :columns="columns"
      :data="runs"
      :loading="loading"
      :bordered="true"
      :pagination="{ pageSize: 10 }"
      :row-key="(row: ImportRun) => row.job_id"
      size="small"
    />
  </n-card>
</template>

<script setup lang="ts">
import { computed, h } from 'vue'
import { useI18n } from 'vue-i18n'
import { NCard, NButton, NDataTable, NTag, NText, NPopconfirm } from 'naive-ui'
import type { DataTableColumns } from 'naive-ui'
import type { ImportRun } from '../api'

const props = defineProps<{ runs: ImportRun[]; loading?: boolean; activeJobs?: string[] }>()
const emit = defineEmits<{ (e: 'refresh'): void; (e: 'cancel', jobID: string): void }>()
const { t } = useI18n()

function statusLabel(status: string): string {
  const key = `import.status_${status}`
  const label = t(key)
  return label === key ? status : label
}

const statusType: Record<string, 'success' | 'error' | 'warning' | 'info' | 'default'> = {
  done: 'success',
  failed: 'error',
  canceled: 'warning',
  running: 'info',
}

function fmt(ts: string): string {
  if (!ts || ts.startsWith('0001')) return '—'
  return new Date(ts).toLocaleString()
}

const columns = computed<DataTableColumns<ImportRun>>(() => [
  {
    title: t('import.status'),
    key: 'status',
    width: 100,
    render: (row) =>
      h(NTag, { type: statusType[row.status] || 'default', size: 'small', bordered: false }, { default: () => statusLabel(row.status) }),
  },
  {
    title: t('import.summary'),
    key: 'summary',
    render: (row) =>
      h(NText, { depth: 3, style: 'font-size:12px' }, {
        default: () =>
          `${t('import.convs')}: ${row.conversations} · ${t('import.msgs')}: ${row.messages_stored} · ${t('import.facts')}: ${row.facts}` +
          (row.parse_errors ? ` · ${t('import.parseErrors')}: ${row.parse_errors}` : ''),
      }),
  },
  { title: t('import.started'), key: 'started_at', width: 170, render: (row) => fmt(row.started_at) },
  {
    title: '',
    key: 'actions',
    width: 100,
    render: (row) => {
      // Cancel is only possible while the import is still queued (pending). Once the
      // worker has emitted progress, hide the button to avoid an inevitable 409.
      if (row.status !== 'running' || (props.activeJobs || []).includes(row.job_id)) return null
      return h(
        NPopconfirm,
        { onPositiveClick: () => emit('cancel', row.job_id) },
        {
          trigger: () => h(NButton, { size: 'tiny', type: 'warning', ghost: true }, { default: () => t('import.cancel') }),
          default: () => t('import.cancelConfirm'),
        },
      )
    },
  },
])

// keep props referenced for template
void props
</script>
