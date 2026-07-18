<template>
  <n-card :title="t('import.archiveTitle')" size="small">
    <n-data-table
      remote
      :columns="columns"
      :data="conversations"
      :loading="loading"
      :bordered="true"
      :pagination="pagination"
      :row-key="(row: ImportedConversation) => row.SourceUUID"
      :row-props="rowProps"
      size="small"
      @update:page="onPage"
    />

    <n-drawer v-model:show="drawerVisible" :width="640" placement="right">
      <n-drawer-content :title="drawerTitle" closable>
        <n-spin :show="messagesLoading">
          <n-empty v-if="!messagesLoading && messages.length === 0" :description="t('import.noMessages')" />
          <n-list v-else>
            <n-list-item v-for="m in messages" :id="`msg-${m.ID}`" :key="m.SourceUUID">
              <n-space vertical :size="4" style="width: 100%">
                <n-tag size="tiny" :type="m.Sender === 'human' ? 'info' : 'success'" :bordered="false">
                  {{ m.Sender }}
                </n-tag>
                <span style="white-space: pre-wrap; word-break: break-word; font-size: 13px">{{ m.Content }}</span>
              </n-space>
            </n-list-item>
          </n-list>
        </n-spin>
      </n-drawer-content>
    </n-drawer>
  </n-card>
</template>

<script setup lang="ts">
import { ref, reactive, computed, nextTick, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { NCard, NDataTable, NDrawer, NDrawerContent, NSpin, NEmpty, NList, NListItem, NSpace, NTag, useMessage } from 'naive-ui'
import type { DataTableColumns } from 'naive-ui'
import { api } from '../api'
import type { ImportedConversation, ImportedMessage } from '../api'

const { t } = useI18n()
const message = useMessage()

const conversations = ref<ImportedConversation[]>([])
const loading = ref(false)
const pagination = reactive({ page: 1, pageSize: 20, itemCount: 0 })

const drawerVisible = ref(false)
const messages = ref<ImportedMessage[]>([])
const messagesLoading = ref(false)
const currentTitle = ref('')

const drawerTitle = computed(() => currentTitle.value || t('import.conversation'))

function fmt(ts: string): string {
  if (!ts || ts.startsWith('0001')) return '—'
  return new Date(ts).toLocaleDateString()
}

const columns = computed<DataTableColumns<ImportedConversation>>(() => [
  { title: t('import.convTitle'), key: 'Title', ellipsis: { tooltip: true } },
  { title: t('import.msgCount'), key: 'MessageCount', width: 90 },
  { title: t('import.created'), key: 'CreatedAt', width: 120, render: (row) => fmt(row.CreatedAt) },
])

function rowProps(row: ImportedConversation) {
  return { style: 'cursor: pointer', onClick: () => openConversation(row.SourceUUID, row.Title) }
}

async function loadPage() {
  loading.value = true
  try {
    const offset = (pagination.page - 1) * pagination.pageSize
    const rows = await api.listImportedConversations(pagination.pageSize, offset)
    // The API is not count-aware. If a page past the first comes back empty (e.g. the
    // total is an exact multiple of pageSize), clamp back one page rather than stranding
    // the user on a phantom empty page.
    if ((!rows || rows.length === 0) && pagination.page > 1) {
      pagination.page -= 1
      await loadPage()
      return
    }
    conversations.value = rows || []
    // Approximate itemCount so paging stays usable: advertise one more page only while
    // pages are full.
    if (rows && rows.length === pagination.pageSize) {
      pagination.itemCount = pagination.page * pagination.pageSize + 1
    } else {
      pagination.itemCount = (pagination.page - 1) * pagination.pageSize + (rows?.length || 0)
    }
  } catch (e: any) {
    message.error(e.message)
  } finally {
    loading.value = false
  }
}

function onPage(page: number) {
  pagination.page = page
  loadPage()
}

async function openConversation(uuid: string, title: string, messageID?: number) {
  currentTitle.value = title
  drawerVisible.value = true
  messagesLoading.value = true
  messages.value = []
  try {
    messages.value = (await api.getImportedConversationMessages(uuid)) || []
    if (messageID) {
      await nextTick()
      document.getElementById(`msg-${messageID}`)?.scrollIntoView({ block: 'center' })
    }
  } catch (e: any) {
    message.error(e.message)
  } finally {
    messagesLoading.value = false
  }
}

// Allow the parent (search result click) to open a conversation directly.
defineExpose({ openConversation })

onMounted(loadPage)
</script>
