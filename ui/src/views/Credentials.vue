<template>
  <n-space vertical :size="16">
    <n-page-header :title="t('credentials.title')" :subtitle="t('credentials.subtitle')">
      <template #extra>
        <n-button type="primary" @click="openCreateModal">{{ t('credentials.create') }}</n-button>
      </template>
    </n-page-header>

    <n-data-table
      :columns="columns"
      :data="credentials"
      :loading="loading"
      :row-key="(r: CredentialView) => r.id"
    />

    <n-space>
      <n-text :depth="3">{{ t('credentials.total', { count: credentials.length }) }}</n-text>
    </n-space>

    <!-- Create credential modal -->
    <n-modal v-model:show="showCreateModal" preset="dialog" :title="t('credentials.createTitle')">
      <n-form>
        <n-form-item :label="t('credentials.name')">
          <n-input v-model:value="createForm.name" :placeholder="t('credentials.namePlaceholder')" />
        </n-form-item>
        <n-form-item :label="t('credentials.type')">
          <n-select v-model:value="createForm.type" :options="typeOptions" />
        </n-form-item>
        <n-form-item :label="t('credentials.scope')">
          <n-select v-model:value="createForm.scope" :options="scopeOptions" />
        </n-form-item>
        <n-form-item :label="t('credentials.value')">
          <n-input
            v-model:value="createForm.value"
            type="password"
            show-password-on="click"
            :placeholder="t('credentials.valuePlaceholder')"
          />
        </n-form-item>
        <n-form-item :label="t('credentials.description')">
          <n-input
            v-model:value="createForm.description"
            type="textarea"
            :rows="2"
            :placeholder="t('credentials.descriptionPlaceholder')"
          />
        </n-form-item>
        <n-form-item :label="t('credentials.tags')">
          <n-dynamic-tags v-model:value="createForm.tags" />
        </n-form-item>
        <n-form-item :label="t('credentials.expiresAt')">
          <n-date-picker
            v-model:value="createForm.expires_at"
            type="datetime"
            clearable
            :placeholder="t('credentials.expiresAtPlaceholder')"
            style="width: 100%"
          />
        </n-form-item>
      </n-form>
      <template #action>
        <n-button @click="showCreateModal = false">{{ t('common.cancel') }}</n-button>
        <n-button type="primary" :loading="creating" @click="handleCreate">{{ t('common.create') }}</n-button>
      </template>
    </n-modal>

    <!-- Detail drawer -->
    <n-drawer v-model:show="showDetailDrawer" :width="560" placement="right">
      <n-drawer-content :title="detailCredential?.name ?? ''" closable>
        <n-tabs type="line" animated>
          <!-- Details tab -->
          <n-tab-pane :name="t('credentials.detailsTab')" :tab="t('credentials.detailsTab')">
            <n-descriptions v-if="detailCredential" bordered :column="1" label-placement="left">
              <n-descriptions-item :label="t('credentials.name')">
                {{ detailCredential.name }}
              </n-descriptions-item>
              <n-descriptions-item :label="t('credentials.type')">
                <n-tag type="info" size="small">{{ detailCredential.type }}</n-tag>
              </n-descriptions-item>
              <n-descriptions-item :label="t('credentials.scope')">
                <n-tag :type="detailCredential.scope === 'global' ? 'warning' : 'default'" size="small">
                  {{ detailCredential.scope }}
                </n-tag>
              </n-descriptions-item>
              <n-descriptions-item :label="t('credentials.status')">
                <n-tag :type="detailCredential.has_value ? 'success' : 'warning'" size="small">
                  {{ detailCredential.has_value ? t('credentials.valueSet') : t('credentials.notSet') }}
                </n-tag>
              </n-descriptions-item>
              <n-descriptions-item :label="t('credentials.encrypted')">
                <n-tag :type="detailCredential.encrypted ? 'success' : 'default'" size="small">
                  {{ detailCredential.encrypted ? t('common.yes') : t('common.no') }}
                </n-tag>
              </n-descriptions-item>
              <n-descriptions-item :label="t('credentials.description')">
                {{ detailCredential.description || '-' }}
              </n-descriptions-item>
              <n-descriptions-item :label="t('credentials.tags')">
                <n-space v-if="detailCredential.tags && detailCredential.tags.length" :size="4">
                  <n-tag v-for="tag in detailCredential.tags" :key="tag" size="small">{{ tag }}</n-tag>
                </n-space>
                <n-text v-else :depth="3">-</n-text>
              </n-descriptions-item>
              <n-descriptions-item :label="t('credentials.ownerId')">
                {{ detailCredential.owner_id || '-' }}
              </n-descriptions-item>
              <n-descriptions-item :label="t('credentials.createdBy')">
                {{ detailCredential.created_by || '-' }}
              </n-descriptions-item>
              <n-descriptions-item :label="t('credentials.updatedBy')">
                {{ detailCredential.updated_by || '-' }}
              </n-descriptions-item>
              <n-descriptions-item :label="t('credentials.createdAt')">
                {{ detailCredential.created_at ? new Date(detailCredential.created_at).toLocaleString() : '-' }}
              </n-descriptions-item>
              <n-descriptions-item :label="t('credentials.updatedAt')">
                {{ detailCredential.updated_at ? new Date(detailCredential.updated_at).toLocaleString() : '-' }}
              </n-descriptions-item>
              <n-descriptions-item :label="t('credentials.rotatedAt')">
                {{ detailCredential.rotated_at ? new Date(detailCredential.rotated_at).toLocaleString() : '-' }}
              </n-descriptions-item>
              <n-descriptions-item :label="t('credentials.expiresAt')">
                {{ detailCredential.expires_at ? new Date(detailCredential.expires_at).toLocaleString() : '-' }}
              </n-descriptions-item>
            </n-descriptions>

            <n-divider />

            <n-form-item :label="t('credentials.updateValue')">
              <n-input
                v-model:value="rotateValue"
                type="password"
                show-password-on="click"
                :placeholder="t('credentials.newValuePlaceholder')"
              />
            </n-form-item>
            <n-space :size="8">
              <n-button
                type="primary"
                :loading="rotating"
                :disabled="!rotateValue"
                @click="handleRotate"
              >
                {{ t('credentials.rotate') }}
              </n-button>
              <n-popconfirm @positive-click="handleDelete">
                <template #trigger>
                  <n-button type="error" :loading="deleting">{{ t('common.delete') }}</n-button>
                </template>
                {{ t('credentials.deleteConfirm', { name: detailCredential?.name }) }}
              </n-popconfirm>
            </n-space>
          </n-tab-pane>

          <!-- Bindings tab -->
          <n-tab-pane :name="t('credentials.bindingsTab')" :tab="t('credentials.bindingsTab')">
            <n-data-table
              :columns="bindingColumns"
              :data="detailBindings"
              :loading="loadingBindings"
              size="small"
            />
            <n-empty v-if="!loadingBindings && detailBindings.length === 0" :description="t('credentials.noBindings')" />

            <n-divider />

            <n-text strong>{{ t('credentials.addBinding') }}</n-text>
            <n-form style="margin-top: 8px">
              <n-form-item :label="t('credentials.consumerType')">
                <n-select v-model:value="bindForm.consumer_type" :options="consumerTypeOptions" />
              </n-form-item>
              <n-form-item :label="t('credentials.consumerId')">
                <n-input v-model:value="bindForm.consumer_id" :placeholder="t('credentials.consumerIdPlaceholder')" />
              </n-form-item>
              <n-button
                type="primary"
                size="small"
                :loading="binding"
                :disabled="!bindForm.consumer_type || !bindForm.consumer_id"
                @click="handleBind"
              >
                {{ t('credentials.bind') }}
              </n-button>
            </n-form>
          </n-tab-pane>

          <!-- HTTP origin grants tab -->
          <n-tab-pane :name="t('credentials.httpGrantsTab')" :tab="t('credentials.httpGrantsTab')">
            <n-text :depth="3" style="display: block; margin-bottom: 8px">
              {{ t('credentials.httpGrantsHint') }}
            </n-text>
            <n-data-table
              :columns="grantColumns"
              :data="detailGrants"
              :loading="loadingGrants"
              size="small"
            />
            <n-empty v-if="!loadingGrants && detailGrants.length === 0" :description="t('credentials.noGrants')" />

            <n-divider />

            <n-text strong>{{ t('credentials.addGrant') }}</n-text>
            <n-form style="margin-top: 8px">
              <n-form-item :label="t('credentials.grantPrincipal')">
                <n-select v-model:value="grantForm.principal_type" :options="grantPrincipalOptions" />
              </n-form-item>
              <n-form-item v-if="grantForm.principal_type === 'user'" :label="t('credentials.grantPrincipalId')">
                <n-input v-model:value="grantForm.principal_id" :placeholder="t('credentials.grantPrincipalIdPlaceholder')" />
              </n-form-item>
              <n-form-item :label="t('credentials.grantCaller')">
                <n-input v-model:value="grantForm.caller_id" placeholder="http_request" />
              </n-form-item>
              <n-form-item :label="t('credentials.grantOrigin')">
                <n-input-group>
                  <n-select v-model:value="grantForm.scheme" :options="grantSchemeOptions" style="width: 110px" />
                  <n-input v-model:value="grantForm.host" placeholder="api.example.com" />
                  <n-input v-model:value="grantForm.port" placeholder="443" style="width: 90px" />
                </n-input-group>
              </n-form-item>
              <n-form-item :label="t('credentials.grantUsages')">
                <n-select v-model:value="grantForm.usages" multiple :options="grantUsageOptions" />
              </n-form-item>
              <n-button
                type="primary"
                size="small"
                :loading="granting"
                :disabled="!grantForm.host || (grantForm.principal_type === 'user' && !grantForm.principal_id)"
                @click="handleCreateGrant"
              >
                {{ t('credentials.addGrant') }}
              </n-button>
            </n-form>
          </n-tab-pane>

          <!-- Audit tab -->
          <n-tab-pane :name="t('credentials.auditTab')" :tab="t('credentials.auditTab')">
            <n-data-table
              :columns="auditColumns"
              :data="auditEntries"
              :loading="loadingAudit"
              size="small"
            />
            <n-empty v-if="!loadingAudit && auditEntries.length === 0" :description="t('credentials.noAudit')" />
          </n-tab-pane>
        </n-tabs>
      </n-drawer-content>
    </n-drawer>
  </n-space>
