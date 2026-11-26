<template>
  <n-card :title="t('secretaryActions.title')" size="small" class="account-actions">
    <p class="help">{{ t('secretaryActions.hint') }}</p>
    <div v-if="error" class="notice" role="alert">{{ error }}</div>
    <label>{{ t('secretaryActions.scope') }}<select :value="scope" :disabled="busy" @change="changeScope"><option value="account">{{ t('secretaryActions.accountScope') }}</option><option value="chat" :disabled="!peer">{{ t('secretaryActions.chatScope') }}</option></select></label>
    <p><bdi>{{ connection.account_label }} · {{ connection.account_ref }}</bdi><template v-if="scope === 'chat' && peer"> → <bdi>{{ peer.label }} · {{ peer.recipient_ref }}</bdi></template></p>
    <p v-if="listLoading" role="status">{{ t('secretary.loading') }}</p><n-button :disabled="busy || listLoading" @click="loadActions">{{ t('secretaryActions.refreshList') }}</n-button>
    <div v-if="savedActions.length" class="saved-actions"><h3>{{ t('secretaryActions.saved') }}</h3><n-button v-for="saved in savedActions" :key="saved.id" :disabled="busy" @click="loadSaved(saved)">{{ t(`secretaryActions.methods.${saved.method}`) }} · {{ stateLabel(saved.state) }}</n-button></div>
    <label>{{ t('secretaryActions.operation') }}<select :value="method" :disabled="busy" @change="changeMethod"><option v-for="item in methods" :key="item.method" :value="item.method">{{ t(`secretaryActions.methods.${item.method}`) }}</option></select></label>
    <p class="help">{{ t('secretaryActions.exactScope') }}</p>
    <p v-if="windowClosed" class="notice">{{ t('secretary.errors.reply_window_closed') }}</p>
    <p v-if="!granted" class="notice">{{ t('secretaryActions.noRight') }}</p>
    <p v-if="!ready" class="help">{{ t('secretaryActions.policyRequired') }}</p><n-button v-if="!allowed || !['confirmed_send','auto'].includes(policy.mode || '')" :disabled="busy || !canConfigure || !granted" @click="allowMethod">{{ t('secretaryActions.allowMethod') }}</n-button>
    <p v-if="method === 'transferBusinessAccountStars'"><strong>{{ t('secretaryActions.botRecipient') }}:</strong> <bdi>{{ connection.bot_username || t('secretaryActions.connectedBot') }} · {{ connection.bot_ref || t('secretary.unavailable') }}</bdi></p>
    <p v-if="spec.financial" class="notice">{{ t('secretaryActions.financialHint') }}</p>
    <form @submit.prevent="prepare">
      <template v-for="field in spec.fields" :key="field.key">
        <fieldset v-if="field.type === 'giftTypes'"><legend>{{ t(`secretaryActions.fields.${field.key}`) }}</legend><label v-for="kind in giftTypes" :key="kind" class="check"><input v-model="values[kind]" type="checkbox" :disabled="busy || canCancel" />{{ t(`secretaryActions.fields.${kind}`) }}</label></fieldset>
        <template v-else-if="field.type === 'media'">
          <label>{{ t(`secretaryActions.fields.${field.key}`) }}<input type="file" :accept="mediaAccept" :disabled="busy || canCancel" @change="chooseFile" /></label>
          <p class="help">{{ t('secretaryActions.uploadHint') }}</p><p v-if="file"><bdi>{{ file.name }}</bdi> · {{ file.size }} {{ t('secretaryActions.bytes') }}</p>
          <n-button :disabled="busy || canCancel || !file || !canConfigure" @click="upload">{{ t('secretaryActions.upload') }}</n-button><p v-if="mediaRef" role="status">{{ t('secretaryActions.uploaded') }}</p>
        </template>
        <label v-else-if="field.type === 'boolean'" class="check"><input v-model="values[field.key]" type="checkbox" :disabled="busy || canCancel" />{{ t(`secretaryActions.fields.${field.key}`) }}</label>
        <label v-else-if="field.type === 'period'">{{ t(`secretaryActions.fields.${field.key}`) }}<select v-model="values[field.key]" :disabled="busy || canCancel"><option v-for="hours in [6,12,24,48]" :key="hours" :value="String(hours * 3600)">{{ t('secretaryActions.hours', { count: hours }) }}</option></select></label>
        <label v-else>{{ t(`secretaryActions.fields.${field.key}`) }}<textarea v-if="['text','caption','bio'].includes(field.key)" v-model="values[field.key] as string" :maxlength="field.limit" :disabled="busy || canCancel" rows="3" dir="auto" /><input v-else v-model="values[field.key]" :type="field.type === 'number' ? 'number' : 'text'" :min="field.min" :max="field.max" :maxlength="field.limit" :required="field.required" :disabled="busy || canCancel" dir="auto" /></label>
      </template>
      <p v-if="['setBusinessAccountName','setBusinessAccountBio','setBusinessAccountUsername'].includes(method)" class="help">{{ t('secretaryActions.emptyClears') }}</p>
      <div class="controls"><n-button attr-type="submit" :disabled="busy || !ready || !allowed || !granted || unresolved">{{ t('secretaryActions.prepare') }}</n-button></div>
    </form>
    <section v-if="action" class="preview" :aria-label="t('secretaryActions.preview')">
      <h3>{{ t('secretaryActions.preview') }}</h3><p>{{ t(`secretaryActions.methods.${action.method}`) }}</p>
      <p><bdi>{{ connection.account_label }} · {{ connection.account_ref }}</bdi><template v-if="scope === 'chat' && peer"> → <bdi>{{ peer.label }} · {{ peer.recipient_ref }}</bdi></template></p>
      <dl><div v-for="(value, key) in action.params" :key="key"><dt>{{ fieldLabel(String(key)) }}</dt><dd dir="auto">{{ display(value) }}</dd></div></dl>
      <p v-if="action.method === 'transferBusinessAccountStars'"><strong>{{ t('secretaryActions.botRecipient') }}:</strong> <bdi>{{ connection.bot_username || t('secretaryActions.connectedBot') }} · {{ connection.bot_ref || t('secretary.unavailable') }}</bdi></p>
      <p v-if="fileName"><strong>{{ t('secretaryActions.asset') }}:</strong> <bdi>{{ fileName }}</bdi></p><p>{{ t('secretary.expires') }}: {{ date(action.expires_at) }}</p>
      <p v-if="scope === 'chat' && peer?.reply_until">{{ t('secretary.replyDeadline') }}: {{ date(peer.reply_until) }}</p>
      <p>{{ t('secretaryActions.approvalProgress', { done: action.approval_steps, total: action.required_steps }) }} · {{ stateLabel(action.state) }}</p>
      <p v-if="action.state === 'unknown'" class="notice" role="alert">{{ t('secretary.unknown') }}</p>
      <p v-if="!liveWrites" class="notice">{{ t('secretary.liveDisabled') }}</p>
      <p v-if="canCancel" class="help">{{ t('secretaryActions.editPendingHint') }}</p>
      <label v-if="canApprove" class="check"><input v-model="exactConfirmed" type="checkbox" :disabled="busy" />{{ t(spec.financial && action.approval_steps > 0 ? 'secretaryActions.financialConfirm' : 'secretaryActions.exactConfirm') }}</label>
      <div class="controls"><n-button :disabled="busy" @click="refreshAction">{{ t('secretaryActions.checkStatus') }}</n-button><n-button v-if="canCancel" :disabled="busy" @click="cancelAction()">{{ t('secretaryActions.cancel') }}</n-button><n-button v-if="canApprove" :disabled="busy || !exactConfirmed" @click="approve">{{ t('secretaryActions.approveStep', { step: action.approval_steps + 1, total: action.required_steps }) }}</n-button><n-button v-if="action.state === 'approved'" :disabled="busy || !liveWrites || !ready || !granted || expired" @click="execute">{{ t('secretaryActions.execute') }}</n-button></div>
      <div v-if="action.receipt" class="receipt"><h4>{{ t('secretaryActions.receipt') }}</h4><pre dir="auto">{{ display(action.receipt) }}</pre></div>
    </section>
  </n-card>
