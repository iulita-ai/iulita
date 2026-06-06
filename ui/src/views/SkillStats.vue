<template>
  <n-spin :show="loading">
    <n-space vertical :size="24">
      <n-page-header :title="t('skillStats.title')">
        <template #extra>
          <n-space>
            <n-date-picker
              v-model:value="dateRange"
              type="daterange"
              :default-value="defaultDateRange"
              clearable
              @update:value="fetchData"
            />
            <n-select
              v-model:value="selectedOrigin"
              :options="originOptions"
              :placeholder="t('skillStats.allOrigins')"
              clearable
              style="width: 200px"
              @update:value="fetchData"
            />
          </n-space>
        </template>
      </n-page-header>

      <!-- KPI cards -->
      <n-grid :cols="4" :x-gap="16" :y-gap="16" responsive="screen" :item-responsive="true">
        <n-grid-item span="4 m:1">
          <n-card>
            <n-statistic :label="t('skillStats.skillCount')" :value="summary?.skill_count ?? 0" />
          </n-card>
        </n-grid-item>
        <n-grid-item span="4 m:1">
          <n-card>
            <n-statistic :label="t('skillStats.totalCalls')" :value="summary?.total_calls ?? 0" />
          </n-card>
        </n-grid-item>
        <n-grid-item span="4 m:1">
          <n-card>
            <n-statistic :label="t('skillStats.successCalls')" :value="summary?.success_calls ?? 0" />
          </n-card>
        </n-grid-item>
        <n-grid-item span="4 m:1">
          <n-card>
            <n-statistic :label="t('skillStats.failureCalls')" :value="summary?.failure_calls ?? 0" />
          </n-card>
        </n-grid-item>
      </n-grid>

      <!-- By Skill -->
      <n-card :title="t('skillStats.bySkill')">
        <n-empty v-if="rows.length === 0" :description="t('skillStats.noData')" />
        <n-data-table
          v-else
          :columns="columns"
          :data="rows"
          :bordered="true"
          :pagination="{ pageSize: 25 }"
          :row-key="(row: SkillStatRow) => row.skill_name"
          size="small"
        />
      </n-card>
    </n-space>
  </n-spin>
</template>

<script setup lang="ts">
import { ref, computed, h, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { useMessage, NTag } from 'naive-ui'
import {
  NSpace, NGrid, NGridItem, NCard, NStatistic,
  NDataTable, NPageHeader, NSpin, NDatePicker, NSelect, NEmpty,
} from 'naive-ui'
import type { DataTableColumns, SelectOption } from 'naive-ui'
import { api } from '../api'
import type { SkillStatRow, SkillStatsResponse } from '../api'

const { t } = useI18n()
const message = useMessage()
const loading = ref(true)

const rows = ref<SkillStatRow[]>([])
const summary = ref<SkillStatsResponse['summary'] | null>(null)
const selectedOrigin = ref<string | null>(null)

const now = Date.now()
const thirtyDaysAgo = now - 30 * 24 * 60 * 60 * 1000
const defaultDateRange: [number, number] = [thirtyDaysAgo, now]
const dateRange = ref<[number, number] | null>(defaultDateRange)

const originOptions = computed<SelectOption[]>(() => [
  { label: t('skillStats.originMain'), value: 'main' },
  { label: t('skillStats.originSubagent'), value: 'subagent' },
])

function formatDate(ts: number): string {
  return new Date(ts).toISOString().split('T')[0]
}

function formatDuration(ms: number): string {
  if (ms < 1000) return `${Math.round(ms)} ms`
  return `${(ms / 1000).toFixed(2)} s`
}

function formatLastUsed(iso: string): string {
  if (!iso) return '-'
  const d = new Date(iso)
  if (isNaN(d.getTime())) return '-'
  return d.toLocaleString()
}

function successRate(row: SkillStatRow): number {
  if (row.total_calls === 0) return 0
  return Math.round((row.success_calls / row.total_calls) * 100)
}

const columns = computed<DataTableColumns<SkillStatRow>>(() => [
  { title: t('skillStats.skill'), key: 'skill_name', sorter: 'default', fixed: 'left' },
  { title: t('skillStats.calls'), key: 'total_calls', sorter: 'default', width: 90 },
  { title: t('skillStats.success'), key: 'success_calls', sorter: 'default', width: 100 },
  { title: t('skillStats.failures'), key: 'failure_calls', sorter: 'default', width: 100 },
  {
    title: t('skillStats.successRate'),
    key: 'success_rate',
    width: 120,
    sorter: (a, b) => successRate(a) - successRate(b),
    render: (row) => {
      const rate = successRate(row)
      const type = rate >= 90 ? 'success' : rate >= 60 ? 'warning' : 'error'
      return h(NTag, { type, size: 'small', bordered: false }, { default: () => `${rate}%` })
    },
  },
  {
    title: t('skillStats.avgDuration'),
    key: 'avg_duration_ms',
    width: 120,
    sorter: 'default',
    render: (row) => formatDuration(row.avg_duration_ms),
  },
  {
    title: t('skillStats.maxDuration'),
    key: 'max_duration_ms',
    width: 120,
    sorter: 'default',
    render: (row) => formatDuration(row.max_duration_ms),
  },
  {
    title: t('skillStats.lastUsed'),
    key: 'last_used',
    sorter: 'default',
    render: (row) => formatLastUsed(row.last_used),
  },
])

async function fetchData() {
  loading.value = true
  try {
    const params: { from?: string; to?: string; origin?: string } = {}
    if (dateRange.value) {
      params.from = formatDate(dateRange.value[0])
      params.to = formatDate(dateRange.value[1])
    }
    if (selectedOrigin.value) {
      params.origin = selectedOrigin.value
    }
    const resp = await api.getSkillStats(params)
    rows.value = resp.rows || []
    summary.value = resp.summary
  } catch (e: any) {
    message.error(e.message || t('skillStats.loadFailed'))
  } finally {
    loading.value = false
  }
}

onMounted(fetchData)
</script>
