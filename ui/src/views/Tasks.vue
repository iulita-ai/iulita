<template>
  <div>
    <n-page-header :title="t('tasks.title')">
      <template #extra>
        <n-space>
          <n-button size="small" quaternary @click="handleSync" :loading="syncing">
            <template #icon><n-icon :component="RefreshOutline" /></template>
            {{ t('tasks.sync') }}
          </n-button>
          <n-button type="primary" size="small" @click="showCreateModal = true">
            <template #icon><n-icon :component="AddOutline" /></template>
            {{ t('tasks.addTask') }}
          </n-button>
        </n-space>
      </template>
    </n-page-header>

    <n-tabs v-model:value="activeTab" type="line" style="margin-top: 16px;" @update:value="onTabChange">
      <n-tab-pane name="today" :tab="todayTabLabel">
        <task-list :items="todayItems" :loading="loadingToday" @complete="handleComplete" @delete="handleDelete" @select="showDetail" />
      </n-tab-pane>
      <n-tab-pane name="overdue" :tab="overdueTabLabel">
        <task-list :items="overdueItems" :loading="loadingOverdue" @complete="handleComplete" @delete="handleDelete" @select="showDetail" />
      </n-tab-pane>
      <n-tab-pane name="upcoming" :tab="t('tasks.upcoming')">
        <task-list :items="upcomingItems" :loading="loadingUpcoming" @complete="handleComplete" @delete="handleDelete" @select="showDetail" />
      </n-tab-pane>
      <n-tab-pane name="all" :tab="t('tasks.all')">
        <task-list :items="allItems" :loading="loadingAll" @complete="handleComplete" @delete="handleDelete" @select="showDetail" />
      </n-tab-pane>
    </n-tabs>

    <!-- Detail drawer -->
    <n-drawer v-model:show="drawerVisible" :width="400" placement="right">
      <n-drawer-content :title="selectedItem?.title || t('tasks.taskDetails')">
        <template v-if="selectedItem">
          <n-descriptions :column="1" label-placement="left" bordered>
            <n-descriptions-item :label="t('tasks.provider')">
              <n-tag :type="providerTagType(selectedItem.provider)" size="small">{{ providerLabel(selectedItem.provider) }}</n-tag>
            </n-descriptions-item>
            <n-descriptions-item :label="t('tasks.priority')">
              <n-tag :type="priorityTagType(selectedItem.priority)" size="small">{{ priorityLabel(selectedItem.priority) }}</n-tag>
            </n-descriptions-item>
            <n-descriptions-item v-if="selectedItem.due_date" :label="t('tasks.due')">
              {{ formatDate(selectedItem.due_date) }}
            </n-descriptions-item>
            <n-descriptions-item v-if="selectedItem.notes" :label="t('tasks.notes')">
              {{ selectedItem.notes }}
            </n-descriptions-item>
            <n-descriptions-item v-if="selectedItem.labels" :label="t('tasks.labels')">
              {{ selectedItem.labels }}
            </n-descriptions-item>
            <n-descriptions-item v-if="selectedItem.project_name" :label="t('tasks.project')">
              {{ selectedItem.project_name }}
            </n-descriptions-item>
            <n-descriptions-item :label="t('tasks.created')">
              {{ formatDateTime(selectedItem.created_at) }}
            </n-descriptions-item>
          </n-descriptions>
          <n-space style="margin-top: 16px;">
            <n-button v-if="!selectedItem.completed_at" type="success" @click="handleComplete(selectedItem)">{{ t('tasks.complete') }}</n-button>
            <n-button v-if="selectedItem.provider === 'builtin'" type="error" @click="handleDelete(selectedItem)">{{ t('common.delete') }}</n-button>
            <n-button v-if="selectedItem.url" tag="a" :href="selectedItem.url" target="_blank">{{ t('tasks.open', { provider: providerLabel(selectedItem.provider) }) }}</n-button>
          </n-space>
        </template>
      </n-drawer-content>
    </n-drawer>

    <!-- Create modal -->
    <n-modal v-model:show="showCreateModal" preset="card" :title="t('tasks.addTask')" style="max-width: 500px;">
      <n-form ref="createFormRef" :model="createForm">
        <n-form-item :label="t('tasks.titleField')" path="title">
          <n-input v-model:value="createForm.title" :placeholder="t('tasks.whatNeedsDone')" />
        </n-form-item>
        <n-form-item :label="t('tasks.notes')" path="notes">
          <n-input v-model:value="createForm.notes" type="textarea" :rows="2" :placeholder="t('tasks.optionalNotes')" />
        </n-form-item>
        <n-form-item :label="t('tasks.dueDate')" path="due_date">
          <n-date-picker v-model:formatted-value="createForm.due_date" type="date" value-format="yyyy-MM-dd" clearable style="width: 100%;" />
        </n-form-item>
        <n-form-item :label="t('tasks.priority')" path="priority">
          <n-select v-model:value="createForm.priority" :options="priorityOpts" />
        </n-form-item>
        <n-form-item :label="t('tasks.provider')" path="provider">
          <n-select v-model:value="createForm.provider" :options="providerOptions" />
        </n-form-item>
      </n-form>
      <template #action>
        <n-space justify="end">
          <n-button @click="showCreateModal = false">{{ t('common.cancel') }}</n-button>
          <n-button type="primary" @click="handleCreate" :loading="creating">{{ t('common.create') }}</n-button>
        </n-space>
      </template>
    </n-modal>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, onMounted, h } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  NPageHeader, NTabs, NTabPane, NButton, NIcon, NSpace, NDrawer, NDrawerContent,
  NDescriptions, NDescriptionsItem, NTag, NModal, NForm, NFormItem, NInput,
  NDatePicker, NSelect, NBadge, NCheckbox, NEmpty, NSpin, useMessage,
} from 'naive-ui'
import type { SelectOption } from 'naive-ui'
import { RefreshOutline, AddOutline } from '@vicons/ionicons5'
import { api } from '../api'
import type { TodoItem, TodoProvider } from '../api'

