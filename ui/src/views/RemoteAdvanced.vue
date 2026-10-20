<template>
  <n-space vertical :size="16">
    <n-alert v-if="error" type="error" role="alert">{{ error }}</n-alert>
    <n-card :title="L('Проверенные команды и расписания','Runbooks and schedules')">
      <n-alert type="info">{{ L('Для подагентов и расписаний нужны отдельные права delegate и scheduler, привязанные к версии команды и числу запусков.','Delegates and schedules require exact versioned delegate/scheduler grants with a finite use count.') }}</n-alert>
      <form class="advanced-form" @submit.prevent="saveBook">
        <label>{{ L('Редактировать','Edit') }}<select v-model="book.id" @change="editBook"><option value="">{{ L('Новый runbook','New runbook') }}</option><option v-for="b in snapshot.runbooks" :key="b.id" :value="b.id">{{ b.label }} · v{{ b.revision }}</option></select></label>
        <label>{{ L('Название','Label') }}<input v-model="book.label" required></label>
        <label>{{ L('Маршрут','Route') }}<select v-model="book.route_id" required><option v-for="r in snapshot.routes" :key="r.id" :value="r.id">{{ r.label || r.id }}</option></select></label>
        <label>{{ L('Команда с параметрами в двойных фигурных скобках','Command with parameters in double braces') }}<textarea v-model="book.command" rows="3" required spellcheck="false"></textarea></label>
        <label>{{ L('Параметры: одна простая команда, отдельные аргументы без кавычек и shell-синтаксиса (JSON)','Parameters: one simple command, standalone arguments, no quotes or shell syntax (JSON)') }}<textarea v-model="parameterSchema" rows="3" spellcheck="false" placeholder='{"service":{"values":["nginx","postgres"]}}'></textarea></label>
        <label><input type="checkbox" v-model="book.enabled"> {{ L('Включён','Enabled') }}</label>
        <n-button attr-type="submit" :loading="busy">{{ L('Сохранить новую версию','Save new version') }}</n-button>
      </form>
      <form class="advanced-form" @submit.prevent="runBook">
        <label>Runbook<select v-model="selectedBook" required><option v-for="b in snapshot.runbooks" :key="b.id" :value="b.id">{{ b.label }} · v{{ b.revision }}</option></select></label>
        <label>{{ L('Параметры запуска (JSON)','Run parameters (JSON)') }}<textarea v-model="runParameters" rows="2" placeholder='{"service":"nginx"}'></textarea></label>
        <n-button attr-type="submit" :loading="busy">{{ L('Запустить по standing grant main','Run with main standing grant') }}</n-button>
        <label>{{ L('Интервал расписания (1m, 1h, 24h)','Schedule interval (1m, 1h, 24h)') }}<input v-model="interval"></label>
        <n-button :disabled="busy || !selectedBook" @click="schedule">{{ L('Создать расписание','Create schedule') }}</n-button>
      </form>
    </n-card>
    <n-card title="SFTP">
      <n-alert type="warning">{{ L('Только приватный доверенный каталог. Существующие файлы не заменяются. Скачивание не отправляет файл модели.','Use a private trusted root. Existing files are never replaced. Downloading does not send the file to the model.') }}</n-alert>
      <form class="advanced-form" @submit.prevent="prepareFile">
        <RouteSelect v-model="fileRoute" :routes="snapshot.routes" />
        <label>{{ L('Приватный корень','Private root') }}<input v-model="fileRoot" required placeholder="/home/user/private"></label>
        <label>{{ L('Относительный путь','Relative path') }}<input v-model="filePath" required placeholder="report.txt"></label>
        <label>{{ L('Действие','Action') }}<select v-model="fileAction"><option value="file_get">Download</option><option value="file_put">Upload</option><option value="file_stat">Stat</option></select></label>
        <label v-if="fileAction==='file_put'">{{ L('Файл до 10 MiB','File up to 10 MiB') }}<input type="file" @change="selectFile"></label>
        <n-button attr-type="submit" :disabled="busy || !snapshot.enabled">{{ L('Подготовить передачу','Preview transfer') }}</n-button>
      </form>
      <n-button v-if="lastArtifact" @click="downloadArtifact">{{ L('Скачать приватный результат','Download private result') }}</n-button>
      <pre v-if="lastFileMeta">{{ lastFileMeta }}</pre>
    </n-card>
    <n-card :title="L('Управляемые фоновые задачи','Managed background jobs')">
      <n-alert type="warning">{{ L('Нужны Linux/procfs и Python3. Helper устанавливается отдельным подтверждением. Закрытие SSH не останавливает фоновую задачу.','Requires Linux/procfs and Python3. Helper installation needs separate consent. SSH disconnection does not stop a background job.') }}</n-alert>
      <form class="advanced-form" @submit.prevent="prepareJob">
        <RouteSelect v-model="jobRoute" :routes="snapshot.routes" />
        <label>{{ L('Приватный каталог 0700','Private directory 0700') }}<input v-model="jobRoot" required></label>
        <label>Job ID<input v-model="jobID" required pattern="[a-zA-Z0-9_-]{16,80}"></label>
        <label>{{ L('Действие','Action') }}<select v-model="jobAction"><option value="background_install">Install helper</option><option value="background_start">Start</option><option value="background_status">Status</option><option value="background_cancel">Cancel</option></select></label>
        <label v-if="jobAction==='background_start'">{{ L('Команда','Command') }}<textarea v-model="jobCommand" rows="3" required></textarea></label>
        <n-button attr-type="submit" :disabled="busy">{{ L('Подготовить действие','Preview action') }}</n-button>
      </form>
      <n-card v-for="job in snapshot.jobs" :key="job.id || job.job_id" size="small"><strong>{{ job.id || job.job_id }} · {{ job.state }}</strong><pre>{{ job.root }} · {{ job.route_id }}</pre></n-card>
    </n-card>
    <n-card :title="L('Интерактивный терминал','Interactive terminal')">
      <n-alert type="warning">{{ L('Полные права удалённого аккаунта на время сессии. Один оператор. Нет автоматического reconnect или повторов ввода.','Full remote account privileges for the session. One writer. No automatic reconnect or input replay.') }}</n-alert>
      <form class="advanced-form" @submit.prevent="prepareTerminal">
        <RouteSelect v-model="terminalRoute" :routes="snapshot.routes" />
        <n-button attr-type="submit" :disabled="busy || connected || !snapshot.enabled">{{ L('Подготовить PTY','Preview PTY') }}</n-button>
        <n-button v-if="connected" type="error" @click="closeTerminal">{{ L('Отключить терминал','Disconnect terminal') }}</n-button>
      </form>
      <p role="status">{{ terminalState }}</p><div ref="terminalHost" class="terminal-host" aria-label="Remote SSH terminal"></div>
    </n-card>
    <n-modal v-model:show="showApproval" preset="card" :title="L('Подтверждение точного действия','Approve exact operation')" class="advanced-modal">
      <n-alert type="warning">{{ L('Проверьте маршрут, действие и параметры. Разрешение одноразовое.','Review route, operation and parameters. Consent is single-use.') }}</n-alert>
      <p>{{ approval?.servers.map((s:any,i:number)=>`${approval?.identities[i]?.username}@${s.address}:${s.port}`).join(' → ') }}</p><pre>{{ approval?.command }}</pre><small>{{ approval?.plan_hash }}</small>
      <n-space><n-button @click="showApproval=false">{{ L('Отмена','Cancel') }}</n-button><n-button type="primary" :disabled="busy || (!snapshot.enabled && !['background_status','background_cancel'].includes(frozen.operation))" @click="approve">{{ L('Разрешить один раз','Allow once') }}</n-button></n-space>
    </n-modal>
  </n-space>