</template>

<script setup lang="ts">
import { ref, h, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  NSpace, NPageHeader, NDataTable, NButton, NTag, NText, NEmpty,
  NModal, NForm, NFormItem, NInput, NSelect,
  NDrawer, NDrawerContent, NTabs, NTabPane, NInputGroup,
  NDescriptions, NDescriptionsItem, NDynamicTags, NPopconfirm,
  NDivider, NDatePicker,
  useMessage,
} from 'naive-ui'
import type { DataTableColumn } from 'naive-ui'
import { api } from '../api'
import type {
  CredentialView,
  CredentialDetail,
  CredentialBinding,
  CredentialAudit,
  HTTPGrantView,
} from '../api'

const { t } = useI18n()
const message = useMessage()

// --- List state ---
const credentials = ref<CredentialView[]>([])
const loading = ref(false)

// --- Select options ---
const typeOptions = [
  { label: 'API Key', value: 'api_key' },
  { label: 'Bearer Token', value: 'bearer' },
  { label: 'HTTP Basic (JSON {"username","password"})', value: 'http_basic' },
  { label: 'OAuth2 Client', value: 'oauth2_client' },
  { label: 'OAuth2 Tokens', value: 'oauth2_tokens' },
  { label: 'Service Account JSON', value: 'service_account_json' },
  { label: 'Bot Token', value: 'bot_token' },
]

