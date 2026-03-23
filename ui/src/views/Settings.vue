<template>
  <n-space vertical :size="24">
    <n-h2>{{ t('settings.title') }}</n-h2>

    <n-card :title="t('settings.systemInfo')">
      <n-descriptions bordered :column="2" label-placement="left">
        <n-descriptions-item :label="t('settings.app')">{{ system?.app ?? '-' }}</n-descriptions-item>
        <n-descriptions-item :label="t('settings.version')">{{ system?.version ?? '-' }}</n-descriptions-item>
        <n-descriptions-item :label="t('settings.uptime')">{{ system?.uptime ?? '-' }}</n-descriptions-item>
        <n-descriptions-item :label="t('settings.goVersion')">{{ system?.go_version ?? '-' }}</n-descriptions-item>
        <n-descriptions-item :label="t('settings.startedAt')">{{ system?.started_at ? new Date(system.started_at).toLocaleString() : '-' }}</n-descriptions-item>
      </n-descriptions>
    </n-card>

    <n-card :title="t('settings.googleWorkspace')">
      <template #header-extra>
        <n-space :size="8">
          <n-button size="small" quaternary @click="openSkillConfig('google_workspace')">{{ t('settings.configure') }}</n-button>
          <n-button size="small" @click="loadGoogleAccounts(); loadGoogleStatus()">{{ t('common.refresh') }}</n-button>
        </n-space>
      </template>

      <!-- Credential Status -->
      <n-card size="small" embedded style="margin-bottom: 16px;">
        <template #header>
          <n-space :size="8" align="center">
            <n-text strong style="font-size: 13px;">{{ t('settings.credentialStatus') }}</n-text>
            <n-tag
              v-if="googleStatus"
              :type="sourceTagType(googleStatus.source)"
              size="small"
            >
              {{ googleStatus.source === 'none' ? t('settings.notConfigured') : t('settings.active') }}
            </n-tag>
          </n-space>
        </template>
        <n-descriptions v-if="googleStatus" bordered :column="2" label-placement="left" size="small">
          <n-descriptions-item :label="t('settings.source')">
            {{ sourceLabels[googleStatus.source] || googleStatus.source }}
          </n-descriptions-item>
          <n-descriptions-item :label="t('settings.scopes')">
            {{ googleStatus.active_scopes || t('settings.default') }}
          </n-descriptions-item>
          <n-descriptions-item v-if="googleStatus.credential_type" :label="t('settings.type')">
            {{ googleStatus.credential_type }}
          </n-descriptions-item>
          <n-descriptions-item v-if="googleStatus.file_path" :label="t('settings.file')">
            <n-text code>{{ googleStatus.file_path }}</n-text>
          </n-descriptions-item>
          <n-descriptions-item v-if="googleStatus.db_accounts > 0" :label="t('settings.dbAccounts')">
            {{ googleStatus.db_accounts }}
          </n-descriptions-item>
        </n-descriptions>
        <n-space v-if="googleStatus" :size="8" style="margin-top: 8px;">
          <n-upload
            :show-file-list="false"
            accept=".json,application/json"
            :custom-request="handleCredentialUpload"
          >
            <n-button size="small" type="primary" :loading="credentialUploading">
              {{ t('settings.uploadCredentials') }}
            </n-button>
          </n-upload>
        </n-space>
        <n-text v-if="googleStatus && googleStatus.source === 'none'" :depth="3" style="display: block; margin-top: 8px; font-size: 12px;">
          {{ t('settings.credentialHint') }}
        </n-text>
      </n-card>

      <!-- Connected Accounts (OAuth2) -->
      <n-space :size="8" align="center" style="margin-bottom: 8px;">
        <n-text strong style="font-size: 13px;">{{ t('settings.connectedAccounts') }}</n-text>
        <n-input v-model:value="googleAlias" :placeholder="t('settings.aliasPlaceholder')" size="small" style="width: 150px" />
        <n-button type="primary" size="small" :loading="googleConnecting" @click="connectGoogle">
          {{ t('settings.connectAccount') }}
        </n-button>
      </n-space>
      <n-data-table
        :columns="googleColumns"
        :data="googleAccounts"
        :max-height="300"
        size="small"
        :row-key="(row: GoogleAccount) => row.id"
      />
      <n-empty v-if="googleAccounts.length === 0" :description="t('settings.noGoogleAccounts')" />
    </n-card>

    <n-card :title="t('settings.systemConfig')">
      <template #header-extra>
        <n-space :size="8">
          <n-button size="small" :loading="schemaLoading" @click="loadSchema">{{ t('common.refresh') }}</n-button>
        </n-space>
      </template>

      <n-spin v-if="schemaLoading" />
      <n-collapse v-else>
        <n-collapse-item
          v-for="section in schemaSections"
          :key="section.name"
          :title="section.label"
          :name="section.name"
        >
          <template #header-extra>
            <n-text :depth="3" style="font-size: 12px;">{{ section.description }}</n-text>
          </template>
          <n-space vertical :size="12">
            <div v-for="field in section.fields" :key="field.key" style="border-bottom: 1px solid var(--n-border-color); padding-bottom: 12px;">
              <n-space vertical :size="4">
                <n-space :size="8" align="center">
                  <n-text strong style="font-size: 13px;">{{ field.label }}</n-text>
                  <n-tag v-if="field.required" size="small" type="error" :bordered="false">{{ t('settings.required') }}</n-tag>
                  <n-tag v-if="field.secret" size="small" type="warning" :bordered="false">{{ t('settings.secret') }}</n-tag>
                  <n-tag v-if="field.has_override" size="small" type="info" :bordered="false">{{ t('settings.override') }}</n-tag>
                </n-space>
                <n-text :depth="3" style="font-size: 12px;">{{ field.description }} <code>{{ field.key }}</code></n-text>

                <n-space :size="8" align="center" style="margin-top: 4px;">
                  <!-- Secret field -->
                  <template v-if="field.secret">
                    <n-space vertical :size="4" style="flex: 1;">
                      <n-tag v-if="field.has_value" size="small" type="success">{{ t('settings.valueSet') }}</n-tag>
                      <n-tag v-else size="small" type="warning">{{ t('settings.notConfigured') }}</n-tag>
                      <n-input
                        v-model:value="schemaEdits[field.key]"
                        type="password"
                        :placeholder="t('settings.enterNewValue')"
                        show-password-on="click"
                        style="max-width: 400px"
                      />
                    </n-space>
                  </template>

                  <!-- Bool field -->
                  <template v-else-if="field.type === 'bool'">
                    <n-switch
                      :value="schemaBoolValue(field)"
                      @update:value="(val: boolean) => schemaEdits[field.key] = val ? 'true' : 'false'"
                    />
                  </template>

                  <!-- Select field with dynamic models -->
                  <template v-else-if="field.type === 'select' || field.model_source">
                    <n-select
                      :value="schemaEdits[field.key] || field.value || field.default || null"
                      :options="schemaSelectOptions(field)"
                      :loading="modelLoading[field.key]"
                      filterable
                      tag
                      placeholder="Select..."
                      style="min-width: 280px"
                      @update:value="(val: string) => schemaEdits[field.key] = val"
                      @focus="maybeFetchModels(field)"
                    />
                  </template>

                  <!-- Int field -->
                  <template v-else-if="field.type === 'int'">
                    <n-input
                      v-model:value="schemaEdits[field.key]"
                      :placeholder="field.default || ''"
                      style="max-width: 200px"
                    />
                  </template>

                  <!-- URL / String field -->
                  <template v-else>
                    <n-input
                      v-model:value="schemaEdits[field.key]"
                      :placeholder="field.default || ''"
                      style="max-width: 400px"
                    />
                  </template>

                  <n-button
                    size="small"
                    type="primary"
                    :disabled="!schemaFieldChanged(field)"
                    :loading="schemaSaving[field.key]"
                    @click="saveSchemaField(field)"
                  >
                    {{ t('common.save') }}
                  </n-button>
                  <n-popconfirm
                    v-if="field.has_override"
                    @positive-click="resetSchemaField(field.key)"
                  >
                    <template #trigger>
                      <n-button size="small" type="warning" quaternary>{{ t('settings.reset') }}</n-button>
                    </template>
                    {{ t('settings.resetFieldConfirm', { key: field.key }) }}
                  </n-popconfirm>
                </n-space>
              </n-space>
            </div>
            <n-empty v-if="section.fields.length === 0" :description="t('settings.noFields')" />
          </n-space>
        </n-collapse-item>
      </n-collapse>
    </n-card>

    <n-card :title="t('settings.configOverrides')">
      <template #header-extra>
        <n-space :size="8">
          <n-tag v-if="encryptionEnabled" type="success" size="small">{{ t('settings.encryptionOn') }}</n-tag>
          <n-tag v-else type="warning" size="small">{{ t('settings.encryptionOff') }}</n-tag>
          <n-button size="small" @click="loadConfig">{{ t('common.refresh') }}</n-button>
        </n-space>
      </template>

      <n-space vertical :size="16">
        <!-- Add new override -->
        <n-card size="small" embedded :title="t('settings.addOverride')">
          <n-space :size="8" align="center">
            <n-input v-model:value="newKey" placeholder="Key (e.g. claude.thinking)" style="width: 250px" />
            <n-input
              v-model:value="newValue"
              :type="newEncrypt ? 'password' : 'text'"
              placeholder="Value"
              style="width: 300px"
              show-password-on="click"
            />
            <n-switch v-model:value="newEncrypt" :disabled="!encryptionEnabled">
              <template #checked>{{ t('settings.encrypted') }}</template>
              <template #unchecked>{{ t('settings.plain') }}</template>
            </n-switch>
            <n-button type="primary" size="small" :loading="saving" @click="saveConfig">
              {{ t('common.save') }}
            </n-button>
          </n-space>
        </n-card>

        <!-- Existing overrides -->
        <n-data-table
          :columns="configColumns"
          :data="configEntries"
          :max-height="400"
          size="small"
          :row-key="(row: ConfigEntry) => row.key"
        />
        <n-empty v-if="configEntries.length === 0" :description="t('settings.noOverrides')" />
      </n-space>
    </n-card>

    <n-card :title="t('settings.schedulers')">
      <n-space vertical :size="12">
        <n-card v-for="job in schedulers" :key="job.name" :title="job.name" size="small" embedded>
          <n-descriptions bordered :column="2" label-placement="left" size="small">
            <n-descriptions-item :label="t('settings.status')">
              <n-tag :type="job.enabled ? 'success' : 'default'" size="small">
                {{ job.enabled ? t('common.enabled') : t('common.disabled') }}
              </n-tag>
            </n-descriptions-item>
            <n-descriptions-item :label="t('settings.interval')">{{ job.interval }}</n-descriptions-item>
            <n-descriptions-item :label="t('settings.lastRun')">
              {{ job.last_run ? new Date(job.last_run).toLocaleString() : t('settings.never') }}
            </n-descriptions-item>
            <n-descriptions-item :label="t('settings.nextRun')">
              {{ job.next_run ? new Date(job.next_run).toLocaleString() : '-' }}
            </n-descriptions-item>
          </n-descriptions>
          <template #action>
            <n-button
              type="primary"
              size="small"
              :loading="triggeringJob === job.name"
              @click="triggerJob(job.name)"
            >
              {{ t('settings.triggerNow') }}
            </n-button>
          </template>
        </n-card>
        <n-empty v-if="schedulers.length === 0" :description="t('settings.noSchedulers')" />
      </n-space>
    </n-card>

    <n-card :title="t('settings.taskQueue')">
      <template #header-extra>
        <n-button size="small" @click="loadTasks">{{ t('common.refresh') }}</n-button>
      </template>
      <n-space :size="12" style="margin-bottom: 16px">
        <n-tag v-for="(count, status) in taskCounts" :key="status" :type="statusTagType(status as string)">
          {{ status }}: {{ count }}
        </n-tag>
      </n-space>
      <n-data-table
        :columns="taskColumns"
        :data="tasks"
        :max-height="400"
        size="small"
        :row-key="(row: Task) => row.id"
      />
    </n-card>

    <n-card :title="t('settings.skills')">
      <template #header-extra>
        <n-button size="small" @click="loadSkills">{{ t('common.refresh') }}</n-button>
      </template>
      <n-list bordered>
        <!-- Skill groups (by manifest) -->
        <template v-for="group in skillGroups" :key="group.name">
          <n-list-item>
            <n-thing :title="group.name" :description="group.description">
              <template #header-extra>
                <n-space :size="8" align="center">
                  <n-tag v-if="!group.hasCapabilities" size="small" type="warning" :bordered="false">
                    {{ t('settings.missingCapability') }}
                  </n-tag>
                  <n-tag size="small" type="info" :bordered="false">
                    {{ t('settings.tools', { count: group.skills.length }) }}
                  </n-tag>
                  <n-button
                    v-if="group.hasConfig"
                    size="tiny"
                    quaternary
                    @click="openSkillConfig(group.name)"
                  >
                    {{ t('settings.configure') }}
                  </n-button>
                  <n-switch
                    :value="group.enabled"
                    :disabled="togglingSkill === group.name"
                    @update:value="(val: boolean) => toggleGroup(group.name, val)"
                    size="small"
                  />
                </n-space>
              </template>
            </n-thing>
            <!-- Expand to show individual skills -->
            <div v-if="group.skills.length > 1" style="padding-left: 24px; margin-top: 4px;">
              <n-space vertical :size="2">
                <div v-for="sk in group.skills" :key="sk.name" style="display: flex; justify-content: space-between; align-items: center; padding: 2px 0;">
                  <n-text :depth="3" style="font-size: 13px;">{{ sk.name }}</n-text>
                  <n-switch
                    :value="sk.enabled"
                    :disabled="togglingSkill === sk.name"
                    @update:value="(val: boolean) => toggleSkill(sk.name, val)"
                    size="small"
                  />
                </div>
              </n-space>
            </div>
          </n-list-item>
        </template>
        <!-- Standalone skills (no manifest group) -->
        <n-list-item v-for="sk in standaloneSkills" :key="sk.name">
          <n-thing :title="sk.name" :description="sk.description">
            <template #header-extra>
              <n-space :size="8" align="center">
                <n-tag size="small" :type="sk.type === 'tool' ? 'info' : 'warning'" :bordered="false">
                  {{ sk.type }}
                </n-tag>
                <n-switch
                  v-if="sk.type === 'tool'"
                  :value="sk.enabled"
                  :disabled="togglingSkill === sk.name"
                  @update:value="(val: boolean) => toggleSkill(sk.name, val)"
                  size="small"
                />
              </n-space>
            </template>
          </n-thing>
        </n-list-item>
        <n-empty v-if="skills.length === 0" :description="t('settings.noSkills')" />
      </n-list>
    </n-card>

    <!-- Skill Config Drawer -->
    <n-drawer v-model:show="drawerVisible" :width="520" placement="right">
      <n-drawer-content :title="`${t('settings.configure')}: ${drawerSkill}`" closable>
        <n-spin v-if="drawerLoading" />
        <n-space v-else vertical :size="16">
          <n-empty v-if="drawerFields.length === 0" :description="t('settings.noConfigKeys')" />
          <n-card
            v-for="field in drawerFields"
            :key="field.key"
            size="small"
            embedded
          >
            <template #header>
              <n-space :size="8" align="center">
                <span style="font-family: monospace; font-size: 13px">{{ shortKey(field.key) }}</span>
                <n-tag v-if="field.secret" size="small" type="error" :bordered="false">{{ t('settings.secret') }}</n-tag>
                <n-tag v-if="field.has_override" size="small" type="info" :bordered="false">{{ t('settings.override') }}</n-tag>
              </n-space>
            </template>
            <template #header-extra>
              <n-popconfirm
                v-if="field.has_override"
                @positive-click="resetSkillKey(field.key)"
              >
                <template #trigger>
                  <n-button size="tiny" type="warning" quaternary>{{ t('settings.reset') }}</n-button>
                </template>
                {{ t('settings.resetSkillKeyConfirm', { key: shortKey(field.key) }) }}
              </n-popconfirm>
            </template>

            <!-- Secret field -->
            <template v-if="field.secret">
              <n-space vertical :size="8">
                <n-tag v-if="field.has_value" size="small" type="success">{{ t('settings.valueSet') }}</n-tag>
                <n-tag v-else size="small" type="warning">{{ t('settings.notConfigured') }}</n-tag>

                <!-- Credential selector -->
                <n-select
                  :value="fieldCredentialBindings[field.key] ?? null"
                  @update:value="v => bindCredentialToField(field.key, v)"
                  :options="availableCredentials.map(c => ({ label: c.name + ' (' + c.type + ')', value: c.id }))"
                  clearable
                  :placeholder="t('settings.selectCredential')"
                  :loading="fieldSaving[field.key]"
                  style="max-width: 400px"
                />

                <!-- Direct value input — only when no credential is bound -->
                <template v-if="!fieldCredentialBindings[field.key]">
                  <n-text :depth="3" style="font-size: 12px;">{{ t('settings.orEnterDirectly') }}</n-text>
                  <n-input
                    v-model:value="fieldEdits[field.key]"
                    type="password"
                    :placeholder="t('settings.enterNewValue')"
                    show-password-on="click"
                    style="max-width: 400px"
                  />
                  <n-button
                    size="small"
                    type="primary"
                    :disabled="!fieldEdits[field.key]"
                    :loading="fieldSaving[field.key]"
                    @click="saveSkillKey(field.key)"
                  >
                    {{ t('common.save') }}
                  </n-button>
                </template>
              </n-space>
            </template>

            <!-- Credentials file field — upload instead of text input -->
            <template v-else-if="field.key.endsWith('.credentials_file')">
              <n-space vertical :size="8">
                <n-text v-if="field.has_value" code style="font-size: 12px;">{{ field.value }}</n-text>
                <n-text v-else :depth="3" style="font-size: 12px;">{{ t('settings.notSet') }}</n-text>
                <n-upload
                  :show-file-list="false"
                  accept=".json,application/json"
                  :custom-request="(opts: any) => handleDrawerCredentialUpload(opts, field.key)"
                >
                  <n-button size="small" type="primary" :loading="fieldSaving[field.key]">
                    {{ t('settings.uploadJsonFile') }}
                  </n-button>
                </n-upload>
              </n-space>
            </template>

            <!-- Regular field -->
            <template v-else>
              <n-space vertical :size="8">
                <n-input
                  v-if="field.key.endsWith('.system_prompt')"
                  v-model:value="fieldEdits[field.key]"
                  type="textarea"
                  :autosize="{ minRows: 2, maxRows: 10 }"
                  :placeholder="field.has_value ? '' : t('settings.notSet')"
                  style="max-width: 460px"
                />
                <n-input
                  v-else
                  v-model:value="fieldEdits[field.key]"
                  :placeholder="field.has_value ? '' : t('settings.notSet')"
                  style="max-width: 400px"
                />
                <n-button
                  size="small"
                  type="primary"
                  :disabled="fieldEdits[field.key] === (field.value ?? '') || fieldEdits[field.key] === undefined"
                  :loading="fieldSaving[field.key]"
                  @click="saveSkillKey(field.key)"
                >
                  {{ t('common.save') }}
                </n-button>
              </n-space>
            </template>
          </n-card>
        </n-space>
      </n-drawer-content>
    </n-drawer>

    <n-card :title="t('settings.directives')">
      <n-select
        v-model:value="selectedChat"
        :options="chatOptions"
        :placeholder="t('settings.selectChat')"
        style="max-width: 300px; margin-bottom: 16px"
      />
      <n-code v-if="directiveContent" :code="directiveContent" language="markdown" word-wrap />
      <n-empty v-else :description="t('settings.noDirective')" />
    </n-card>
  </n-space>
