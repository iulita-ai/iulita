<template>
  <div class="setup-container">
    <n-card style="max-width: 700px; margin: 40px auto">
      <n-h2 style="text-align: center">{{ t('setup.title') }}</n-h2>

      <n-steps :current="currentStep" style="margin-bottom: 24px">
        <n-step :title="t('setup.welcome')" />
        <n-step :title="t('setup.llmProvider')" />
        <n-step :title="t('setup.providerConfig')" />
        <n-step :title="t('setup.features')" />
        <n-step :title="t('setup.complete')" />
      </n-steps>

      <!-- Step 1: Welcome -->
      <div v-if="currentStep === 1">
        <n-alert type="info" :title="t('setup.welcomeTitle')" style="margin-bottom: 16px">
          {{ t('setup.welcomeDesc') }}
        </n-alert>

        <n-card v-if="hasBaseConfig" :title="t('setup.existingConfig')" size="small" style="margin-bottom: 16px">
          <n-text>
            {{ t('setup.existingConfigDesc') }}
          </n-text>
          <template #footer>
            <n-space>
              <n-button type="primary" :loading="importing" @click="handleImportTOML">
                {{ t('setup.importAll') }}
              </n-button>
            </n-space>
          </template>
        </n-card>

        <div v-if="importResult">
          <n-alert :type="importResult.status === 'ok' ? 'success' : 'warning'" style="margin-bottom: 16px">
            Imported {{ importResult.imported }} settings, skipped {{ importResult.skipped }}.
            <span v-if="importResult.errors?.length"> Errors: {{ importResult.errors.join(', ') }}</span>
          </n-alert>
        </div>

        <n-space justify="end">
          <n-button v-if="importResult?.status === 'ok'" type="primary" @click="handleFinishImport">
            {{ t('setup.finishRestart') }}
          </n-button>
          <n-button v-else type="primary" @click="currentStep = 2">
            {{ t('setup.configureManually') }}
          </n-button>
        </n-space>
      </div>

      <!-- Step 2: Choose LLM Provider -->
      <div v-if="currentStep === 2">
        <n-text style="display: block; margin-bottom: 16px">
          {{ t('setup.selectProviders') }}
        </n-text>

        <n-checkbox-group v-model:value="selectedProviders" style="margin-bottom: 16px">
          <n-space vertical>
            <n-checkbox
              v-for="section in llmSections"
              :key="section.name"
              :value="section.name"
            >
              <n-text strong>{{ section.label }}</n-text>
              <n-text depth="3" style="margin-left: 8px">{{ section.description }}</n-text>
            </n-checkbox>
          </n-space>
        </n-checkbox-group>

        <n-alert v-if="selectedProviders.length === 0" type="warning" style="margin-bottom: 16px">
          {{ t('setup.selectOneProvider') }}
        </n-alert>

        <n-space justify="space-between">
          <n-button @click="currentStep = 1">{{ t('setup.back') }}</n-button>
          <n-button type="primary" :disabled="selectedProviders.length === 0" @click="currentStep = 3">
            {{ t('setup.next') }}
          </n-button>
        </n-space>
      </div>

      <!-- Step 3: Provider Config -->
      <div v-if="currentStep === 3">
        <n-collapse :default-expanded-names="selectedProviders">
          <n-collapse-item
            v-for="section in activeLLMSections"
            :key="section.name"
            :title="section.label"
            :name="section.name"
          >
            <div v-for="field in section.fields" :key="field.key" style="margin-bottom: 12px">
              <n-form-item :label="field.label">
                <template #feedback>
                  <n-text depth="3" style="font-size: 12px">{{ field.description }}</n-text>
                </template>

                <!-- Secret field -->
                <n-input
                  v-if="field.secret || field.type === 'secret'"
                  v-model:value="fieldValues[field.key]"
                  type="password"
                  show-password-on="click"
                  :placeholder="field.has_value ? '(configured)' : field.default || ''"
                  @update:value="fieldDirty[field.key] = true"
                />

                <!-- Select field -->
                <n-select
                  v-else-if="field.type === 'select'"
                  v-model:value="fieldValues[field.key]"
                  :options="selectOptions(field)"
                  :placeholder="field.default || 'Select...'"
                  filterable
                  tag
                  @update:value="fieldDirty[field.key] = true"
                />

                <!-- Bool field -->
                <n-switch
                  v-else-if="field.type === 'bool'"
                  :value="fieldValues[field.key] === 'true'"
                  @update:value="(v: boolean) => { fieldValues[field.key] = String(v); fieldDirty[field.key] = true }"
                />

                <!-- Int field -->
                <n-input-number
                  v-else-if="field.type === 'int'"
                  :value="fieldValues[field.key] ? Number(fieldValues[field.key]) : undefined"
                  :placeholder="field.default || ''"
                  style="width: 100%"
                  @update:value="(v: number | null) => { fieldValues[field.key] = v != null ? String(v) : ''; fieldDirty[field.key] = true }"
                />

                <!-- Default: string/url -->
                <n-input
                  v-else
                  v-model:value="fieldValues[field.key]"
                  :placeholder="field.default || ''"
                  @update:value="fieldDirty[field.key] = true"
                />
              </n-form-item>
            </div>
          </n-collapse-item>
        </n-collapse>

        <n-space justify="space-between" style="margin-top: 16px">
          <n-button @click="currentStep = 2">{{ t('setup.back') }}</n-button>
          <n-button type="primary" :loading="savingConfig" @click="saveProviderConfig">
            {{ t('setup.saveNext') }}
          </n-button>
        </n-space>
      </div>

      <!-- Step 4: Features -->
      <div v-if="currentStep === 4">
        <n-text style="display: block; margin-bottom: 16px">
          {{ t('setup.configureFeatures') }}
        </n-text>

        <n-collapse>
          <n-collapse-item
            v-for="section in featureSections"
            :key="section.name"
            :title="section.label"
            :name="section.name"
          >
            <div v-for="field in section.fields" :key="field.key" style="margin-bottom: 12px">
              <n-form-item :label="field.label">
                <template #feedback>
                  <n-text depth="3" style="font-size: 12px">{{ field.description }}</n-text>
                </template>

                <n-input
                  v-if="field.secret || field.type === 'secret'"
                  v-model:value="fieldValues[field.key]"
                  type="password"
                  show-password-on="click"
                  :placeholder="field.default || ''"
                  @update:value="fieldDirty[field.key] = true"
                />
                <n-switch
                  v-else-if="field.type === 'bool'"
                  :value="fieldValues[field.key] === 'true'"
                  @update:value="(v: boolean) => { fieldValues[field.key] = String(v); fieldDirty[field.key] = true }"
                />
                <n-select
                  v-else-if="field.type === 'select'"
                  v-model:value="fieldValues[field.key]"
                  :options="selectOptions(field)"
                  filterable
                  tag
                  @update:value="fieldDirty[field.key] = true"
                />
                <n-input
                  v-else
                  v-model:value="fieldValues[field.key]"
                  :placeholder="field.default || ''"
                  @update:value="fieldDirty[field.key] = true"
                />
              </n-form-item>
            </div>
          </n-collapse-item>
        </n-collapse>

        <n-space justify="space-between" style="margin-top: 16px">
          <n-button @click="currentStep = 3">{{ t('setup.back') }}</n-button>
          <n-button type="primary" :loading="savingConfig" @click="saveFeatureConfig">
            {{ t('setup.saveNext') }}
          </n-button>
        </n-space>
      </div>

      <!-- Step 5: Complete -->
      <div v-if="currentStep === 5">
        <n-result
          :status="completeError ? 'error' : 'success'"
          :title="completeError ? t('setup.setupError') : t('setup.setupComplete')"
          :description="completeError || t('setup.setupCompleteDesc')"
        >
          <template #footer>
            <n-space vertical align="center">
              <n-button v-if="completeError" type="primary" @click="handleComplete">
                {{ t('setup.retry') }}
              </n-button>
              <n-alert v-else type="info" :title="t('setup.nextSteps')" style="max-width: 400px; text-align: left">
                <ul style="margin: 0; padding-left: 20px">
                  <li>{{ t('setup.restartApp') }}</li>
                  <li>{{ t('setup.configureChannels') }}</li>
                  <li>{{ t('setup.customizeSkills') }}</li>
                </ul>
              </n-alert>
            </n-space>
          </template>
        </n-result>
      </div>
    </n-card>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  NCard, NH2, NSteps, NStep, NAlert, NButton, NSpace, NText,
  NCheckboxGroup, NCheckbox, NCollapse, NCollapseItem, NFormItem,
  NInput, NSelect, NSwitch, NInputNumber, NResult, useMessage,
} from 'naive-ui'
import { api } from '../api'
import type { WizardSchemaSection, WizardSchemaField, ImportTOMLResponse } from '../api'

