<template>
  <n-space vertical :size="16">
    <n-h2>{{ $t('profile.title') }}</n-h2>

    <n-select
      v-model:value="selectedChat"
      :options="chatOptions"
      :placeholder="t('profile.allChats')"
      clearable
      style="max-width: 300px"
    />

    <n-empty v-if="Object.keys(grouped).length === 0" :description="t('profile.noData')" />

    <n-space vertical :size="12">
      <n-card v-for="(facts, category) in grouped" :key="category" :title="categoryLabel(category)" size="small">
        <n-data-table :columns="columns" :data="facts" :bordered="false" size="small" />
      </n-card>
    </n-space>
  </n-space>
</template>

<script setup lang="ts">
import { ref, computed, onMounted, watch, h } from 'vue'
import { useI18n } from 'vue-i18n'
import { NH2, NSpace, NSelect, NCard, NEmpty, NDataTable, NTag } from 'naive-ui'
import type { DataTableColumn } from 'naive-ui'
import { api } from '../api'
import type { ChatInfo, TechFactGroup } from '../api'

const { t } = useI18n()

const selectedChat = ref<string | null>(null)
const chats = ref<ChatInfo[]>([])
const grouped = ref<TechFactGroup>({})

const chatOptions = computed(() =>
  chats.value.map(c => ({
    label: `${c.chat_id} (${c.messages} msgs)`,
    value: c.chat_id,
  }))
)

const columns = computed<DataTableColumn[]>(() => [
  { title: t('profile.key'), key: 'key', width: 200 },
  { title: t('profile.value'), key: 'value' },
  {
    title: t('profile.confidence'),
    key: 'confidence',
    width: 120,
    render(row: any) {
      const pct = Math.round((row.confidence ?? 0) * 100)
      return h(NTag, { size: 'small', type: pct >= 70 ? 'success' : pct >= 40 ? 'warning' : 'default', bordered: false }, () => `${pct}%`)
    },
  },
  { title: t('profile.updates'), key: 'update_count', width: 90 },
  {
    title: t('profile.lastUpdated'),
    key: 'updated_at',
    width: 180,
    render(row: any) {
      return row.updated_at ? new Date(row.updated_at).toLocaleString() : '-'
    },
  },
])

function categoryLabel(cat: string): string {
  const key = `profile.category_${cat}`
  const translated = t(key)
  // If no translation found (returns the key itself), capitalize
  return translated !== key ? translated : cat.charAt(0).toUpperCase() + cat.slice(1)
}

async function loadTechFacts() {
  grouped.value = await api.getTechFacts(selectedChat.value ?? undefined) ?? {}
}

onMounted(async () => {
  chats.value = await api.getChats()
  await loadTechFacts()
})

watch(selectedChat, () => loadTechFacts())
</script>
