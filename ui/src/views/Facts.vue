<template>
  <n-space vertical :size="16">
    <n-h2>{{ $t('facts.title') }}</n-h2>

    <n-space :size="12">
      <n-select
        v-model:value="selectedUser"
        :options="userOptions"
        :placeholder="t('facts.allUsers')"
        clearable
        style="width: 250px"
      />
      <n-input
        v-model:value="searchQuery"
        :placeholder="t('facts.searchPlaceholder')"
        clearable
        style="width: 300px"
        @update:value="debouncedSearch"
      />
    </n-space>

    <n-data-table
      :columns="columns"
      :data="facts"
      :loading="loading"
      :bordered="true"
      :pagination="{ pageSize: 20 }"
      :row-key="(row: Fact) => row.ID"
      :row-props="rowProps"
    />

    <!-- Fact detail drawer -->
    <n-drawer v-model:show="drawerVisible" :width="500" placement="right">
      <n-drawer-content :title="editing ? t('facts.editFact') : t('facts.factDetails')" closable>
        <template v-if="selectedFact">
          <n-descriptions bordered :column="1" label-placement="left">
            <n-descriptions-item :label="t('facts.id')">{{ selectedFact.ID }}</n-descriptions-item>
            <n-descriptions-item :label="t('facts.content')">
              <template v-if="editing">
                <n-input
                  v-model:value="editContent"
                  type="textarea"
                  :autosize="{ minRows: 3, maxRows: 15 }"
                />
              </template>
              <template v-else>
                <span style="white-space: pre-wrap; word-break: break-word">{{ selectedFact.Content }}</span>
              </template>
            </n-descriptions-item>
            <n-descriptions-item :label="t('facts.source')">
              <n-tag size="small" :bordered="false">{{ selectedFact.SourceType }}</n-tag>
            </n-descriptions-item>
            <n-descriptions-item :label="t('facts.userId')">{{ selectedFact.UserID || '-' }}</n-descriptions-item>
            <n-descriptions-item :label="t('facts.chatId')">{{ selectedFact.ChatID || '-' }}</n-descriptions-item>
            <n-descriptions-item :label="t('facts.accessCount')">{{ selectedFact.AccessCount }}</n-descriptions-item>
            <n-descriptions-item :label="t('facts.created')">{{ formatDateTime(selectedFact.CreatedAt) }}</n-descriptions-item>
            <n-descriptions-item :label="t('facts.lastAccessed')">{{ formatDateTime(selectedFact.LastAccessedAt) }}</n-descriptions-item>
          </n-descriptions>
        </template>

        <template #footer>
          <n-space justify="space-between" style="width: 100%">
            <n-popconfirm @positive-click="handleDelete">
              <template #trigger>
                <n-button type="error" ghost>{{ $t('common.delete') }}</n-button>
              </template>
              {{ $t('facts.deleteConfirm') }}
            </n-popconfirm>
            <n-space :size="8">
              <template v-if="editing">
                <n-button @click="cancelEdit">{{ $t('common.cancel') }}</n-button>
                <n-button type="primary" :loading="saving" @click="handleSave">{{ $t('common.save') }}</n-button>
              </template>
              <template v-else>
                <n-button type="primary" @click="startEdit">{{ $t('common.edit') }}</n-button>
              </template>
            </n-space>
          </n-space>
        </template>
      </n-drawer-content>
    </n-drawer>
  </n-space>
</template>

<script setup lang="ts">
import { ref, computed, onMounted, watch, h } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  NH2, NSpace, NSelect, NInput, NDataTable, NTag,
  NDrawer, NDrawerContent, NDescriptions, NDescriptionsItem,
  NButton, NPopconfirm, useMessage,
} from 'naive-ui'
import type { DataTableColumns } from 'naive-ui'
import { api } from '../api'
import type { Fact, UserInfo } from '../api'

const { t } = useI18n()
const message = useMessage()

