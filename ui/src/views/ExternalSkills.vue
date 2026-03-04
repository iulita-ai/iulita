<template>
  <n-space vertical :size="16">
    <n-page-header :title="t('externalSkills.title')" :subtitle="t('externalSkills.subtitle')">
      <template #extra>
        <n-button @click="openUrlModal">{{ t('externalSkills.installFromUrl') }}</n-button>
      </template>
    </n-page-header>

    <!-- Error alerts (above tabs for visibility) -->
    <n-alert v-if="lastError" :title="t('common.error')" type="error" closable @close="lastError = ''">
      {{ lastError }}
    </n-alert>

    <n-alert
      v-if="isolationError"
      :title="t('externalSkills.isolationDisabled', { level: isolationLabel(isolationError.level) })"
      type="error"
      closable
      @close="isolationError = null"
    >
      <n-space align="center" :size="12" style="margin-top: 4px">
        <n-text>
          {{ t('externalSkills.isolationRequired', { level: isolationLabel(isolationError.level) }) }}
        </n-text>
        <n-button
          type="primary"
          size="small"
          :loading="enablingIsolation"
          @click="handleEnableAndRetry"
        >
          {{ t('externalSkills.enableRetry') }}
        </n-button>
      </n-space>
    </n-alert>

    <n-alert v-if="lastWarnings.length > 0" :title="t('externalSkills.warnings')" type="warning" closable @close="lastWarnings = []">
      <ul style="margin: 0; padding-left: 20px">
        <li v-for="(w, i) in lastWarnings" :key="i">{{ w }}</li>
      </ul>
    </n-alert>

    <n-tabs v-model:value="activeTab" type="line" animated>
      <!-- Installed Tab -->
      <n-tab-pane name="installed" :tab="t('externalSkills.installed')">
        <n-spin v-if="loadingInstalled" style="display: flex; justify-content: center; padding: 48px 0" />
        <template v-else>
          <n-empty v-if="installed.length === 0" :description="t('externalSkills.noInstalled')">
            <template #extra>
              <n-button type="primary" @click="activeTab = 'marketplace'">{{ t('externalSkills.browseMarketplace') }}</n-button>
            </template>
          </n-empty>
          <n-list v-else bordered>
            <n-list-item v-for="sk in installed" :key="sk.slug">
              <n-thing>
                <template #header>
                  <n-space :size="8" align="center">
                    <n-text strong style="cursor: pointer; text-decoration: underline dotted" @click="openDetail(sk)">{{ sk.name }}</n-text>
                    <n-tag size="small" :bordered="false">v{{ sk.version }}</n-tag>
                    <n-tag size="small" :bordered="false" :type="isolationTagType(sk.isolation)">
                      {{ isolationLabel(sk.isolation) }}
                    </n-tag>
                    <n-tag
                      v-if="sk.effective_mode && sk.effective_mode !== sk.isolation"
                      size="small"
                      :bordered="false"
                      type="warning"
                    >
                      {{ effectiveModeLabel(sk.effective_mode) }}
                    </n-tag>
                    <n-tag v-if="sk.has_code" size="small" :bordered="false" type="info">{{ t('externalSkills.hasCode') }}</n-tag>
                  </n-space>
                </template>
                <template #header-extra>
                  <n-space :size="8" align="center">
                    <n-button
                      size="small"
                      :loading="updatingSlug === sk.slug"
                      :disabled="!!updatingSlug"
                      @click="handleUpdate(sk.slug)"
                    >
                      {{ t('externalSkills.update') }}
                    </n-button>
                    <n-popconfirm @positive-click="handleUninstall(sk.slug)">
                      <template #trigger>
                        <n-button size="small" type="error" quaternary :disabled="!!uninstallingSlug">
                          {{ t('externalSkills.remove') }}
                        </n-button>
                      </template>
                      {{ t('externalSkills.removeConfirm', { name: sk.name }) }}
                    </n-popconfirm>
                    <n-switch
                      :value="sk.enabled"
                      :disabled="togglingSlug === sk.slug"
                      @update:value="(val: boolean) => handleToggle(sk.slug, val)"
                      size="small"
                    />
                  </n-space>
                </template>
                <template #description>
                  <n-space vertical :size="4">
                    <n-text :depth="3" style="font-size: 13px">{{ sk.description || t('externalSkills.noDescription') }}</n-text>
                    <n-space v-if="parseJSON(sk.capabilities).length" :size="4" style="margin-top: 4px">
                      <n-tag v-for="cap in parseJSON(sk.capabilities)" :key="cap" size="tiny" :bordered="false" type="success">{{ cap }}</n-tag>
                    </n-space>
                    <n-space v-if="parseJSON(sk.requires_bins).length || parseJSON(sk.requires_env).length" :size="4" style="margin-top: 2px">
                      <n-tag v-for="bin in parseJSON(sk.requires_bins)" :key="'bin:'+bin" size="tiny" :bordered="false" type="warning">bin: {{ bin }}</n-tag>
                      <n-tag v-for="env in parseJSON(sk.requires_env)" :key="'env:'+env" size="tiny" :bordered="false" type="warning">env: {{ env }}</n-tag>
                    </n-space>
                    <n-alert
                      v-if="parseJSON(sk.install_warnings).length"
                      type="warning"
                      size="small"
                      style="margin-top: 4px; font-size: 12px"
                      :show-icon="false"
                    >
                      <ul style="margin: 0; padding-left: 16px">
                        <li v-for="(w, i) in parseJSON(sk.install_warnings)" :key="i">{{ w }}</li>
                      </ul>
                    </n-alert>
                  </n-space>
                </template>
              </n-thing>
              <template #suffix>
                <n-space :size="4" style="margin-top: 8px">
                  <n-tag v-if="sk.author" size="small" :bordered="false" type="info">{{ sk.author }}</n-tag>
                  <n-tag v-for="tag in parseTags(sk.tags)" :key="tag" size="small" :bordered="false">{{ tag }}</n-tag>
                  <n-tag size="small" :bordered="false" type="default">{{ sk.source }}</n-tag>
                  <n-text v-if="sk.installed_at" :depth="3" style="font-size: 11px; margin-left: 8px">
                    installed {{ formatDate(sk.installed_at) }}
                  </n-text>
                </n-space>
              </template>
            </n-list-item>
          </n-list>
        </template>
      </n-tab-pane>

      <!-- Marketplace Tab -->
      <n-tab-pane name="marketplace" :tab="t('externalSkills.marketplace')">
        <n-space vertical :size="16">
          <n-input-group>
            <n-input
              v-model:value="searchQuery"
              :placeholder="t('externalSkills.searchPlaceholder')"
              clearable
              @keydown.enter="handleSearch"
              style="max-width: 500px"
            />
            <n-button type="primary" :loading="searching" @click="handleSearch" :disabled="!searchQuery.trim()">
              {{ t('common.search') }}
            </n-button>
          </n-input-group>

          <n-spin v-if="searching" style="display: flex; justify-content: center; padding: 48px 0" />
          <template v-else-if="searchResults.length > 0">
            <n-text :depth="3" style="font-size: 13px">{{ t('externalSkills.results', { count: searchResults.length }) }}</n-text>
            <n-grid :cols="3" :x-gap="12" :y-gap="12" responsive="screen" :item-responsive="true">
              <n-grid-item v-for="sk in searchResults" :key="sk.slug" span="3 m:1">
                <n-card hoverable size="small" @click="openMarketplaceDetail(sk)" style="cursor: pointer">
                  <template #header>
                    <n-text strong>{{ sk.name }}</n-text>
                  </template>
                  <template #header-extra>
                    <n-tag v-if="sk.version" size="small" :bordered="false">v{{ sk.version }}</n-tag>
                  </template>
                  <n-text :depth="3" style="font-size: 13px; display: -webkit-box; -webkit-line-clamp: 3; -webkit-box-orient: vertical; overflow: hidden;">
                    {{ sk.description || t('externalSkills.noDescription') }}
                  </n-text>
                  <div style="margin-top: 8px">
                    <n-space :size="4" align="center">
                      <n-tag v-if="sk.author" size="small" :bordered="false" type="info">{{ sk.author }}</n-tag>
                      <n-tag v-for="tag in (sk.tags || []).slice(0, 3)" :key="tag" size="small" :bordered="false">{{ tag }}</n-tag>
                    </n-space>
                  </div>
                  <n-space v-if="sk.downloads || sk.stars || sk.updated_at" :size="12" style="margin-top: 6px; font-size: 12px">
                    <n-text v-if="sk.downloads" :depth="3">{{ t('externalSkills.downloads', { count: sk.downloads }) }}</n-text>
                    <n-text v-if="sk.stars" :depth="3">{{ t('externalSkills.stars', { count: sk.stars }) }}</n-text>
                    <n-text v-if="sk.updated_at" :depth="3">{{ t('externalSkills.updatedAt', { time: formatTimestamp(sk.updated_at) }) }}</n-text>
                  </n-space>
                  <template #action>
                    <n-button
                      v-if="isInstalled(sk.slug)"
                      size="small"
                      type="success"
                      disabled
                      block
                    >
                      {{ t('externalSkills.installed') }}
                    </n-button>
                    <n-button
                      v-else
                      size="small"
                      type="primary"
                      block
                      :loading="installingSlug === sk.slug"
                      :disabled="!!installingSlug"
                      @click.stop="handleInstall(sk.slug, sk.source)"
                    >
                      {{ t('externalSkills.install') }}
                    </n-button>
                  </template>
                </n-card>
              </n-grid-item>
            </n-grid>
          </template>
          <n-empty v-else-if="hasSearched" :description="t('externalSkills.noResults')" />
          <n-empty v-else :description="t('externalSkills.searchPrompt')" />
        </n-space>
      </n-tab-pane>
    </n-tabs>

    <!-- Skill detail drawer -->
    <n-drawer v-model:show="showDetail" :width="480" placement="right">
      <n-drawer-content v-if="detailSkill" :title="detailSkill.name" closable>
        <n-space vertical :size="16">
          <!-- Header badges -->
          <n-space :size="8" align="center" wrap>
            <n-tag size="small">v{{ detailSkill.version }}</n-tag>
            <n-tag size="small" :type="isolationTagType(detailSkill.isolation)">
              {{ isolationLabel(detailSkill.isolation) }}
            </n-tag>
            <n-tag
              v-if="detailSkill.effective_mode && detailSkill.effective_mode !== detailSkill.isolation"
              size="small"
              type="warning"
            >
              {{ effectiveModeLabel(detailSkill.effective_mode) }}
            </n-tag>
            <n-tag v-if="detailSkill.has_code" size="small" type="info">{{ t('externalSkills.hasCode') }}</n-tag>
            <n-tag :type="detailSkill.enabled ? 'success' : 'default'" size="small">
              {{ detailSkill.enabled ? t('common.enabled') : t('common.disabled') }}
            </n-tag>
          </n-space>

          <!-- Description -->
          <n-descriptions bordered :column="1" label-placement="top" size="small">
            <n-descriptions-item :label="t('externalSkills.description')">
              {{ detailSkill.description || t('externalSkills.noDescription') }}
            </n-descriptions-item>
            <n-descriptions-item :label="t('externalSkills.author')">
              {{ detailSkill.author || '—' }}
            </n-descriptions-item>
            <n-descriptions-item :label="t('externalSkills.sourceLabel')">
              <n-space :size="4" align="center">
                <n-tag size="small" :bordered="false">{{ detailSkill.source }}</n-tag>
                <n-text v-if="detailSkill.source_ref" :depth="3" style="font-size: 12px">
                  {{ detailSkill.source_ref }}
                </n-text>
              </n-space>
            </n-descriptions-item>
            <n-descriptions-item :label="t('externalSkills.slug')">
              <n-text code>{{ detailSkill.slug }}</n-text>
            </n-descriptions-item>
            <n-descriptions-item :label="t('externalSkills.checksum')">
              <n-text v-if="detailSkill.checksum" code style="font-size: 11px; word-break: break-all">
                {{ detailSkill.checksum }}
              </n-text>
              <n-text v-else :depth="3">—</n-text>
            </n-descriptions-item>
          </n-descriptions>

          <!-- Tags -->
          <div v-if="parseTags(detailSkill.tags).length">
            <n-text strong style="font-size: 13px; display: block; margin-bottom: 6px">{{ t('externalSkills.tags') }}</n-text>
            <n-space :size="4">
              <n-tag v-for="tag in parseTags(detailSkill.tags)" :key="tag" size="small" :bordered="false">{{ tag }}</n-tag>
            </n-space>
          </div>

          <!-- Capabilities -->
          <div v-if="parseJSON(detailSkill.capabilities).length">
            <n-text strong style="font-size: 13px; display: block; margin-bottom: 6px">{{ t('externalSkills.capabilities') }}</n-text>
            <n-space :size="4">
              <n-tag v-for="cap in parseJSON(detailSkill.capabilities)" :key="cap" size="small" :bordered="false" type="success">{{ cap }}</n-tag>
            </n-space>
          </div>

          <!-- Dependencies -->
          <div v-if="parseJSON(detailSkill.requires_bins).length || parseJSON(detailSkill.requires_env).length">
            <n-text strong style="font-size: 13px; display: block; margin-bottom: 6px">{{ t('externalSkills.dependencies') }}</n-text>
            <n-space :size="4" wrap>
              <n-tag v-for="bin in parseJSON(detailSkill.requires_bins)" :key="'bin:'+bin" size="small" :bordered="false" type="warning">
                bin: {{ bin }}
              </n-tag>
              <n-tag v-for="env in parseJSON(detailSkill.requires_env)" :key="'env:'+env" size="small" :bordered="false" type="warning">
                env: {{ env }}
              </n-tag>
            </n-space>
          </div>

          <!-- Allowed tools -->
          <div v-if="parseJSON(detailSkill.allowed_tools).length">
            <n-text strong style="font-size: 13px; display: block; margin-bottom: 6px">{{ t('externalSkills.allowedTools') }}</n-text>
            <n-space :size="4">
              <n-tag v-for="tool in parseJSON(detailSkill.allowed_tools)" :key="tool" size="small" :bordered="false" type="info">{{ tool }}</n-tag>
            </n-space>
          </div>

          <!-- Config keys -->
          <div v-if="parseJSON(detailSkill.config_keys).length">
            <n-text strong style="font-size: 13px; display: block; margin-bottom: 6px">{{ t('externalSkills.configKeys') }}</n-text>
            <n-space vertical :size="2">
              <n-text v-for="key in parseJSON(detailSkill.config_keys)" :key="key" code style="font-size: 12px">
                {{ key }}
                <n-tag v-if="parseJSON(detailSkill.secret_keys).includes(key)" size="tiny" type="error" :bordered="false" style="margin-left: 4px">secret</n-tag>
              </n-text>
            </n-space>
          </div>

          <!-- Install warnings -->
          <n-alert
            v-if="parseJSON(detailSkill.install_warnings).length"
            type="warning"
            :title="t('externalSkills.warnings')"
            size="small"
          >
            <ul style="margin: 0; padding-left: 16px">
              <li v-for="(w, i) in parseJSON(detailSkill.install_warnings)" :key="i" style="font-size: 12px">{{ w }}</li>
            </ul>
          </n-alert>

          <!-- Timestamps -->
          <n-descriptions bordered :column="1" label-placement="left" size="small">
            <n-descriptions-item :label="t('externalSkills.installedAt')">
              {{ formatDateTime(detailSkill.installed_at) }}
            </n-descriptions-item>
            <n-descriptions-item v-if="detailSkill.updated_at" :label="t('externalSkills.updatedLabel')">
              {{ formatDateTime(detailSkill.updated_at) }}
            </n-descriptions-item>
            <n-descriptions-item :label="t('externalSkills.installDir')">
              <n-text code style="font-size: 11px; word-break: break-all">{{ detailSkill.install_dir }}</n-text>
            </n-descriptions-item>
          </n-descriptions>
        </n-space>
      </n-drawer-content>
    </n-drawer>

    <!-- Marketplace skill detail drawer -->
    <n-drawer v-model:show="showMarketplaceDetail" :width="480" placement="right">
      <n-drawer-content v-if="marketplaceDetailSkill" :title="marketplaceDetailSkill.name" closable>
        <n-space vertical :size="16">
          <!-- Header badges -->
          <n-space :size="8" align="center" wrap>
            <n-tag v-if="marketplaceDetailSkill.version" size="small">v{{ marketplaceDetailSkill.version }}</n-tag>
            <n-tag size="small" :bordered="false" type="default">{{ marketplaceDetailSkill.source }}</n-tag>
            <n-spin v-if="loadingMarketplaceDetail" :size="14" style="margin-left: 4px" />
          </n-space>

          <!-- Description (full, no truncation) -->
          <div>
            <n-text strong style="font-size: 13px; display: block; margin-bottom: 6px">{{ t('externalSkills.description') }}</n-text>
            <n-text :depth="2" style="font-size: 13px; white-space: pre-wrap">
              {{ marketplaceDetailSkill.description || t('externalSkills.noDescAvailable') }}
            </n-text>
          </div>

          <!-- Author -->
          <n-descriptions bordered :column="1" label-placement="left" size="small">
            <n-descriptions-item :label="t('externalSkills.author')">
              <n-space :size="4" align="center">
                <n-text>{{ marketplaceDetailSkill.author || '—' }}</n-text>
                <n-text
                  v-if="marketplaceDetailSkill.owner_display_name && marketplaceDetailSkill.owner_display_name !== marketplaceDetailSkill.author"
                  :depth="3"
                  style="font-size: 12px"
                >
                  ({{ marketplaceDetailSkill.owner_display_name }})
                </n-text>
              </n-space>
            </n-descriptions-item>
            <n-descriptions-item :label="t('externalSkills.sourceRef')">
              {{ marketplaceDetailSkill.source_ref || '—' }}
            </n-descriptions-item>
            <n-descriptions-item :label="t('externalSkills.slug')">
              <n-text code>{{ marketplaceDetailSkill.slug }}</n-text>
            </n-descriptions-item>
          </n-descriptions>

          <!-- Tags -->
          <div v-if="(marketplaceDetailSkill.tags || []).length">
            <n-text strong style="font-size: 13px; display: block; margin-bottom: 6px">{{ t('externalSkills.tags') }}</n-text>
            <n-space :size="4">
              <n-tag v-for="tag in marketplaceDetailSkill.tags" :key="tag" size="small" :bordered="false">{{ tag }}</n-tag>
            </n-space>
          </div>

          <!-- Stats -->
          <div v-if="marketplaceDetailSkill.downloads || marketplaceDetailSkill.stars || marketplaceDetailSkill.updated_at">
            <n-text strong style="font-size: 13px; display: block; margin-bottom: 6px">{{ t('externalSkills.stats') }}</n-text>
            <n-descriptions bordered :column="1" label-placement="left" size="small">
              <n-descriptions-item v-if="marketplaceDetailSkill.downloads" :label="t('externalSkills.downloadsLabel')">
                {{ marketplaceDetailSkill.downloads.toLocaleString() }}
              </n-descriptions-item>
              <n-descriptions-item v-if="marketplaceDetailSkill.stars" :label="t('externalSkills.starsLabel')">
                {{ marketplaceDetailSkill.stars.toLocaleString() }}
              </n-descriptions-item>
              <n-descriptions-item v-if="marketplaceDetailSkill.updated_at" :label="t('externalSkills.lastUpdated')">
                {{ formatTimestamp(marketplaceDetailSkill.updated_at) }}
              </n-descriptions-item>
            </n-descriptions>
          </div>
        </n-space>

        <template #footer>
          <n-button
            v-if="isInstalled(marketplaceDetailSkill.slug)"
            type="success"
            disabled
            block
          >
            {{ t('externalSkills.installed') }}
          </n-button>
          <n-button
            v-else
            type="primary"
            block
            :loading="installingSlug === marketplaceDetailSkill.slug"
            :disabled="!!installingSlug"
            @click.stop="handleInstallFromDrawer(marketplaceDetailSkill.slug, marketplaceDetailSkill.source)"
          >
            {{ t('externalSkills.install') }}
          </n-button>
        </template>
      </n-drawer-content>
    </n-drawer>

    <!-- Install from URL modal -->
    <n-modal v-model:show="showUrlModal" preset="dialog" :title="t('externalSkills.installFromUrlTitle')" style="width: 500px">
      <n-space vertical :size="12">
        <n-text :depth="3">{{ t('externalSkills.installFromUrlDesc') }}</n-text>
        <n-input
          v-model:value="urlInput"
          :placeholder="t('externalSkills.urlPlaceholder')"
          @keydown.enter="handleUrlInstall"
        />
      </n-space>
      <template #action>
        <n-button @click="showUrlModal = false">{{ t('common.cancel') }}</n-button>
        <n-button type="primary" :loading="installingUrl" :disabled="!urlInput.trim()" @click="handleUrlInstall">
          {{ t('externalSkills.install') }}
        </n-button>
      </template>
    </n-modal>
  </n-space>