const { t } = useI18n()

// --- Task list sub-component ---
const TaskList = {
  name: 'TaskList',
  props: {
    items: { type: Array as () => TodoItem[], default: () => [] },
    loading: { type: Boolean, default: false },
  },
  emits: ['complete', 'delete', 'select'],
  setup(props: any, { emit }: any) {
    return () => {
      if (props.loading) {
        return h('div', { style: 'padding: 40px; text-align: center;' }, [h(NSpin, { size: 'medium' })])
      }
      if (!props.items?.length) {
        return h(NEmpty, { description: t('tasks.noTasks'), style: 'padding: 40px;' })
      }
      return h('div', { style: 'display: flex; flex-direction: column; gap: 2px;' },
        props.items.map((item: TodoItem) =>
          h('div', {
            key: item.id,
            style: 'display: flex; align-items: center; gap: 12px; padding: 10px 12px; border-radius: 6px; cursor: pointer; transition: background 0.2s;',
            class: 'task-row',
            onClick: () => emit('select', item),
          }, [
            h(NCheckbox, {
              checked: !!item.completed_at,
              onUpdateChecked: (checked: boolean) => { if (checked) emit('complete', item) },
              onClick: (e: Event) => e.stopPropagation(),
            }),
            h('div', { style: 'flex: 1; min-width: 0;' }, [
              h('div', { style: 'font-size: 14px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap;' }, item.title),
              h('div', { style: 'font-size: 12px; opacity: 0.5; margin-top: 2px;' }, [
                item.due_date ? formatDate(item.due_date) : '',
                item.project_name ? ` · ${item.project_name}` : '',
              ].filter(Boolean).join('')),
            ]),
            h(NTag, { size: 'tiny', type: providerTagType(item.provider), bordered: false }, () => providerLabel(item.provider)),
            item.priority > 0 ? h(NTag, { size: 'tiny', type: priorityTagType(item.priority), bordered: false }, () => priorityLabel(item.priority)) : null,
          ]),
        ),
      )
    }
  },
}