</template>

<script setup lang="ts">
import { ref, reactive, computed, onMounted, watch, h } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  NH2, NSpace, NCard, NDescriptions, NDescriptionsItem, NList, NListItem,
  NThing, NTag, NSelect, NCode, NEmpty, NButton, NDataTable, NInput,
  NSwitch, NPopconfirm, NDrawer, NDrawerContent, NSpin, NText, NCollapse,
  NCollapseItem, NUpload, useMessage,
} from 'naive-ui'
import type { DataTableColumns } from 'naive-ui'
import { api } from '../api'
import type { SystemInfo, SkillInfo, ChatInfo, JobInfo, Task, TaskCounts, ConfigEntry, GoogleAccount, GoogleCredentialStatus, SkillConfigField, ConfigSchemaSection, ConfigSchemaField, CredentialView } from '../api'

const { t } = useI18n()
const message = useMessage()
const system = ref<SystemInfo | null>(null)
const skills = ref<SkillInfo[]>([])
const chats = ref<ChatInfo[]>([])
const selectedChat = ref<string | null>(null)
const directiveContent = ref<string | null>(null)
const schedulers = ref<JobInfo[]>([])
const triggeringJob = ref<string | null>(null)
const tasks = ref<Task[]>([])
const taskCounts = ref<TaskCounts>({})