</template>

<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  NSpace, NPageHeader, NTabs, NTabPane, NList, NListItem, NThing,
  NButton, NSwitch, NTag, NText, NEmpty, NCard, NGrid, NGridItem,
  NInput, NInputGroup, NPopconfirm, NModal, NAlert, NSpin,
  NDrawer, NDrawerContent, NDescriptions, NDescriptionsItem,
  useMessage,
} from 'naive-ui'
import { api } from '../api'
import type { InstalledSkill, ExternalSkillResult, ExternalSkillDetail } from '../api'

const { t } = useI18n()
const message = useMessage()

// Installed tab
const installed = ref<InstalledSkill[]>([])
const loadingInstalled = ref(false)
const togglingSlug = ref('')
const updatingSlug = ref('')
const uninstallingSlug = ref('')

// Marketplace tab
const activeTab = ref('installed')
const searchQuery = ref('')
const searchResults = ref<ExternalSkillResult[]>([])
const searching = ref(false)
const hasSearched = ref(false)
const installingSlug = ref('')

// Skill detail drawer (installed)
const showDetail = ref(false)
const detailSkill = ref<InstalledSkill | null>(null)

// Marketplace detail drawer
const showMarketplaceDetail = ref(false)
const marketplaceDetailSkill = ref<ExternalSkillDetail | null>(null)
const loadingMarketplaceDetail = ref(false)