</template>
<script setup lang="ts">
import { computed, nextTick, onUnmounted, ref, watch } from 'vue'
import { NButton, NCard } from 'naive-ui'
import { useI18n } from 'vue-i18n'
import { businessApi, BusinessApiError, type BusinessAction, type BusinessConnection, type BusinessPeer } from '../businessApi'
import { actionSpecs, buildActionParams, giftTypes } from '../businessActions'
const props = defineProps<{ connection: BusinessConnection; peer?: BusinessPeer; liveWrites: boolean }>()
const emit = defineEmits<{ refresh: [] }>()
const { t, locale } = useI18n()
const scope = ref<'account' | 'chat'>('account'), method = ref('setBusinessAccountName')
const values = ref<Record<string, string | boolean>>({ active_period: '86400', limit: '20', star_count: '0' }), action = ref<BusinessAction | null>(null)
const replyWindowBlocked = ref(false)
const busy = ref(false), error = ref(''), exactConfirmed = ref(false), blocked = ref(false)
const savedActions = ref<BusinessAction[]>([]), listLoading = ref(false)
const file = ref<File | null>(null), mediaRef = ref(''), fileName = ref('')
let sequence = 0, mounted = true
const methods = computed(() => actionSpecs.filter(item => item.peer === (scope.value === 'chat')))
const spec = computed(() => methods.value.find(item => item.method === method.value) || methods.value[0]!)
const currentPeer = computed(() => scope.value === 'chat' ? props.peer : undefined)
const unresolved = computed(() => !!action.value && ['unknown','dispatching'].includes(action.value.state))
const policy = computed(() => currentPeer.value || props.connection)
const canConfigure = computed(() => props.connection.is_enabled && props.connection.processing_consent && !props.connection.paused && !['quarantined','disconnected','revoked','processing_off'].includes(props.connection.state) && (scope.value !== 'chat' || !!props.peer?.processing_consent && !props.peer.paused))
const windowClosed = computed(() => scope.value === 'chat' && (props.peer?.reply_available === false || replyWindowBlocked.value) && ['sendMessage','sendPhoto','sendVoice','sendAudio','sendVideo','sendDocument','editMessageText','readBusinessMessage'].includes(method.value))
const ready = computed(() => !windowClosed.value && !blocked.value && canConfigure.value && ['confirmed_send','auto'].includes(policy.value.mode || ''))
const granted = computed(() => props.connection.rights[spec.value.right] === true && (method.value !== 'transferBusinessAccountStars' || !!props.connection.bot_ref) && (!['upgradeGift','transferGift'].includes(method.value) || Number(action.value?.params.star_count ?? values.value.star_count ?? 0) === 0 || props.connection.rights.can_transfer_stars === true))
const allowed = computed(() => policy.value.allowed_methods?.includes(method.value) === true)
const expired = computed(() => !action.value || !Number.isFinite(Date.parse(action.value.expires_at)) || Date.parse(action.value.expires_at) <= Date.now())
const canCancel = computed(() => !!action.value && ['prepared','approved'].includes(action.value.state))
const canApprove = computed(() => !!action.value && ready.value && granted.value && !expired.value && action.value.state === 'prepared' && action.value.approval_steps < action.value.required_steps)
const mediaAccept = computed(() => ['setBusinessAccountProfilePhoto','postStory','editStory','sendPhoto'].includes(method.value) ? 'image/jpeg,image/png' : method.value === 'sendVoice' ? 'audio/ogg' : method.value === 'sendAudio' ? 'audio/mpeg' : method.value === 'sendVideo' ? 'video/mp4' : 'application/pdf,text/plain')
function reset() { sequence++; action.value = null; exactConfirmed.value = false; error.value = ''; blocked.value = false; replyWindowBlocked.value = false; values.value = { active_period: '86400', limit: '20', star_count: '0' }; file.value = null; mediaRef.value = ''; fileName.value = '' }
watch([scope, () => props.connection.id, () => props.connection.revision, () => props.peer?.id, () => props.peer?.revision], async () => { method.value = scope.value === 'chat' ? 'sendMessage' : 'setBusinessAccountName'; reset(); savedActions.value = []; await nextTick(); if (mounted) await loadActions() }, { immediate: true })
watch(method, reset)
watch(values, () => { if (!canCancel.value) action.value = null; exactConfirmed.value = false }, { deep: true })
function fail(cause: unknown) { const code = cause instanceof BusinessApiError ? cause.code : 'invalid_request'; const known = ['unauthorized','forbidden','not_found','revision_conflict','expired','disabled','unsupported','unavailable','invalid_request','budget_exhausted','rate_limited','unknown_outcome', 'reply_window_closed']; error.value = t(`secretary.errors.${known.includes(code) ? code : 'unavailable'}`); exactConfirmed.value = false; if (code === 'reply_window_closed') replyWindowBlocked.value = true; if (['revision_conflict','expired','unknown_outcome'].includes(code)) blocked.value = true }
async function run(work: () => Promise<void>) { if (busy.value) return; busy.value = true; error.value = ''; try { await work() } catch (cause) { fail(cause) } finally { busy.value = false } }
function fieldLabel(key: string) { return t(`secretaryActions.fields.${key}`) }
function display(value: unknown): string { if (value === '') return t('secretaryActions.emptyValue'); const text = typeof value === 'string' ? value : JSON.stringify(value, null, 2); return text && text.length > 16384 ? text.slice(0, 16384) + '\n' + t('secretaryActions.truncated') : text || '' }
function date(value: string) { return Number.isFinite(Date.parse(value)) ? new Date(value).toLocaleString(locale.value) : t('secretary.unavailable') }
function stateLabel(value: string) { return ['prepared','approved','sent','failed','unknown','cancelled','expired','dispatching'].includes(value) ? t(`secretary.states.${value}`) : t('secretary.states.unverified') }
async function allowMethod() {
  if (!canConfigure.value || !granted.value || !window.confirm(t('secretaryActions.allowConfirm', { method: t(`secretaryActions.methods.${method.value}`) }))) return
  const p = currentPeer.value, c = props.connection
  await run(async () => { const body = { expected_revision: p?.revision ?? c.revision, mode: 'confirmed_send' as const, processing_consent: true, policy_version: c.policy_version, auto_reply: false, media_consent: policy.value.media_consent === true, media_provider: policy.value.media_provider || c.media_provider, token_only: policy.value.token_only === true, allowed_methods: [...new Set([...(policy.value.allowed_methods || []), method.value])] }; if (p) await businessApi.peerPolicy(c.id, p.id, body); else await businessApi.policy(c.id, body); emit('refresh') })
}
function chooseFile(event: Event) { file.value = null; mediaRef.value = ''; fileName.value = ''; action.value = null; const selected = (event.target as HTMLInputElement).files?.[0]; if (!selected) return; if (!selected.size || selected.size > 1048576) { error.value = t('secretaryActions.fileLimit'); return }; file.value = selected }
async function upload() {
  if (!file.value || !canConfigure.value) return
  const selected = file.value, seq = sequence, c = props.connection, p = currentPeer.value
  await run(async () => { const data = await new Promise<string>((resolve, reject) => { const reader = new FileReader(); reader.onerror = reject; reader.onload = () => resolve(String(reader.result).split(',')[1] || ''); reader.readAsDataURL(selected) }); if (!mounted || seq !== sequence) return; const result = await businessApi.uploadMedia(c.id, p?.id, selected.name, selected.type, data); if (!mounted || seq !== sequence) return; mediaRef.value = result.media_ref; fileName.value = selected.name })
}
async function prepare() {
  if (!ready.value || !allowed.value || !granted.value || unresolved.value) return
  const seq = sequence, c = props.connection, p = currentPeer.value
  await run(async () => { const params = buildActionParams(spec.value, values.value, mediaRef.value); const result = await businessApi.prepareAction(c.id, p?.id, p?.revision ?? c.revision, method.value, params); if (!mounted || seq !== sequence) return; action.value = result.action; exactConfirmed.value = false })
}
async function cancelAction(confirm = true) { if (!action.value || !canCancel.value || confirm && !window.confirm(t('secretaryActions.cancelConfirm'))) return; const a=action.value,seq=sequence,c=props.connection,p=currentPeer.value; await run(async()=>{const result=await businessApi.cancelAction(c.id,p?.id,a); if(!mounted||seq!==sequence)return; action.value=result.action; exactConfirmed.value=false; savedActions.value=savedActions.value.map(item=>item.id===a.id?result.action:item)}) }
async function releasePreview() { if(unresolved.value){error.value=t('secretary.unknown');return false};if(!canCancel.value)return true;if(!window.confirm(t('secretaryActions.cancelBeforeChange')))return false;await cancelAction(false);return !canCancel.value && !blocked.value }
async function changeMethod(event: Event) { const input=event.target as HTMLSelectElement,next=input.value;input.value=method.value;if(await releasePreview())method.value=next }
async function changeScope(event: Event) { const input=event.target as HTMLSelectElement,next=input.value as 'account'|'chat';input.value=scope.value;if(await releasePreview())scope.value=next }
async function loadActions() { const seq = sequence, c = props.connection, p = currentPeer.value; listLoading.value = true; try { const result = await businessApi.listActions(c.id, p?.id); if (mounted && seq === sequence) savedActions.value = (result.actions || []).filter(a => actionSpecs.some(s => s.method === a.method && s.peer === (scope.value === 'chat'))).slice(-20).reverse() } catch (cause) { if (mounted && seq === sequence) fail(cause) } finally { if (mounted) listLoading.value = false } }
async function loadSaved(saved: BusinessAction) { if (busy.value || !await releasePreview() || !methods.value.some(item => item.method === saved.method)) return; method.value = saved.method; await nextTick(); action.value = { ...saved }; await refreshAction() }
async function refreshAction() { if (!action.value) return; const a = action.value, seq = sequence, c = props.connection, p = currentPeer.value; await run(async () => { const result = await businessApi.getAction(c.id, p?.id, a.id); if (!mounted || seq !== sequence) return; action.value = result.action; blocked.value = false; exactConfirmed.value = false }) }
async function approve() { if (!action.value || !canApprove.value || !exactConfirmed.value) return; const a = action.value, seq = sequence, c = props.connection, p = currentPeer.value; await run(async () => { const result = await businessApi.approveAction(c.id, p?.id, a); if (!mounted || seq !== sequence) return; action.value = result.action; exactConfirmed.value = false }) }
async function execute() { if (!action.value || action.value.state !== 'approved' || !props.liveWrites || !ready.value || !granted.value || expired.value) return; const a = action.value, seq = sequence, c = props.connection, p = currentPeer.value; await run(async () => { const result = await businessApi.executeAction(c.id, p?.id, a); if (!mounted || seq !== sequence) return; action.value = result.action; exactConfirmed.value = false }) }
onUnmounted(() => { mounted = false; sequence++; action.value = null; file.value = null })
</script>
<style scoped>
.account-actions { min-width: 0; }
form, fieldset { display: grid; gap: 12px; margin-block: 16px; min-width: 0; }
label { display: grid; gap: 6px; margin-block: 10px; }
select, input:not([type=checkbox]), textarea { width: 100%; box-sizing: border-box; min-height: 44px; padding: 10px; color: inherit; background: #202024; border: 1px solid #ffffff40; border-radius: 7px; font: inherit; }
.check { min-height: 44px; display: flex; gap: 10px; align-items: start; } .check input { min-width: 20px; min-height: 20px; }
.help { color: #aaa; font-size: 13px; line-height: 1.5; } .notice { padding: 12px; border: 1px solid #e8b56b; border-radius: 8px; }
.controls { display: flex; flex-wrap: wrap; gap: 10px; margin-block: 14px; } :deep(button) { min-height: 44px; height: auto; max-width: 100%; min-width: 0; } :deep(.n-button__content) { white-space: normal; flex-shrink: 1; min-width: 0; overflow-wrap: anywhere; } .controls :deep(button) { flex: 1 1 150px; }
.saved-actions { display: grid; gap: 8px; margin-block: 16px; }
.preview { margin-top: 20px; border-top: 1px solid #ffffff30; padding-top: 12px; } .preview dl div { margin-block: 12px; } dt { color: #aaa; } dd { margin: 4px 0; white-space: pre-wrap; overflow-wrap: anywhere; } pre { white-space: pre-wrap; overflow-wrap: anywhere; font: inherit; } bdi { overflow-wrap: anywhere; } fieldset { border: 1px solid #ffffff40; border-radius: 7px; }
</style>