// Skill toggling
const togglingSkill = ref<string | null>(null)

// Skill grouping — computed from skills list
interface SkillGroup {
  name: string
  description: string
  hasConfig: boolean
  hasCapabilities: boolean
  enabled: boolean
  skills: SkillInfo[]
}

const skillGroups = computed<SkillGroup[]>(() => {
  const groups = new Map<string, SkillGroup>()
  for (const sk of skills.value) {
    if (!sk.manifest_group || sk.type !== 'tool') continue
    const g = sk.manifest_group
    if (!groups.has(g)) {
      groups.set(g, {
        name: g,
        description: '',
        hasConfig: false,
        hasCapabilities: true,
        enabled: true,
        skills: [],
      })
    }
    const group = groups.get(g)!
    group.skills.push(sk)
    if (sk.has_config) group.hasConfig = true
    if (!sk.has_capabilities) group.hasCapabilities = false
    if (!sk.enabled) group.enabled = false
    // Use first skill's description or build from group
    if (!group.description) group.description = sk.description
  }
  // Override description with skill list for multi-skill groups
  for (const g of groups.values()) {
    if (g.skills.length > 1) {
      g.description = g.skills.map(s => s.name).join(', ')
    }
  }
  return Array.from(groups.values()).sort((a, b) => a.name.localeCompare(b.name))
})

