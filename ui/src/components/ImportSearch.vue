<template>
  <n-card :title="t('import.searchTitle')" size="small">
    <n-space vertical :size="12">
      <n-text depth="3" style="font-size: 12px">{{ t('import.searchNote') }}</n-text>
      <n-input
        v-model:value="query"
        :placeholder="t('import.searchPlaceholder')"
        clearable
        @update:value="debouncedSearch"
      />
      <n-text v-if="searched && !vectorSearch" depth="3" style="font-size: 12px">
        {{ t('import.ftsOnly') }}
      </n-text>

      <n-text v-if="!searched && !loading" depth="3" style="font-size: 13px">{{ t('import.searchHint') }}</n-text>

      <n-spin v-if="searched || loading" :show="loading">
        <n-empty v-if="searched && results.length === 0" :description="t('import.noResults')" />
        <n-list v-else bordered>
          <n-list-item
            v-for="r in results"
            :key="r.message_id"
            style="cursor: pointer"
            @click="$emit('open', r.conversation_uuid, r.message_id)"
          >
            <n-space vertical :size="4">
              <n-tag size="tiny" :bordered="false">{{ r.sender }}</n-tag>
              <span style="white-space: pre-wrap; word-break: break-word; font-size: 13px">{{ r.snippet }}</span>
            </n-space>
          </n-list-item>
        </n-list>
      </n-spin>
    </n-space>
  </n-card>
</template>

<script setup lang="ts">
import { ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { NCard, NSpace, NText, NInput, NSpin, NEmpty, NList, NListItem, NTag, useMessage } from 'naive-ui'
import { api } from '../api'
import type { ImportSearchResult } from '../api'

const { t } = useI18n()
const message = useMessage()
defineEmits<{ (e: 'open', conversationUUID: string, messageID: number): void }>()

const query = ref('')
const results = ref<ImportSearchResult[]>([])
const vectorSearch = ref(false)
const loading = ref(false)
const searched = ref(false)

let timer: ReturnType<typeof setTimeout> | null = null
function debouncedSearch() {
  if (timer) clearTimeout(timer)
  timer = setTimeout(runSearch, 300)
}

async function runSearch() {
  const q = query.value.trim()
  if (!q) {
    results.value = []
    searched.value = false
    return
  }
  loading.value = true
  try {
    const res = await api.searchImported(q, 30)
    results.value = res.results || []
    vectorSearch.value = res.vector_search
    searched.value = true
  } catch (e: any) {
    message.error(e.message)
  } finally {
    loading.value = false
  }
}
</script>
