<template>
  <n-spin :show="loading">
    <n-space vertical :size="24">
      <n-page-header :title="t('usage.title')">
        <template #extra>
          <n-space>
            <n-date-picker
              v-model:value="dateRange"
              type="daterange"
              :default-value="defaultDateRange"
              clearable
              @update:value="onDateChange"
            />
            <n-select
              v-model:value="selectedModel"
              :options="modelOptions"
              :placeholder="t('usage.allModels')"
              clearable
              style="width: 220px"
              @update:value="fetchData"
            />
          </n-space>
        </template>
      </n-page-header>

      <!-- KPI cards -->
      <n-grid :cols="5" :x-gap="16" :y-gap="16" responsive="screen" :item-responsive="true">
        <n-grid-item span="5 m:1">
          <n-card>
            <n-statistic :label="t('usage.totalInputTokens')" :value="summary?.total_input_tokens ?? 0" />
          </n-card>
        </n-grid-item>
        <n-grid-item span="5 m:1">
          <n-card>
            <n-statistic :label="t('usage.totalOutputTokens')" :value="summary?.total_output_tokens ?? 0" />
          </n-card>
        </n-grid-item>
        <n-grid-item span="5 m:1">
          <n-card>
            <n-statistic :label="t('usage.totalCacheRead')" :value="summary?.total_cache_read_tokens ?? 0" />
          </n-card>
        </n-grid-item>
        <n-grid-item span="5 m:1">
          <n-card>
            <n-statistic :label="t('usage.totalRequests')" :value="summary?.total_requests ?? 0" />
          </n-card>
        </n-grid-item>
        <n-grid-item span="5 m:1">
          <n-card>
            <n-statistic :label="t('usage.totalCost')">
              <template #default>
                ${{ (summary?.total_cost_usd ?? 0).toFixed(4) }}
              </template>
            </n-statistic>
          </n-card>
        </n-grid-item>
      </n-grid>

      <!-- By Model -->
      <n-card :title="t('usage.byModel')" v-if="modelRows.length > 0">
        <n-data-table
          :columns="modelColumns"
          :data="modelRows"
          :bordered="true"
          :pagination="false"
          :row-key="(row: any) => `${row.model}-${row.provider}`"
          size="small"
        />
      </n-card>

      <!-- Daily Breakdown -->
      <n-card :title="t('usage.dailyBreakdown')">
        <n-empty v-if="dailyRows.length === 0" :description="t('usage.noData')" />
        <n-data-table
          v-else
          :columns="dailyColumns"
          :data="dailyRows"
          :bordered="true"
          :pagination="{ pageSize: 31 }"
          :row-key="(row: any) => row.date"
          size="small"
        />
      </n-card>
    </n-space>
  </n-spin>
</template>

<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { useMessage } from 'naive-ui'
import {
  NSpace, NGrid, NGridItem, NCard, NStatistic,
  NDataTable, NPageHeader, NSpin, NDatePicker,
  NSelect, NEmpty,
} from 'naive-ui'
import type { DataTableColumns, SelectOption } from 'naive-ui'
import { api } from '../api'
import type { UsageSummaryResponse, UsageRow, ModelUsageRow } from '../api'

const { t } = useI18n()
const message = useMessage()
const loading = ref(true)

const summary = ref<UsageSummaryResponse | null>(null)
const dailyRows = ref<UsageRow[]>([])
const modelRows = ref<ModelUsageRow[]>([])
const selectedModel = ref<string | null>(null)

// Default: last 30 days
const now = Date.now()
const thirtyDaysAgo = now - 30 * 24 * 60 * 60 * 1000
const defaultDateRange: [number, number] = [thirtyDaysAgo, now]
const dateRange = ref<[number, number] | null>(defaultDateRange)

function formatDate(ts: number): string {
  const d = new Date(ts)
  return d.toISOString().split('T')[0]
}

const modelOptions = computed<SelectOption[]>(() => {
  return modelRows.value.map(m => ({
    label: m.model || '(unknown)',
    value: m.model,
  }))
})

const dailyColumns = computed<DataTableColumns<UsageRow>>(() => [
  { title: t('usage.date'), key: 'date', sorter: 'default', width: 120 },
  { title: t('usage.inputTokens'), key: 'input_tokens', sorter: 'default', render: (row) => row.input_tokens.toLocaleString() },
  { title: t('usage.outputTokens'), key: 'output_tokens', sorter: 'default', render: (row) => row.output_tokens.toLocaleString() },
  { title: t('usage.cacheRead'), key: 'cache_read_tokens', sorter: 'default', render: (row) => row.cache_read_tokens.toLocaleString() },
  { title: t('usage.requests'), key: 'requests', sorter: 'default' },
  { title: t('usage.costUsd'), key: 'cost_usd', sorter: 'default', render: (row) => `$${row.cost_usd.toFixed(4)}` },
])

const modelColumns = computed<DataTableColumns<ModelUsageRow>>(() => [
  { title: t('usage.model'), key: 'model', render: (row) => row.model || '(unknown)' },
  { title: t('usage.provider'), key: 'provider', render: (row) => row.provider || '-' },
  { title: t('usage.inputTokens'), key: 'input_tokens', sorter: 'default', render: (row) => row.input_tokens.toLocaleString() },
  { title: t('usage.outputTokens'), key: 'output_tokens', sorter: 'default', render: (row) => row.output_tokens.toLocaleString() },
  { title: t('usage.cacheRead'), key: 'cache_read_tokens', sorter: 'default', render: (row) => row.cache_read_tokens.toLocaleString() },
  { title: t('usage.requests'), key: 'requests', sorter: 'default' },
  { title: t('usage.costUsd'), key: 'cost_usd', sorter: 'default', render: (row) => `$${row.cost_usd.toFixed(4)}` },
])

function onDateChange() {
  fetchData()
}

async function fetchData() {
  loading.value = true
  try {
    const params: { from?: string; to?: string; model?: string } = {}
    if (dateRange.value) {
      params.from = formatDate(dateRange.value[0])
      params.to = formatDate(dateRange.value[1])
    }
    if (selectedModel.value) {
      params.model = selectedModel.value
    }

    const [dailyResp, modelResp] = await Promise.all([
      api.getUsageByDay(params),
      api.getUsageByModel({ from: params.from, to: params.to }),
    ])

    summary.value = dailyResp.summary
    dailyRows.value = dailyResp.rows || []
    modelRows.value = modelResp.rows || []
  } catch (e: any) {
    message.error(e.message || t('usage.loadFailed'))
  } finally {
    loading.value = false
  }
}

onMounted(fetchData)
</script>