const standaloneSkills = computed(() =>
  skills.value.filter(sk => !sk.manifest_group).sort((a, b) => a.name.localeCompare(b.name))
)

// Skill config drawer
const drawerVisible = ref(false)
const drawerSkill = ref('')
const drawerLoading = ref(false)
const drawerFields = ref<SkillConfigField[]>([])
const fieldEdits = reactive<Record<string, string>>({})
const fieldSaving = reactive<Record<string, boolean>>({})
// Credential store integration for secret fields
const availableCredentials = ref<CredentialView[]>([])
const fieldCredentialBindings = reactive<Record<string, number | null>>({}) // field.key → credential_id

function shortKey(key: string): string {
  // "skills.craft.api_key" → "api_key"
  const parts = key.split('.')
  return parts.length > 2 ? parts.slice(2).join('.') : key
}

async function openSkillConfig(name: string) {
  drawerSkill.value = name
  drawerVisible.value = true
  drawerLoading.value = true
  // Clear previous edits
  for (const k in fieldEdits) delete fieldEdits[k]
  for (const k in fieldSaving) delete fieldSaving[k]
  for (const k in fieldCredentialBindings) delete fieldCredentialBindings[k]
  try {
    const [result, creds] = await Promise.all([
      api.getSkillConfig(name),
      api.listCredentials().catch(() => [] as CredentialView[]),
    ])
    drawerFields.value = result.schema ?? []
    availableCredentials.value = creds
    // Pre-fill non-secret values
    for (const f of drawerFields.value) {
      if (!f.secret && f.value !== undefined) {
        fieldEdits[f.key] = f.value
      }
    }
    // Check credential bindings for secret fields
    for (const f of drawerFields.value) {
      if (f.secret) {
        try {
          const bindings = await api.listCredentialsByConsumer('config_key', f.key)
          if (bindings.length > 0) {
            fieldCredentialBindings[f.key] = bindings[0].credential_id
          }
        } catch { /* no bindings */ }
      }
    }
  } catch (e: any) {
    message.error(e.message)
    drawerFields.value = []
  } finally {
    drawerLoading.value = false
  }
}