</template>
<script setup lang="ts">
import { defineComponent, h, nextTick, onUnmounted, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { NAlert,NButton,NCard,NModal,NSpace } from 'naive-ui'
import { remoteRequest, type RemoteSnapshot } from '../remoteAccessApi'
import { Terminal } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import '@xterm/xterm/css/xterm.css'
const props=defineProps<{snapshot:RemoteSnapshot}>(),emit=defineEmits<{changed:[]}>(),{locale}=useI18n()
const L=(ru:string,en:string)=>locale.value==='ru'?ru:en
const RouteSelect=defineComponent({props:{modelValue:String,routes:{type:Array,required:true}},emits:['update:modelValue'],setup(p,{emit:e}){return()=>h('label',[h('span','Route'),h('select',{value:p.modelValue,required:true,onChange:(event:Event)=>e('update:modelValue',(event.target as HTMLSelectElement).value)},[h('option',{value:''},'—'),...(p.routes as any[]).map(r=>h('option',{value:r.id},r.label||r.id))])])}})
const busy=ref(false),error=ref(''),book=reactive({id:'',label:'',route_id:'',command:'',enabled:true}),parameterSchema=ref('{}'),selectedBook=ref(''),runParameters=ref('{}'),interval=ref('24h')
const fileRoute=ref(''),fileRoot=ref(''),filePath=ref(''),fileAction=ref('file_get'),selectedFile=ref<File|null>(null),lastArtifact=ref(''),lastFileMeta=ref('')
const jobRoute=ref(''),jobRoot=ref(''),jobID=ref(crypto.randomUUID().replace(/-/g,'')),jobAction=ref('background_install'),jobCommand=ref('')
const terminalRoute=ref(''),terminalHost=ref<HTMLDivElement|null>(null),connected=ref(false),terminalState=ref(''),showApproval=ref(false),approval=ref<any>(null)
let frozen:any={},operationKind='operation',socket:WebSocket|null=null,terminal:Terminal|null=null,observer:ResizeObserver|null=null
async function action(f:()=>Promise<void>){if(busy.value)return;busy.value=true;error.value='';try{await f();emit('changed')}catch{error.value=L('Действие отклонено или исход неизвестен. Автоматического повтора нет.','Operation denied or outcome unknown. No automatic retry.')}finally{busy.value=false}}
function editBook(){const b=props.snapshot.runbooks?.find(b=>b.id===book.id);if(b){Object.assign(book,{id:b.id,label:b.label,route_id:b.route_id,command:b.command,enabled:b.enabled});parameterSchema.value=JSON.stringify(b.parameters,null,2)}}
async function saveBook(){await action(async()=>{await remoteRequest(`/runbooks${book.id?'/'+encodeURIComponent(book.id):''}`,book.id?'PUT':'POST',{label:book.label,route_id:book.route_id,command:book.command,enabled:book.enabled,parameters:JSON.parse(parameterSchema.value)})})}
function runRequest(){return {runbook_id:selectedBook.value,parameters:JSON.parse(runParameters.value),timeout_seconds:60}}
async function runBook(){await action(async()=>{await remoteRequest('/runbooks/execute','POST',runRequest())})}
async function schedule(){await action(async()=>{await remoteRequest('/schedules','POST',{name:props.snapshot.runbooks?.find(b=>b.id===selectedBook.value)?.label||'SSH runbook',interval:interval.value,request:runRequest()})})}
function selectFile(e:Event){const f=(e.target as HTMLInputElement).files?.[0];selectedFile.value=f&&f.size<=10*1024*1024?f:null;if(f&&!selectedFile.value)error.value='10 MiB maximum'}
function base64(data:Uint8Array){let binary='';for(let i=0;i<data.length;i+=8192)binary+=String.fromCharCode(...data.subarray(i,i+8192));return btoa(binary)}
async function prepareFile(){await action(async()=>{let data:Uint8Array|undefined,sha='';if(fileAction.value==='file_put'){if(!selectedFile.value)throw Error();data=new Uint8Array(await selectedFile.value.arrayBuffer());sha=Array.from(new Uint8Array(await crypto.subtle.digest('SHA-256',data.buffer as ArrayBuffer))).map(n=>n.toString(16).padStart(2,'0')).join('')};frozen={route_id:fileRoute.value,operation:fileAction.value,file:{root:fileRoot.value,path:filePath.value,sha256:sha,...(data?{data:base64(data)}:{})},timeout_seconds:60};operationKind='operation';approval.value=await remoteRequest('/operations/preview','POST',frozen);showApproval.value=true})}
async function prepareJob(){await action(async()=>{frozen={route_id:jobRoute.value,operation:jobAction.value,background:{root:jobRoot.value,job_id:jobID.value,...(jobAction.value==='background_start'?{command:jobCommand.value}:{})},timeout_seconds:60};operationKind='operation';approval.value=await remoteRequest('/operations/preview','POST',frozen);showApproval.value=true})}
async function prepareTerminal(){await action(async()=>{frozen={route_id:terminalRoute.value,options:{rows:24,cols:80,term:'xterm-256color',timeout_seconds:3600}};operationKind='terminal';approval.value=await remoteRequest('/terminals/preview','POST',frozen);showApproval.value=true})}
async function approve(){await action(async()=>{const req={...frozen,approval_id:approval.value.approval_id,expected_plan_hash:approval.value.plan_hash};showApproval.value=false;if(operationKind==='terminal'){const t=await remoteRequest<{ticket:string}>('/terminals/ticket','POST',req);await openTerminal(t.ticket)}else{const x=await remoteRequest<any>('/operations','POST',req);lastArtifact.value=x.artifact_id||'';lastFileMeta.value=JSON.stringify(x,null,2)}})}
async function downloadArtifact(){await action(async()=>{const a=await remoteRequest<any>('/artifacts/'+encodeURIComponent(lastArtifact.value));if(!a.file){lastFileMeta.value=JSON.stringify(a.background||a,null,2);return};lastFileMeta.value=JSON.stringify({...a.file,data:undefined},null,2);if(a.file.data || a.file.size===0){const bytes=Uint8Array.from(atob(a.file.data||''),c=>c.charCodeAt(0)),blob=new Blob([bytes]),url=URL.createObjectURL(blob),link=document.createElement('a');link.href=url;link.download=a.file.path.split('/').pop()||'ssh-file';link.click();URL.revokeObjectURL(url)}})}
async function openTerminal(ticket:string){closeTerminal();await nextTick();if(!terminalHost.value)throw Error();terminal=new Terminal({scrollback:2000,allowProposedApi:false,convertEol:false});terminal.parser.registerOscHandler(52,()=>true);terminal.parser.registerOscHandler(8,()=>true);const fit=new FitAddon();terminal.loadAddon(fit);terminal.open(terminalHost.value);fit.fit();const url=new URL('/api/remote-access/terminals/connect/'+encodeURIComponent(ticket),location.href);url.protocol=location.protocol==='https:'?'wss:':'ws:';socket=new WebSocket(url);const openedSocket=socket,openedTerminal=terminal;terminalState.value=L('Подключение…','Connecting…');socket.onmessage=e=>{if(socket!==openedSocket)return;const f=JSON.parse(e.data);if(f.kind==='ready'){connected.value=true;terminalState.value=f.execution_id;fit.fit();terminal?.focus()}else if(f.kind==='output')openedTerminal.write(Uint8Array.from(atob(f.data),c=>c.charCodeAt(0)))};socket.onclose=()=>{if(socket!==openedSocket)return;connected.value=false;terminalState.value=L('Соединение закрыто. Исход проверьте в операциях.','Connection closed. Check execution outcome.');emit('changed')};terminal.onData(data=>{if(socket!==openedSocket)return;const bytes=new TextEncoder().encode(data);if(bytes.length>65536){error.value='Input exceeds 64 KiB';return};for(let n=0;n<bytes.length;n+=4096)send({kind:'input',data:base64(bytes.subarray(n,n+4096))})});terminal.onResize(size=>{if(socket!==openedSocket)return;if(size.rows<=500&&size.cols<=500)send({kind:'resize',rows:size.rows,cols:size.cols})});observer=new ResizeObserver(()=>fit.fit());observer.observe(terminalHost.value)}
function send(frame:unknown){if(socket?.readyState===WebSocket.OPEN&&connected.value)socket.send(JSON.stringify(frame))}
function closeTerminal(){observer?.disconnect();observer=null;if(socket){socket.onmessage=null;socket.onclose=null;socket.onerror=null;socket.close()}socket=null;terminal?.dispose();terminal=null;connected.value=false}
onUnmounted(closeTerminal)
</script>
<style scoped>
.advanced-form{display:grid;gap:12px;margin-block:16px}.advanced-form label{display:grid;gap:6px}.advanced-form input,.advanced-form select,.advanced-form textarea{color:inherit;background:transparent;border:1px solid #8886;border-radius:5px;padding:8px;width:100%;box-sizing:border-box}.advanced-form input[type=checkbox]{width:auto}.terminal-host{height:400px;width:100%;background:#111}.advanced-modal{width:min(760px,94vw)}pre{white-space:pre-wrap;overflow-wrap:anywhere;max-height:320px;overflow:auto}
</style>
