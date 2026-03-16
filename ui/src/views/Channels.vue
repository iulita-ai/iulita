<template>
  <n-space vertical :size="16">
    <n-page-header :title="t('channels.title')" :subtitle="t('channels.subtitle')">
      <template #extra>
        <n-button type="primary" @click="showCreateModal = true">{{ t('channels.addChannel') }}</n-button>
      </template>
    </n-page-header>

    <n-data-table
      :columns="columns"
      :data="instances"
      :loading="loading"
      :row-key="(r: ChannelInstance) => r.id"
    />

    <n-space>
      <n-text :depth="3">{{ t('channels.totalChannels', { count: instances.length }) }}</n-text>
    </n-space>

    <!-- Create channel modal -->
    <n-modal v-model:show="showCreateModal" preset="dialog" :title="t('channels.addInstance')">
      <n-form>
        <n-form-item :label="t('channels.idSlug')">
          <n-input v-model:value="createForm.id" :placeholder="t('channels.idPlaceholder')" />
        </n-form-item>
        <n-form-item :label="t('channels.type')">
          <n-select v-model:value="createForm.type" :options="typeOptions" />
        </n-form-item>
        <n-form-item :label="t('channels.name')">
          <n-input v-model:value="createForm.name" :placeholder="t('channels.displayName')" />
        </n-form-item>
        <channel-config-form
          :channel-type="createForm.type"
          v-model:model-value="createForm.config"
          :required="true"
        />
      </n-form>
      <template #action>
        <n-button @click="showCreateModal = false">{{ t('common.cancel') }}</n-button>
        <n-button type="primary" :loading="creating" @click="handleCreate">{{ t('common.create') }}</n-button>
      </template>
    </n-modal>

    <!-- Edit channel modal -->
    <n-modal v-model:show="showEditModal" preset="dialog" :title="t('channels.editInstance')">
      <n-form v-if="editInstance">
        <n-form-item label="ID">
          <n-input :value="editInstance.id" disabled />
        </n-form-item>
        <n-form-item :label="t('channels.type')">
          <n-input :value="editInstance.type" disabled />
        </n-form-item>
        <n-form-item :label="t('channels.source')">
          <n-tag :type="editInstance.source === 'config' ? 'warning' : 'success'" size="small">
            {{ editInstance.source }}
          </n-tag>
        </n-form-item>
        <n-form-item :label="t('channels.name')">
          <n-input v-model:value="editForm.name" :disabled="editInstance.source === 'config'" />
        </n-form-item>
        <template v-if="editInstance.source === 'dashboard'">
          <channel-config-form
            :channel-type="editInstance.type"
            v-model:model-value="editForm.config"
          />
        </template>
        <n-form-item :label="t('common.enabled')">
          <n-switch v-model:value="editForm.enabled" />
        </n-form-item>
      </n-form>
      <template #action>
        <n-button @click="showEditModal = false">{{ t('common.cancel') }}</n-button>
        <n-button type="primary" :loading="saving" @click="handleUpdate">{{ t('common.save') }}</n-button>
      </template>
    </n-modal>

    <!-- Set bot photo modal -->
    <n-modal v-model:show="showPhotoModal" preset="dialog" :title="t('channels.setBotPhoto')" style="width: 480px">
      <n-space vertical :size="16">
        <n-text :depth="2">{{ t('channels.setBotPhotoDesc') }}</n-text>

        <n-card :title="t('channels.builtInLogo')" size="small">
          <n-space align="center" :size="16">
            <img src="/logo-512x512.png" alt="Iulita" style="width:64px;height:64px;border-radius:50%;" />
            <n-button
              type="primary"
              :loading="settingPhoto"
              :disabled="settingPhoto"
              @click="handleSetBuiltInPhoto"
            >
              {{ t('channels.useBuiltIn') }}
            </n-button>
          </n-space>
        </n-card>

        <n-card :title="t('channels.customImage')" size="small">
          <n-space vertical :size="12">
            <n-space align="center" :size="16">
              <img
                v-if="customPhotoPreview"
                :src="customPhotoPreview"
                alt="Preview"
                style="width:64px;height:64px;border-radius:50%;object-fit:cover;"
              />
              <div v-else style="width:64px;height:64px;border-radius:50%;background:#333;display:flex;align-items:center;justify-content:center;">
                <n-text :depth="3" style="font-size:12px;">?</n-text>
              </div>
              <n-upload
                :show-file-list="false"
                accept="image/png,image/jpeg"
                :custom-request="handleCustomPhotoSelect"
              >
                <n-button>{{ t('channels.uploadCustom') }}</n-button>
              </n-upload>
            </n-space>
            <n-button
              v-if="customPhotoFile"
              type="primary"
              :loading="settingPhoto"
              :disabled="settingPhoto"
              @click="handleSetCustomPhoto"
            >
              {{ t('channels.applyPhoto') }}
            </n-button>
          </n-space>
        </n-card>
      </n-space>
      <template #action>
        <n-button @click="showPhotoModal = false">{{ t('common.cancel') }}</n-button>
      </template>
    </n-modal>

    <!-- Bindings modal -->
    <n-modal v-model:show="showBindingsModal" preset="dialog" :title="`Bindings: ${bindingsInstanceId}`" style="width: 700px">
      <n-data-table
        :columns="bindingColumns"
        :data="bindings"
        :loading="loadingBindings"
        size="small"
      />
    </n-modal>
  </n-space>
