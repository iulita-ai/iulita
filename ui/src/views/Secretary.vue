<template>
  <section class="secretary" :aria-label="t('secretary.title')">
    <header class="heading"><h1>{{ t('secretary.title') }}</h1><p>{{ t('secretary.subtitle') }}</p></header>
    <n-alert v-if="!overview.live_writes_enabled" type="info" :show-icon="false">{{ t('secretary.liveDisabled') }}</n-alert>
    <div v-if="error" class="notice error" role="alert">{{ error }}</div>
    <div v-if="notice" class="notice" role="status" aria-live="polite">{{ notice }}</div>
    <div class="toolbar"><n-button :loading="loading" :disabled="busy" @click="refresh(false)">{{ t('secretary.refresh') }}</n-button><span>{{ t('secretary.refreshHint') }}</span></div>
    <n-empty v-if="!loading && !error && !overview.connections.length" :description="t('secretary.noConnections')"><p class="help">{{ t('secretary.connectHint') }}</p></n-empty>
    <div v-if="overview.connections.length" class="workspace">
      <aside class="connection-list" :aria-label="t('secretary.accounts')">
        <button v-for="item in overview.connections" :key="item.id" class="connection-choice" :class="{ selected: selectedID === item.id }" :aria-pressed="selectedID === item.id" :disabled="busy" @click="selectConnection(item.id)">
          <strong><bdi>{{ item.account_label }}</bdi></strong><span><bdi>{{ item.account_ref }}</bdi></span><span>{{ stateLabel(item.state) }}</span>
        </button>
      </aside>
      <div v-if="connection" class="details">
        <n-card :title="connection.account_label" size="small">
          <div class="identity"><bdi>{{ connection.account_ref }}</bdi><bdi v-if="connection.bot_username">@{{ connection.bot_username.replace(/^@/, '') }}</bdi></div>
          <p>{{ stateLabel(connection.state) }} · {{ modeLabel(connection.mode) }}</p>
          <p class="muted">{{ t('secretary.verified') }}: {{ date(connection.verified_at) }}</p>
          <p v-if="connection.storage_ready === false" class="notice" role="status">{{ t('secretary.encryptionRequired') }}</p><p v-if="connection.paused" class="notice">{{ t('secretary.paused') }}</p>
          <div class="controls"><n-button :disabled="pauseBusy" @click="pause()">{{ t(connection.paused ? 'secretary.resume' : 'secretary.pause') }}</n-button><n-button :disabled="busy" @click="exportData()">{{ t('secretary.export') }}</n-button><n-button v-if="connection.state === 'quarantined'" :disabled="busy" @click="rebind">{{ t('secretary.rebind') }}</n-button><n-button type="error" secondary :disabled="busy" @click="openPurge()">{{ t('secretary.erase') }}</n-button></div>
        </n-card>
        <n-card :title="t('secretary.processing')" size="small">
          <details :open="!connection.processing_consent || connection.mode === 'off'"><summary>{{ t('secretary.policyDetails') }}</summary>
          <dl class="policy-summary"><div><dt>{{ t('secretary.provider') }}</dt><dd>{{ connection.provider_label || t('secretary.providerUnavailable') }}</dd></div><div><dt>{{ t('secretary.retention') }}</dt><dd>{{ connection.retention_days == null ? t('secretary.unavailable') : t('secretary.days', { count: connection.retention_days }) }}</dd></div><div><dt>{{ t('secretary.budget') }}</dt><dd>{{ budgetLabel }}</dd></div></dl>
          <form class="form" @submit.prevent="savePolicy">
            <label for="business-mode">{{ t('secretary.mode') }}</label>
            <select id="business-mode" v-model="policyMode" @change="policyAuto = false" :disabled="busy"><option value="off">{{ modeLabel('off') }}</option><option value="draft_only">{{ modeLabel('draft_only') }}</option><option value="confirmed_send">{{ modeLabel('confirmed_send') }}</option><option value="auto">{{ modeLabel('auto') }}</option></select>
            <label class="check"><input v-model="policyConsent" type="checkbox" :disabled="busy || connection.storage_ready === false" />{{ t('secretary.consent') }}</label>
            <p class="help">{{ t('secretary.consentHint') }}</p><label class="check"><input v-model="policyTokenOnly" type="checkbox" :disabled="busy" />{{ t('secretary.tokenOnlyConsent') }}</label><section class="media-policy"><p class="help">{{ t('secretary.mediaHint') }}</p><p class="help">{{ t('secretary.mediaBudget') }}</p><p><bdi>{{ connection.media_provider || t('secretary.unavailable') }}</bdi></p><label class="check"><input v-model="policyMedia" type="checkbox" :disabled="busy || !connection.media_available" />{{ t('secretary.mediaConsent') }}</label></section><BusinessAutoRules v-if="policyMode === 'auto'" v-model="policyRules" :disabled="busy" /><label v-if="policyMode === 'auto'" class="check"><input v-model="policyAuto" type="checkbox" :disabled="busy" />{{ t('secretary.autoConsent') }}</label><p v-if="policyMode === 'auto' && !overview.live_writes_enabled" class="notice">{{ t('secretary.autoDisabled') }}</p>
            <n-button attr-type="submit" :disabled="busy || needsRefresh || (policyMode !== 'off' && (!policyConsent || connection.storage_ready === false)) || (policyMode === 'auto' && (!policyAuto || !validBusinessAutoRules(policyRules)))" :loading="busy">{{ t('secretary.savePolicy') }}</n-button>
          </form>
          </details>
        </n-card>
        <n-card :title="t('secretary.dialogues')" size="small">
          <p class="help">{{ t('secretary.historyLimit') }}</p>
          <p v-if="peerLoading" role="status">{{ t('secretary.loading') }}</p>
          <n-empty v-else-if="!peers.length" :description="t('secretary.noPeers')" />
          <label v-else for="business-peer">{{ t('secretary.recipient') }}<select id="business-peer" :value="selectedPeerID" :disabled="busy" @change="selectPeer(($event.target as HTMLSelectElement).value)"><option v-for="item in peers" :key="item.id" :value="item.id">{{ item.label }} · {{ item.recipient_ref }}</option></select></label>
          <template v-if="peer">
            <div class="identity"><bdi>{{ peer.label }}</bdi><bdi>{{ peer.recipient_ref }}</bdi></div><p v-if="peer.reply_until" class="help">{{ t('secretary.replyDeadline') }}: {{ date(peer.reply_until) }}</p><p v-if="processingLabel(peer)" class="help" role="status">{{ processingLabel(peer) }}<template v-if="peer.last_processed_at"> · {{ date(peer.last_processed_at) }}</template></p><p v-if="peer.reply_available === false" class="notice">{{ t('secretary.errors.reply_window_closed') }}</p>
            <div class="controls"><n-button :disabled="busy || needsRefresh || !connection.processing_consent || connection.mode === 'off'" @click="setPeerPolicy()">{{ t(peerEnabled ? 'secretary.disablePeer' : 'secretary.enablePeer') }}</n-button><n-button :disabled="pauseBusy" @click="pause(peer)">{{ t(peer.paused ? 'secretary.resumePeer' : 'secretary.pausePeer') }}</n-button><n-button :disabled="busy" @click="exportData(peer)">{{ t('secretary.exportPeer') }}</n-button><n-button type="error" secondary :disabled="busy" @click="openPurge(peer)">{{ t('secretary.erasePeer') }}</n-button></div>
            <n-button v-if="peerEnabled && peer.mode !== connection.mode && connection.mode !== 'auto'" :disabled="busy || needsRefresh" @click="setPeerPolicy(true)">{{ t('secretary.matchPeerMode') }}</n-button><p v-if="!peerEnabled" class="help">{{ t('secretary.peerOff') }}</p>
            <details v-if="connection.mode === 'auto'"><summary>{{ t('secretary.peerAutoSettings') }}</summary><BusinessAutoRules v-model="peerRules" :disabled="busy" /><label class="check"><input v-model="peerAutoConsent" type="checkbox" :disabled="busy" />{{ t('secretary.peerAutoConsent') }}</label><n-button :disabled="busy || !peerAutoConsent || !validBusinessAutoRules(peerRules) || needsRefresh" @click="enablePeerAuto">{{ t(peer.mode === 'auto' ? 'secretary.savePeerAuto' : 'secretary.enablePeerAuto') }}</n-button></details><details><summary>{{ t('secretary.peerMediaSettings') }}</summary><p class="help">{{ t('secretary.mediaHint') }}</p><p class="help">{{ t('secretary.mediaBudget') }}</p><p><bdi>{{ connection.media_provider || t('secretary.unavailable') }}</bdi></p><label class="check"><input v-model="peerMediaConsent" type="checkbox" :disabled="busy || !connection.media_consent || !connection.media_available" />{{ t('secretary.peerMediaConsent') }}</label><n-button :disabled="busy || needsRefresh || !connection.media_consent || !connection.media_available" @click="savePeerMedia">{{ t('secretary.savePeerMedia') }}</n-button></details><p v-if="mediaOmitted > 0" class="notice">{{ t('secretary.mediaOmitted', { count: mediaOmitted }) }}</p><n-button v-if="connection.requires_token_only && connection.token_only && !peer.token_only" :disabled="busy || needsRefresh" @click="ackPeerTokenLimits">{{ t('secretary.applyTokenConsent') }}</n-button><div class="controls"><n-button :disabled="busy || !canDraft || !canGenerate" @click="compactContext">{{ t('secretary.compact') }}</n-button></div><p v-if="compaction" role="status">{{ t(compaction.changed ? 'secretary.compacted' : 'secretary.compactUnchanged', { before: compaction.before_bytes, after: compaction.after_bytes, count: compaction.messages }) }}</p>
            <ol class="messages" :aria-label="t('secretary.messages')"><li v-for="message in messages" :key="message.id"><small>{{ t(message.direction === 'incoming' ? 'secretary.incoming' : 'secretary.outgoing') }} · {{ date(message.created_at) }}</small><p v-if="message.text || message.deleted" dir="auto">{{ message.deleted ? t('secretary.deletedMessage') : message.text }}</p><details v-if="!message.deleted && message.extracted_text"><summary>{{ t('secretary.extractedSource') }}</summary><p dir="auto">{{ message.extracted_text }}</p><small><bdi>{{ message.extracted_provider }}</bdi></small></details></li></ol>
            <p v-if="!peerLoading && !messages.length" class="muted">{{ t('secretary.noMessages') }}</p>
          </template>
        </n-card>
        <n-card v-if="peer" :title="t('secretary.drafts')" size="small">
          <p class="help">{{ t('secretary.draftHint') }}</p>
          <label for="business-draft">{{ t('secretary.text') }}</label><textarea id="business-draft" v-model="editor" rows="7" maxlength="4000" :disabled="busy" dir="auto" @input="invalidateReview" />
          <p class="muted" :class="{ 'error-text': draftBytes > 4000 }">{{ t('secretary.textLimit', { count: draftBytes }) }}</p>
          <div class="controls"><n-button :disabled="busy || !canDraft || !editor.trim() || draftBytes > 4000" @click="saveDraft">{{ t(currentDraft ? 'secretary.saveDraft' : 'secretary.createDraft') }}</n-button><n-button :disabled="busy || !canDraft || !canGenerate || connection.budget?.exhausted" @click="generate">{{ t('secretary.generate') }}</n-button></div>
          <p v-if="connection.requires_token_only && (!connection.token_only || !peer.token_only)" class="notice">{{ t('secretary.tokenOnlyRequired') }}</p><p v-if="!connection.provider_available" class="muted">{{ t('secretary.generationUnavailable') }}</p>
          <div v-for="draft in drafts" :key="draft.id" class="draft" :class="{ uncertain: ['unknown', 'unknown_outcome'].includes(draft.state) }">
            <div class="identity"><strong>{{ stateLabel(draft.state) }}</strong><bdi>{{ draft.account_label || connection.account_label }} → {{ draft.recipient_label || peer.label }}</bdi><bdi>{{ draft.recipient_ref || peer.recipient_ref }}</bdi></div>
            <p v-if="['unknown', 'unknown_outcome'].includes(draft.state)" role="alert">{{ t('secretary.unknown') }}</p>
            <pre dir="auto">{{ draft.text }}</pre><small v-if="draft.expires_at">{{ t('secretary.expires') }}: {{ date(draft.expires_at) }}</small>
            <div class="controls"><n-button :disabled="busy || terminal(draft)" @click="edit(draft)">{{ t('secretary.edit') }}</n-button><n-button :disabled="busy || !canReview(draft)" @click="openReview(draft)">{{ t('secretary.review') }}</n-button><n-button :disabled="busy || !canReview(draft)" @click="rejectDraft(draft)">{{ t('secretary.rejectDraft') }}</n-button></div>
          </div>
        </n-card>
        <SecretaryActions :connection="connection" :peer="peer" :live-writes="overview.live_writes_enabled" @refresh="refresh(true)" />
        <n-card :title="t('secretary.capabilities')" size="small"><p class="help">{{ t('secretary.capabilityHint') }}</p><ul class="capabilities"><li v-for="right in rights" :key="right"><strong>{{ t(`secretary.rights.${right}`) }}</strong><span>{{ rightLabel(right) }}</span><span>{{ capabilityLabel(right) }}</span></li></ul></n-card>
      </div>
    </div>
    <n-modal :show="!!review" preset="card" :title="t('secretary.reviewTitle')" class="secretary-modal" @update:show="value => { if (!value && !busy) review = null }">
      <template v-if="review && connection && peer">
        <n-alert type="info" :show-icon="false">{{ t('secretary.notSent') }}</n-alert><p><strong>{{ t('secretary.account') }}:</strong> <bdi>{{ review.account_label || connection.account_label }}</bdi> · <bdi>{{ connection.account_ref }}</bdi></p><p><strong>{{ t('secretary.recipient') }}:</strong> <bdi>{{ review.recipient_label || peer.label }}</bdi> · <bdi>{{ review.recipient_ref || peer.recipient_ref }}</bdi></p><pre dir="auto">{{ review.text }}</pre><p>{{ t('secretary.expires') }}: {{ date(review.expires_at) }}</p>
        <p v-if="!overview.live_writes_enabled" class="notice">{{ t('secretary.liveDisabled') }}</p><p v-if="peer.reply_until" class="help">{{ t('secretary.replyDeadline') }}: {{ date(peer.reply_until) }}</p><p v-if="peer.reply_available === false" class="notice">{{ t('secretary.errors.reply_window_closed') }}</p><p v-if="!canApprove && peer.reply_available !== false" class="help">{{ t('secretary.approvalPolicyHint') }}</p><p v-if="review.state === 'approved'" role="status">{{ t('secretary.approvedHint') }}</p>
        <div class="controls"><n-button :disabled="busy" @click="review = null">{{ t('common.cancel') }}</n-button><n-button v-if="review.state !== 'approved'" type="primary" :disabled="busy || !canReview(review) || !canApprove" @click="approve">{{ t('secretary.approve') }}</n-button><n-button v-else type="primary" :disabled="busy || !overview.live_writes_enabled || !canSend" @click="execute">{{ t('secretary.send') }}</n-button></div>
      </template>
    </n-modal>
    <n-modal v-model:show="showPurge" preset="card" :title="t(purgePeer ? 'secretary.erasePeerTitle' : 'secretary.eraseTitle')" class="secretary-modal"><p>{{ t(purgePeer ? 'secretary.erasePeerHint' : 'secretary.eraseHint') }}</p><p><bdi>{{ connection?.account_label }} · {{ connection?.account_ref }}</bdi></p><p v-if="purgePeer"><bdi>{{ purgePeer.label }} · {{ purgePeer.recipient_ref }}</bdi></p><label class="check"><input v-model="purgeConfirmed" type="checkbox" :disabled="busy" />{{ t(purgePeer ? 'secretary.erasePeerConfirm' : 'secretary.eraseConfirm') }}</label><div class="controls"><n-button :disabled="busy" @click="showPurge = false">{{ t('common.cancel') }}</n-button><n-button type="error" :disabled="busy || !purgeConfirmed" @click="purge">{{ t('secretary.erase') }}</n-button></div></n-modal>
  </section>