const { t } = useI18n()
const message = useMessage()

const currentStep = ref(1)
const loading = ref(true)
const sections = ref<WizardSchemaSection[]>([])
const selectedProviders = ref<string[]>([])
const fieldValues = ref<Record<string, string>>({})
const fieldDirty = ref<Record<string, boolean>>({})
const savingConfig = ref(false)
const importing = ref(false)
const importResult = ref<ImportTOMLResponse | null>(null)
const completeError = ref('')
const hasBaseConfig = ref(false)

const llmSections = computed(() => sections.value.filter(s => s.is_llm))
const activeLLMSections = computed(() =>
  llmSections.value.filter(s => selectedProviders.value.includes(s.name))
)
const featureSections = computed(() => sections.value.filter(s => !s.is_llm))

function selectOptions(field: WizardSchemaField) {
  return (field.options || []).map(o => ({ label: o, value: o }))
}

async function loadSchema() {
  loading.value = true
  try {
    const resp = await api.getWizardSchema()
    sections.value = resp.sections

    // Pre-fill field values from effective values.
    for (const section of resp.sections) {
      for (const field of section.fields) {
        if (field.has_value && !field.secret) {
          fieldValues.value[field.key] = field.value || ''
        } else if (field.default) {
          fieldValues.value[field.key] = field.default
        }
      }
    }

    // Check if base config has any values (for import detection).
    hasBaseConfig.value = resp.sections.some(s =>
      s.fields.some(f => f.has_value)
    )

    // Pre-select providers that already have values.
    const preSelected: string[] = []
    for (const s of resp.sections) {
      if (s.is_llm && s.fields.some(f => f.has_value)) {
        preSelected.push(s.name)
      }
    }
    if (preSelected.length > 0) {
      selectedProviders.value = preSelected
    }
  } catch (e: any) {
    message.error(t('setup.loadSchemaFailed'))
  } finally {
    loading.value = false
  }
}

