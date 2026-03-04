<template>
  <n-space vertical :size="16">
    <n-h2>{{ $t('reminders.title') }}</n-h2>

    <n-space :size="12">
      <n-select
        v-model:value="selectedChat"
        :options="chatOptions"
        :placeholder="t('reminders.allChats')"
        clearable
        style="width: 250px"
      />
      <n-select
        v-model:value="statusFilter"
        :options="statusOptions"
        :placeholder="t('reminders.allStatuses')"
        clearable
        style="width: 180px"
      />
    </n-space>

    <n-data-table
      :columns="columns"
      :data="filteredReminders"
      :loading="loading"
      :bordered="true"
      :pagination="{ pageSize: 20 }"
      :row-key="(row: Reminder) => row.ID"
    />
  </n-space>
</template>

<script setup lang="ts">
import { ref, computed, onMounted, watch, h } from 'vue'
import { useI18n } from 'vue-i18n'
import { NH2, NSpace, NSelect, NDataTable, NTag } from 'naive-ui'
import type { DataTableColumns } from 'naive-ui'
import { api } from '../api'
import type { Reminder, ChatInfo } from '../api'

const { t } = useI18n()

const loading = ref(true)
const selectedChat = ref<string | null>(null)
const statusFilter = ref<string | null>(null)
const chats = ref<ChatInfo[]>([])
const reminders = ref<Reminder[]>([])

const chatOptions = computed(() =>
  chats.value.map(c => ({
    label: `${c.chat_id} (${c.messages} msgs)`,
    value: c.chat_id,
  }))
)

const statusOptions = computed(() => [
  { label: t('reminders.pending'), value: 'pending' },
  { label: t('reminders.fired'), value: 'fired' },
  { label: t('reminders.cancelled'), value: 'cancelled' },
])

const filteredReminders = computed(() => {
  if (!statusFilter.value) return reminders.value
  return reminders.value.filter(r => r.Status === statusFilter.value)
})

function statusType(status: string): 'success' | 'default' | 'error' {
  switch (status) {
    case 'pending': return 'success'
    case 'fired': return 'default'
    case 'cancelled': return 'error'
    default: return 'default'
  }
}

const columns = computed<DataTableColumns<Reminder>>(() => [
  { title: t('reminders.id'), key: 'ID', width: 60, sorter: 'default' },
  { title: t('reminders.titleField'), key: 'Title', ellipsis: { tooltip: true } },
  {
    title: t('reminders.dueAt'),
    key: 'DueAt',
    width: 170,
    sorter: 'default',
    render(row) {
      return new Date(row.DueAt).toLocaleString()
    },
  },
  {
    title: t('reminders.status'),
    key: 'Status',
    width: 110,
    render(row) {
      return h(NTag, { size: 'small', type: statusType(row.Status), bordered: false }, { default: () => row.Status })
    },
  },
  { title: t('reminders.chat'), key: 'ChatID', width: 120 },
  {
    title: t('reminders.created'),
    key: 'CreatedAt',
    width: 170,
    sorter: 'default',
    render(row) {
      return new Date(row.CreatedAt).toLocaleString()
    },
  },
])

async function loadReminders() {
  reminders.value = await api.getReminders(selectedChat.value ?? undefined) ?? []
}

onMounted(async () => {
  try {
    chats.value = await api.getChats()
    await loadReminders()
  } finally {
    loading.value = false
  }
})

watch(selectedChat, () => loadReminders())
</script>