</template>

<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { NAlert, NButton, NCard, NEmpty, NModal } from 'naive-ui'
import BusinessAutoRules from '../components/BusinessAutoRules.vue'
import SecretaryActions from '../components/SecretaryActions.vue'
import { onLogout } from '../api'
import { businessApi, BusinessApiError, defaultBusinessAutoRules, copyBusinessAutoRules, validBusinessAutoRules, type BusinessContext, type BusinessConnection, type BusinessDraft, type BusinessMessage, type BusinessMode, type BusinessOverview, type BusinessPeer } from '../businessApi'
const { t, locale } = useI18n()
const overview = ref<BusinessOverview>({ connections: [], live_writes_enabled: false, capabilities: [] })
const selectedID = ref(''), selectedPeerID = ref(''), peers = ref<BusinessPeer[]>([]), messages = ref<BusinessMessage[]>([]), drafts = ref<BusinessDraft[]>([])
const policyRules = ref(defaultBusinessAutoRules()), peerRules = ref(defaultBusinessAutoRules()), policyMedia = ref(false), peerMediaConsent = ref(false), mediaOmitted = ref(0)
const compaction = ref<BusinessContext | null>(null), peerAutoConsent = ref(false), policyAuto = ref(false)
const pauseBusy = ref(false), needsRefresh = ref(false)
const loading = ref(false), peerLoading = ref(false), busy = ref(false), error = ref(''), notice = ref(''), editor = ref('')
const purgePeer = ref<BusinessPeer | null>(null)
const currentDraft = ref<BusinessDraft | null>(null), review = ref<BusinessDraft | null>(null)
const policyMode = ref<BusinessMode>('off'), policyConsent = ref(false), policyTokenOnly = ref(false), showPurge = ref(false), purgeConfirmed = ref(false)
let sequence = 0, controller: AbortController | undefined, mounted = true
const connection = computed(() => overview.value.connections.find(item => item.id === selectedID.value))
const peer = computed(() => peers.value.find(item => item.id === selectedPeerID.value))
const peerEnabled = computed(() => peer.value?.processing_consent === true && !!peer.value.mode && peer.value.mode !== 'off')
const canDraft = computed(() => !loading.value && !peerLoading.value && !needsRefresh.value && !!connection.value?.is_enabled && ['connected', 'enabled', 'active', 'draft_only', 'confirmed_send', 'auto'].includes(connection.value.state) && !connection.value.paused && connection.value.processing_consent && connection.value.mode !== 'off' && peerEnabled.value && !peer.value?.paused)
const canApprove = computed(() => canDraft.value && peer.value?.reply_available !== false && ['confirmed_send','auto'].includes(connection.value?.mode || '') && ['confirmed_send','auto'].includes(peer.value?.mode || '') && connection.value?.rights?.can_reply === true)
const canGenerate = computed(() => !!connection.value?.provider_available && (!connection.value.requires_token_only || connection.value.token_only === true && peer.value?.token_only === true))
const draftBytes = computed(() => new TextEncoder().encode(editor.value).byteLength)
const canSend = computed(() => canApprove.value)
const budgetLabel = computed(() => { const budget = connection.value?.budget; if (budget?.daily_usd != null) return t('secretary.budgetValue', { remaining: budget.remaining_usd ?? '—', total: budget.daily_usd }); if (budget?.input_tokens != null && budget.output_tokens != null) return t('secretary.tokenBudget', { input: budget.input_tokens.toLocaleString(locale.value), output: budget.output_tokens.toLocaleString(locale.value) }); return t('secretary.unavailable') })
const rights = ['can_reply', 'can_read_messages', 'can_delete_sent_messages', 'can_delete_all_messages', 'can_edit_name', 'can_edit_bio', 'can_edit_profile_photo', 'can_edit_username', 'can_change_gift_settings', 'can_view_gifts_and_stars', 'can_convert_gifts_to_stars', 'can_transfer_and_upgrade_gifts', 'can_transfer_stars', 'can_manage_stories']
const knownStates = new Set(['connected', 'enabled', 'active', 'disabled', 'paused', 'unverified', 'unavailable', 'revoked', 'draft', 'approved', 'queued', 'sending', 'dispatching', 'prepared', 'sent', 'failed', 'unknown', 'unknown_outcome', 'cancelled', 'expired', 'stale', 'processing_off', 'metadata_only', 'disconnected', 'quarantined', 'draft_only', 'confirmed_send', 'auto'])
function stateLabel(state: string) { return state === 'draft_only' || state === 'confirmed_send' || state === 'auto' ? modeLabel(state) : t(`secretary.states.${knownStates.has(state) ? state : 'unverified'}`) }
function processingLabel(item: BusinessPeer) { if (item.last_processing_state === 'processing') return t('secretary.processingInProgress'); if (item.last_processing_state === 'ready') return t('secretary.processingReady'); if (item.last_processing_state === 'skipped') return t('secretary.processingSkipped'); if (item.last_processing_state !== 'failed') return ''; if (item.last_processing_reason === 'unknown_outcome') return t('secretary.unknown'); if (item.last_processing_reason === 'stale_context') return t('secretary.errors.revision_conflict'); if (item.last_processing_reason === 'cancelled') return t('secretary.states.cancelled'); const known=['unsupported','budget_exhausted','unavailable','revision_conflict','disabled','reply_window_closed','forbidden']; return known.includes(item.last_processing_reason || '') ? t(`secretary.errors.${item.last_processing_reason}`) : t('secretary.processingFailed') }
function modeLabel(mode: BusinessMode) { return t(`secretary.modes.${mode || 'off'}`) }
function date(value?: string) { const parsed = value ? new Date(value) : null; return parsed && Number.isFinite(parsed.getTime()) ? parsed.toLocaleString(locale.value) : t('secretary.unavailable') }
function rightLabel(right: string) { const granted = connection.value?.rights?.[right]; return granted === undefined ? t('secretary.unavailable') : t(granted ? 'secretary.telegramGranted' : 'secretary.telegramNotGranted') }
function capabilityLabel(right: string) { const items = overview.value.capabilities.filter(item => item.right === right); return t(items.some(item => item.supported && item.enabled) ? 'secretary.iulitaEnabled' : items.some(item => item.supported) ? 'secretary.iulitaDisabled' : 'secretary.iulitaUnsupported') }
function terminal(draft: BusinessDraft) { return ['sent', 'sending', 'dispatching', 'queued', 'unknown', 'unknown_outcome', 'cancelled', 'expired', 'stale'].includes(draft.state) }
function expired(draft: BusinessDraft) { return !!draft.expires_at && (!Number.isFinite(Date.parse(draft.expires_at)) || Date.parse(draft.expires_at) <= Date.now()) }
function canReview(draft: BusinessDraft) { return !(currentDraft.value?.id === draft.id && editor.value !== draft.text) && canDraft.value && !terminal(draft) && !expired(draft) && draft.peer_id === selectedPeerID.value && draft.connection_id === selectedID.value }
function clearDetail() { peers.value = []; messages.value = []; drafts.value = []; selectedPeerID.value = ''; editor.value = ''; currentDraft.value = null; review.value = null; showPurge.value = false; purgeConfirmed.value = false; purgePeer.value = null; compaction.value = null; peerAutoConsent.value = false; peerMediaConsent.value = false; mediaOmitted.value = 0 }
function clearPrivate() { sequence++; controller?.abort(); clearDetail(); overview.value = { connections: [], live_writes_enabled: false, capabilities: [] }; selectedID.value = ''; policyConsent.value = false; policyTokenOnly.value = false; policyMode.value = 'off'; needsRefresh.value = false }
onLogout(clearPrivate)
function showError(cause: unknown) { const code = cause instanceof BusinessApiError ? cause.code : 'unavailable'; const known = ['unauthorized', 'forbidden', 'not_found', 'revision_conflict', 'expired', 'disabled', 'unsupported', 'unavailable', 'invalid_request', 'budget_exhausted', 'rate_limited', 'unknown_outcome', 'reply_window_closed']; error.value = t(`secretary.errors.${known.includes(code) ? code : 'unavailable'}`); if (code === 'reply_window_closed') { peers.value = peers.value.map(item => item.id === selectedPeerID.value ? { ...item, reply_available:false } : item) }; if (code === 'unauthorized' || code === 'forbidden') clearPrivate(); if (['revision_conflict', 'expired', 'unknown_outcome'].includes(code)) { review.value = null; currentDraft.value = null; needsRefresh.value = true } }
function unsaved() { return !!editor.value && editor.value !== (currentDraft.value?.text || '') }
function allowDiscard() { return !unsaved() || window.confirm(t('secretary.discard')) }
async function refresh(force = false) {
  if (!force && !allowDiscard()) return
  const seq = ++sequence; controller?.abort(); controller = new AbortController(); loading.value = true; error.value = ''; review.value = null
  try { const result = await businessApi.connections(controller.signal); if (!mounted || seq !== sequence) return; overview.value = { ...result, connections: result.connections || [], capabilities: result.capabilities || [], live_writes_enabled: result.live_writes_enabled === true }; const id = result.connections?.some(item => item.id === selectedID.value) ? selectedID.value : result.connections?.[0]?.id || ''; await selectConnection(id) }
  catch (cause) { if (mounted && seq === sequence) showError(cause) }
  finally { if (mounted) loading.value = false }
}
async function selectConnection(id: string) {
  if (id !== selectedID.value && !allowDiscard()) return
  const seq = ++sequence; controller?.abort(); controller = new AbortController(); selectedID.value = id; clearDetail(); needsRefresh.value = false; error.value = ''; notice.value = ''; policyMode.value = connection.value?.mode || 'off'; policyConsent.value = connection.value?.processing_consent === true; policyTokenOnly.value = connection.value?.token_only === true; policyAuto.value = connection.value?.mode === 'auto' && connection.value?.auto_reply === true; policyRules.value = copyBusinessAutoRules(connection.value?.auto_rules); policyMedia.value = connection.value?.media_consent === true
  if (!id) return; peerLoading.value = true
  try { const result = await businessApi.peers(id, controller.signal); if (!mounted || seq !== sequence) return; peers.value = result.peers || []; drafts.value = result.drafts || []; await selectPeer(peers.value[0]?.id || '') }
  catch (cause) { if (mounted && seq === sequence) showError(cause) }
  finally { if (mounted) peerLoading.value = false }
}
async function selectPeer(id: string) {
  if (id !== selectedPeerID.value && !allowDiscard()) return
  const seq = ++sequence; controller?.abort(); controller = new AbortController(); selectedPeerID.value = id; compaction.value = null; peerAutoConsent.value = false; peerMediaConsent.value = peer.value?.media_consent === true; peerRules.value = copyBusinessAutoRules(peer.value?.auto_rules); mediaOmitted.value = 0; messages.value = []; drafts.value = []; editor.value = ''; currentDraft.value = null; review.value = null; error.value = ''
  if (!id || !selectedID.value) return; peerLoading.value = true
  try { const result = await businessApi.messages(selectedID.value, id, controller.signal); if (!mounted || seq !== sequence) return; messages.value = result.messages || []; compaction.value = result.context || null; mediaOmitted.value = result.media_omitted || 0; drafts.value = (result.drafts || []).filter(item => item.connection_id === selectedID.value && item.peer_id === id) }
  catch (cause) { if (mounted && seq === sequence) showError(cause) }
  finally { if (mounted) peerLoading.value = false }
}
async function mutate(action: () => Promise<void>) { if (busy.value || needsRefresh.value) return; busy.value = true; error.value = ''; notice.value = ''; try { await action() } catch (cause) { showError(cause) } finally { busy.value = false } }
function invalidateReview() { review.value = null }
function replaceDraft(draft: BusinessDraft) { if (draft.connection_id !== selectedID.value || draft.peer_id !== selectedPeerID.value) return; drafts.value = [draft, ...drafts.value.filter(item => item.id !== draft.id)]; currentDraft.value = draft; editor.value = draft.text }
function edit(draft: BusinessDraft) { currentDraft.value = draft; editor.value = draft.text; review.value = null; document.getElementById('business-draft')?.focus() }
async function saveDraft() { if (!connection.value || !peer.value || !canDraft.value || !editor.value.trim() || draftBytes.value > 4000) return; await mutate(async () => { const seq = sequence; const result = currentDraft.value ? await businessApi.editDraft(currentDraft.value.id, currentDraft.value.revision, editor.value) : await businessApi.createDraft(connection.value!.id, peer.value!.id, peer.value!.revision, editor.value); if (!mounted || seq !== sequence) return; replaceDraft(result.draft); notice.value = t('secretary.savedNotSent') }) }
async function generate() { if (!connection.value || !peer.value || !canDraft.value || !canGenerate.value) return; await mutate(async () => { const seq = sequence; const result = await businessApi.createDraft(connection.value!.id, peer.value!.id, peer.value!.revision, '', true); if (!mounted || seq !== sequence) return; replaceDraft(result.draft); notice.value = t('secretary.savedNotSent') }) }
function openReview(draft: BusinessDraft) { if (!canReview(draft)) return; review.value = { ...draft } }
async function rejectDraft(draft: BusinessDraft) { if (!canReview(draft) || !window.confirm(t('secretary.rejectDraftConfirm'))) return; const seq = sequence; await mutate(async () => { const result = await businessApi.rejectDraft(draft); if (!mounted || seq !== sequence || !result.rejected) return; drafts.value = drafts.value.filter(item=>item.id!==draft.id); if (currentDraft.value?.id === draft.id) { currentDraft.value=null; editor.value='' }; review.value=null; notice.value=t('secretary.draftRejected') }) }
async function approve() { if (!review.value || !canReview(review.value) || !canApprove.value) return; const frozen = { ...review.value }; await mutate(async () => { const seq = sequence; const result = await businessApi.approveDraft(frozen); if (!mounted || seq !== sequence) return; replaceDraft(result.draft); review.value = result.draft; notice.value = t('secretary.approvedHint') }) }
async function execute() { if (!review.value || review.value.state !== 'approved' || !canSend.value || !overview.value.live_writes_enabled || expired(review.value)) return; const frozen = { ...review.value }; await mutate(async () => { const seq = sequence; const result = await businessApi.executeDraft(frozen); if (!mounted || seq !== sequence) return; replaceDraft(result.draft); review.value = null; notice.value = stateLabel(result.draft.state) }) }
async function savePolicy() { if (!connection.value || (policyMode.value !== 'off' && (!policyConsent.value || connection.value.storage_ready === false)) || (policyMode.value === 'auto' && (!policyAuto.value || !validBusinessAutoRules(policyRules.value)))) return; const item = connection.value; await mutate(async () => { const result = await businessApi.policy(item.id, { expected_revision: item.revision, mode: policyMode.value, processing_consent: policyConsent.value, token_only: policyTokenOnly.value, auto_reply: policyMode.value === 'auto' && policyAuto.value, auto_rules: policyMode.value === 'auto' ? policyRules.value : undefined, media_consent: policyMedia.value, media_provider: item.media_provider, ...(item.allowed_methods ? { allowed_methods: item.allowed_methods } : {}), ...(item.policy_version ? { policy_version: item.policy_version } : {}) }); overview.value.connections = overview.value.connections.map(c => c.id === item.id ? result.connection : c); await selectConnection(item.id) }) }
async function setPeerPolicy(matchMode = false) { if (!connection.value || !peer.value) return; const item = peer.value; const allowed = matchMode || !peerEnabled.value; await mutate(async () => { const result = await businessApi.peerPolicy(connection.value!.id, item.id, { expected_revision: item.revision, mode: allowed ? (connection.value!.mode === 'confirmed_send' ? 'confirmed_send' : 'draft_only') : 'off', processing_consent: allowed, token_only: item.token_only === true || connection.value!.token_only === true, auto_reply: false, media_consent: allowed && item.media_consent === true, media_provider: item.media_provider || connection.value!.media_provider, ...(item.allowed_methods ? { allowed_methods: item.allowed_methods } : {}), ...(connection.value!.policy_version ? { policy_version: connection.value!.policy_version } : {}) }); peers.value = peers.value.map(p => p.id === item.id ? result.peer : p); review.value = null }) }
async function enablePeerAuto() { if (!connection.value || !peer.value || connection.value.mode !== 'auto' || (!peerAutoConsent.value || !validBusinessAutoRules(peerRules.value))) return; const c = connection.value, p = peer.value; await mutate(async () => { const result = await businessApi.peerPolicy(c.id, p.id, { expected_revision:p.revision, mode:'auto', auto_reply:true, auto_rules:peerRules.value, media_consent:p.media_consent === true, media_provider:p.media_provider || c.media_provider, processing_consent:true, token_only:p.token_only === true || c.token_only === true, policy_version:c.policy_version, allowed_methods:[...new Set([...(p.allowed_methods || []),'sendMessage'])] }); peers.value = peers.value.map(item => item.id === p.id ? result.peer : item); peerAutoConsent.value = false; review.value = null }) }
async function ackPeerTokenLimits() { if (!connection.value || !peer.value || !connection.value.token_only || !window.confirm(t('secretary.tokenOnlyConsent'))) return; const c = connection.value, p = peer.value; await mutate(async () => { const result = await businessApi.peerPolicy(c.id,p.id,{expected_revision:p.revision,mode:p.mode || 'off',processing_consent:p.processing_consent === true,auto_reply:p.auto_reply === true,auto_rules:p.auto_rules,media_consent:p.media_consent === true,media_provider:p.media_provider || c.media_provider,token_only:true,policy_version:c.policy_version,allowed_methods:p.allowed_methods}); peers.value = peers.value.map(item => item.id === p.id ? result.peer : item); review.value = null }) }
async function savePeerMedia() { if (!connection.value || !peer.value || !connection.value.media_consent || !connection.value.media_available) return; const c = connection.value, p = peer.value; await mutate(async () => { const result = await businessApi.peerPolicy(c.id,p.id,{expected_revision:p.revision,mode:p.mode || 'off',processing_consent:p.processing_consent === true,auto_reply:p.auto_reply === true,auto_rules:p.auto_rules,token_only:p.token_only === true,media_consent:peerMediaConsent.value,media_provider:c.media_provider,policy_version:c.policy_version,allowed_methods:p.allowed_methods}); peers.value=peers.value.map(item=>item.id===p.id?result.peer:item); review.value=null }) }
async function compactContext() { if (!connection.value || !peer.value || !canDraft.value || !canGenerate.value || !allowDiscard()) return; const c = connection.value, p = peer.value; await mutate(async () => { const result = await businessApi.compact(c.id,p.id,p.revision); await refresh(true); if (selectedID.value === c.id && selectedPeerID.value === p.id) compaction.value = result.context }) }
async function pause(item?: BusinessPeer) {
  if (!connection.value || pauseBusy.value) return
  const account = connection.value
  const seq = sequence
  pauseBusy.value = true
  error.value = ''
  try {
    await businessApi.pause(account.id, item?.revision ?? account.revision, !(item?.paused ?? account.paused), item?.id)
    if (mounted && seq === sequence) await refresh(true)
  } catch (cause) { if (mounted && seq === sequence) showError(cause) }
  finally { pauseBusy.value = false }
}
function openPurge(item?: BusinessPeer) { purgeConfirmed.value = false; purgePeer.value = item ? { ...item } : null; showPurge.value = true }
async function purge() { if (!connection.value || !purgeConfirmed.value) return; const item = connection.value; await mutate(async () => { const result = await businessApi.purge(item.id, purgePeer.value?.revision ?? item.revision, purgePeer.value?.id); if (result.purged) { showPurge.value = false; purgeConfirmed.value = false; clearDetail(); notice.value = t('secretary.erased'); await refresh(true) } }) }
async function rebind() {
  if (!connection.value || !window.confirm(t('secretary.rebindHint'))) return
  const item = connection.value
  await mutate(async () => {
    const result = await businessApi.rebind(item.id, item.revision)
    if (!mounted || selectedID.value !== item.id) return
    overview.value.connections = overview.value.connections.map(c => c.id === item.id ? result.connection : c)
    await refresh(true)
  })
}
async function exportData(item?: BusinessPeer) {
  if (!connection.value || !window.confirm(t('secretary.exportHint'))) return
  const account = connection.value
  const seq = sequence
  await mutate(async () => {
    const blob = await businessApi.export(account.id, item?.id)
    if (!mounted || seq !== sequence) return
    const url = URL.createObjectURL(blob)
    const anchor = document.createElement('a')
    anchor.href = url
    anchor.download = 'telegram-business-export.json'
    anchor.click()
    URL.revokeObjectURL(url)
  })
}
onMounted(refresh)
onUnmounted(() => { mounted = false; clearPrivate() })
</script>

