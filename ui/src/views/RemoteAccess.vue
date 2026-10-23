<template>
  <n-space vertical :size="16">
    <n-page-header :title="t('remoteAccess.title')" :subtitle="t('remoteAccess.subtitle')">
      <template #extra><n-space>
        <n-button :loading="loading" @click="load">{{ t('remoteAccess.refresh') }}</n-button>
        <n-popconfirm @positive-click="toggleEnabled">
          <template #trigger><n-button :type="state.enabled ? 'error' : 'primary'" :disabled="busy || !loaded">{{ t(`remoteAccess.${state.enabled ? 'disable' : 'enable'}`) }}</n-button></template>
          {{ t('remoteAccess.disableHint') }} — {{ t('remoteAccess.active') }}: {{ activeCount }}
        </n-popconfirm>
      </n-space></template>
    </n-page-header>
    <n-alert v-if="error" type="error" role="alert">{{ error }}</n-alert>
    <n-alert :type="state.enabled ? 'success' : 'warning'">{{ t(`remoteAccess.${state.enabled ? 'enabled' : 'disabled'}`) }}. {{ t('remoteAccess.unsupported') }}</n-alert>
    <n-card v-for="a in pendingApprovals" :key="a.execution_id" :title="t('remoteAccess.pending')"><n-alert type="warning">{{ t('remoteAccess.approvalHint') }}</n-alert><n-alert v-if="a.model_output" type="warning">{{ t('remoteAccess.modelOutputWarning') }}</n-alert><p>{{ a.servers.map((s,i) => `${a.identities[i]?.username}@${s.address}:${s.port}`).join(' → ') }}</p><pre>{{ a.command }}</pre><p>{{ a.plan_hash }} · {{ a.expires_at }}</p><n-space><n-button :disabled="busy" @click="decide(a,false)">{{ t('remoteAccess.cancel') }}</n-button><n-button type="primary" :disabled="busy || !state.enabled" @click="decide(a,true)">{{ t('remoteAccess.approve') }}</n-button></n-space></n-card>
    <n-tabs v-model:value="tab" type="line">
      <n-tab-pane v-for="kind in collections" :key="kind" :name="kind" :tab="t(`remoteAccess.${kind}`)">
        <n-space vertical>
          <n-space><n-button @click="openEditor(kind)" :disabled="!loaded">{{ t('remoteAccess.add') }}</n-button><n-button v-if="kind === 'identities'" @click="showGenerate=true" :disabled="!loaded">{{ t('remoteAccess.generate') }}</n-button></n-space>
          <n-alert v-if="kind === 'identities' || kind === 'grants'" type="info">{{ t('remoteAccess.revokeHint') }}</n-alert>
          <n-empty v-if="!state[kind].length" :description="t('remoteAccess.empty')" />
          <n-card v-for="item in state[kind]" :key="item.id" size="small">
            <n-space justify="space-between" align="center">
              <div><strong>{{ itemLabel(item) }}</strong><div class="metadata">{{ describe(kind, item) }}</div><n-tag size="small" :type="item.enabled ? 'success' : 'warning'">{{ t(`remoteAccess.${item.enabled ? 'enabled' : 'disabled'}`) }}</n-tag></div>
              <n-space><n-button size="small" @click="openEditor(kind, item)">{{ t('remoteAccess.edit') }}</n-button></n-space>
            </n-space>
          </n-card>
        </n-space>
      </n-tab-pane>
      <n-tab-pane name="advanced" :tab="t('remoteAccess.advanced')"><RemoteAdvanced :snapshot="state" @changed="load" /></n-tab-pane>
      <n-tab-pane name="keys" :tab="t('remoteAccess.keys')"><n-space vertical><n-button :disabled="!state.enabled" @click="showKey=true">{{ t('remoteAccess.keyOperation') }}</n-button><n-alert type="warning">{{ t('remoteAccess.keyHint') }}</n-alert><n-card v-for="op in keyOperations" :key="op.id" size="small"><strong>{{ op.action }} · {{ op.state }}</strong><p>{{ op.id }} · {{ op.installation_id }}</p><n-alert v-if="op.error_code" type="error">{{ op.error_code }}</n-alert></n-card><n-card v-for="i in state.installations" :key="i.id" size="small"><strong>{{ i.id }} · {{ i.state }}</strong><p>{{ i.fingerprint }}</p><p>{{ i.server_id }} · {{ i.identity_id }}</p></n-card></n-space></n-tab-pane>
      <n-tab-pane name="executions" :tab="t('remoteAccess.executions')">
        <n-space vertical>
          <n-button type="primary" :disabled="!state.enabled || !state.routes.length || executing" @click="showRun = true">{{ t('remoteAccess.run') }}</n-button>
          <n-empty v-if="!state.executions.length" :description="t('remoteAccess.empty')" />
          <n-card v-for="execution in state.executions" :key="execution.id" size="small">
            <n-space vertical>
              <div><strong>{{ execution.id }}</strong> · {{ execution.state }} · {{ routeLabel(execution.route_id) }}</div>
              <n-alert v-if="execution.state === 'unknown_outcome'" type="warning">{{ t('remoteAccess.unknown') }}</n-alert>
              <div v-if="execution.exit_code !== undefined">{{ t('remoteAccess.exitCode') }}: {{ execution.exit_code }}</div>
              <template v-if="pending(execution)">
                <n-alert type="warning">{{ t('remoteAccess.approvalHint') }}</n-alert>
                <n-alert v-if="execution.model_output" type="warning">{{ t('remoteAccess.modelOutputWarning') }}</n-alert>
                <pre>{{ execution.command }}</pre>
                <small>{{ t('remoteAccess.planHash') }}: {{ execution.plan_hash }}</small>

              </template>
              <n-space>
                <n-button v-if="cancellable(execution)" :disabled="busy" @click="cancel(execution.id)">{{ t('remoteAccess.stop') }}</n-button>
                <n-button v-if="execution.artifact_id" :disabled="busy" @click="output(execution.artifact_id!)">{{ t('remoteAccess.output') }}</n-button>
              </n-space>
            </n-space>
          </n-card>
        </n-space>
      </n-tab-pane>
    </n-tabs>

    <n-modal :show="!!editor" @update:show="value => { if (!value) closeEditor() }" preset="card" :title="t(`remoteAccess.${editingID ? 'edit' : 'add'}`)" class="remote-modal">
      <form v-if="editor" @submit.prevent="save" class="remote-form" autocomplete="off">
        <label v-if="editor === 'servers' || editor === 'routes'">{{ t('remoteAccess.name') }}<input v-model="form.label" required maxlength="120"></label>
        <label>{{ t('remoteAccess.enabled') }}<input v-model="form.enabled" type="checkbox"></label>
        <template v-if="editor === 'servers'">
          <label>{{ t('remoteAccess.host') }}<input v-model="form.address" required></label>
          <label>{{ t('remoteAccess.port') }}<input v-model.number="form.port" type="number" min="1" max="65535" required></label>
          <n-alert type="warning">{{ t('remoteAccess.trustHint') }}</n-alert>
          <label>{{ t('remoteAccess.trustSource') }}<input v-model="form.trust_source" required></label>
          <label>{{ t('remoteAccess.fingerprints') }}<textarea v-model="fingerprints" rows="3" spellcheck="false"></textarea></label>
        </template>
        <template v-if="editor === 'identities'">
          <n-alert type="info">{{ t('remoteAccess.secretHint') }}</n-alert>
          <label>{{ t('remoteAccess.username') }}<input v-model="form.username" required autocomplete="off"></label>
          <label>{{ t('remoteAccess.authType') }}<select v-model="form.auth_type"><option value="password">{{ t('remoteAccess.password') }}</option><option value="private_key">{{ t('remoteAccess.privateKey') }}</option></select></label>
          <label v-if="form.auth_type === 'password'">{{ t('remoteAccess.password') }}<input v-model="password" type="password" autocomplete="new-password" :required="!editingID"></label>
          <template v-else>
            <label>{{ t('remoteAccess.privateKey') }}<input :value="privateKey" type="password" autocomplete="new-password" :required="!editingID" @paste.prevent="pastePrivateKey" @input="privateKey=($event.target as HTMLInputElement).value"><input type="file" accept=".pem,.key" @change="readPrivateKey"></label>
            <label>{{ t('remoteAccess.passphrase') }}<input v-model="passphrase" type="password" autocomplete="new-password"></label>
          </template>
        </template>
        <template v-if="editor === 'routes'">
          <p>{{ t('remoteAccess.hops') }}</p>
          <fieldset v-for="(hop,index) in hops" :key="index"><legend>{{ index + 1 }}</legend>
            <label>{{ t('remoteAccess.server') }}<select v-model="hop.server_id" required><option value="" disabled>—</option><option v-for="server in state.servers" :key="server.id" :value="server.id">{{ server.label }} · {{ server.address }}</option></select></label>
            <label>{{ t('remoteAccess.identity') }}<select v-model="hop.identity_id" required><option value="" disabled>—</option><option v-for="identity in state.identities" :key="identity.id" :value="identity.id">{{ identity.username }} · {{ identity.id }}</option></select></label>
            <n-button :disabled="hops.length === 1" @click="hops.splice(index,1)">{{ t('remoteAccess.remove') }}</n-button>
          </fieldset>
          <n-button :disabled="hops.length >= 4" @click="hops.push({server_id:'',identity_id:''})">{{ t('remoteAccess.addHop') }}</n-button>
        </template>
        <template v-if="editor === 'grants'">

          <label>{{ t('remoteAccess.caller') }}<input v-model="form.caller_id" required placeholder="remote_exec"></label>
          <label>{{ t('remoteAccess.route') }}<select v-model="form.route_id" required><option v-for="route in state.routes" :key="route.id" :value="route.id">{{ routeLabel(route.id) }}</option></select></label>
          <label>{{ t('remoteAccess.capabilities') }}<select v-model="form.capability" required><option value="exec">exec</option><option value="key_install">key_install</option><option value="key_remove">key_remove</option><option v-for="cap in ['file_get','file_put','file_stat','background_install','background_start','background_status','background_cancel','terminal','runbook']" :key="cap" :value="cap">{{ cap }}</option></select></label>
          <label v-if="form.capability?.startsWith('file_') || form.capability?.startsWith('background_')">{{ t('remoteAccess.root') }}<input v-model="form.file_root" placeholder="/home/user/private" required></label>
          <template v-if="form.capability==='runbook'"><label>Runbook<select v-model="form.runbook_id" required><option v-for="book in state.runbooks" :key="book.id" :value="book.id">{{ book.label }} · v{{ book.revision }}</option></select></label><label>{{ t('remoteAccess.maxUses') }}<input v-model.number="form.max_uses" type="number" min="1" max="1000" required></label></template>
          <template v-if="form.capability==='exec'"><label>{{ t('remoteAccess.allowModelOutput') }}<input v-model="form.allow_model_output" type="checkbox" data-testid="grant-model-output"></label><p>{{ t('remoteAccess.modelOutputGrantHint') }}</p></template>
          <label>{{ t('remoteAccess.permanent') }}<input v-model="form.permanent" type="checkbox" data-testid="grant-permanent"></label>
          <p v-if="form.permanent">{{ t('remoteAccess.permanentHint') }}</p>
          <label v-else>{{ t('remoteAccess.expires') }}<input v-model="expires" type="datetime-local" required></label>
        </template>
        <n-space><n-button @click="closeEditor">{{ t('remoteAccess.cancel') }}</n-button><n-button attr-type="submit" type="primary" :loading="busy">{{ t('remoteAccess.save') }}</n-button></n-space>
      </form>
    </n-modal>
    <n-modal v-model:show="showKey" preset="card" :title="t('remoteAccess.keyOperation')" class="remote-modal"><form class="remote-form" @submit.prevent="keyOperation"><n-alert type="warning">{{ t('remoteAccess.keyHint') }}</n-alert><label>{{ t('remoteAccess.action') }}<select v-model="keyForm.action"><option value="install">{{ t('remoteAccess.install') }}</option><option value="verify">{{ t('remoteAccess.verify') }}</option><option value="remove">{{ t('remoteAccess.removeKey') }}</option><option value="reconcile">{{ t('remoteAccess.reconcile') }}</option></select></label><label>{{ t('remoteAccess.route') }}<select v-model="keyForm.route_id" required><option v-for="r in state.routes" :key="r.id" :value="r.id">{{ routeLabel(r.id) }}</option></select></label><label v-if="keyForm.action==='install'">{{ t('remoteAccess.identity') }}<select v-model="keyForm.identity_id" required><option v-for="i in state.identities.filter(i=>i.auth_type==='private_key')" :key="i.id" :value="i.id">{{ i.username }} · {{ i.public_fingerprint }} · {{ i.id }}</option></select></label><label v-else>{{ t('remoteAccess.installation') }}<select v-model="keyForm.installation_id" required><option v-for="i in state.installations" :key="i.id" :value="i.id">{{ i.fingerprint }} · {{ i.state }} · {{ i.id }}</option></select></label><template v-if="keyForm.action==='remove'"><label>{{ t('remoteAccess.alternate') }}<select v-model="keyForm.alternate_id" required><option v-for="i in (state.installations||[]).filter(i=>i.state==='verified' && i.id!==keyForm.installation_id)" :key="i.id" :value="i.id">{{ i.fingerprint }} · {{ i.id }}</option></select></label></template><n-button attr-type="submit" type="primary" :loading="busy">{{ t('remoteAccess.prepare') }}</n-button></form></n-modal>
    <n-modal v-model:show="showGenerate" preset="card" :title="t('remoteAccess.generate')" class="remote-modal"><form class="remote-form" @submit.prevent="generate"><label>{{ t('remoteAccess.username') }}<input v-model="generateUsername" required></label><n-alert type="info">{{ t('remoteAccess.generateHint') }}</n-alert><n-button attr-type="submit" type="primary" :loading="busy">{{ t('remoteAccess.generate') }}</n-button></form></n-modal>
    <n-modal v-model:show="showRun" preset="card" :title="t('remoteAccess.run')" class="remote-modal">
      <form @submit.prevent="execute" class="remote-form">
        <n-alert type="warning">{{ t('remoteAccess.commandHint') }}</n-alert>
        <label>{{ t('remoteAccess.route') }}<select v-model="runRoute" required><option value="" disabled>—</option><option v-for="route in state.routes.filter(r => r.enabled)" :key="route.id" :value="route.id">{{ routeLabel(route.id) }}</option></select></label>
        <p>{{ t('remoteAccess.modelOutputChatHint') }}</p>
        <label>{{ t('remoteAccess.command') }}<textarea v-model="command" required rows="6" spellcheck="false"></textarea></label>
        <label>{{ t('remoteAccess.timeout') }}<input v-model.number="timeout" type="number" min="1" max="300" required></label>
        <n-button attr-type="submit" type="primary" :loading="busy">{{ t('remoteAccess.prepare') }}</n-button>
      </form>
    </n-modal>
    <n-modal v-model:show="showApproval" preset="card" :title="t('remoteAccess.approve')" class="remote-modal"><n-alert type="warning">{{ t('remoteAccess.approvalHint') }}</n-alert><n-alert v-if="approval?.model_output" type="warning">{{ t('remoteAccess.modelOutputWarning') }}</n-alert><p>{{ approval?.servers.map((s,i) => `${approval?.identities[i]?.username}@${s.address}:${s.port}`).join(' → ') }}</p><pre>{{ approval?.command }}</pre><small>{{ approval?.plan_hash }}</small><n-space><n-button @click="showApproval=false">{{ t('remoteAccess.cancel') }}</n-button><n-button type="primary" :disabled="busy || !state.enabled" @click="confirmExecution">{{ t('remoteAccess.approve') }}</n-button></n-space></n-modal>
    <n-modal v-model:show="showOutput" preset="card" :title="t('remoteAccess.output')" class="remote-modal">
      <n-alert type="warning">{{ t('remoteAccess.outputHint') }}</n-alert>
      <h3>{{ t('remoteAccess.stdout') }}</h3><pre>{{ transcript.stdout }}</pre>
      <h3>{{ t('remoteAccess.stderr') }}</h3><pre>{{ transcript.stderr }}</pre>
    </n-modal>

  </n-space>
