<template>
  <n-space vertical :size="16">
    <n-page-header :title="t('files.title')" :subtitle="t('files.subtitle')" />

    <n-data-table
      :columns="columns"
      :data="artifacts"
      :loading="loading"
      :row-key="(r: HTTPArtifactView) => r.id"
    />

    <n-space>
      <n-text :depth="3">{{ t('files.total', { count: artifacts.length }) }}</n-text>
    </n-space>
  </n-space>
</template>

<script setup lang="ts">
import { ref, h, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  NSpace, NPageHeader, NDataTable, NButton, NTag, NText, NPopconfirm,
  useMessage,
} from 'naive-ui'
import type { DataTableColumn } from 'naive-ui'
import { api } from '../api'
import type { HTTPArtifactView } from '../api'

const { t } = useI18n()
const message = useMessage()

const artifacts = ref<HTTPArtifactView[]>([])
const loading = ref(false)

function formatBytes(n: number): string {
  if (n >= 1_000_000_000) return (n / 1e9).toFixed(1) + ' GB'
  if (n >= 1_000_000) return (n / 1e6).toFixed(1) + ' MB'
  if (n >= 1_000) return (n / 1e3).toFixed(1) + ' KB'
  return n + ' B'
}

async function load() {
  loading.value = true
  try {
    artifacts.value = await api.getHTTPArtifacts()
  } catch {
    // 404 when artifact delivery is not configured — empty list, no error spam.
    artifacts.value = []
  } finally {
    loading.value = false
  }
}

// Download flow: issue a one-time same-origin ticket via the authenticated
// API, then navigate the browser through it (no giant JS blob, no token in
// the URL longer than its 60s TTL).
async function download(row: HTTPArtifactView) {
  try {
    const ticket = await api.issueArtifactTicket(row.id)
    window.location.href = ticket.download_url
  } catch {
    message.error(t('files.downloadFailed'))
  }
}

async function remove(row: HTTPArtifactView) {
  try {
    await api.deleteHTTPArtifact(row.id)
    message.success(t('files.deleted'))
    await load()
  } catch {
    message.error(t('files.deleteFailed'))
  }
}

const columns: DataTableColumn<HTTPArtifactView>[] = [
  { title: t('files.filename'), key: 'filename', minWidth: 200, ellipsis: { tooltip: true } },
  {
    title: t('files.size'),
    key: 'bytes',
    width: 110,
    render: (row) => formatBytes(row.bytes),
  },
  {
    title: 'SHA-256',
    key: 'sha256',
    width: 160,
    render: (row) => h('code', { style: 'font-size:11px; word-break:break-all' }, row.sha256.slice(0, 16) + '…'),
  },
  {
    title: t('files.expires'),
    key: 'expires_at',
    width: 180,
    render: (row) => new Date(row.expires_at).toLocaleString(),
  },
  {
    title: '',
    key: 'actions',
    width: 200,
    render: (row) =>
      h(NSpace, { size: 'small' }, () => [
        h(NButton, { size: 'small', type: 'primary', onClick: () => download(row) }, () => t('files.download')),
        h(
          NPopconfirm,
          { onPositiveClick: () => remove(row) },
          {
            trigger: () => h(NButton, { size: 'small', type: 'error', quaternary: true }, () => t('common.delete')),
            default: () => t('files.deleteConfirm'),
          },
        ),
      ]),
  },
]

onMounted(load)
</script>
