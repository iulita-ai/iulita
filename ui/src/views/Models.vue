<template>
  <n-space vertical :size="20" class="models-page">
    <n-h2>{{ t('models.title') }}</n-h2>
    <n-alert v-if="!admin" type="info" :show-icon="false">{{ t('models.adminManaged') }}</n-alert>
    <template v-else>
      <n-p>{{ t('models.intro') }}</n-p>
      <n-alert v-if="error" type="error" role="alert">
        {{ error }}
        <n-button v-if="authError" text @click="leaveForLogin">{{ t('models.signIn') }}</n-button>
      </n-alert>
      <n-spin :show="loading">
        <template v-if="view">
          <n-space align="center" :size="12" aria-live="polite">
            <n-tag>{{ t('models.savedRevision', { n: view.revision }) }}</n-tag>
            <n-tag :type="view.health === 'ready' ? 'success' : 'warning'">{{ t('models.activeRevision', { n: view.active_revision }) }}</n-tag>
            <n-tag v-if="view.activation_status && view.activation_status !== 'applied'" type="warning">{{ t(view.activation_status === 'failed' ? 'models.activationFailed' : 'models.waitingActivation') }}</n-tag>
            <n-text v-if="view.affected_roles?.length" type="warning">{{ t('models.suspended', { roles: view.affected_roles.map(role => t(`models.roles.${role}`)).join(', ') }) }}</n-text>
          </n-space>
          <n-alert v-if="setupServer && view.settings.policy.everyday" type="warning" class="section-gap">
            {{ t('models.setupRestartHint') }}
            <n-button text @click="router.push({ name: 'setup' })">{{ t('models.finishSetup') }}</n-button>
          </n-alert>
          <n-alert v-if="!view.encryption_available" type="warning" class="section-gap">{{ t('models.encryptionRequired') }}</n-alert>
          <n-alert v-if="stage" type="info" class="section-gap" :title="t('models.savedForTesting')">
            {{ t('models.stageHint', { date: formatDate(stage.expires_at) }) }}
            <n-space class="section-gap">
              <n-button data-testid="activate" type="primary" :disabled="busy || dirty || !readyToActivate" :loading="busy" @click="activate">{{ t('models.activate') }}</n-button>
              <n-button data-testid="discard" :disabled="busy" @click="discard">{{ t('models.discard') }}</n-button>
            </n-space>
            <n-text v-if="!readyToActivate" :depth="3">{{ t('models.assignmentGate') }}</n-text>
          </n-alert>
          <n-alert v-if="view.legacy_settings" type="info" :show-icon="false" class="section-gap">
            {{ t('models.importLegacyHint') }}
            <n-button data-testid="import-legacy" :disabled="busy || operationRunning" class="section-gap" @click="importLegacy">{{ t('models.importLegacy') }}</n-button>
            <n-p v-if="view.legacy_classifier_active" class="helper">{{ t('models.importClassifierWarning') }}</n-p>
          </n-alert>
          <n-tabs v-model:value="activeTab" type="line" animated class="section-gap">
            <n-tab-pane name="models" :tab="t('models.connectionsProfiles')">
              <n-h3>{{ t('models.connections') }}</n-h3>
              <div class="connection-grid">
                <n-card v-for="connection in connections" :key="connection.provider" size="small" :title="providerLabel(connection.provider)">
                  <n-space vertical :size="8">
                    <n-space align="center">
                      <n-tag :type="connection.credential_set ? 'success' : 'warning'" size="small">{{ t(connection.credential_set ? 'models.keySet' : 'models.keyMissing') }}</n-tag>
                      <n-tag v-if="connection.availability === 'suspended'" type="error" size="small">{{ t('models.connectionBlocked') }}</n-tag>
                      <n-text :depth="3">{{ t('models.source') }}: {{ connection.source }}</n-text>
                    </n-space>
                    <n-form-item :label="t('models.endpoint')" :show-feedback="false">
                      <n-input :value="endpoints[connection.provider]" :disabled="!editableConnection(connection)" :input-props="{ dir: 'ltr', autocomplete: 'off', 'aria-label': t('models.endpoint') }" @update:value="endpoints[connection.provider] = $event" />
                    </n-form-item>
                    <n-form-item :label="t('models.apiKey')" :show-feedback="false">
                      <n-input v-model:value="keys[connection.provider]" type="password" show-password-on="click" :placeholder="t('models.keepKey')" :disabled="!view.encryption_available || !editableConnection(connection)" :input-props="{ autocomplete: 'new-password', 'aria-label': `${providerLabel(connection.provider)} ${t('models.apiKey')}` }" />
                    </n-form-item>
                    <n-text :depth="3" class="helper">{{ t(editableConnection(connection) ? 'models.keyHint' : ['env', 'environment'].includes(connection.source) ? 'models.environmentOverride' : 'models.legacyConnectionReadonly') }}</n-text>
                    <n-popconfirm v-if="activeConnection(connection.provider)?.credential_set" @positive-click="revoke(activeConnection(connection.provider)!)">
                      <template #trigger><n-button type="error" size="small" :disabled="busy">{{ t('models.revoke') }}</n-button></template>
                      {{ t('models.revokeHint') }}
                    </n-popconfirm>
                  </n-space>
                </n-card>
              </div>
              <n-h3>{{ t('models.profiles') }}</n-h3>
              <n-space align="center">
                <n-button v-for="preset in availablePresets" :key="preset.id" size="small" @click="addPreset(preset)">{{ t('models.addPreset', { name: preset.name }) }}</n-button>
              </n-space>
              <n-empty v-if="!draft.profiles.length" :description="t('models.empty')" class="section-gap" />
              <n-collapse v-model:expanded-names="expandedProfiles" class="section-gap">
                <n-collapse-item v-for="(profile, index) in draft.profiles" :key="profile.id" :name="profile.id" :title="profile.name || profile.id">
                  <template #header-extra><n-tag :type="isLegacyPreserved(profile.id) ? 'info' : eligible(profile.id) ? 'success' : 'warning'" size="small">{{ t(isLegacyPreserved(profile.id) ? 'models.legacyPreserved' : eligible(profile.id) ? 'models.ready' : 'models.savedForTesting') }}</n-tag></template>
                  <div class="profile-grid">
                    <n-form-item :data-field="`profiles[${index}].name`" :label="t('models.displayName')" :feedback="fieldError(`profiles[${index}].name`)" :validation-status="fieldError(`profiles[${index}].name`) ? 'error' : undefined"><n-input v-model:value="profile.name" :input-props="{ 'aria-label': t('models.displayName') }" /></n-form-item>
                    <n-form-item :label="t('models.provider')"><n-select v-model:value="profile.connection" :options="providerOptions" @update:value="normaliseProfile(profile)" /></n-form-item>
                    <n-form-item :data-field="`profiles[${index}].model`" :label="t('models.model')" :feedback="fieldError(`profiles[${index}].model`)"><n-select v-model:value="profile.model" :options="modelOptions(profile)" filterable tag @update:value="normaliseProfile(profile)" /></n-form-item>
                    <n-form-item :data-field="`profiles[${index}].max_output_tokens`" :label="t('models.outputLimit')" :feedback="fieldError(`profiles[${index}].max_output_tokens`)"><n-input-number v-model:value="profile.max_output_tokens" :min="1" :max="32768" :precision="0" /></n-form-item>
                    <n-form-item :label="t('models.thinking')"><n-select v-model:value="profile.thinking" :disabled="['zai', 'claude'].includes(profile.connection)" :options="thinkingOptions" @update:value="normaliseProfile(profile)" /></n-form-item>
                    <n-form-item v-if="profile.thinking === 'enabled' && ['deepseek', 'zai'].includes(profile.connection)" :label="t('models.effort')"><n-select v-model:value="profile.reasoning_effort" :options="effortOptions(profile)" /></n-form-item>
                  </div>
                  <n-space align="center">
                    <n-tag>{{ t(supportsImages(profile) ? 'models.imagesDocumented' : 'models.textOnly') }}</n-tag>
                    <n-text v-if="definition(profile)" :depth="3">{{ t('models.context', { n: definition(profile)!.context_tokens.toLocaleString() }) }}</n-text>
                  </n-space>
                  <n-p depth="3" class="helper">{{ t('models.capacityHint') }}</n-p>
                  <n-alert v-if="profile.connection === 'deepseek' && profile.thinking === 'enabled'" type="warning">{{ t('models.deepseekThinkingGate') }}</n-alert>
                  <n-space class="section-gap">
                    <n-button v-for="kind in ['text', 'tools', 'vision']" :key="kind" size="small" :disabled="!stage || dirty || busy || operationRunning || (kind === 'vision' && !supportsImages(profile))" @click="startProbe(profile.id, kind)">{{ t(`models.tests.${kind}`) }}</n-button>
                    <n-button size="small" :disabled="referenced(profile.id) || busy" @click="removeProfile(profile.id)">{{ t('models.remove') }}</n-button>
                  </n-space>
                  <n-text v-if="referenced(profile.id)" :depth="3" class="helper">{{ t('models.usedProfile') }}</n-text>
                  <n-ul v-if="evidence(profile.id).length">
                    <n-li v-for="item in evidence(profile.id)" :key="item.kind">{{ t(`models.tests.${item.kind}`) }}: {{ t(item.passed ? 'models.passed' : 'models.failed') }} · {{ formatDate(item.checked_at) }}<span v-if="item.served_model"> · {{ item.served_model }}</span></n-li>
                  </n-ul>
                </n-collapse-item>
              </n-collapse>
            </n-tab-pane>
            <n-tab-pane name="roles" :tab="t('models.taskRoles')">
              <n-p>{{ t('models.rolesHint') }}</n-p>
              <div class="role-grid">
                <n-form-item v-for="role in roles" :key="role" :data-field="`policy.${role}`" :label="t(`models.roles.${role}`)" :feedback="fieldError(`policy.${role}`)">
                  <n-select :value="draft.policy[role] || null" :options="roleOptions(role)" clearable @update:value="draft.policy[role] = $event || ''" />
                </n-form-item>
              </div>
              <n-alert type="info" :show-icon="false">{{ t('models.plannerHint') }}</n-alert>
              <n-h3>{{ t('models.automaticSelection') }}</n-h3>
              <n-p class="helper">{{ t('models.classifierHelp') }}</n-p>
              <n-checkbox data-testid="classifier-enabled" v-model:checked="draft.policy.classifier.enabled">{{ t('models.enableClassifier') }}</n-checkbox>
              <div class="role-grid section-gap">
                <n-form-item data-field="policy.classifier.profile" :label="t('models.classifierProfile')" :feedback="fieldError('policy.classifier.profile')">
                  <n-select data-testid="classifier-profile" :value="draft.policy.classifier.profile || null" :options="classifierOptions" clearable @update:value="draft.policy.classifier.profile = $event || ''" />
                </n-form-item>
                <n-form-item data-field="policy.classifier.timeout_ms" :label="t('models.classifierTimeout')" :feedback="fieldError('policy.classifier.timeout_ms')">
                  <n-input-number data-testid="classifier-timeout" :value="draft.policy.classifier.timeout_ms || 5000" :min="500" :max="10000" :step="500" :precision="0" @update:value="draft.policy.classifier.timeout_ms = $event || 5000" />
                </n-form-item>
              </div>
              <n-button data-testid="classifier-evaluate" :disabled="!stage || dirty || busy || operationRunning || !draft.policy.classifier.profile" @click="startProbe(draft.policy.classifier.profile!, 'classifier')">{{ t('models.tests.classifier') }}</n-button>
              <n-p v-if="classifierEvaluation" class="helper" aria-live="polite">{{ t('models.classifierResult', { correct: classifierEvaluation.correct, total: classifierEvaluation.cases, complex: classifierEvaluation.complex_correct, complexTotal: classifierEvaluation.complex_cases, ms: classifierEvaluation.max_latency_ms }) }} · {{ t(classifierReady ? 'models.passed' : 'models.testsRequired') }}</n-p>
              <n-h3>{{ t('models.fallbackTitle') }}</n-h3>
              <n-p class="helper">{{ t('models.fallbackHelp') }}</n-p>
              <div class="role-grid">
                <n-form-item v-for="role in roles" :key="`fallback-${role}`" :data-field="`policy.fallbacks.${role}`" :label="t('models.fallbackRole', { role: t(`models.roles.${role}`) })" :feedback="fieldError(`policy.fallbacks.${role}`)">
                  <n-select :data-testid="`fallback-${role}`" :value="draft.policy.fallbacks?.[role] || []" :options="fallbackOptions(role)" multiple clearable :max="2" @update:value="setFallback(role, $event)" />
                </n-form-item>
                <n-form-item data-field="policy.fallback_timeout_ms" :label="t('models.fallbackTimeout')" :feedback="fieldError('policy.fallback_timeout_ms')">
                  <n-input-number :value="draft.policy.fallback_timeout_ms || 60000" :min="1000" :max="120000" :step="1000" :precision="0" @update:value="draft.policy.fallback_timeout_ms = $event || 60000" />
                </n-form-item>
              </div>
              <n-collapse v-if="Object.keys(draft.policy.legacy_hints || {}).length || Object.keys(draft.policy.legacy_profile_hints || {}).length" class="section-gap">
                <n-collapse-item :title="t('models.existingRoutes')" name="legacy-routes">
                  <n-p class="helper">{{ t('models.existingRoutesHint') }}</n-p>
                  <n-form-item v-for="hint in Object.keys(draft.policy.legacy_hints || {})" :key="hint" :label="hint">
                    <n-select :value="draft.policy.legacy_hints![hint]" :options="roles.map(role => ({ value: role, label: t(`models.roles.${role}`) }))" @update:value="draft.policy.legacy_hints![hint] = $event" />
                  </n-form-item>
                  <n-form-item v-for="hint in Object.keys(draft.policy.legacy_profile_hints || {})" :key="hint" :label="hint">
                    <n-select :value="draft.policy.legacy_profile_hints![hint]" :options="roleOptions('complex')" @update:value="draft.policy.legacy_profile_hints![hint] = $event" />
                  </n-form-item>
                </n-collapse-item>
              </n-collapse>
              <n-checkbox v-model:checked="claudeForbidden" class="section-gap">{{ t('models.forbidClaude') }}</n-checkbox>
              <n-p depth="3" class="helper">{{ t('models.forbidClaudeHint') }}</n-p>
            </n-tab-pane>
          </n-tabs>
          <n-collapse class="section-gap">
            <n-collapse-item name="history" :title="t('models.previousSettings')">
              <n-p class="helper">{{ t('models.historyHint') }}</n-p>
              <n-button size="small" :loading="historyLoading" @click="loadHistory">{{ t('models.refreshHistory') }}</n-button>
              <n-empty v-if="historyLoaded && !history.length" :description="t('models.noHistory')" class="section-gap" />
              <n-card v-for="entry in history" :key="entry.revision" size="small" class="section-gap" :title="t('models.savedRevision', { n: entry.revision })">
                <n-space vertical>
                  <n-text :depth="3">{{ formatDate(entry.created_at) }}</n-text>
                  <n-text>{{ entry.settings.profiles.map(profile => profile.name).join(', ') }}</n-text>
                  <n-button data-testid="restore-history" :disabled="!!stage || dirty || busy || operationRunning" @click="restoreHistory(entry.revision)">{{ t('models.preparePrevious') }}</n-button>
                </n-space>
              </n-card>
            </n-collapse-item>
          </n-collapse>
          <n-space class="save-bar" align="center">
            <n-button data-testid="save-stage" type="primary" :loading="busy" :disabled="busy || operationRunning" @click="saveStage">{{ t('models.saveForTesting') }}</n-button>
            <n-button :disabled="loading || busy" @click="refresh(true)">{{ t('models.refreshStatus') }}</n-button>
            <n-text :depth="3" class="helper">{{ t('models.saveHint') }}</n-text>
          </n-space>
          <n-card v-if="operation || pendingProbe" class="section-gap" size="small" :title="t('models.testStatus')" aria-live="polite">
            <n-space vertical>
              <n-text>{{ operation ? t(`models.probeStates.${operation.status}`, t('models.probeStates.unknown')) : t('models.probeStates.unknown') }}</n-text>
              <n-alert v-if="operation?.error_code" type="error" data-testid="probe-error">{{ t(`models.errors.${operation.error_code}`, t('models.errors.request_failed')) }}</n-alert>
              <n-text :depth="3">{{ t('models.probeCharge') }}</n-text>
              <n-space>
                <n-button v-if="operationRunning" data-testid="cancel-probe" :disabled="cancelPending" @click="cancelProbe">{{ t(cancelPending ? 'models.stopping' : 'models.cancelTest') }}</n-button>
                <n-button v-if="operation" @click="pollProbe">{{ t('models.refreshStatus') }}</n-button>
                <n-button v-if="pendingProbe && !operation" @click="retryProbe">{{ t('models.recoverTest') }}</n-button>
              </n-space>
            </n-space>
          </n-card>
        </template>
      </n-spin>
    </template>
  </n-space>