async function bindCredentialToField(fieldKey: string, credentialId: number | null) {
  fieldSaving[fieldKey] = true
  try {
    // Unbind existing credential for this config key
    const existingBindings = await api.listCredentialsByConsumer('config_key', fieldKey).catch(() => [])
    for (const b of existingBindings) {
      await api.unbindCredential(b.credential_id, 'config_key', fieldKey)
    }
    // Bind new credential if selected
    if (credentialId) {
      await api.bindCredential(credentialId, 'config_key', fieldKey)
      fieldCredentialBindings[fieldKey] = credentialId
      message.success(t('settings.credentialBound'))
    } else {
      delete fieldCredentialBindings[fieldKey]
      message.success(t('settings.credentialUnbound'))
    }
    // Refresh drawer
    const result = await api.getSkillConfig(drawerSkill.value)
    drawerFields.value = result.schema ?? []
    for (const f of drawerFields.value) {
      if (!f.secret && f.value !== undefined) {
        fieldEdits[f.key] = f.value
      }
    }
  } catch (e: any) {
    message.error(e.message || t('settings.error'))
  } finally {
    fieldSaving[fieldKey] = false
  }
}

async function saveSkillKey(key: string) {
  const value = fieldEdits[key]
  if (!value) {
    message.warning(t('settings.valueRequired'))
    return
  }
  const field = drawerFields.value.find(f => f.key === key)
  fieldSaving[key] = true
  try {
    if (field?.secret) {
      // Secret fields: create/update credential in credential store + auto-bind.
      const created = await api.createCredential({
        name: key,
        type: 'api_key',
        scope: 'global',
        value: value,
        description: `${drawerSkill.value} — ${shortKey(key)}`,
      })
      // Unbind any existing binding for this config key.
      const existing = await api.listCredentialsByConsumer('config_key', key).catch(() => [])
      for (const b of existing) {
        await api.unbindCredential(b.credential_id, 'config_key', key)
      }
      // Bind the credential to this config key.
      await api.bindCredential(created.id, 'config_key', key)
      fieldCredentialBindings[key] = created.id
      // Remove old config_override if exists (credential store takes priority now).
      await api.deleteConfig(key).catch(() => {})
      message.success(t('settings.savedSkillKey', { key: shortKey(key) }))
    } else {
      // Non-secret fields: save via config_overrides as before.
      await api.setSkillConfig(drawerSkill.value, key, value)
      message.success(t('settings.savedSkillKey', { key: shortKey(key) }))
    }
    // Refresh drawer.
    const [result, creds] = await Promise.all([
      api.getSkillConfig(drawerSkill.value),
      api.listCredentials().catch(() => [] as CredentialView[]),
    ])
    drawerFields.value = result.schema ?? []
    availableCredentials.value = creds
    for (const f of drawerFields.value) {
      if (f.secret) {
        delete fieldEdits[f.key]
      } else if (f.value !== undefined) {
        fieldEdits[f.key] = f.value
      }
    }
    await loadConfig()
  } catch (e: any) {
    message.error(e.message)
  } finally {
    fieldSaving[key] = false
  }
}