<style scoped>
.secretary { display: grid; gap: 16px; max-width: 1160px; margin-inline: auto; min-width: 0; }
.heading h1 { font-size: 22px; line-height: 1.35; margin: 0 0 6px; font-weight: 600; }
.heading p { margin: 0; color: #aaa; line-height: 1.5; }
summary { cursor: pointer; min-height: 44px; display: list-item; padding-block: 8px; }
.workspace { display: grid; grid-template-columns: minmax(170px, 240px) minmax(0, 1fr); gap: 20px; align-items: start; }
.details, .connection-list, .form { display: grid; gap: 16px; min-width: 0; }
.connection-list { gap: 8px; }
.connection-choice { text-align: start; padding: 14px; border: 1px solid #ffffff25; border-radius: 10px; color: inherit; background: transparent; display: grid; gap: 6px; cursor: pointer; min-height: 64px; overflow-wrap: anywhere; }
.connection-choice.selected { border-color: #63e2b7; background: #63e2b710; }
.identity, .controls, .toolbar { display: flex; flex-wrap: wrap; align-items: center; gap: 10px; }
.identity { overflow-wrap: anywhere; }
bdi { overflow-wrap: anywhere; unicode-bidi: isolate; }
.identity bdi { unicode-bidi: isolate; overflow-wrap: anywhere; }
.controls { margin-block: 14px 2px; }
.controls :deep(button) { min-height: 44px; height: auto; white-space: normal; }
.toolbar :deep(button) { min-height: 44px; }
.toolbar { font-size: 13px; color: #aaa; }
.help, .muted, small { color: #aaa; font-size: 13px; line-height: 1.5; }
.help { max-width: 70ch; }
.notice { padding: 12px; border-radius: 8px; background: #63e2b710; border: 1px solid #63e2b745; overflow-wrap: anywhere; }
.error-text { color: #e8b56b; }
.notice.error, .uncertain { border-color: #e8b56b; background: #e8b56b10; }
label, input, select, textarea { font: inherit; }
select, textarea { box-sizing: border-box; width: 100%; min-height: 44px; border: 1px solid #ffffff40; border-radius: 7px; background: #202024; color: inherit; padding: 10px; }
textarea, pre, .messages p { white-space: pre-wrap; overflow-wrap: anywhere; word-break: break-word; }
.check { min-height: 44px; display: flex; gap: 10px; align-items: start; line-height: 1.5; }
.check input { width: 20px; height: 20px; flex-shrink: 0; margin-top: 2px; }
.policy-summary { display: grid; gap: 10px; margin-top: 0; }
.policy-summary div { display: grid; grid-template-columns: minmax(95px, 1fr) minmax(0, 2fr); gap: 12px; }
dt { color: #aaa; } dd { margin: 0; overflow-wrap: anywhere; }
.messages { padding-inline-start: 20px; display: grid; gap: 12px; }
.messages li { padding-inline-start: 4px; }
.messages p { margin: 5px 0; }
.draft { margin-top: 16px; padding: 14px; border: 1px solid #ffffff30; border-radius: 10px; }
pre { font: inherit; line-height: 1.6; max-width: 100%; }
.capabilities { display: grid; gap: 0; list-style: none; margin: 0; padding: 0; }
.capabilities li { display: grid; gap: 4px; padding-block: 12px; border-bottom: 1px solid #ffffff20; font-size: 13px; }
:deep(.n-card__content), :deep(.n-page-header) { min-width: 0; }
:deep(.n-button__content) { white-space: normal; }
@media (max-width: 760px) { .workspace { grid-template-columns: minmax(0, 1fr); gap: 16px; } .connection-list { grid-template-columns: repeat(auto-fit, minmax(min(100%, 190px), 1fr)); } .controls { align-items: stretch; } .controls :deep(button) { flex: 1 1 140px; } .policy-summary div { grid-template-columns: 1fr; gap: 3px; } }
</style>
<style>
.secretary-modal { width: min(680px, calc(100vw - 32px)); max-height: calc(100vh - 40px); overflow-y: auto; }
.secretary-modal pre { white-space: pre-wrap; overflow-wrap: anywhere; font: inherit; line-height: 1.6; }
.secretary-modal .controls button { min-height: 44px; height: auto; }
.secretary-modal .controls { display: flex; gap: 10px; flex-wrap: wrap; margin-top: 16px; }
.secretary-modal .check { min-height: 44px; display: flex; gap: 10px; align-items: start; }
.secretary-modal bdi { unicode-bidi: isolate; overflow-wrap: anywhere; }
</style>