const loading = ref(true)
const selectedUser = ref<string | null>(null)
const searchQuery = ref('')
const users = ref<UserInfo[]>([])
const facts = ref<Fact[]>([])

// Drawer state
const drawerVisible = ref(false)
const selectedFact = ref<Fact | null>(null)
const editing = ref(false)
const editContent = ref('')
const saving = ref(false)

const userOptions = computed(() =>
  users.value.map(u => ({
    label: u.display_name || u.username,
    value: u.id,
  }))
)

function relativeTime(dateStr: string): string {
  const date = new Date(dateStr)
  const now = Date.now()
  const diffMs = now - date.getTime()
  if (diffMs < 0) return 'just now'

  const seconds = Math.floor(diffMs / 1000)
  if (seconds < 60) return `${seconds}s ago`

  const minutes = Math.floor(seconds / 60)
  if (minutes < 60) return `${minutes} min ago`

  const hours = Math.floor(minutes / 60)
  if (hours < 24) return `${hours}h ago`

  const days = Math.floor(hours / 24)
  if (days < 30) return `${days}d ago`

  const months = Math.floor(days / 30)
  if (months < 12) return `${months}mo ago`

  const years = Math.floor(months / 12)
  return `${years}y ago`
}

function formatDateTime(dateStr: string): string {
  return new Date(dateStr).toLocaleString()
}

const columns = computed<DataTableColumns<Fact>>(() => [
  { title: t('facts.id'), key: 'ID', width: 60, sorter: 'default' },
  { title: t('facts.content'), key: 'Content', ellipsis: { tooltip: true } },
  { title: t('facts.access'), key: 'AccessCount', width: 80, sorter: 'default' },
  {
    title: t('facts.created'),
    key: 'CreatedAt',
    width: 120,
    sorter: 'default',
    render(row) {
      return relativeTime(row.CreatedAt)
    },
  },
])

function rowProps(row: Fact) {
  return {
    style: 'cursor: pointer',
    onClick: () => openDrawer(row),
  }
}

function openDrawer(fact: Fact) {
  selectedFact.value = fact
  editing.value = false
  editContent.value = fact.Content
  drawerVisible.value = true
}

function startEdit() {
  editContent.value = selectedFact.value?.Content ?? ''
  editing.value = true
}

function cancelEdit() {
  editing.value = false
  editContent.value = selectedFact.value?.Content ?? ''
}

async function handleSave() {
  if (!selectedFact.value) return
  saving.value = true
  try {
    const updated = await api.updateFact(selectedFact.value.ID, editContent.value)
    // Update local data
    const idx = facts.value.findIndex(f => f.ID === selectedFact.value!.ID)
    if (idx >= 0) {
      facts.value[idx] = updated
    }
    selectedFact.value = updated
    editing.value = false
    message.success(t('facts.updated'))
  } catch (e: any) {
    message.error(e.message)
  } finally {
    saving.value = false
  }
}

async function handleDelete() {
  if (!selectedFact.value) return
  try {
    await api.deleteFact(selectedFact.value.ID)
    drawerVisible.value = false
    selectedFact.value = null
    message.success(t('facts.deleted'))
    await loadFacts()
  } catch (e: any) {
    message.error(e.message)
  }
}

let searchTimeout: ReturnType<typeof setTimeout> | null = null
function debouncedSearch() {
  if (searchTimeout) clearTimeout(searchTimeout)
  searchTimeout = setTimeout(() => loadFacts(), 300)
}

async function loadFacts() {
  loading.value = true
  try {
    facts.value = await api.getFacts({
      user_id: selectedUser.value ?? undefined,
      q: searchQuery.value || undefined,
    }) ?? []
  } finally {
    loading.value = false
  }
}

onMounted(async () => {
  try {
    users.value = await api.listUsers()
  } catch {
    // Non-admin users may not have access to list users
  }
  await loadFacts()
})

watch(selectedUser, () => loadFacts())
</script>