// URL install modal
const showUrlModal = ref(false)
const urlInput = ref('')
const installingUrl = ref(false)

// Warnings
const lastWarnings = ref<string[]>([])

// Generic error (fallback for when toast doesn't show)
const lastError = ref('')

// Isolation error handling
interface IsolationErrorState {
  level: string
  configKey: string
  skillRef: string
  source: string // '__url__' sentinel for URL-modal path
}
const isolationError = ref<IsolationErrorState | null>(null)
const enablingIsolation = ref(false)

const ISOLATION_ERROR_RE = /^(\w+)-isolation skills are disabled \(set ([\w.]+)=true\)$/

function parseIsolationError(msg: string): { level: string; configKey: string } | null {
  const m = ISOLATION_ERROR_RE.exec(msg)
  if (!m) return null
  return { level: m[1], configKey: m[2] }
}

function openUrlModal() {
  urlInput.value = ''
  showUrlModal.value = true
}

function isolationTagType(level: string): 'success' | 'warning' | 'info' | 'default' {
  switch (level) {
    case 'text_only': return 'success'
    case 'shell': return 'warning'
    case 'docker': return 'info'
    default: return 'default'
  }
}

function isolationLabel(level: string): string {
  switch (level) {
    case 'text_only': return t('externalSkills.textOnly')
    case 'shell': return t('externalSkills.shell')
    case 'docker': return t('externalSkills.docker')
    case 'wasm': return t('externalSkills.wasm')
    default: return level
  }
}