</template>

<script setup lang="ts">
import { ref, h, watch, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  NSpace, NPageHeader, NDataTable, NButton, NSwitch, NTag, NText,
  NModal, NForm, NFormItem, NInput, NSelect, NCard, NUpload, useMessage,
} from 'naive-ui'
import type { DataTableColumn } from 'naive-ui'
import { api } from '../api'
import type { ChannelInstance, ChannelBinding } from '../api'
import ChannelConfigForm from '../components/ChannelConfigForm.vue'

const { t } = useI18n()
const message = useMessage()

const instances = ref<ChannelInstance[]>([])
const loading = ref(false)

const typeOptions = [
  { label: 'Telegram', value: 'telegram' },
  { label: 'Discord', value: 'discord' },
  { label: 'Web Chat', value: 'web' },
]

const defaultConfigs: Record<string, string> = {
  telegram: JSON.stringify({ token: '', allowed_ids: [], debounce_window: '1.5s', rate_limit: 0, rate_window: '1m' }),
  discord: JSON.stringify({ token: '', allowed_channel_ids: [] }),
  web: JSON.stringify({}),
}

// Create modal
const showCreateModal = ref(false)
const creating = ref(false)
const createForm = ref({ id: '', type: 'telegram', name: '', config: defaultConfigs['telegram'] })

// Edit modal
const showEditModal = ref(false)
const saving = ref(false)
const editInstance = ref<ChannelInstance | null>(null)
const editForm = ref({ name: '', config: '', enabled: true })

// Bindings modal
const showBindingsModal = ref(false)
const loadingBindings = ref(false)
const bindingsInstanceId = ref('')
const bindings = ref<ChannelBinding[]>([])

// Set photo modal
const showPhotoModal = ref(false)
const settingPhoto = ref(false)
const photoInstanceId = ref('')
const customPhotoFile = ref<File | null>(null)
const customPhotoPreview = ref<string | null>(null)

function openSetPhoto(row: ChannelInstance) {
  photoInstanceId.value = row.id
  customPhotoFile.value = null
  customPhotoPreview.value = null
  showPhotoModal.value = true
}

function handleCustomPhotoSelect({ file }: { file: { file: File | null } }) {
  if (!file.file) return
  customPhotoFile.value = file.file
  customPhotoPreview.value = URL.createObjectURL(file.file)
}

async function handleSetBuiltInPhoto() {
  settingPhoto.value = true
  try {
    const res = await fetch('/logo-512x512.png')
    const blob = await res.blob()
    await api.setChannelPhoto(photoInstanceId.value, blob)
    message.success(t('channels.photoSet'))
    showPhotoModal.value = false
  } catch (e: any) {
    message.error(e.message || t('channels.photoFailed'))
  } finally {
    settingPhoto.value = false
  }
}

async function handleSetCustomPhoto() {
  if (!customPhotoFile.value) return
  settingPhoto.value = true
  try {
    await api.setChannelPhoto(photoInstanceId.value, customPhotoFile.value)
    message.success(t('channels.photoSet'))
    showPhotoModal.value = false
  } catch (e: any) {
    message.error(e.message || t('channels.photoFailed'))
  } finally {
    settingPhoto.value = false
  }
}

// Reset config when channel type changes in create form
watch(() => createForm.value.type, (t) => {
  createForm.value.config = defaultConfigs[t] || '{}'
})

async function toggleEnabled(row: ChannelInstance) {
  const newEnabled = !row.enabled
  try {
    await api.updateChannelInstance(row.id, { enabled: newEnabled })
    row.enabled = newEnabled
  } catch {
    message.error(t('channels.toggleFailed'))
  }
}

function openEdit(row: ChannelInstance) {
  editInstance.value = row
  editForm.value = {
    name: row.name,
    config: row.config || '',
    enabled: row.enabled,
  }
  showEditModal.value = true
}

async function openBindings(row: ChannelInstance) {
  bindingsInstanceId.value = row.id
  showBindingsModal.value = true
  loadingBindings.value = true
  try {
    bindings.value = await api.listChannelBindings(row.id)
  } catch {
    message.error(t('channels.toggleFailed'))
  } finally {
    loadingBindings.value = false
  }
}