// --- State ---
const message = useMessage()
const activeTab = ref('today')
const todayItems = ref<TodoItem[]>([])
const overdueItems = ref<TodoItem[]>([])
const upcomingItems = ref<TodoItem[]>([])
const allItems = ref<TodoItem[]>([])
const loadingToday = ref(false)
const loadingOverdue = ref(false)
const loadingUpcoming = ref(false)
const loadingAll = ref(false)
const syncing = ref(false)

const drawerVisible = ref(false)
const selectedItem = ref<TodoItem | null>(null)

const showCreateModal = ref(false)
const creating = ref(false)
const createForm = ref({ title: '', notes: '', due_date: null as string | null, priority: 0, provider: 'builtin' })

const providers = ref<TodoProvider[]>([])

const todayCount = ref(0)
const overdueCount = ref(0)

const todayTabLabel = computed(() => {
  const count = todayCount.value
  return count > 0 ? `${t('tasks.today')} (${count})` : t('tasks.today')
})

const overdueTabLabel = computed(() => {
  const count = overdueCount.value
  return count > 0 ? `${t('tasks.overdue')} (${count})` : t('tasks.overdue')
})

const priorityOpts = computed<SelectOption[]>(() => [
  { label: t('tasks.priorityNone'), value: 0 },
  { label: t('tasks.priorityLow'), value: 1 },
  { label: t('tasks.priorityMedium'), value: 2 },
  { label: t('tasks.priorityHigh'), value: 3 },
])

const providerOptions = computed<SelectOption[]>(() => {
  return providers.value
    .filter(p => p.available)
    .map(p => ({ label: p.name, value: p.id }))
})

// --- Load data ---
async function loadToday() {
  loadingToday.value = true
  try {
    const resp = await api.getTodosToday()
    todayItems.value = resp.items || []
    todayCount.value = resp.count
  } catch (e: any) {
    message.error(t('tasks.loadFailed'))
  } finally {
    loadingToday.value = false
  }
}

async function loadOverdue() {
  loadingOverdue.value = true
  try {
    const resp = await api.getTodosOverdue()
    overdueItems.value = resp.items || []
    overdueCount.value = resp.count
  } catch (e: any) {
    message.error(t('tasks.loadFailed'))
  } finally {
    loadingOverdue.value = false
  }
}

async function loadUpcoming() {
  loadingUpcoming.value = true
  try {
    const resp = await api.getTodosUpcoming(7)
    upcomingItems.value = resp.items || []
  } catch (e: any) {
    message.error(t('tasks.loadFailed'))
  } finally {
    loadingUpcoming.value = false
  }
}

async function loadAll() {
  loadingAll.value = true
  try {
    const resp = await api.getTodosAll(200)
    allItems.value = resp.items || []
  } catch (e: any) {
    message.error(t('tasks.loadFailed'))
  } finally {
    loadingAll.value = false
  }
}

async function loadProviders() {
  try {
    const resp = await api.getTodoProviders()
    providers.value = resp.providers || []
    const defaultP = resp.providers.find(p => p.is_default)
    if (defaultP) {
      createForm.value.provider = defaultP.id
    }
  } catch {
    // use fallback
    providers.value = [{ id: 'builtin', name: 'Iulita.ai', available: true, is_default: true }]
  }
}

async function loadCounts() {
  try {
    const resp = await api.getTodoCounts()
    todayCount.value = resp.today
    overdueCount.value = resp.overdue
  } catch {}
}

function onTabChange(tab: string) {
  switch (tab) {
    case 'today':
      if (!todayItems.value.length) loadToday()
      break
    case 'overdue':
      if (!overdueItems.value.length && overdueCount.value > 0) loadOverdue()
      break
    case 'upcoming':
      if (!upcomingItems.value.length) loadUpcoming()
      break
    case 'all':
      if (!allItems.value.length) loadAll()
      break
  }
}

// --- Actions ---
async function handleComplete(item: TodoItem) {
  try {
    await api.completeTodo(item.id)
    message.success(t('tasks.completed', { title: item.title }))
    // Remove from all lists.
    todayItems.value = todayItems.value.filter(t => t.id !== item.id)
    overdueItems.value = overdueItems.value.filter(t => t.id !== item.id)
    upcomingItems.value = upcomingItems.value.filter(t => t.id !== item.id)
    allItems.value = allItems.value.filter(t => t.id !== item.id)
    if (drawerVisible.value && selectedItem.value?.id === item.id) {
      drawerVisible.value = false
    }
    loadCounts()
  } catch (e: any) {
    message.error(e.message || t('tasks.completeFailed'))
  }
}