async function saveDirtyFields(): Promise<boolean> {
  const dirtyKeys = Object.keys(fieldDirty.value).filter(k => fieldDirty.value[k])
  if (dirtyKeys.length === 0) return true

  savingConfig.value = true
  try {
    for (const key of dirtyKeys) {
      const val = fieldValues.value[key] || ''
      if (!val) continue
      const field = sections.value.flatMap(s => s.fields).find(f => f.key === key)
      const encrypt = field?.secret || false
      await api.setConfig(key, val, encrypt)
      fieldDirty.value[key] = false
    }
    return true
  } catch (e: any) {
    message.error(t('setup.saveFailed', { error: e.message }))
    return false
  } finally {
    savingConfig.value = false
  }
}

async function saveProviderConfig() {
  // Set routing.default_provider based on first selected provider.
  if (selectedProviders.value.length > 0) {
    fieldValues.value['routing.default_provider'] = selectedProviders.value[0]
    fieldDirty.value['routing.default_provider'] = true
  }

  if (await saveDirtyFields()) {
    currentStep.value = 4
  }
}

async function saveFeatureConfig() {
  if (await saveDirtyFields()) {
    await handleComplete()
  }
}

async function handleComplete() {
  completeError.value = ''
  try {
    await api.completeWizard()
    currentStep.value = 5
  } catch (e: any) {
    completeError.value = e.message || t('setup.completeFailed')
    currentStep.value = 5
  }
}

async function handleImportTOML() {
  importing.value = true
  try {
    importResult.value = await api.importTOML()
    if (importResult.value.status === 'ok') {
      message.success(t('setup.importSuccess', { count: importResult.value.imported }))
    } else {
      message.warning(t('setup.importWarning'))
    }
  } catch (e: any) {
    message.error(t('setup.importFailed', { error: e.message }))
  } finally {
    importing.value = false
  }
}

function handleFinishImport() {
  currentStep.value = 5
  completeError.value = ''
}

onMounted(async () => {
  // Check if wizard is already completed.
  try {
    const status = await api.getWizardStatus()
    if (status.wizard_completed && !status.setup_mode) {
      // Already completed, redirect to dashboard.
      window.location.href = '/'
      return
    }
  } catch {
    // Continue with wizard.
  }
  await loadSchema()
})
</script>

<style scoped>
.setup-container {
  min-height: 100vh;
  display: flex;
  align-items: flex-start;
  justify-content: center;
  padding: 20px;
}
</style>
