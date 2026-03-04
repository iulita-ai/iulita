<template>
  <n-space vertical :size="16">
    <n-page-header :title="t('users.title')" :subtitle="t('users.subtitle')" />

    <n-button type="primary" @click="showCreate = true">{{ t('users.createUser') }}</n-button>

    <n-data-table :columns="columns" :data="users" :loading="loading" :row-key="(r: UserInfo) => r.id" />

    <!-- Create User Modal -->
    <n-modal v-model:show="showCreate" preset="dialog" :title="t('users.createUser')" :positive-text="t('common.create')" @positive-click="handleCreate">
      <n-form>
        <n-form-item :label="t('users.username')">
          <n-input v-model:value="newUser.username" />
        </n-form-item>
        <n-form-item :label="t('users.password')">
          <n-input v-model:value="newUser.password" type="password" show-password-on="click" />
        </n-form-item>
        <n-form-item :label="t('users.role')">
          <n-select v-model:value="newUser.role" :options="roleOptions" />
        </n-form-item>
        <n-form-item :label="t('users.displayName')">
          <n-input v-model:value="newUser.display_name" />
        </n-form-item>
      </n-form>
    </n-modal>

    <!-- Channels Modal -->
    <n-modal v-model:show="showChannels" preset="card" :title="t('users.channelBindings')" style="width: 600px">
      <n-space vertical :size="12">
        <n-data-table :columns="channelColumns" :data="channels" size="small" :row-key="(r: UserChannel) => r.id" />
        <n-divider />
        <n-text strong>{{ t('users.bindNewChannel') }}</n-text>
        <n-space>
          <n-input v-model:value="newChannel.channel_type" :placeholder="t('users.typePlaceholder')" style="width: 120px" />
          <n-input v-model:value="newChannel.channel_user_id" :placeholder="t('users.userIdPlaceholder')" style="width: 140px" />
          <n-input v-model:value="newChannel.channel_id" :placeholder="t('users.chatIdPlaceholder')" style="width: 140px" />
          <n-button type="primary" size="small" @click="handleBindChannel">{{ t('users.bind') }}</n-button>
        </n-space>
      </n-space>
    </n-modal>
  </n-space>
</template>

<script setup lang="ts">
import { ref, h, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { NSpace, NPageHeader, NDataTable, NButton, NModal, NForm, NFormItem, NInput, NSelect, NDivider, NText, NPopconfirm, NTag } from 'naive-ui'
import type { DataTableColumn } from 'naive-ui'
import { api } from '../api'
import type { UserInfo, UserChannel } from '../api'

const { t } = useI18n()

const users = ref<UserInfo[]>([])
const loading = ref(false)

const showCreate = ref(false)
const newUser = ref({ username: '', password: '', role: 'user', display_name: '' })

const showChannels = ref(false)
const selectedUserId = ref('')
const channels = ref<UserChannel[]>([])
const newChannel = ref({ channel_type: 'telegram', channel_user_id: '', channel_id: '' })

const roleOptions = [
  { label: t('users.regular'), value: 'user' },
  { label: t('users.admin'), value: 'admin' },
]

const columns: DataTableColumn<UserInfo>[] = [
  { title: t('users.username'), key: 'username' },
  { title: t('users.role'), key: 'role', render: (row) => h(NTag, { type: row.role === 'admin' ? 'warning' : 'info', size: 'small' }, () => row.role) },
  { title: t('users.displayName'), key: 'display_name' },
  { title: t('facts.created'), key: 'created_at', render: (row) => new Date(row.created_at).toLocaleDateString() },
  {
    title: t('common.actions'),
    key: 'actions',
    render: (row) =>
      h(NSpace, { size: 'small' }, () => [
        h(NButton, { size: 'tiny', onClick: () => openChannels(row.id) }, () => t('users.channels')),
        h(
          NPopconfirm,
          { onPositiveClick: () => handleDelete(row.id) },
          { trigger: () => h(NButton, { size: 'tiny', type: 'error' }, () => t('common.delete')), default: () => t('users.deleteConfirm') }
        ),
      ]),
  },
]

const channelColumns: DataTableColumn<UserChannel>[] = [
  { title: t('users.type'), key: 'channel_type', width: 100 },
  { title: t('users.userIdPlaceholder'), key: 'channel_user_id' },
  { title: t('users.chatIdPlaceholder'), key: 'channel_id' },
  { title: t('users.username'), key: 'channel_username' },
  {
    title: '',
    key: 'actions',
    width: 80,
    render: (row) =>
      h(NButton, { size: 'tiny', type: 'error', onClick: () => handleUnbindChannel(row.id) }, () => t('users.unbind')),
  },
]

async function loadUsers() {
  loading.value = true
  try {
    users.value = await api.listUsers()
  } finally {
    loading.value = false
  }
}

async function handleCreate() {
  await api.createUser(newUser.value)
  newUser.value = { username: '', password: '', role: 'user', display_name: '' }
  showCreate.value = false
  loadUsers()
}

async function handleDelete(id: string) {
  await api.deleteUser(id)
  loadUsers()
}

async function openChannels(userId: string) {
  selectedUserId.value = userId
  channels.value = await api.listUserChannels(userId)
  showChannels.value = true
}

async function handleBindChannel() {
  await api.bindChannel(selectedUserId.value, newChannel.value)
  channels.value = await api.listUserChannels(selectedUserId.value)
  newChannel.value = { channel_type: 'telegram', channel_user_id: '', channel_id: '' }
}

async function handleUnbindChannel(channelId: number) {
  await api.unbindChannel(selectedUserId.value, channelId)
  channels.value = await api.listUserChannels(selectedUserId.value)
}

onMounted(loadUsers)
</script>