async function handleDelete(item: TodoItem) {
  if (item.provider !== 'builtin') {
    message.warning(t('tasks.onlyBuiltin'))
    return
  }
  try {
    await api.deleteTodo(item.id)
    message.success(t('tasks.deletedTask', { title: item.title }))
    todayItems.value = todayItems.value.filter(t => t.id !== item.id)
    overdueItems.value = overdueItems.value.filter(t => t.id !== item.id)
    upcomingItems.value = upcomingItems.value.filter(t => t.id !== item.id)
    allItems.value = allItems.value.filter(t => t.id !== item.id)
    if (drawerVisible.value && selectedItem.value?.id === item.id) {
      drawerVisible.value = false
    }
    loadCounts()
  } catch (e: any) {
    message.error(e.message || t('tasks.deleteFailed'))
  }
}

async function handleCreate() {
  if (!createForm.value.title.trim()) {
    message.warning(t('tasks.titleRequired'))
    return
  }
  creating.value = true
  try {
    await api.createTodo({
      title: createForm.value.title,
      notes: createForm.value.notes || undefined,
      due_date: createForm.value.due_date || undefined,
      priority: createForm.value.priority,
      provider: createForm.value.provider,
    })
    message.success(t('tasks.taskCreated'))
    showCreateModal.value = false
    createForm.value = { title: '', notes: '', due_date: null, priority: 0, provider: createForm.value.provider }
    // Reload active tab.
    loadToday()
    loadCounts()
  } catch (e: any) {
    message.error(e.message || t('tasks.createFailed'))
  } finally {
    creating.value = false
  }
}

async function handleSync() {
  syncing.value = true
  try {
    await api.triggerTodoSync()
    message.success(t('tasks.syncTriggered'))
    // Wait a bit then reload.
    setTimeout(() => {
      loadToday()
      loadCounts()
      if (activeTab.value === 'overdue') loadOverdue()
      if (activeTab.value === 'upcoming') loadUpcoming()
      if (activeTab.value === 'all') loadAll()
    }, 3000)
  } catch (e: any) {
    message.error(e.message || t('tasks.syncFailed'))
  } finally {
    syncing.value = false
  }
}

function showDetail(item: TodoItem) {
  selectedItem.value = item
  drawerVisible.value = true
}

// --- Helpers ---
function providerLabel(id: string): string {
  switch (id) {
    case 'builtin': return 'Iulita.ai'
    case 'todoist': return 'Todoist'
    case 'google_tasks': return 'Google Tasks'
    case 'craft': return 'Craft'
    default: return id
  }
}

function providerTagType(id: string): 'default' | 'success' | 'warning' | 'info' | 'error' {
  switch (id) {
    case 'todoist': return 'error'
    case 'google_tasks': return 'info'
    case 'craft': return 'warning'
    default: return 'default'
  }
}

function priorityLabel(p: number): string {
  switch (p) {
    case 3: return t('tasks.priorityHigh')
    case 2: return t('tasks.priorityMedium')
    case 1: return t('tasks.priorityLow')
    default: return ''
  }
}

function priorityTagType(p: number): 'default' | 'success' | 'warning' | 'info' | 'error' {
  switch (p) {
    case 3: return 'error'
    case 2: return 'warning'
    case 1: return 'info'
    default: return 'default'
  }
}

function formatDate(d: string): string {
  if (!d) return ''
  try {
    return new Date(d).toLocaleDateString(undefined, { month: 'short', day: 'numeric' })
  } catch {
    return d.substring(0, 10)
  }
}

function formatDateTime(d: string): string {
  if (!d) return ''
  try {
    return new Date(d).toLocaleString()
  } catch {
    return d
  }
}

// --- Init ---
onMounted(() => {
  loadProviders()
  loadToday()
  loadCounts()
})
</script>

<style scoped>
.task-row:hover {
  background: rgba(255, 255, 255, 0.05);
}
</style>