function parseTags(tags: string): string[] {
  if (!tags) return []
  return tags.split(',').map(t => t.trim()).filter(Boolean)
}

function parseJSON(jsonStr: string): string[] {
  if (!jsonStr) return []
  try { return JSON.parse(jsonStr) } catch { return [] }
}

function effectiveModeLabel(mode: string): string {
  switch (mode) {
    case 'webfetch_proxy': return t('externalSkills.viaWebfetch')
    case 'text_only': return t('externalSkills.fallbackTextOnly')
    default: return mode
  }
}

function formatDate(iso: string): string {
  if (!iso) return ''
  const d = new Date(iso)
  return d.toLocaleDateString()
}

function formatTimestamp(ts: number): string {
  if (!ts) return ''
  const d = new Date(ts * 1000)
  return d.toLocaleDateString()
}

function formatDateTime(iso: string): string {
  if (!iso) return '—'
  const d = new Date(iso)
  return d.toLocaleString()
}

function openDetail(sk: InstalledSkill) {
  detailSkill.value = sk
  showDetail.value = true
}

async function openMarketplaceDetail(sk: ExternalSkillResult) {
  // Pre-populate from search result data for instant display.
  marketplaceDetailSkill.value = {
    slug: sk.slug,
    name: sk.name,
    version: sk.version,
    description: sk.description,
    author: sk.author,
    tags: sk.tags || [],
    source: sk.source,
    source_ref: sk.source_ref,
    downloads: sk.downloads,
    stars: sk.stars,
    updated_at: sk.updated_at,
  }
  showMarketplaceDetail.value = true
  loadingMarketplaceDetail.value = true
  try {
    const detail = await api.getMarketplaceSkillDetail(sk.slug)
    marketplaceDetailSkill.value = detail
  } catch {
    // Keep pre-populated search data on failure.
  } finally {
    loadingMarketplaceDetail.value = false
  }
}