const scopeOptions = [
  { label: 'Global', value: 'global' },
  { label: 'User', value: 'user' },
]

const consumerTypeOptions = [
  { label: 'Config Key', value: 'config_key' },
  { label: 'Channel Instance', value: 'channel_instance' },
  { label: 'LLM Provider', value: 'llm_provider' },
  { label: 'Skill', value: 'skill' },
]

// --- Create modal ---
const showCreateModal = ref(false)
const creating = ref(false)
const createForm = ref({
  name: '',
  type: 'api_key',
  scope: 'global',
  value: '',
  description: '',
  tags: [] as string[],
  expires_at: null as number | null,
})

function openCreateModal() {
  createForm.value = {
    name: '',
    type: 'api_key',
    scope: 'global',
    value: '',
    description: '',
    tags: [],
    expires_at: null,
  }
  showCreateModal.value = true
}

async function handleCreate() {
  if (!createForm.value.name) {
    message.warning(t('credentials.nameRequired'))
    return
  }
  if (!createForm.value.value) {
    message.warning(t('credentials.valueRequired'))
    return
  }
  creating.value = true
  try {
    await api.createCredential({
      name: createForm.value.name,
      type: createForm.value.type,
      scope: createForm.value.scope,
      value: createForm.value.value,
      description: createForm.value.description,
      tags: createForm.value.tags,
      expires_at: createForm.value.expires_at
        ? new Date(createForm.value.expires_at).toISOString()
        : undefined,
    })
    showCreateModal.value = false
    message.success(t('credentials.created'))
    await loadCredentials()
  } catch (e: any) {
    message.error(e.message || t('credentials.error'))
  } finally {
    creating.value = false
  }
}