async function resetSkillKey(key: string) {
  try {
    await api.deleteConfig(key)
    message.success(t('settings.resetSkillKey', { key: shortKey(key) }))
    // Refresh drawer
    const result = await api.getSkillConfig(drawerSkill.value)
    drawerFields.value = result.schema ?? []
    for (const f of drawerFields.value) {
      if (f.secret) {
        delete fieldEdits[f.key]
      } else {
        fieldEdits[f.key] = f.value ?? ''
      }
    }
    await loadConfig()
  } catch (e: any) {
    message.error(e.message)
  }
}

// Google accounts & credential status
const googleAccounts = ref<GoogleAccount[]>([])
const googleAlias = ref('')
const googleConnecting = ref(false)
const googleStatus = ref<GoogleCredentialStatus | null>(null)

// Config schema
const schemaSections = ref<ConfigSchemaSection[]>([])
const schemaLoading = ref(false)
const schemaEdits = reactive<Record<string, string>>({})
const schemaSaving = reactive<Record<string, boolean>>({})
const modelLoading = reactive<Record<string, boolean>>({})
const dynamicModels = reactive<Record<string, string[]>>({})

async function loadSchema() {
  schemaLoading.value = true
  try {
    const result = await api.getConfigSchema()
    schemaSections.value = result.sections ?? []
    // Pre-fill edits from effective values
    for (const sec of schemaSections.value) {
      for (const f of sec.fields) {
        if (!f.secret && f.value !== undefined && f.value !== '') {
          schemaEdits[f.key] = f.value
        } else if (!f.secret && f.default) {
          schemaEdits[f.key] = schemaEdits[f.key] ?? ''
        }
      }
    }
  } catch (e: any) {
    message.error(t('settings.loadSchemaFailed', { error: e.message }))
  } finally {
    schemaLoading.value = false
  }
}

function schemaBoolValue(field: ConfigSchemaField): boolean {
  const val = schemaEdits[field.key] ?? field.value ?? field.default ?? 'false'
  return val === 'true'
}

function schemaSelectOptions(field: ConfigSchemaField) {
  const models = dynamicModels[field.key]
  const options = models && models.length > 0 ? models : (field.options ?? [])
  return options.map((o: string) => ({ label: o || '(empty)', value: o }))
}

async function maybeFetchModels(field: ConfigSchemaField) {
  if (!field.model_source || dynamicModels[field.key]) return
  modelLoading[field.key] = true
  try {
    const result = await api.getModels(field.model_source)
    dynamicModels[field.key] = result.models ?? []
  } catch {
    // Fallback to static options
  } finally {
    modelLoading[field.key] = false
  }
}

function schemaFieldChanged(field: ConfigSchemaField): boolean {
  const edit = schemaEdits[field.key]
  if (field.secret) return !!edit
  if (edit === undefined) return false
  const current = field.value ?? ''
  return edit !== current
}

async function saveSchemaField(field: ConfigSchemaField) {
  const value = schemaEdits[field.key]
  if (value === undefined || (value === '' && !field.secret)) {
    message.warning(t('settings.valueRequired'))
    return
  }
  schemaSaving[field.key] = true
  try {
    await api.setConfig(field.key, value, field.secret)
    message.success(t('settings.savedField', { label: field.label }))
    // Refresh schema + overrides
    await Promise.all([loadSchema(), loadConfig()])
    if (field.secret) delete schemaEdits[field.key]
  } catch (e: any) {
    message.error(e.message)
  } finally {
    schemaSaving[field.key] = false
  }
}

async function resetSchemaField(key: string) {
  try {
    await api.deleteConfig(key)
    message.success(t('settings.resetField', { key }))
    await Promise.all([loadSchema(), loadConfig()])
  } catch (e: any) {
    message.error(e.message)
  }
}

// Config overrides
const configEntries = ref<ConfigEntry[]>([])
const encryptionEnabled = ref(false)
const newKey = ref('')
const newValue = ref('')
const newEncrypt = ref(false)
const saving = ref(false)

