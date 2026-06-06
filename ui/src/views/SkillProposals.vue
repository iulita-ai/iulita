<template>
  <n-spin :show="loading">
    <n-space vertical :size="24">
      <n-page-header :title="t('skillProposals.title')">
        <template #subtitle>
          <n-text depth="3">{{ t('skillProposals.subtitle') }}</n-text>
        </template>
        <template #extra>
          <n-select
            v-model:value="statusFilter"
            :options="statusOptions"
            :placeholder="t('skillProposals.allStatuses')"
            clearable
            style="width: 200px"
            @update:value="fetchData"
          />
        </template>
      </n-page-header>

      <n-alert type="info" :show-icon="true">
        {{ t('skillProposals.safetyNote') }}
      </n-alert>

      <n-empty v-if="rows.length === 0" :description="t('skillProposals.noData')" />

      <n-card v-for="p in rows" :key="p.id" :title="p.name" size="small">
        <template #header-extra>
          <n-space align="center">
            <n-tag :type="statusType(p.status)" size="small" :bordered="false">{{ p.status }}</n-tag>
            <n-popconfirm v-if="p.status === 'pending'" @positive-click="install(p)">
              <template #trigger>
                <n-button size="tiny" type="primary" :loading="busyId === p.id">
                  {{ t('skillProposals.install') }}
                </n-button>
              </template>
              {{ t('skillProposals.installConfirm') }}
            </n-popconfirm>
            <n-button
              v-if="p.status === 'pending' || p.status === 'rejected'"
              size="tiny"
              type="error"
              tertiary
              :disabled="busyId === p.id"
              @click="discard(p)"
            >
              {{ t('skillProposals.discard') }}
            </n-button>
          </n-space>
        </template>

        <n-space vertical :size="8">
          <n-text depth="3">{{ p.description }}</n-text>
          <div>
            <n-text strong>{{ t('skillProposals.slug') }}:</n-text>
            <n-text code>{{ p.slug }}</n-text>
          </div>
          <div v-if="p.triggers">
            <n-text strong>{{ t('skillProposals.triggers') }}:</n-text>
            <n-tag v-for="tr in p.triggers.split(',')" :key="tr" size="small" style="margin-left: 4px">{{ tr }}</n-tag>
          </div>
          <n-collapse>
            <n-collapse-item :title="t('skillProposals.body')" name="body">
              <n-code :code="p.body" word-wrap />
            </n-collapse-item>
          </n-collapse>
          <n-alert v-if="parseWarnings(p.warnings).length > 0" type="warning" :show-icon="true" size="small">
            <ul style="margin: 0; padding-left: 18px">
              <li v-for="(w, i) in parseWarnings(p.warnings)" :key="i">{{ w }}</li>
            </ul>
          </n-alert>
          <n-text depth="3" style="font-size: 12px">
            {{ t('skillProposals.from') }} {{ p.chat_id }}<template v-if="p.user_id"> · {{ t('skillProposals.user') }} {{ p.user_id }}</template> · {{ formatDate(p.created_at) }}
          </n-text>
        </n-space>
      </n-card>
    </n-space>
  </n-spin>
</template>

<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  useMessage, NSpace, NCard, NTag, NButton, NText, NAlert, NEmpty,
  NPageHeader, NSpin, NSelect, NCollapse, NCollapseItem, NCode, NPopconfirm,
} from 'naive-ui'
import type { SelectOption } from 'naive-ui'
import { api } from '../api'
import type { SkillProposal } from '../api'

const { t } = useI18n()
const message = useMessage()
const loading = ref(true)
const rows = ref<SkillProposal[]>([])
const statusFilter = ref<string | null>(null)
const busyId = ref<number | null>(null)

const statusOptions = computed<SelectOption[]>(() => [
  { label: t('skillProposals.statusPending'), value: 'pending' },
  { label: t('skillProposals.statusRejected'), value: 'rejected' },
  { label: t('skillProposals.statusDiscarded'), value: 'discarded' },
])

function statusType(status: string): 'success' | 'warning' | 'error' | 'default' {
  switch (status) {
    case 'pending': return 'success'
    case 'rejected': return 'error'
    case 'discarded': return 'default'
    default: return 'default'
  }
}

function parseWarnings(raw: string): string[] {
  if (!raw) return []
  try {
    const arr = JSON.parse(raw)
    return Array.isArray(arr) ? arr : []
  } catch {
    return []
  }
}

function formatDate(iso: string): string {
  const d = new Date(iso)
  return isNaN(d.getTime()) ? iso : d.toLocaleString()
}

async function fetchData() {
  loading.value = true
  try {
    const resp = await api.listSkillProposals(statusFilter.value || undefined)
    rows.value = resp.rows || []
  } catch (e: any) {
    message.error(e.message || t('skillProposals.loadFailed'))
  } finally {
    loading.value = false
  }
}

async function install(p: SkillProposal) {
  busyId.value = p.id
  try {
    const res = await api.installSkillProposal(p.id)
    if (res.warnings && res.warnings.length > 0) {
      message.warning(t('skillProposals.installedWithWarnings', { count: res.warnings.length }))
    } else {
      message.success(t('skillProposals.installed'))
    }
    await fetchData()
  } catch (e: any) {
    message.error(e.message || t('skillProposals.installFailed'))
  } finally {
    busyId.value = null
  }
}

async function discard(p: SkillProposal) {
  try {
    await api.discardSkillProposal(p.id)
    message.success(t('skillProposals.discarded'))
    await fetchData()
  } catch (e: any) {
    message.error(e.message || t('skillProposals.discardFailed'))
  }
}

onMounted(fetchData)
</script>