// --- Detail drawer ---
const showDetailDrawer = ref(false)
const detailCredential = ref<CredentialDetail | null>(null)
const detailBindings = ref<CredentialBinding[]>([])
const loadingBindings = ref(false)

// Rotate
const rotateValue = ref('')
const rotating = ref(false)

// Delete
const deleting = ref(false)

// Audit
const auditEntries = ref<CredentialAudit[]>([])
const loadingAudit = ref(false)

// Bind
const binding = ref(false)
const bindForm = ref({
  consumer_type: 'config_key',
  consumer_id: '',
})

// HTTP origin grants
const detailGrants = ref<HTTPGrantView[]>([])
const loadingGrants = ref(false)
const granting = ref(false)
const grantForm = ref({
  principal_type: 'user' as 'user' | 'global',
  principal_id: '',
  caller_id: 'http_request',
  scheme: 'https',
  host: '',
  port: '443',
  usages: ['basic', 'bearer', 'header', 'multipart'] as string[],
})

const grantPrincipalOptions = [
  { label: 'User', value: 'user' },
  { label: 'Global (requires caller binding)', value: 'global' },
]
const grantSchemeOptions = [
  { label: 'https', value: 'https' },
  { label: 'http', value: 'http' },
]
const grantUsageOptions = [
  { label: 'basic', value: 'basic' },
  { label: 'bearer', value: 'bearer' },
  { label: 'header', value: 'header' },
  { label: 'multipart', value: 'multipart' },
]

const grantColumns = [
  { title: 'Origin', key: 'origin', render: (g: HTTPGrantView) => `${g.scheme}://${g.host}:${g.port}` },
  { title: 'Principal', key: 'principal', render: (g: HTTPGrantView) => g.principal_type === 'user' ? `user ${g.principal_id || '?'}` : `global${g.caller_id ? ` (${g.caller_id})` : ''}` },
  { title: 'Usages', key: 'usages', render: (g: HTTPGrantView) => g.usages.join(', ') },
  { title: 'Expires', key: 'expires_at', render: (g: HTTPGrantView) => g.expires_at ? new Date(g.expires_at).toLocaleDateString() : '—' },
  {
    title: '',
    key: 'actions',
    width: 90,
    render: (g: HTTPGrantView) =>
      h(NButton, { size: 'tiny', type: 'error', quaternary: true, onClick: () => handleDeleteGrant(g) }, () => t('common.delete')),
  },
]

