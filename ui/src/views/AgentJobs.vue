<template>
  <n-space vertical :size="16">
    <n-page-header :title="t('agentJobs.title')" :subtitle="t('agentJobs.subtitle')">
      <template #extra>
        <n-button type="primary" @click="openCreate">{{ t('agentJobs.addJob') }}</n-button>
      </template>
    </n-page-header>

    <n-data-table
      :columns="columns"
      :data="jobs"
      :loading="loading"
      :row-key="(r: AgentJob) => r.id"
    />

    <!-- Create/Edit modal -->
    <n-modal v-model:show="showModal" preset="dialog" :title="editingJob ? t('agentJobs.editJob') : t('agentJobs.createJob')" style="width: 600px">
      <n-form label-placement="left" label-width="140">
        <n-form-item :label="t('agentJobs.name')" required>
          <n-input v-model:value="form.name" placeholder="daily-summary" />
        </n-form-item>
        <n-form-item :label="t('agentJobs.prompt')" required>
          <n-input v-model:value="form.prompt" type="textarea" :rows="4" placeholder="Generate a daily summary of..." />
        </n-form-item>
        <n-form-item :label="t('agentJobs.model')">
          <n-input v-model:value="form.model" placeholder="Leave empty for default" />
          <template #feedback>Override LLM model (e.g. "ollama"). Empty = default provider.</template>
        </n-form-item>
        <n-form-item :label="t('agentJobs.schedule')">
          <n-radio-group v-model:value="scheduleMode" style="margin-bottom: 8px">
            <n-radio value="interval">{{ t('agentJobs.interval') }}</n-radio>
            <n-radio value="cron">{{ t('agentJobs.cronExpression') }}</n-radio>
          </n-radio-group>
        </n-form-item>
        <n-form-item v-if="scheduleMode === 'interval'" :label="t('agentJobs.interval')">
          <n-input v-model:value="form.interval" placeholder="24h" style="width: 200px" />
          <template #feedback>Go duration (e.g. "6h", "30m", "24h")</template>
        </n-form-item>
        <n-form-item v-else :label="t('agentJobs.cronExpression')">
          <n-input v-model:value="form.cron_expr" placeholder="0 9 * * *" style="width: 200px" />
          <template #feedback>Standard cron (min hour day month weekday)</template>
        </n-form-item>
        <n-form-item :label="t('agentJobs.deliveryChatId')">
          <n-input v-model:value="form.delivery_chat_id" placeholder="Telegram chat ID for results" />
          <template #feedback>Send results to this chat when done. Empty = no delivery.</template>
        </n-form-item>
        <n-form-item :label="t('agentJobs.enabled')">
          <n-switch v-model:value="form.enabled" />
        </n-form-item>
      </n-form>
      <template #action>
        <n-button @click="showModal = false">{{ t('common.cancel') }}</n-button>
        <n-button type="primary" :loading="saving" @click="handleSave">
          {{ editingJob ? t('common.save') : t('common.create') }}
        </n-button>
      </template>
    </n-modal>
  </n-space>
</template>

<script setup lang="ts">
import { ref, h, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  NSpace, NPageHeader, NDataTable, NButton, NSwitch, NTag, NModal,
  NForm, NFormItem, NInput, NRadioGroup, NRadio, useMessage,
} from 'naive-ui'
import type { DataTableColumn } from 'naive-ui'
import { api } from '../api'
import type { AgentJob } from '../api'

const { t } = useI18n()
const message = useMessage()
const jobs = ref<AgentJob[]>([])
const loading = ref(false)
const showModal = ref(false)
const saving = ref(false)
const editingJob = ref<AgentJob | null>(null)
const scheduleMode = ref<'interval' | 'cron'>('interval')

const form = ref({
  name: '',
  prompt: '',
  model: '',
  cron_expr: '',
  interval: '24h',
  delivery_chat_id: '',
  enabled: true,
})