</template>

<script setup lang="ts">
import RemoteAdvanced from './RemoteAdvanced.vue'
import { computed, onMounted, onUnmounted, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { NAlert, NButton, NCard, NEmpty, NModal, NPageHeader, NPopconfirm, NSpace, NTabPane, NTabs, NTag } from 'naive-ui'
import { remoteAccessApi, type RemoteCollection, type RemoteSnapshot, type SSHExecution, type SSHHop, type SSHApproval, type SSHKeyOperation, type SSHExecuteRequest } from '../remoteAccessApi'
const { t } = useI18n()
const collections: RemoteCollection[] = ['servers','identities','routes','grants']
const state = reactive<RemoteSnapshot>({enabled:false,servers:[],identities:[],routes:[],grants:[],executions:[]})
const executing=ref(false)
const loaded=ref(false), loading=ref(false), busy=ref(false), error=ref(''), tab=ref('servers')
const editor=ref<RemoteCollection|null>(null), editingID=ref(''), form=ref<Record<string,any>>({})
const password=ref(''),privateKey=ref(''),passphrase=ref(''),fingerprints=ref(''),expires=ref(''),hops=ref<SSHHop[]>([])
const keyOperations=ref<SSHKeyOperation[]>([]), pendingApprovals=ref<SSHApproval[]>([]), showKey=ref(false),keyForm=reactive({action:'install',route_id:'',identity_id:'',installation_id:'',alternate_id:'',new_route_id:''})
const showGenerate=ref(false),generateUsername=ref('')
const showApproval=ref(false),approval=ref<SSHApproval|null>(null), frozenRequest=ref<SSHExecuteRequest>({route_id:'',command:''})
const showRun=ref(false), runRoute=ref(''),command=ref(''),timeout=ref(60),showOutput=ref(false),transcript=ref({stdout:'',stderr:''})
watch(()=>form.value.capability,cap=>{if(cap!=='exec')form.value.allow_model_output=false})
const activeCount=computed(()=>state.executions.filter(cancellable).length)
function pending(e:SSHExecution){return ['pending_approval','awaiting_approval'].includes(e.state)}
function cancellable(e:SSHExecution){return pending(e)||['running','queued','dispatch_started','approved'].includes(e.state)}
function itemLabel(item:any){return item.label||item.username||item.id}
function routeLabel(id?:string){const r=state.routes.find(r=>r.id===id);return r?`${r.label}: ${r.hops.map(h=>{const s=state.servers.find(s=>s.id===h.server_id);const i=state.identities.find(i=>i.id===h.identity_id);return `${i?.username||'?'}@${s?.address||'?'}:${s?.port||22}`}).join(' → ')}`:id||'—'}
function displayExpiry(value?:string){return !value || value.startsWith('0001-') ? '—' : value}
function localExpiry(value?:string){if(!value || value.startsWith('0001-'))return '';const date=new Date(value);return Number.isNaN(date.getTime())?'':new Date(date.getTime()-date.getTimezoneOffset()*60000).toISOString().slice(0,16)}
function describe(kind:RemoteCollection,item:any){if(kind==='servers')return `${item.address}:${item.port} · ${(item.host_keys||[]).join(', ')}`;if(kind==='identities')return `${item.username} · ${item.auth_type} · ${item.public_fingerprint||''} ${item.public_key||''}`;if(kind==='routes')return routeLabel(item.id);return `${item.caller_id} · ${routeLabel(item.route_id)} · ${item.capability} · ${item.permanent?t('remoteAccess.permanent'):displayExpiry(item.expires_at)}${item.allow_model_output===true?' · '+t('remoteAccess.modelOutputEnabled'):''}`}
async function load(){loading.value=true;try{const s=await remoteAccessApi.snapshot();Object.assign(state,{...s,servers:s.servers||[],identities:s.identities||[],routes:s.routes||[],grants:s.grants||[],executions:s.executions||[]});pendingApprovals.value=await remoteAccessApi.approvals();keyOperations.value=await remoteAccessApi.keyOperations();loaded.value=true}catch{error.value=t('remoteAccess.error')}finally{loading.value=false}}
async function mutate(action:()=>Promise<unknown>){if(busy.value)return;busy.value=true;error.value='';try{await action();await load()}catch{error.value=t('remoteAccess.error')}finally{busy.value=false}}
function closeEditor(){editor.value=null;password.value='';privateKey.value='';passphrase.value='';form.value={}}
function openEditor(kind:RemoteCollection,item?:any){closeEditor();editor.value=kind;editingID.value=item?.id||'';form.value=item?JSON.parse(JSON.stringify(item)):{label:'',enabled:true,address:'',port:22,auth_type:'password',username:'',caller_id:'dashboard',route_id:'',capability:'exec',permanent:false};form.value.permanent=item?.permanent===true;form.value.allow_model_output=item?.allow_model_output===true;fingerprints.value=(item?.host_keys||[]).join('\n');hops.value=item?.hops?JSON.parse(JSON.stringify(item.hops)):[{server_id:'',identity_id:''}];expires.value=localExpiry(item?.expires_at)}
async function save(){
  if(!editor.value)return
  const kind=editor.value, f=form.value
  let data:Record<string,unknown>={enabled:f.enabled}
  if(kind==='servers')Object.assign(data,{label:f.label,address:f.address,port:f.port,host_keys:fingerprints.value.split(/\n/).map(s=>s.trim()).filter(Boolean),trust_source:f.trust_source})
  if(kind==='routes')Object.assign(data,{label:f.label,hops:hops.value})
  if(kind==='grants')Object.assign(data,{caller_id:f.caller_id,route_id:f.route_id,capability:f.capability,permanent:f.permanent===true,allow_model_output:f.capability==='exec'&&f.allow_model_output===true,...(f.permanent?{}:{expires_at:new Date(expires.value).toISOString()}),...(f.file_root?{file_root:f.file_root}:{}),...(f.capability==='runbook'?{runbook_id:f.runbook_id,max_uses:f.max_uses}: {})})
  if(kind==='identities'){
    const identity={username:f.username,auth_type:f.auth_type,enabled:f.enabled,...(f.expires_at?{expires_at:f.expires_at}:{})}
    const secret={...(password.value?{password:password.value}:{}),...(privateKey.value?{private_key:privateKey.value}:{}),...(passphrase.value?{passphrase:passphrase.value}:{})}
    data={identity,...(Object.keys(secret).length?{secret}:{})}
  }
  await mutate(async()=>{await remoteAccessApi.save(kind,data,editingID.value||undefined);closeEditor()})
}
async function decide(a:SSHApproval,approved:boolean){await mutate(()=>remoteAccessApi.decide(a.execution_id,a.plan_hash,approved))}
async function keyOperation(){await mutate(async()=>{await remoteAccessApi.keyOperation({...keyForm});showKey.value=false;tab.value='executions'})}
async function generate(){await mutate(async()=>{await remoteAccessApi.generate(generateUsername.value);generateUsername.value='';showGenerate.value=false})}
async function toggleEnabled(){await mutate(()=>remoteAccessApi.enabled(!state.enabled))}
async function execute(){await mutate(async()=>{frozenRequest.value={route_id:runRoute.value,command:command.value,timeout_seconds:timeout.value};approval.value=await remoteAccessApi.preview(frozenRequest.value);showRun.value=false;showApproval.value=true})}
async function confirmExecution(){if(!approval.value||executing.value)return;const request={...frozenRequest.value,expected_plan_hash:approval.value.plan_hash,approval_id:approval.value.approval_id};showApproval.value=false;approval.value=null;frozenRequest.value={route_id:'',command:''};command.value='';tab.value='executions';executing.value=true;error.value='';try{await remoteAccessApi.execute(request)}catch{error.value=t('remoteAccess.error')}finally{executing.value=false;await load()}}
async function cancel(id:string){await mutate(()=>remoteAccessApi.cancel(id))}
function pastePrivateKey(event:ClipboardEvent){privateKey.value=event.clipboardData?.getData('text/plain')||''}
async function readPrivateKey(event:Event){const input=event.target as HTMLInputElement;const file=input.files?.[0];if(!file)return;if(file.size>65536){error.value=t('remoteAccess.error');input.value='';return}privateKey.value=await file.text();input.value=''}
function stripControls(s:string){return s.replace(/\x1b\][^\x07]*(?:\x07|\x1b\\)/g,'').replace(/\x1b\[[0-?]*[ -/]*[@-~]/g,'').replace(/[\x00-\x08\x0b-\x1f\x7f]/g,'')}
async function output(id:string){await mutate(async()=>{const out=await remoteAccessApi.artifact(id);transcript.value={stdout:stripControls(out.stdout||''),stderr:stripControls(out.stderr||'')};showOutput.value=true})}
let poll:ReturnType<typeof setInterval>|undefined
onMounted(()=>{void load();poll=setInterval(()=>{if(!busy.value&&!loading.value&&(activeCount.value||executing.value||keyOperations.value.some(o=>['running','pending','awaiting_approval'].includes(o.state))||pendingApprovals.value.length))void load()},5000)})
onUnmounted(()=>{clearInterval(poll);closeEditor();transcript.value={stdout:'',stderr:''}})
</script>
<style scoped>
.remote-modal{width:min(720px,calc(100vw - 24px));max-height:90vh;overflow:auto}.remote-form{display:grid;gap:16px}.remote-form label{display:grid;gap:6px}.remote-form input:not([type=checkbox]),.remote-form textarea,.remote-form select{box-sizing:border-box;width:100%;padding:9px;border:1px solid #8886;border-radius:4px;background:transparent;color:inherit;font:inherit}.remote-form input[type=checkbox]{justify-self:start}.remote-form fieldset{display:grid;gap:12px;border:1px solid #8886;border-radius:4px}pre{white-space:pre-wrap;overflow-wrap:anywhere;max-height:50vh;overflow:auto;font-size:12px}.metadata{overflow-wrap:anywhere;margin:6px 0;font-size:12px}
</style>