async function handleCreateGrant() {
  if (!detailCredential.value || !grantForm.value.host) return
  granting.value = true
  try {
    await api.createHTTPGrant(detailCredential.value.id, {
      ...grantForm.value,
      port: grantForm.value.port || (grantForm.value.scheme === 'https' ? '443' : '80'),
    } as Partial<HTTPGrantView>)
    message.success(t('credentials.grantAdded'))
    grantForm.value.host = ''
    detailGrants.value = await api.getHTTPGrants(detailCredential.value.id)
  } catch (e: any) {
    message.error(e.message || t('credentials.grantError'))
  } finally {
    granting.value = false
  }
}

async function handleDeleteGrant(g: HTTPGrantView) {
  try {
    await api.deleteHTTPGrant(g.id)
    message.success(t('credentials.grantDeleted'))
    if (detailCredential.value) {
      detailGrants.value = await api.getHTTPGrants(detailCredential.value.id)
    }
  } catch (e: any) {
    message.error(e.message || t('credentials.grantError'))
  }
}

async function openDetail(row: CredentialView) {
  showDetailDrawer.value = true
  rotateValue.value = ''
  bindForm.value = { consumer_type: 'config_key', consumer_id: '' }
  grantForm.value = {
    principal_type: 'user', principal_id: '', caller_id: 'http_request',
    scheme: 'https', host: '', port: '443', usages: ['basic', 'bearer', 'header', 'multipart'],
  }
  detailCredential.value = null
  detailBindings.value = []
  auditEntries.value = []
  detailGrants.value = []
  loadingBindings.value = true
  loadingAudit.value = true
  loadingGrants.value = true

  try {
    const detail = await api.getCredential(row.id)
    detailCredential.value = detail
    detailBindings.value = detail.bindings || []
  } catch (e: any) {
    message.error(e.message || t('credentials.error'))
  } finally {
    loadingBindings.value = false
  }

  try {
    auditEntries.value = await api.listCredentialAudit(row.id)
  } catch (e: any) {
    message.error(e.message || t('credentials.error'))
  } finally {
    loadingAudit.value = false
  }

  try {
    detailGrants.value = await api.getHTTPGrants(row.id)
  } catch {
    // Endpoint absent on older deployments — empty list, no error spam.
    detailGrants.value = []
  } finally {
    loadingGrants.value = false
  }
}

async function handleRotate() {
  if (!detailCredential.value || !rotateValue.value) return
  rotating.value = true
  try {
    await api.rotateCredential(detailCredential.value.id, rotateValue.value)
    message.success(t('credentials.rotated'))
    rotateValue.value = ''
    await openDetail(detailCredential.value)
    await loadCredentials()
  } catch (e: any) {
    message.error(e.message || t('credentials.error'))
  } finally {
    rotating.value = false
  }
}

async function handleDelete() {
  if (!detailCredential.value) return
  deleting.value = true
  try {
    await api.deleteCredential(detailCredential.value.id)
    message.success(t('credentials.deleted'))
    showDetailDrawer.value = false
    await loadCredentials()
  } catch (e: any) {
    message.error(e.message || t('credentials.error'))
  } finally {
    deleting.value = false
  }
}

async function handleBind() {
  if (!detailCredential.value) return
  binding.value = true
  try {
    await api.bindCredential(
      detailCredential.value.id,
      bindForm.value.consumer_type,
      bindForm.value.consumer_id,
    )
    message.success(t('credentials.bound'))
    bindForm.value = { consumer_type: 'config_key', consumer_id: '' }
    // Reload detail to refresh bindings
    await openDetail(detailCredential.value)
  } catch (e: any) {
    message.error(e.message || t('credentials.error'))
  } finally {
    binding.value = false
  }
}