async function handleInstallFromDrawer(slug: string, source: string) {
  await handleInstall(slug, source)
  if (!lastError.value && !isolationError.value) {
    showMarketplaceDetail.value = false
  }
}

function isInstalled(slug: string): boolean {
  return installed.value.some(s => s.slug === slug)
}

async function loadInstalled() {
  loadingInstalled.value = true
  try {
    installed.value = await api.listExternalSkills()
  } catch (e: any) {
    message.error(t('externalSkills.loadFailed', { error: e.message }))
  } finally {
    loadingInstalled.value = false
  }
}

async function handleToggle(slug: string, enabled: boolean) {
  togglingSlug.value = slug
  try {
    if (enabled) {
      await api.enableExternalSkill(slug)
    } else {
      await api.disableExternalSkill(slug)
    }
    const sk = installed.value.find(s => s.slug === slug)
    if (sk) sk.enabled = enabled
    message.success(t('externalSkills.toggleSuccess', { slug, state: enabled ? t('common.enabled') : t('common.disabled') }))
  } catch (e: any) {
    message.error(e.message)
  } finally {
    togglingSlug.value = ''
  }
}

async function handleUpdate(slug: string) {
  updatingSlug.value = slug
  try {
    const res = await api.updateExternalSkill(slug)
    const idx = installed.value.findIndex(s => s.slug === slug)
    if (idx !== -1) {
      installed.value[idx] = res.skill
      if (detailSkill.value?.slug === slug) {
        detailSkill.value = res.skill
      }
    }
    if (res.warnings.length > 0) {
      lastWarnings.value = res.warnings
    }
    message.success(t('externalSkills.updateSuccess', { slug, version: res.skill.version }))
  } catch (e: any) {
    message.error(t('externalSkills.updateFailed', { error: e.message }))
  } finally {
    updatingSlug.value = ''
  }
}

