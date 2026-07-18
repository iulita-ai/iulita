<template>
  <n-card :title="t('import.uploadTitle')" size="small">
    <n-space vertical :size="14">
      <n-upload
        :show-file-list="false"
        :custom-request="captureFile"
        accept=".zip"
        :disabled="uploading"
      >
        <n-upload-dragger>
          <div style="padding: 8px 0">
            <n-icon size="40" :depth="3">
              <cloud-upload-outline />
            </n-icon>
          </div>
          <n-text style="font-size: 15px">{{ t('import.dropzone') }}</n-text>
          <n-p depth="3" style="margin: 6px 0 0">{{ t('import.dropzoneHint') }}</n-p>
        </n-upload-dragger>
      </n-upload>

      <template v-if="selectedFile">
        <n-alert type="warning" :show-icon="true" :title="t('import.piiTitle')">
          {{ conversationsOnly ? t('import.piiBodyConversations') : t('import.piiBody') }}
        </n-alert>

        <n-descriptions bordered :column="1" label-placement="left" size="small">
          <n-descriptions-item :label="t('import.file')">
            {{ selectedFile.name }} ({{ formatSize(selectedFile.size) }})
          </n-descriptions-item>
        </n-descriptions>

        <n-checkbox v-model:checked="conversationsOnly" :disabled="uploading">
          {{ t('import.conversationsOnly') }}
        </n-checkbox>
        <n-text depth="3" style="font-size: 12px; display: block">{{ t('import.idempotencyNote') }}</n-text>

        <n-progress
          v-if="uploading"
          type="line"
          :percentage="uploadPct"
          :indicator-placement="'inside'"
          :processing="uploadPct >= 100"
        />

        <n-space>
          <n-button type="primary" :loading="uploading" @click="startUpload">
            {{ t('import.importButton') }}
          </n-button>
          <n-button :disabled="uploading" @click="reset">{{ t('common.cancel') }}</n-button>
        </n-space>
      </template>
    </n-space>
  </n-card>
</template>

<script setup lang="ts">
import { ref } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  NCard, NSpace, NUpload, NUploadDragger, NIcon, NText, NP, NAlert,
  NDescriptions, NDescriptionsItem, NCheckbox, NProgress, NButton, useMessage,
} from 'naive-ui'
import { CloudUploadOutline } from '@vicons/ionicons5'
import { api } from '../api'
import type { ImportUploadResult } from '../api'

const { t } = useI18n()
const message = useMessage()

const emit = defineEmits<{ (e: 'queued', result: ImportUploadResult): void }>()

const selectedFile = ref<File | null>(null)
const conversationsOnly = ref(false)
const uploading = ref(false)
const uploadPct = ref(0)

function formatSize(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`
  return `${(bytes / 1024 / 1024).toFixed(1)} MB`
}

function captureFile({ file, onFinish }: any) {
  selectedFile.value = file.file as File
  uploadPct.value = 0
  onFinish()
}

function reset() {
  selectedFile.value = null
  uploadPct.value = 0
}

async function startUpload() {
  if (!selectedFile.value) return
  uploading.value = true
  uploadPct.value = 0
  try {
    const result = await api.importClaudeExport(
      selectedFile.value,
      !conversationsOnly.value,
      (pct) => (uploadPct.value = pct),
    )
    if (result.status === 'already_imported') {
      message.info(t('import.alreadyImported'))
    } else if (result.status === 'already_queued') {
      message.info(t('import.alreadyQueued'))
    } else {
      message.success(t('import.queued'))
    }
    emit('queued', result)
    reset()
  } catch (e: any) {
    message.error(e.message)
  } finally {
    uploading.value = false
  }
}
</script>
