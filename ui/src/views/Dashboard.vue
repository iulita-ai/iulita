<template>
  <n-spin :show="loading">
  <n-space vertical :size="24">
    <n-h2>{{ $t('dashboard.title') }}</n-h2>

    <n-select
      v-model:value="selectedChat"
      :options="chatOptions"
      :placeholder="t('dashboard.allChats')"
      clearable
      style="max-width: 300px"
    />

    <n-grid :cols="5" :x-gap="16" :y-gap="16">
      <n-grid-item>
        <n-card>
          <n-statistic :label="t('dashboard.messages')" :value="stats?.messages ?? 0" />
        </n-card>
      </n-grid-item>
      <n-grid-item>
        <n-card>
          <n-statistic :label="t('dashboard.facts')" :value="stats?.facts ?? 0" />
        </n-card>
      </n-grid-item>
      <n-grid-item>
        <n-card>
          <n-statistic :label="t('dashboard.insights')" :value="stats?.insights ?? 0" />
        </n-card>
      </n-grid-item>
      <n-grid-item>
        <n-card>
          <n-statistic :label="t('dashboard.profileFacts')" :value="stats?.tech_facts ?? 0" />
        </n-card>
      </n-grid-item>
      <n-grid-item>
        <n-card>
          <n-statistic :label="t('dashboard.reminders')" :value="stats?.reminders ?? 0" />
        </n-card>
      </n-grid-item>
    </n-grid>

    <n-h3>{{ $t('dashboard.recentFacts') }}</n-h3>
    <n-list bordered>
      <n-list-item v-for="fact in recentFacts" :key="fact.ID">
        <n-thing :title="`#${fact.ID}`" :description="fact.Content">
          <template #header-extra>
            <n-tag size="small" :bordered="false">{{ fact.SourceType }}</n-tag>
          </template>
        </n-thing>
      </n-list-item>
      <n-empty v-if="recentFacts.length === 0" :description="t('dashboard.noFacts')" />
    </n-list>

    <n-h3>{{ $t('dashboard.recentInsights') }}</n-h3>
    <n-space vertical :size="12">
      <n-card v-for="insight in recentInsights" :key="insight.id" size="small">
        <template #header>
          <n-text depth="3">{{ formatDate(insight.created_at) }}</n-text>
        </template>
        <p style="white-space: pre-wrap">{{ insight.content }}</p>
        <template #footer>
          <n-space :size="4">
            <n-tag v-for="fid in parseFactIDs(insight.fact_ids)" :key="fid" size="small" type="info">
              {{ t('dashboard.factPrefix', { id: fid }) }}
            </n-tag>
          </n-space>
        </template>
      </n-card>
      <n-empty v-if="recentInsights.length === 0" :description="t('dashboard.noInsights')" />
    </n-space>
  </n-space>
  </n-spin>
</template>

<script setup lang="ts">
import { ref, computed, onMounted, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  NH2, NH3, NSpace, NGrid, NGridItem, NCard, NStatistic, NSelect,
  NList, NListItem, NThing, NTag, NText, NEmpty, NSpin,
} from 'naive-ui'
import { api } from '../api'
import type { Stats, ChatInfo, Fact, Insight } from '../api'

const { t } = useI18n()

const loading = ref(true)
const selectedChat = ref<string | null>(null)
const chats = ref<ChatInfo[]>([])
const stats = ref<Stats | null>(null)
const recentFacts = ref<Fact[]>([])
const recentInsights = ref<Insight[]>([])

const chatOptions = computed(() =>
  chats.value.map(c => ({
    label: `${c.chat_id} (${c.messages} msgs)`,
    value: c.chat_id,
  }))
)

async function loadData() {
  const chatId = selectedChat.value ?? undefined
  const [s, f, d] = await Promise.all([
    api.getStats(chatId),
    api.getFacts({ chat_id: chatId, limit: 5 }),
    api.getInsights({ chat_id: chatId, limit: 3 }),
  ])
  stats.value = s
  recentFacts.value = f ?? []
  recentInsights.value = d ?? []
}

onMounted(async () => {
  try {
    chats.value = await api.getChats()
    await loadData()
  } finally {
    loading.value = false
  }
})

watch(selectedChat, () => loadData())

function formatDate(iso: string): string {
  return new Date(iso).toLocaleString()
}

function parseFactIDs(ids: string): string[] {
  if (!ids) return []
  return ids.split(',').map(s => s.trim()).filter(Boolean)
}
</script>
