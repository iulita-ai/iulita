<template>
  <n-space vertical :size="16">
    <n-h2>{{ t('import.title') }}</n-h2>

    <n-tabs v-model:value="tab" type="line" animated>
      <n-tab-pane name="import" :tab="t('import.tabImport')">
        <n-space vertical :size="16">
          <import-uploader @queued="onQueued" />
          <import-progress
            v-if="activeProgress"
            :payload="activeProgress"
            :done="activeDone"
            :failed="activeFailed"
            @close="activeProgress = null"
          />
          <import-history :runs="runs" :loading="loadingRuns" :active-jobs="activeJobs" @refresh="loadStatus" @cancel="onCancel" />
        </n-space>
      </n-tab-pane>

      <n-tab-pane name="archive" :tab="t('import.tabArchive')">
        <n-space vertical :size="16">
          <n-space justify="end">
            <n-button size="small" :loading="reindexing" @click="onReindex">{{ t('import.reindex') }}</n-button>
          </n-space>
          <import-search @open="openFromSearch" />
          <import-archive ref="archiveRef" />
        </n-space>
      </n-tab-pane>
    </n-tabs>
  </n-space>
</template>

<script setup lang="ts">
import { ref, onMounted, onBeforeUnmount, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { NSpace, NH2, NTabs, NTabPane, NButton, useMessage } from 'naive-ui'
import ImportUploader from '../components/ImportUploader.vue'
import ImportProgress from '../components/ImportProgress.vue'
import ImportHistory from '../components/ImportHistory.vue'
import ImportSearch from '../components/ImportSearch.vue'
import ImportArchive from '../components/ImportArchive.vue'
import { useWebSocket } from '../composables/useWebSocket'
import { api } from '../api'
import type { ImportRun, ImportUploadResult } from '../api'

const { t } = useI18n()
const message = useMessage()

const tab = ref('import')
const runs = ref<ImportRun[]>([])
const loadingRuns = ref(false)

const activeProgress = ref<any | null>(null)
const activeDone = ref(false)
const activeFailed = ref(false)
// job_ids that have emitted a progress event (worker started) — no longer cancelable.
const activeJobs = ref<string[]>([])

const archiveRef = ref<InstanceType<typeof ImportArchive> | null>(null)
const reindexing = ref(false)

const ws = useWebSocket('/ws')

async function onReindex() {
  reindexing.value = true
  try {
    const res = await api.reindexImport()
    message.info(res.status === 'already_queued' ? t('import.reindexQueued') : t('import.reindexStarted'))
    tab.value = 'import'
    loadStatus()
  } catch (e: any) {
    message.error(e.message)
  } finally {
    reindexing.value = false
  }
}

async function loadStatus() {
  loadingRuns.value = true
  try {
    runs.value = (await api.getImportStatus()) || []
    // Rehydrate the live progress card on reload: if a run is still running and we
    // have no live WS payload yet, synthesize one from its persisted last-progress.
    if (!activeProgress.value) {
      const running = runs.value.find((r) => r.status === 'running')
      if (running) {
        activeProgress.value = {
          job_id: running.job_id,
          phase: running.last_phase || 'starting',
          done: running.last_done,
          total: running.last_total,
          messages_stored: running.messages_stored,
          messages_skipped: running.messages_skipped,
        }
        activeDone.value = false
        activeFailed.value = false
      }
    }
  } catch (e: any) {
    message.error(e.message)
  } finally {
    loadingRuns.value = false
  }
}

function onQueued(_result: ImportUploadResult) {
  loadStatus()
  setTimeout(loadStatus, 1500)
}

async function onCancel(jobID: string) {
  try {
    await api.cancelImport(jobID)
    message.success(t('import.canceled'))
    await loadStatus()
  } catch (e: any) {
    // The worker may have started between render and click → 409. Not a real error.
    if (/409|already running|cannot cancel/i.test(e.message || '')) {
      message.info(t('import.cancelTooLate'))
      await loadStatus()
    } else {
      message.error(e.message)
    }
  }
}

function openFromSearch(uuid: string, messageID: number) {
  tab.value = 'archive'
  setTimeout(() => archiveRef.value?.openConversation(uuid, '', messageID), 50)
}

// WebSocket has no replay, so status polling is the source of truth; WS is an
// optimization that updates the live progress card and refreshes history.
function handleProgress(payload: any) {
  if (payload?.job_id && !activeJobs.value.includes(payload.job_id)) {
    activeJobs.value = [...activeJobs.value, payload.job_id]
  }
  activeProgress.value = payload
  activeDone.value = false
  activeFailed.value = false
}
function handleDone(payload: any) {
  activeProgress.value = payload
  activeDone.value = true
  activeFailed.value = false
  loadStatus()
  setTimeout(() => {
    if (activeDone.value) activeProgress.value = null
  }, 6000)
}
function handleFailed(payload: any) {
  activeProgress.value = payload
  activeFailed.value = true
  loadStatus()
}

watch(
  () => ws.connected.value,
  (up) => {
    if (up) loadStatus()
  },
)

onMounted(() => {
  ws.connect()
  ws.on('import.started', handleProgress)
  ws.on('import.progress', handleProgress)
  ws.on('import.done', handleDone)
  ws.on('import.failed', handleFailed)
  loadStatus()
})

onBeforeUnmount(() => {
  ws.off('import.started', handleProgress)
  ws.off('import.progress', handleProgress)
  ws.off('import.done', handleDone)
  ws.off('import.failed', handleFailed)
  ws.close()
})
</script>
