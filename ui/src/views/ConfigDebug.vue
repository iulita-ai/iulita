<template>
  <n-space vertical :size="24">
    <n-h2>{{ t('configDebug.title') }}</n-h2>

    <n-alert type="info" :bordered="false">
      {{ t('configDebug.description') }}
    </n-alert>

    <n-card :title="t('configDebug.resolvedPaths')">
      <n-spin v-if="loading" />
      <n-descriptions v-else-if="data" bordered :column="1" label-placement="left" size="small">
        <n-descriptions-item :label="t('configDebug.configFile')">
          <n-text :type="data.paths.config_exists ? 'success' : 'error'">
            {{ data.paths.config_file }}
          </n-text>
          <n-tag v-if="!data.paths.config_exists" size="small" type="error" :bordered="false" style="margin-left: 8px">
            {{ t('configDebug.notFound') }}
          </n-tag>
        </n-descriptions-item>
        <n-descriptions-item :label="t('configDebug.database')">{{ data.paths.database_file }}</n-descriptions-item>
        <n-descriptions-item :label="t('configDebug.modelsDir')">{{ data.paths.models_dir }}</n-descriptions-item>
        <n-descriptions-item :label="t('configDebug.logFile')">{{ data.paths.log_file }}</n-descriptions-item>
        <n-descriptions-item :label="t('configDebug.configDir')">{{ data.paths.config_dir }}</n-descriptions-item>
        <n-descriptions-item :label="t('configDebug.dataDir')">{{ data.paths.data_dir }}</n-descriptions-item>
        <n-descriptions-item :label="t('configDebug.cacheDir')">{{ data.paths.cache_dir }}</n-descriptions-item>
        <n-descriptions-item :label="t('configDebug.stateDir')">{{ data.paths.state_dir }}</n-descriptions-item>
        <n-descriptions-item :label="t('configDebug.encryptionKey')">{{ data.paths.encryption_key }}</n-descriptions-item>
        <n-descriptions-item :label="t('configDebug.dbManagedSentinel')">
          <n-text :type="data.paths.sentinel_exists ? 'warning' : 'default'">
            {{ data.paths.sentinel_file }}
          </n-text>
          <n-tag v-if="data.paths.sentinel_exists" size="small" type="warning" :bordered="false" style="margin-left: 8px">
            {{ t('configDebug.active') }}
          </n-tag>
        </n-descriptions-item>
        <n-descriptions-item :label="t('configDebug.encryption')">
          <n-tag :type="data.encryption_enabled ? 'success' : 'default'" size="small">
            {{ data.encryption_enabled ? t('common.enabled') : t('common.disabled') }}
          </n-tag>
        </n-descriptions-item>
      </n-descriptions>
    </n-card>

    <n-card v-if="data && data.env_vars && data.env_vars.length > 0" :title="t('configDebug.envVars')">
      <n-data-table
        :columns="envColumns"
        :data="data.env_vars"
        size="small"
        :bordered="true"
        :single-line="false"
        :max-height="200"
      />
    </n-card>

    <n-card :title="t('configDebug.configValues')">
      <template #header-extra>
        <n-space :size="8">
          <n-switch v-model:value="showOnlyDiffs" size="small">
            <template #checked>{{ t('configDebug.diffsOnly') }}</template>
            <template #unchecked>{{ t('configDebug.all') }}</template>
          </n-switch>
          <n-button size="small" :loading="loading" @click="load">{{ t('common.refresh') }}</n-button>
        </n-space>
      </template>

      <n-spin v-if="loading" />
      <n-data-table
        v-else-if="data"
        :columns="columns"
        :data="filteredRows"
        size="small"
        :bordered="true"
        :single-line="false"
        :max-height="600"
        :row-class-name="rowClassName"
      />
    </n-card>
  </n-space>
</template>

<script setup lang="ts">
import { ref, computed, onMounted, h } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  NH2, NSpace, NCard, NAlert, NDescriptions, NDescriptionsItem,
  NDataTable, NTag, NText, NSpin, NSwitch, NButton,
} from 'naive-ui'
import { api } from '../api'
import type { ConfigDebugResponse, ConfigDebugRow } from '../api'

const { t } = useI18n()

const data = ref<ConfigDebugResponse | null>(null)
const loading = ref(false)
const showOnlyDiffs = ref(false)

async function load() {
  loading.value = true
  try {
    data.value = await api.getConfigDebug()
  } finally {
    loading.value = false
  }
}

onMounted(load)

const filteredRows = computed(() => {
  if (!data.value) return []
  if (!showOnlyDiffs.value) return data.value.rows
  return data.value.rows.filter(r => r.has_base || r.has_db)
})

function rowClassName(row: ConfigDebugRow) {
  if (row.has_db) return 'row-db'
  if (row.has_base) return 'row-config'
  return ''
}

const sourceTag = (source: string) => {
  const typeMap: Record<string, 'success' | 'info' | 'default'> = {
    database: 'success',
    config: 'info',
    default: 'default',
  }
  return h(NTag, { size: 'small', type: typeMap[source] || 'default', bordered: false }, () => source)
}

const cellValue = (val: string, has: boolean) => {
  if (!has) return h(NText, { depth: 3 }, () => '-')
  if (!val) return h(NText, { depth: 3 }, () => t('configDebug.empty'))
  return h(NText, { code: true }, () => val)
}

const columns = [
  {
    title: t('configDebug.section'),
    key: 'section',
    width: 90,
    sorter: (a: ConfigDebugRow, b: ConfigDebugRow) => a.section.localeCompare(b.section),
  },
  {
    title: t('configDebug.key'),
    key: 'key',
    width: 220,
    sorter: (a: ConfigDebugRow, b: ConfigDebugRow) => a.key.localeCompare(b.key),
    render: (row: ConfigDebugRow) => h('span', {}, [
      h(NText, { strong: true, style: 'font-size: 12px' }, () => row.key),
      row.secret ? h(NTag, { size: 'tiny', type: 'warning', bordered: false, style: 'margin-left: 4px' }, () => t('configDebug.secret')) : null,
    ]),
  },
  {
    title: t('configDebug.configToml'),
    key: 'base',
    width: 180,
    render: (row: ConfigDebugRow) => cellValue(row.base, row.has_base),
  },
  {
    title: t('configDebug.dbColumn'),
    key: 'db',
    width: 180,
    render: (row: ConfigDebugRow) => cellValue(row.db, row.has_db),
  },
  {
    title: t('configDebug.effective'),
    key: 'effective',
    width: 180,
    render: (row: ConfigDebugRow) => {
      if (!row.effective) return h(NText, { depth: 3 }, () => '-')
      return h(NText, { code: true, strong: true }, () => row.effective)
    },
  },
  {
    title: t('channels.source'),
    key: 'source',
    width: 90,
    render: (row: ConfigDebugRow) => sourceTag(row.source),
    sorter: (a: ConfigDebugRow, b: ConfigDebugRow) => a.source.localeCompare(b.source),
  },
]

const envColumns = [
  { title: t('configDebug.variable'), key: 'name', width: 300 },
  { title: t('configDebug.value'), key: 'value' },
]
</script>

<style scoped>
:deep(.row-db) {
  background-color: rgba(24, 160, 88, 0.06);
}
:deep(.row-config) {
  background-color: rgba(32, 128, 240, 0.06);
}
</style>