const columns: DataTableColumn<ChannelInstance>[] = [
  {
    title: t('channels.type'),
    key: 'type',
    width: 110,
    render: (row) => h(NTag, { type: 'info', size: 'small', bordered: false }, () => row.type),
  },
  {
    title: 'ID',
    key: 'id',
    width: 160,
  },
  {
    title: t('channels.name'),
    key: 'name',
  },
  {
    title: t('channels.source'),
    key: 'source',
    width: 110,
    render: (row) => h(NTag, {
      type: row.source === 'config' ? 'warning' : 'success',
      size: 'small',
      bordered: false,
    }, () => row.source),
  },
  {
    title: t('common.enabled'),
    key: 'enabled',
    width: 90,
    render: (row) => h(NSwitch, {
      value: row.enabled,
      onUpdateValue: () => toggleEnabled(row),
    }),
  },
  {
    title: t('common.actions'),
    key: 'actions',
    width: 260,
    render: (row) => h(NSpace, { size: 'small' }, () => [
      h(NButton, { size: 'small', quaternary: true, onClick: () => openEdit(row) }, () => t('common.edit')),
      h(NButton, { size: 'small', quaternary: true, onClick: () => openBindings(row) }, () => t('channels.bindings')),
      row.type === 'telegram'
        ? h(NButton, { size: 'small', quaternary: true, type: 'info', onClick: () => openSetPhoto(row) }, () => t('channels.setPhoto'))
        : null,
      row.source === 'dashboard'
        ? h(NButton, { size: 'small', quaternary: true, type: 'error', onClick: () => handleDelete(row) }, () => t('common.delete'))
        : null,
    ]),
  },
]

const bindingColumns: DataTableColumn<ChannelBinding>[] = [
  { title: t('channels.user'), key: 'owner', render: (row) => row.owner_display_name || row.owner_username },
  { title: t('channels.channelUserId'), key: 'channel_user_id' },
  { title: t('users.username'), key: 'channel_username', render: (row) => row.channel_username || '-' },
  { title: t('channels.chatId'), key: 'channel_id', render: (row) => row.channel_id || '-' },
  {
    title: t('common.enabled'),
    key: 'enabled',
    width: 80,
    render: (row) => h(NTag, { type: row.enabled ? 'success' : 'default', size: 'small' }, () => row.enabled ? t('common.yes') : t('common.no')),
  },
]

async function handleCreate() {
  if (!createForm.value.id || !createForm.value.type || !createForm.value.name) {
    message.warning(t('channels.idTypeNameRequired'))
    return
  }
  if (createForm.value.type === 'telegram' || createForm.value.type === 'discord') {
    try {
      const cfg = JSON.parse(createForm.value.config || '{}')
      if (!cfg.token) {
        message.warning(t('channels.tokenRequired', { type: createForm.value.type }))
        return
      }
    } catch {
      message.warning(t('channels.invalidConfig'))
      return
    }
  }
  creating.value = true
  try {
    await api.createChannelInstance({
      id: createForm.value.id,
      type: createForm.value.type,
      name: createForm.value.name,
      config: createForm.value.config || undefined,
    })
    showCreateModal.value = false
    createForm.value = { id: '', type: 'telegram', name: '', config: defaultConfigs['telegram'] }
    message.success(t('channels.created'))
    await loadInstances()
  } catch (e: any) {
    message.error(e.message || t('channels.createFailed'))
  } finally {
    creating.value = false
  }
}

async function handleUpdate() {
  if (!editInstance.value) return
  saving.value = true
  try {
    const data: { name?: string; config?: string; enabled?: boolean } = {
      enabled: editForm.value.enabled,
    }
    if (editInstance.value.source === 'dashboard') {
      data.name = editForm.value.name
      data.config = editForm.value.config
    }
    await api.updateChannelInstance(editInstance.value.id, data)
    showEditModal.value = false
    message.success(t('channels.updated'))
    await loadInstances()
  } catch (e: any) {
    message.error(e.message || t('channels.updateFailed'))
  } finally {
    saving.value = false
  }
}

async function handleDelete(row: ChannelInstance) {
  if (!confirm(t('channels.deleteConfirm', { name: row.name, id: row.id }))) return
  try {
    await api.deleteChannelInstance(row.id)
    message.success(t('channels.deleted'))
    await loadInstances()
  } catch (e: any) {
    message.error(e.message || t('channels.deleteFailed'))
  }
}

async function loadInstances() {
  loading.value = true
  try {
    instances.value = await api.listChannelInstances()
  } finally {
    loading.value = false
  }
}

onMounted(loadInstances)
</script>