async function handleUninstall(slug: string) {
  uninstallingSlug.value = slug
  try {
    await api.uninstallExternalSkill(slug)
    installed.value = installed.value.filter(s => s.slug !== slug)
    message.success(t('externalSkills.removeSuccess', { slug }))
  } catch (e: any) {
    message.error(t('externalSkills.removeFailed', { error: e.message }))
  } finally {
    uninstallingSlug.value = ''
  }
}

async function handleSearch() {
  const q = searchQuery.value.trim()
  if (!q) return
  searching.value = true
  hasSearched.value = true
  try {
    const res = await api.searchExternalSkills(q)
    searchResults.value = res.results
  } catch (e: any) {
    message.error(t('externalSkills.searchFailed', { error: e.message }))
  } finally {
    searching.value = false
  }
}

async function handleInstall(skillRef: string, source?: string) {
  installingSlug.value = skillRef
  lastError.value = ''
  try {
    const res = await api.installExternalSkill(skillRef, source)
    installed.value.push(res.skill)
    if (res.warnings.length > 0) {
      lastWarnings.value = res.warnings
    }
    message.success(t('externalSkills.installSuccess', { name: res.skill.name }))
  } catch (e: any) {
    const parsed = parseIsolationError(e.message)
    if (parsed) {
      isolationError.value = { ...parsed, skillRef, source: source ?? 'clawhub' }
    } else {
      lastError.value = t('externalSkills.installFailed', { error: e.message })
    }
  } finally {
    installingSlug.value = ''
  }
}