const chatOptions = computed(() =>
  chats.value.map(c => ({
    label: `${c.chat_id} (${c.messages} msgs)`,
    value: c.chat_id,
  }))
)

const configColumns: DataTableColumns<ConfigEntry> = [
  { title: t('settings.key'), key: 'key', width: 250 },
  {
    title: t('settings.value'), key: 'value', ellipsis: { tooltip: true },
    render: (row) => row.encrypted ? '***' : row.value,
  },
  {
    title: t('settings.encrypted'), key: 'encrypted', width: 100,
    render: (row) => h(NTag, {
      type: row.encrypted ? 'success' : 'default',
      size: 'small',
    }, { default: () => row.encrypted ? t('common.yes') : t('common.no') }),
  },
  {
    title: t('settings.updated'), key: 'updated_at', width: 160,
    render: (row) => new Date(row.updated_at).toLocaleString(),
  },
  { title: t('settings.by'), key: 'updated_by', width: 80 },
  {
    title: '', key: 'actions', width: 80,
    render: (row) => h(NPopconfirm, {
      onPositiveClick: () => deleteConfig(row.key),
    }, {
      trigger: () => h(NButton, { size: 'tiny', type: 'error', quaternary: true }, { default: () => t('common.delete') }),
      default: () => t('settings.deleteOverrideConfirm', { key: row.key }),
    }),
  },
]

const taskColumns: DataTableColumns<Task> = [
  { title: t('settings.id'), key: 'id', width: 60 },
  { title: t('settings.type'), key: 'type', width: 150 },
  {
    title: t('common.status'), key: 'status', width: 90,
    render: (row) => h(NTag, {
      type: statusTagType(row.status),
      size: 'small',
    }, { default: () => row.status }),
  },
  { title: t('settings.worker'), key: 'worker_id', width: 120, ellipsis: { tooltip: true } },
  { title: t('settings.attempts'), key: 'attempts', width: 80 },
  {
    title: t('settings.created'), key: 'created_at', width: 160,
    render: (row) => new Date(row.created_at).toLocaleString(),
  },
  {
    title: t('settings.errorCol'), key: 'error', ellipsis: { tooltip: true },
  },
]

const googleColumns: DataTableColumns<GoogleAccount> = [
  { title: t('settings.email'), key: 'account_email', width: 220 },
  { title: t('settings.alias'), key: 'account_alias', width: 120 },
  {
    title: t('settings.default'), key: 'is_default', width: 80,
    render: (row) => h(NTag, {
      type: row.is_default ? 'success' : 'default',
      size: 'small',
    }, { default: () => row.is_default ? t('common.yes') : t('common.no') }),
  },
  {
    title: t('settings.tokenExpiry'), key: 'token_expiry', width: 160,
    render: (row) => new Date(row.token_expiry).toLocaleString(),
  },
  {
    title: t('settings.connected'), key: 'created_at', width: 160,
    render: (row) => new Date(row.created_at).toLocaleString(),
  },
  {
    title: '', key: 'actions', width: 80,
    render: (row) => h(NPopconfirm, {
      onPositiveClick: () => deleteGoogleAccount(row.id),
    }, {
      trigger: () => h(NButton, { size: 'tiny', type: 'error', quaternary: true }, { default: () => t('settings.disconnect') }),
      default: () => t('settings.disconnectConfirm', { email: row.account_email }),
    }),
  },
]

function statusTagType(status: string): 'success' | 'warning' | 'error' | 'info' | 'default' {
  switch (status) {
    case 'done': return 'success'
    case 'running': return 'warning'
    case 'claimed': return 'info'
    case 'failed': return 'error'
    case 'pending': return 'default'
    default: return 'default'
  }
}

async function loadSkills() {
  skills.value = await api.getSkills() ?? []
}

async function toggleSkill(name: string, enabled: boolean) {
  togglingSkill.value = name
  try {
    await api.toggleSkill(name, enabled)
    const sk = skills.value.find(s => s.name === name)
    if (sk) sk.enabled = enabled
    message.success(t('settings.skillToggled', { name, state: enabled ? t('common.enabled') : t('common.disabled') }))
  } catch (e: any) {
    message.error(e.message)
  } finally {
    togglingSkill.value = null
  }
}

async function toggleGroup(groupName: string, enabled: boolean) {
  togglingSkill.value = groupName
  try {
    await api.toggleSkill(groupName, enabled)
    // Update all skills in the group.
    for (const sk of skills.value) {
      if (sk.manifest_group === groupName) sk.enabled = enabled
    }
    message.success(t('settings.groupToggled', { name: groupName, state: enabled ? t('common.enabled') : t('common.disabled') }))
  } catch (e: any) {
    message.error(e.message)
  } finally {
    togglingSkill.value = null
  }
}

async function loadGoogleAccounts() {
  try {
    googleAccounts.value = await api.listGoogleAccounts()
  } catch {
    googleAccounts.value = []
  }
}

async function loadGoogleStatus() {
  try {
    googleStatus.value = await api.getGoogleStatus()
  } catch {
    googleStatus.value = null
  }
}

const credentialUploading = ref(false)