function openCreate() {
  editingJob.value = null
  scheduleMode.value = 'interval'
  form.value = { name: '', prompt: '', model: '', cron_expr: '', interval: '24h', delivery_chat_id: '', enabled: true }
  showModal.value = true
}

function openEdit(job: AgentJob) {
  editingJob.value = job
  scheduleMode.value = job.cron_expr ? 'cron' : 'interval'
  form.value = {
    name: job.name,
    prompt: job.prompt,
    model: job.model,
    cron_expr: job.cron_expr,
    interval: job.interval,
    delivery_chat_id: job.delivery_chat_id,
    enabled: job.enabled,
  }
  showModal.value = true
}

async function handleSave() {
  if (!form.value.name || !form.value.prompt) {
    message.warning(t('agentJobs.namePromptRequired'))
    return
  }
  saving.value = true
  try {
    const data = {
      name: form.value.name,
      prompt: form.value.prompt,
      model: form.value.model || undefined,
      cron_expr: scheduleMode.value === 'cron' ? form.value.cron_expr : '',
      interval: scheduleMode.value === 'interval' ? form.value.interval : '',
      delivery_chat_id: form.value.delivery_chat_id || undefined,
      enabled: form.value.enabled,
    }
    if (editingJob.value) {
      await api.updateAgentJob(editingJob.value.id, data)
      message.success(t('agentJobs.jobUpdated'))
    } else {
      await api.createAgentJob(data as Parameters<typeof api.createAgentJob>[0])
      message.success(t('agentJobs.jobCreated'))
    }
    showModal.value = false
    await loadJobs()
  } catch (e: any) {
    message.error(e.message)
  } finally {
    saving.value = false
  }
}

async function toggleEnabled(job: AgentJob) {
  try {
    await api.updateAgentJob(job.id, { enabled: !job.enabled })
    job.enabled = !job.enabled
  } catch {
    message.error(t('agentJobs.toggleFailed'))
  }
}

async function handleDelete(job: AgentJob) {
  if (!confirm(t('agentJobs.deleteConfirm', { name: job.name }))) return
  try {
    await api.deleteAgentJob(job.id)
    message.success(t('agentJobs.jobDeleted'))
    await loadJobs()
  } catch (e: any) {
    message.error(e.message)
  }
}

const columns: DataTableColumn<AgentJob>[] = [
  { title: t('agentJobs.name'), key: 'name', width: 180 },
  { title: t('agentJobs.prompt'), key: 'prompt', ellipsis: { tooltip: true } },
  {
    title: t('agentJobs.schedule'), key: 'schedule', width: 140,
    render: (row) => row.cron_expr || row.interval || '-',
  },
  {
    title: t('agentJobs.model'), key: 'model', width: 100,
    render: (row) => row.model || t('agentJobs.default'),
  },
  {
    title: t('agentJobs.lastRun'), key: 'last_run', width: 160,
    render: (row) => row.last_run ? new Date(row.last_run).toLocaleString() : t('agentJobs.never'),
  },
  {
    title: t('agentJobs.nextRun'), key: 'next_run', width: 160,
    render: (row) => row.next_run ? new Date(row.next_run).toLocaleString() : '-',
  },
  {
    title: t('agentJobs.enabled'), key: 'enabled', width: 90,
    render: (row) => h(NSwitch, {
      value: row.enabled,
      onUpdateValue: () => toggleEnabled(row),
    }),
  },
  {
    title: '', key: 'actions', width: 120,
    render: (row) => h(NSpace, { size: 'small' }, () => [
      h(NButton, { size: 'small', quaternary: true, onClick: () => openEdit(row) }, () => t('common.edit')),
      h(NButton, { size: 'small', quaternary: true, type: 'error', onClick: () => handleDelete(row) }, () => t('common.delete')),
    ]),
  },
]

async function loadJobs() {
  loading.value = true
  try {
    jobs.value = await api.listAgentJobs() ?? []
  } finally {
    loading.value = false
  }
}

onMounted(loadJobs)
</script>