async function handleUrlInstall() {
  const input = urlInput.value.trim()
  if (!input) return
  installingUrl.value = true
  try {
    // Auto-detect source: URL → "url", otherwise → "clawhub"
    const source = input.startsWith('http://') || input.startsWith('https://') ? 'url' : 'clawhub'
    const res = await api.installExternalSkill(input, source)
    installed.value.push(res.skill)
    if (res.warnings.length > 0) {
      lastWarnings.value = res.warnings
    }
    message.success(t('externalSkills.installSuccess', { name: res.skill.name }))
    showUrlModal.value = false
    urlInput.value = ''
    activeTab.value = 'installed'
  } catch (e: any) {
    const parsed = parseIsolationError(e.message)
    if (parsed) {
      isolationError.value = { ...parsed, skillRef: input, source: '__url__' }
    } else {
      lastError.value = t('externalSkills.installFailed', { error: e.message })
    }
  } finally {
    installingUrl.value = false
  }
}

async function handleEnableAndRetry() {
  if (!isolationError.value) return
  const { configKey, skillRef, source } = isolationError.value
  enablingIsolation.value = true
  try {
    await api.setConfig(configKey, 'true', false)
    isolationError.value = null
    if (source === '__url__') {
      urlInput.value = skillRef
      showUrlModal.value = true
      await handleUrlInstall()
    } else {
      await handleInstall(skillRef, source)
    }
  } catch (e: any) {
    message.error(t('externalSkills.isolationFailed', { error: e.message }))
    isolationError.value = null
  } finally {
    enablingIsolation.value = false
  }
}

onMounted(loadInstalled)
</script>