async function handleCredentialUpload({ file, onFinish, onError }: { file: { file: File | null }; onFinish: () => void; onError: () => void }) {
  if (!file.file) {
    onError()
    return
  }
  credentialUploading.value = true
  try {
    const result = await api.uploadGoogleCredentials(file.file)
    message.success(t('settings.credentialUploaded', { type: result.credential_type, filename: result.filename }))
    await loadGoogleStatus()
    onFinish()
  } catch (e: any) {
    message.error(e.message || t('settings.uploadFailed'))
    onError()
  } finally {
    credentialUploading.value = false
  }
}

async function handleDrawerCredentialUpload({ file, onFinish, onError }: { file: { file: File | null }; onFinish: () => void; onError: () => void }, fieldKey: string) {
  if (!file.file) {
    onError()
    return
  }
  fieldSaving[fieldKey] = true
  try {
    const result = await api.uploadGoogleCredentials(file.file)
    message.success(t('settings.credentialUploaded', { type: result.credential_type, filename: result.filename }))
    // Refresh drawer to show updated path.
    const config = await api.getSkillConfig(drawerSkill.value)
    drawerFields.value = config.schema ?? []
    for (const f of drawerFields.value) {
      if (f.secret) {
        delete fieldEdits[f.key]
      } else if (f.value !== undefined) {
        fieldEdits[f.key] = f.value
      }
    }
    await loadGoogleStatus()
    onFinish()
  } catch (e: any) {
    message.error(e.message || t('settings.uploadFailed'))
    onError()
  } finally {
    fieldSaving[fieldKey] = false
  }
}

const sourceLabels: Record<string, string> = {
  'env_token': 'Environment Variable (IULITA_GOOGLE_TOKEN)',
  'env_credentials_file': 'Environment Variable (IULITA_GOOGLE_CREDENTIALS_FILE)',
  'config_credentials_file': 'Credentials File (config)',
  'db_account': 'Connected Account (OAuth2)',
  'adc': 'Application Default Credentials',
  'none': 'Not configured',
}

const sourceTagType = (source: string): 'success' | 'warning' | 'error' | 'info' | 'default' => {
  if (source === 'none') return 'error'
  if (source === 'env_token') return 'warning'
  return 'success'
}

async function connectGoogle() {
  googleConnecting.value = true
  try {
    const result = await api.getGoogleAuthURL(googleAlias.value || undefined)
    window.location.href = result.url
  } catch (e: any) {
    message.error(e.message)
  } finally {
    googleConnecting.value = false
  }
}

async function deleteGoogleAccount(id: number) {
  try {
    await api.deleteGoogleAccount(id)
    message.success(t('settings.googleDisconnected'))
    await loadGoogleAccounts()
  } catch (e: any) {
    message.error(e.message)
  }
}

async function loadConfig() {
  try {
    const result = await api.getConfig()
    configEntries.value = result.overrides ?? []
    encryptionEnabled.value = result.encryption_enabled
  } catch {
    configEntries.value = []
  }
}

async function saveConfig() {
  if (!newKey.value || !newValue.value) {
    message.warning(t('settings.keyValueRequired'))
    return
  }
  saving.value = true
  try {
    await api.setConfig(newKey.value, newValue.value, newEncrypt.value)
    message.success(t('settings.configSaved', { key: newKey.value }))
    newKey.value = ''
    newValue.value = ''
    newEncrypt.value = false
    await loadConfig()
  } catch (e: any) {
    message.error(e.message)
  } finally {
    saving.value = false
  }
}

async function deleteConfig(key: string) {
  try {
    await api.deleteConfig(key)
    message.success(t('settings.configDeleted', { key }))
    await loadConfig()
  } catch (e: any) {
    message.error(e.message)
  }
}

async function loadDirective() {
  if (!selectedChat.value) {
    directiveContent.value = null
    return
  }
  const result = await api.getDirective(selectedChat.value)
  if ('directive' in result && result.directive === null) {
    directiveContent.value = null
  } else if ('Content' in result) {
    directiveContent.value = result.Content
  }
}

async function loadSchedulers() {
  schedulers.value = await api.getSchedulers()
}

async function loadTasks() {
  const [t, c] = await Promise.all([
    api.getTasks({ limit: 50 }),
    api.getTaskCounts(),
  ])
  tasks.value = t ?? []
  taskCounts.value = c ?? {}
}

async function triggerJob(name: string) {
  triggeringJob.value = name
  try {
    await api.triggerJob(name)
    message.success(t('settings.jobTriggered', { name }))
    setTimeout(loadTasks, 1000)
    setTimeout(loadSchedulers, 1000)
  } catch (e: any) {
    message.error(e.message)
  } finally {
    triggeringJob.value = null
  }
}

onMounted(async () => {
  // Show success message if redirected from Google OAuth
  const params = new URLSearchParams(window.location.search)
  if (params.get('google') === 'connected') {
    message.success(t('settings.googleConnected'))
    window.history.replaceState({}, '', window.location.pathname)
  }

  const [sys, sk, ch] = await Promise.all([
    api.getSystem(),
    api.getSkills(),
    api.getChats(),
    loadSchedulers(),
    loadTasks(),
    loadConfig(),
    loadGoogleAccounts(),
    loadGoogleStatus(),
    loadSchema(),
  ])
  system.value = sys
  skills.value = sk ?? []
  chats.value = ch
})

watch(selectedChat, () => loadDirective())
</script>
