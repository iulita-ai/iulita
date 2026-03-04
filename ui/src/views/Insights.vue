<template>
  <n-spin :show="loading">
  <n-space vertical :size="16">
    <n-h2>{{ $t('insights.title') }}</n-h2>

    <n-select
      v-model:value="selectedChat"
      :options="chatOptions"
      :placeholder="t('insights.allChats')"
      clearable
      style="max-width: 300px"
    />

    <n-space vertical :size="12">
      <n-card v-for="insight in insights" :key="insight.id" size="small">
        <template #header>
          {{ t('insights.insightPrefix', { id: insight.id }) }}
        </template>
        <template #header-extra>
          <n-text depth="3">{{ formatDate(insight.created_at) }}</n-text>
        </template>
        <p style="white-space: pre-wrap">{{ insight.content }}</p>
        <template #footer>
          <n-space :size="4">
            <n-tag v-if="insight.user_id" size="small" :bordered="false" type="success">user:{{ insight.user_id.slice(0, 8) }}</n-tag>
            <n-tag v-if="insight.chat_id" size="small" :bordered="false" type="info">{{ insight.chat_id }}</n-tag>
            <n-tag v-if="insight.quality" size="small" :bordered="false" type="warning">Q{{ insight.quality }}</n-tag>
            <n-tag v-for="fid in parseFactIDs(insight.fact_ids)" :key="fid" size="small" type="default">
              {{ t('insights.factPrefix', { id: fid }) }}
            </n-tag>
          </n-space>
        </template>
      </n-card>
      <n-empty v-if="!loading && insights.length === 0" :description="t('insights.noInsights')" />
    </n-space>
  </n-space>
  </n-spin>
</template>

<script setup lang="ts">
import { ref, computed, onMounted, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { NH2, NSpace, NSelect, NCard, NTag, NText, NEmpty, NSpin } from 'naive-ui'
import { api } from '../api'
import type { Insight, ChatInfo } from '../api'

const { t } = useI18n()

const loading = ref(true)
const selectedChat = ref<string | null>(null)
const chats = ref<ChatInfo[]>([])
const insights = ref<Insight[]>([])

const chatOptions = computed(() =>
  chats.value.map(c => ({
    label: `${c.chat_id} (${c.messages} msgs)`,
    value: c.chat_id,
  }))
)

async function loadInsights() {
  insights.value = await api.getInsights({
    chat_id: selectedChat.value ?? undefined,
  }) ?? []
}

onMounted(async () => {
  try {
    chats.value = await api.getChats()
    await loadInsights()
  } finally {
    loading.value = false
  }
})

watch(selectedChat, () => loadInsights())

function formatDate(iso: string): string {
  return new Date(iso).toLocaleString()
}

function parseFactIDs(ids: string): string[] {
  if (!ids) return []
  return ids.split(',').map(s => s.trim()).filter(Boolean)
}
</script>