</template>

<script setup lang="ts">
import { computed, nextTick, onMounted, onBeforeUnmount, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import { NAlert, NButton, NCard, NCheckbox, NCollapse, NCollapseItem, NEmpty, NFormItem, NH2, NH3, NInput, NInputNumber, NLi, NP, NPopconfirm, NSelect, NSpace, NSpin, NTabPane, NTabs, NTag, NText, NUl } from 'naive-ui'
import { api, currentUser, isAdmin } from '../api'
import { modelsApi, ModelAPIError, type ConnectionMutation, type ModelConnection, type ModelDefinition, type ModelFieldError, type ModelHistoryEntry, type ModelProbe, type ModelProfile, type ModelSettings, type ModelSettingsView, type ModelStage, type ProbeRequest } from '../modelApi'
const { t } = useI18n()
const router = useRouter()
const admin = isAdmin()
const actor = currentUser()?.user_id || 'unknown'
const storageKey = `iulita_models_draft_${actor}`
const operationKey = `iulita_models_operation_${actor}`
const view = ref<ModelSettingsView>()
const stage = ref<ModelStage>()
const catalog = ref<ModelDefinition[]>([])
const draft = ref<ModelSettings>({ schema_version: 1, profiles: [], policy: { everyday: '', complex: '', vision: '', background: '', classifier: { enabled: false } } })
const keys = ref<Record<string, string>>({})
const endpoints = ref<Record<string, string>>({})
const fields = ref<ModelFieldError[]>([])
const error = ref('')
const authError = ref(false)
const loading = ref(false)
const setupServer = ref(false)
const history = ref<ModelHistoryEntry[]>([])
const historyLoaded = ref(false)
const historyLoading = ref(false)
const busy = ref(false)
const operation = ref<ModelProbe>()
const pendingProbe = ref<ProbeRequest>()
const cancelPending = ref(false)
const activeTab = ref('models')
const expandedProfiles = ref<string[]>([])
let pollTimer: ReturnType<typeof setTimeout> | undefined
let disposed = false
const roles = ['everyday', 'complex', 'vision', 'background'] as const
const providers = ['deepseek', 'zai', 'claude', 'openai', 'ollama']
const defaults: Record<string, string> = { deepseek: 'https://api.deepseek.com/v1', zai: 'https://api.z.ai/api/paas/v4' }
const providerLabel = (provider: string) => ({ zai: 'Z.ai', deepseek: 'DeepSeek', claude: 'Claude', openai: 'OpenAI', ollama: 'Ollama' }[provider] || provider)
const editableConnection = (connection: ModelConnection) => ['deepseek', 'zai'].includes(connection.provider) && !['environment', 'env'].includes(connection.source)
const activeConnection = (provider: string) => view.value?.connections.find(c => c.provider === provider)
const providerOptions = providers.map(value => ({ value, label: providerLabel(value) }))
const thinkingOptions = computed(() => ['disabled', 'enabled'].map(value => ({ value, label: t(`models.thinkingValues.${value}`) })))
const connections = computed(() => providers.map(provider => (stage.value?.connections || view.value?.connections || []).find(item => item.provider === provider) || { provider, endpoint: defaults[provider] || '', credential_set: false, source: 'none', generation: '', availability: 'unconfigured' }))
const dirty = computed(() => !!view.value && (JSON.stringify(draft.value) !== JSON.stringify(stage.value?.settings || view.value.settings) || Object.values(keys.value).some(Boolean) || connections.value.some(c => (endpoints.value[c.provider] || '') !== c.endpoint)))
const operationRunning = computed(() => !!pendingProbe.value || ['running', 'cancel_requested'].includes(operation.value?.status || ''))
const claudeForbidden = computed({ get: () => draft.value.policy.forbidden_providers?.includes('claude') || false, set: (enabled: boolean) => { draft.value.policy.forbidden_providers = [...(draft.value.policy.forbidden_providers || []).filter(p => p !== 'claude'), ...(enabled ? ['claude'] : [])] } })
const clone = <T,>(value: T): T => JSON.parse(JSON.stringify(value))
const presets: ModelProfile[] = [
  { id: 'ds-flash', name: 'DeepSeek Flash', connection: 'deepseek', model: 'deepseek-flash', max_output_tokens: 8192, thinking: 'disabled', clear_thinking: false },
  { id: 'ds-pro', name: 'DeepSeek Pro', connection: 'deepseek', model: 'deepseek-v4-pro', max_output_tokens: 16384, thinking: 'disabled', clear_thinking: false },
  { id: 'glm-flash', name: 'GLM Flash', connection: 'zai', model: 'glm-5.3-flash', max_output_tokens: 8192, thinking: 'enabled', reasoning_effort: 'low', clear_thinking: true },
  { id: 'glm-main', name: 'GLM 5.3', connection: 'zai', model: 'glm-5.3', max_output_tokens: 16384, thinking: 'enabled', reasoning_effort: 'high', clear_thinking: true },
  { id: 'glm-selector', name: 'GLM Flash Selector', connection: 'zai', model: 'glm-5.3-flash', max_output_tokens: 512, thinking: 'enabled', reasoning_effort: 'low', clear_thinking: true },
]
const availablePresets = computed(() => presets.filter(p => !draft.value.profiles.some(existing => existing.id === p.id)))
const effective = computed(() => stage.value?.effective_profiles || view.value?.effective_profiles || [])
function unchangedIdentity(id: string) {
  const p = draft.value.profiles.find(profile => profile.id === id)
  const saved = (stage.value?.settings || view.value?.settings)?.profiles.find(profile => profile.id === id)
  if (!p || !saved || keys.value[p.connection]) return false
  const connection = connections.value.find(c => c.provider === p.connection)
  if (connection && endpoints.value[p.connection] !== connection.endpoint) return false
  const identity = (profile: ModelProfile) => JSON.stringify({ connection: profile.connection, model: profile.model, max_output_tokens: profile.max_output_tokens, thinking: profile.thinking, reasoning_effort: profile.reasoning_effort || '', clear_thinking: !!profile.clear_thinking })
  return identity(p) === identity(saved)
}
const eligible = (id: string) => !connections.value.some(connection => connection.provider === draft.value.profiles.find(p => p.id === id)?.connection && connection.availability === 'suspended') && unchangedIdentity(id) && ['production_eligible', 'legacy_preserved'].includes(effective.value.find(p => p.id === id)?.eligibility || '')
const isLegacyPreserved = (id: string) => eligible(id) && effective.value.find(p => p.id === id)?.eligibility === 'legacy_preserved'
const evidence = (id: string) => unchangedIdentity(id) ? effective.value.find(p => p.id === id)?.evidence || [] : []
const definition = (p: ModelProfile) => catalog.value.find(d => d.provider === p.connection && d.model === p.model)
const supportsImages = (p: ModelProfile) => !!definition(p)?.images || p.connection === 'claude'
const referenced = (id: string) => Object.values(draft.value.policy).some(value => value === id) || Object.values(draft.value.policy.fallbacks || {}).some(ids => ids.includes(id)) || Object.values(draft.value.policy.legacy_profile_hints || {}).includes(id) || draft.value.policy.classifier.profile === id
const readyToActivate = computed(() => !!stage.value && !operationRunning.value && roles.every(role => {
  const id = draft.value.policy[role]
  return !id || (eligible(id) && (role !== 'vision' || isLegacyPreserved(id) || evidence(id).some(e => e.kind === 'vision' && e.passed)))
}) && !!draft.value.policy.everyday && (!draft.value.policy.classifier.enabled || (!!draft.value.policy.complex && classifierReady.value)) && roles.every(role => (draft.value.policy.fallbacks?.[role] || []).length <= 2 && (draft.value.policy.fallbacks?.[role] || []).every(id => fallbackOptions(role).some(option => option.value === id && !option.disabled))))
function roleOptions(role: string) {
  return draft.value.profiles.map(p => ({ value: p.id, label: `${p.name}${isLegacyPreserved(p.id) ? ` (${t('models.legacyPreserved')})` : eligible(p.id) ? '' : ` (${t('models.testsRequired')})`}`, disabled: !eligible(p.id) || (role === 'vision' && (!supportsImages(p) || (!isLegacyPreserved(p.id) && !evidence(p.id).some(e => e.kind === 'vision' && e.passed)))) || draft.value.policy.forbidden_providers?.includes(p.connection) }))
}
const classifierOptions = computed(() => draft.value.profiles.filter(p => p.max_output_tokens <= 1024).map(p => ({ value: p.id, label: p.name, disabled: draft.value.policy.forbidden_providers?.includes(p.connection) || connections.value.some(c => c.provider === p.connection && c.availability === 'suspended') })))
const classifierEvaluation = computed(() => evidence(draft.value.policy.classifier.profile || '').find(e => e.kind === 'classifier'))
const classifierReady = computed(() => {
  const id = draft.value.policy.classifier.profile || ''
  const result = classifierEvaluation.value
  return eligible(id) && !isLegacyPreserved(id) && draft.value.profiles.some(p => p.id === id && p.max_output_tokens <= 1024) && !!result?.passed && result.fixture_version === 'bounded-selector-v1-eval-v1' && result.classifier_timeout_ms === (draft.value.policy.classifier.timeout_ms || 5000) && result.cases === 12 && (result.correct || 0) >= 11 && result.complex_cases === 8 && result.complex_correct === 8
})
function fallbackOptions(role: typeof roles[number]) {
  const primary = draft.value.profiles.find(p => p.id === draft.value.policy[role])
  const primaryWindow = primary ? definition(primary)?.context_tokens || 0 : 0
  return roleOptions(role).map(option => {
    const profile = draft.value.profiles.find(p => p.id === option.value)!
    return { ...option, disabled: option.disabled || isLegacyPreserved(option.value) || option.value === draft.value.policy[role] || (definition(profile)?.context_tokens || 0) < primaryWindow }
  })
}
function setFallback(role: typeof roles[number], ids: string[]) {
  draft.value.policy.fallbacks ||= {}
  draft.value.policy.fallbacks[role] = ids
}
function modelOptions(p: ModelProfile) {
  const definitions = catalog.value.filter(d => d.provider === p.connection)
  const options = definitions.map(d => ({ value: d.model, label: d.name }))
  if (!options.some(d => d.value === p.model) && p.model) options.push({ value: p.model, label: p.model })
  return options
}
function effortOptions(p: ModelProfile) { return (p.connection === 'zai' ? ['low', 'high', 'max'] : ['high', 'max']).map(value => ({ value, label: t(`models.efforts.${value}`) })) }
function normaliseProfile(p: ModelProfile) {
  p.clear_thinking = p.connection === 'zai'
  if (p.connection === 'zai') { p.thinking = 'enabled'; p.reasoning_effort = p.reasoning_effort || 'low' }
  else if (p.connection === 'claude') { p.thinking = 'disabled'; delete p.reasoning_effort }
  else if (p.connection === 'deepseek' && p.thinking === 'enabled') p.reasoning_effort = p.reasoning_effort === 'max' ? 'max' : 'high'
  else delete p.reasoning_effort
}
function importLegacy() {
  const legacy = view.value?.legacy_settings
  if (!legacy || busy.value || operationRunning.value) return
  const profiles = new Map([...clone(draft.value.profiles), ...clone(legacy.profiles)].map(profile => [profile.id, profile]))
  draft.value.profiles = [...profiles.values()]
  draft.value.policy = clone(legacy.policy)
  // Existing live classifier remains untouched until this draft is applied.
  // A new classifier assignment needs its separate evaluation gate.
  draft.value.policy.classifier = { enabled: false }
  error.value = ''
}
function addPreset(p: ModelProfile) { draft.value.profiles.push(clone(p)) }
function removeProfile(id: string) { if (!referenced(id)) draft.value.profiles = draft.value.profiles.filter(p => p.id !== id) }
function fieldError(path: string) { return fields.value.find(item => item.path === path)?.message || '' }
function formatDate(value: string) { return new Date(value).toLocaleString() }
function report(err: unknown) {
  keys.value = {}
  authError.value = err instanceof ModelAPIError && ['unauthenticated', 'password_change_required', 'admin_required'].includes(err.code)
  fields.value = err instanceof ModelAPIError ? err.fields : []
  const code = err instanceof ModelAPIError ? err.code : 'network'
  const known = ['revision_conflict', 'stage_conflict', 'stage_expired', 'stage_busy', 'password_change_required', 'unauthenticated', 'admin_required', 'invalid_settings', 'network']
  const aliases: Record<string, string> = { activation_failed: 'activationFailed', stage_owner_required: 'errors.stage_busy', secret_encryption_unavailable: 'encryptionRequired', environment_override: 'environmentOverride', verification_required: 'assignmentGate', vision_verification_required: 'assignmentGate', evaluation_required: 'classifierHelp', credential_revoked: 'revokedConnection', forbidden_provider: 'forbiddenProvider', idempotency_key_expired: 'expiredTest' }
  error.value = t(`models.${aliases[code] || `errors.${known.includes(code) ? code : 'request_failed'}`}`)
  if (err instanceof ModelAPIError && err.status === 410) { pendingProbe.value = undefined; operation.value = undefined; rememberOperation() }
  if (fields.value.length) {
    const path = fields.value[0]!.path
    activeTab.value = path.startsWith('policy.') ? 'roles' : 'models'
    const index = /^profiles\[(\d+)\]/.exec(path)?.[1]
    const profile = index !== undefined ? draft.value.profiles[Number(index)] : draft.value.profiles.find(p => path.startsWith(`profiles.${p.id}`))
    if (profile && !expandedProfiles.value.includes(profile.id)) expandedProfiles.value.push(profile.id)
  }
  if (fields.value.length) void nextTick(() => {
    const first = Array.from(document.querySelectorAll<HTMLElement>('[data-field]')).find(element => element.dataset.field === fields.value[0]?.path)
    first?.querySelector<HTMLElement>('input,button,[tabindex]')?.focus()
  })
}
function preserveDraft() {
  // Only public model settings. Entered credentials never enter browser storage.
  if (admin && view.value) sessionStorage.setItem(storageKey, JSON.stringify({ base_revision: view.value.revision, settings: draft.value }))
}
watch(draft, preserveDraft, { deep: true })
function rememberOperation() {
  const data = operation.value ? { id: operation.value.id } : pendingProbe.value ? { request: pendingProbe.value } : null
  if (data) sessionStorage.setItem(operationKey, JSON.stringify(data)); else sessionStorage.removeItem(operationKey)
}
async function refresh(preserve = false) {
  if (!admin) return
  loading.value = true
  try {
    const result = await modelsApi.settings()
    view.value = result
    stage.value = result.stage
    if (!preserve) {
      draft.value = clone(result.stage?.settings || result.settings)
      const saved = sessionStorage.getItem(storageKey)
      if (saved) { try { const restored = JSON.parse(saved); if (restored.settings?.schema_version === 1) draft.value = restored.settings } catch { sessionStorage.removeItem(storageKey) } }
      endpoints.value = Object.fromEntries(connections.value.map(c => [c.provider, c.endpoint]))
    }
  } catch (err) { report(err) } finally { loading.value = false }
}
function mutations(): ConnectionMutation[] {
  return connections.value.filter(c => keys.value[c.provider] || endpoints.value[c.provider] !== c.endpoint).map(c => ({ provider: c.provider, endpoint: endpoints.value[c.provider], api_key_action: keys.value[c.provider] ? 'replace' : 'keep', ...(keys.value[c.provider] ? { api_key: keys.value[c.provider] } : {}) }))
}
async function loadHistory() {
  historyLoading.value = true
  try { history.value = (await modelsApi.history()).history || []; historyLoaded.value = true }
  catch (err) { report(err) } finally { historyLoading.value = false }
}
async function restoreHistory(revision: number) {
  if (!view.value || stage.value || dirty.value || busy.value || operationRunning.value) return
  busy.value = true; error.value = ''
  try {
    stage.value = await modelsApi.restore(revision, view.value.revision)
    view.value.stage = stage.value
    draft.value = clone(stage.value.settings)
    keys.value = {}
    endpoints.value = Object.fromEntries(connections.value.map(c => [c.provider, c.endpoint]))
  } catch (err) { report(err) } finally { busy.value = false }
}
async function saveStage() {
  if (!view.value || busy.value) return
  busy.value = true; error.value = ''; fields.value = []
  try { stage.value = await modelsApi.stage(view.value.revision, clone(draft.value), mutations()); keys.value = {}; endpoints.value = Object.fromEntries(connections.value.map(c => [c.provider, c.endpoint])); draft.value = clone(stage.value.settings) }
  catch (err) { report(err) } finally { busy.value = false }
}
async function activate() {
  if (!view.value || !stage.value || dirty.value || busy.value) return
  busy.value = true; error.value = ''
  try { view.value = await modelsApi.activate(view.value.revision, stage.value.id); stage.value = view.value.stage; draft.value = clone(view.value.settings); sessionStorage.removeItem(storageKey) } catch (err) { report(err) } finally { busy.value = false }
}
async function discard() {
  if (!view.value || !stage.value) return
  busy.value = true
  try { await modelsApi.discard(stage.value.id, view.value.revision); stage.value = undefined; keys.value = {}; await refresh(true) } catch (err) { report(err) } finally { busy.value = false }
}
async function revoke(c: ModelConnection) {
  if (!view.value) return
  busy.value = true
  try { view.value = await modelsApi.revoke(c.provider, view.value.revision, c.generation); stage.value = undefined; keys.value = {} } catch (err) { report(err) } finally { busy.value = false }
}
async function startProbe(profileID: string, kind: string) {
  if (!view.value || !stage.value || operationRunning.value || dirty.value) return
  operation.value = undefined
  pendingProbe.value = { expected_revision: view.value.revision, stage_id: stage.value.id, profile_id: profileID, kind, idempotency_key: `${Date.now()}:${crypto.randomUUID()}` }
  rememberOperation(); await retryProbe()
}
async function retryProbe() {
  if (!pendingProbe.value || busy.value) return
  busy.value = true; error.value = ''
  try { operation.value = await modelsApi.probe(pendingProbe.value); pendingProbe.value = undefined; rememberOperation(); await pollProbe() } catch (err) { report(err) } finally { busy.value = false }
}
async function pollProbe() {
  if (!operation.value || disposed) return
  clearTimeout(pollTimer)
  try {
    operation.value = await modelsApi.probeStatus(operation.value.id); rememberOperation()
    if (['running', 'cancel_requested'].includes(operation.value.status)) pollTimer = setTimeout(() => { void pollProbe() }, 1500)
    else { cancelPending.value = false; await refresh(true) }
  } catch (err) { report(err) }
}
async function cancelProbe() {
  if (!operation.value || cancelPending.value) return
  cancelPending.value = true
  try { operation.value = await modelsApi.cancelProbe(operation.value.id); rememberOperation(); await pollProbe() } catch (err) { report(err); cancelPending.value = false }
}
function leaveForLogin() { preserveDraft(); keys.value = {}; void router.push({ name: 'login' }) }
onMounted(async () => {
  if (!admin) return
  await refresh()
  try { const status = await api.getWizardStatus(); setupServer.value = !!status.setup_mode } catch { /* Model settings remain usable when the legacy wizard is unavailable. */ }
  try { catalog.value = await modelsApi.catalog() } catch (err) { report(err) }
  const saved = sessionStorage.getItem(operationKey)
  if (saved) { try { const restored = JSON.parse(saved); if (restored.id) { operation.value = { id: restored.id } as ModelProbe; await pollProbe() } else if (restored.request) pendingProbe.value = restored.request } catch { sessionStorage.removeItem(operationKey) } }
})
onBeforeUnmount(() => { disposed = true; clearTimeout(pollTimer); preserveDraft(); keys.value = {}; })
</script>

<style scoped>
.models-page { max-width: 1120px; }
.models-page :deep(.n-h2) { margin: 0; }
.connection-grid, .profile-grid, .role-grid { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 16px; }
.models-page :deep(.n-input-number) { width: 100%; }
.section-gap { margin-block-start: 16px; }
.helper { display: block; max-width: 75ch; font-size: 13px; }
.save-bar { margin-block-start: 24px; padding-block: 16px; border-block-start: 1px solid rgba(255,255,255,.12); }
.models-page :deep(.n-collapse-item__header-extra) { flex-shrink: 0; }
@media (max-width: 680px) { .connection-grid, .profile-grid, .role-grid { grid-template-columns: minmax(0, 1fr); } .models-page :deep(.n-button) { min-height: 40px; } }
@media (prefers-reduced-motion: reduce) { .models-page :deep(*) { transition: none !important; animation: none !important; } }
</style>