async function handleUnbind(b: CredentialBinding) {
  if (!detailCredential.value) return
  try {
    await api.unbindCredential(
      detailCredential.value.id,
      b.consumer_type,
      b.consumer_id,
    )
    message.success(t('credentials.unbound'))
    await openDetail(detailCredential.value)
  } catch (e: any) {
    message.error(e.message || t('credentials.error'))
  }
}

// --- Table columns ---
const columns: DataTableColumn<CredentialView>[] = [
  {
    title: t('credentials.name'),
    key: 'name',
    ellipsis: { tooltip: true },
  },
  {
    title: t('credentials.type'),
    key: 'type',
    width: 150,
    render: (row) => h(NTag, { type: 'info', size: 'small', bordered: false }, () => row.type),
  },
  {
    title: t('credentials.scope'),
    key: 'scope',
    width: 100,
    render: (row) => h(NTag, {
      type: row.scope === 'global' ? 'warning' : 'default',
      size: 'small',
      bordered: false,
    }, () => row.scope),
  },
  {
    title: t('credentials.status'),
    key: 'has_value',
    width: 140,
    render: (row) => h(NSpace, { size: 4, align: 'center' }, () => [
      h(NTag, {
        type: row.has_value ? 'success' : 'warning',
        size: 'small',
      }, () => row.has_value ? t('credentials.valueSet') : t('credentials.notSet')),
      row.encrypted ? h(NTag, { type: 'info', size: 'small', bordered: false }, () => '🔒') : null,
    ]),
  },
  {
    title: t('credentials.updatedAt'),
    key: 'updated_at',
    width: 170,
    render: (row) => row.updated_at ? new Date(row.updated_at).toLocaleString() : '-',
  },
  {
    title: t('common.actions'),
    key: 'actions',
    width: 120,
    render: (row) => h(NSpace, { size: 'small' }, () => [
      h(NButton, {
        size: 'small',
        quaternary: true,
        onClick: () => openDetail(row),
      }, () => t('credentials.details')),
    ]),
  },
]

const bindingColumns: DataTableColumn<CredentialBinding>[] = [
  {
    title: t('credentials.consumerType'),
    key: 'consumer_type',
    width: 140,
    render: (row) => h(NTag, { size: 'small', bordered: false }, () => row.consumer_type),
  },
  {
    title: t('credentials.consumerId'),
    key: 'consumer_id',
    ellipsis: { tooltip: true },
  },
  {
    title: t('credentials.createdBy'),
    key: 'created_by',
    width: 120,
  },
  {
    title: t('credentials.createdAt'),
    key: 'created_at',
    width: 160,
    render: (row) => row.created_at ? new Date(row.created_at).toLocaleString() : '-',
  },
  {
    title: t('common.actions'),
    key: 'actions',
    width: 90,
    render: (row) => h(
      NPopconfirm,
      { onPositiveClick: () => handleUnbind(row) },
      {
        trigger: () => h(NButton, { size: 'small', quaternary: true, type: 'error' }, () => t('credentials.unbind')),
        default: () => t('credentials.unbindConfirm'),
      },
    ),
  },
]

const auditColumns: DataTableColumn<CredentialAudit>[] = [
  {
    title: t('credentials.auditTime'),
    key: 'created_at',
    width: 170,
    render: (row) => row.created_at ? new Date(row.created_at).toLocaleString() : '-',
  },
  {
    title: t('credentials.auditAction'),
    key: 'action',
    width: 120,
    render: (row) => h(NTag, { size: 'small', bordered: false }, () => row.action),
  },
  {
    title: t('credentials.auditActor'),
    key: 'actor',
    width: 130,
  },
  {
    title: t('credentials.auditDetail'),
    key: 'detail',
    ellipsis: { tooltip: true },
  },
]

// --- Load data ---
async function loadCredentials() {
  loading.value = true
  try {
    credentials.value = await api.listCredentials()
  } catch (e: any) {
    message.error(e.message || t('credentials.error'))
  } finally {
    loading.value = false
  }
}

onMounted(loadCredentials)
</script>
