export interface SystemInfo {
  app: string
  version: string
  uptime: string
  uptime_sec: number
  go_version: string
  started_at: string
  setup_mode?: boolean
  wizard_completed?: boolean
  encryption_enabled?: boolean
}

export interface Stats {
  messages: number
  facts: number
  insights: number
  reminders: number
  tech_facts: number
}

export interface ChatInfo {
  chat_id: string
  messages: number
}

export interface Fact {
  ID: number
  ChatID: string
  UserID: string
  Content: string
  SourceType: string
  CreatedAt: string
  LastAccessedAt: string
  AccessCount: number
}

export interface Insight {
  id: number
  chat_id: string
  user_id: string
  content: string
  fact_ids: string
  quality: number
  created_at: string
}

export interface Reminder {
  ID: number
  ChatID: string
  UserID: string
  Title: string
  DueAt: string
  Timezone: string
  Status: string
  CreatedAt: string
}

export interface Directive {
  ID: number
  ChatID: string
  Content: string
  CreatedAt: string
  UpdatedAt: string
}

export interface SkillInfo {
  name: string
  description: string
  type: string
  enabled: boolean
  has_capabilities: boolean
  has_config: boolean
  manifest_group?: string
}

export interface SkillConfigField {
  key: string
  secret: boolean
  value?: string
  has_value: boolean
  has_override: boolean
}

export interface SkillConfigResponse {
  skill: string
  manifest: string
  description: string
  schema: SkillConfigField[]
  encryption_enabled: boolean
}

export interface Message {
  ID: number
  ChatID: string
  Role: string
  Content: string
  CreatedAt: string
}

export interface TechFactEntry {
  id: number
  key: string
  value: string
  confidence: number
  update_count: number
  updated_at: string
}

export type TechFactGroup = Record<string, TechFactEntry[]>

export interface JobInfo {
  name: string
  interval: string
  enabled: boolean
  last_run?: string
  next_run?: string
}

export interface Task {
  id: number
  type: string
  payload: string
  status: string
  priority: number
  capabilities: string
  unique_key: string
  scheduled_at: string
  claimed_at?: string
  started_at?: string
  finished_at?: string
  worker_id: string
  attempts: number
  max_attempts: number
  error: string
  result: string
  created_at: string
}

export type TaskCounts = Record<string, number>

export interface ConfigEntry {
  key: string
  value: string
  encrypted: boolean
  updated_at: string
  updated_by: string
}

export interface ConfigListResponse {
  overrides: ConfigEntry[]
  encryption_enabled: boolean
}

export interface LoginResponse {
  access_token: string
  refresh_token: string
  must_change_password: boolean
}

export interface UserInfo {
  id: string
  username: string
  role: string
  display_name: string
  timezone: string
  must_change_pass: boolean
  created_at: string
}

export interface UserChannel {
  id: number
  user_id: string
  channel_instance_id: string
  channel_type: string
  channel_id: string
  channel_user_id: string
  channel_username: string
  enabled: boolean
  created_at: string
}

export interface ChannelWithUser {
  id: number
  user_id: string
  channel_type: string
  channel_id: string
  channel_user_id: string
  channel_username: string
  enabled: boolean
  created_at: string
  owner_username: string
  owner_display_name: string
}

export interface ChannelInstance {
  id: string
  type: string
  name: string
  config: string
  source: string // "config" | "dashboard"
  enabled: boolean
  created_at: string
  updated_at: string
}

export interface ChannelBinding {
  id: number
  user_id: string
  channel_type: string
  channel_id: string
  channel_user_id: string
  channel_username: string
  enabled: boolean
  created_at: string
  owner_username: string
  owner_display_name: string
}

export interface GoogleAccount {
  id: number
  account_email: string
  account_alias: string
  is_default: boolean
  token_expiry: string
  scopes: string
  created_at: string
}

export interface GoogleCredentialStatus {
  source: string
  credential_type: string
  file_path: string
  db_accounts: number
  active_scopes: string
}

// External skills (marketplace)
export interface InstalledSkill {
  id: number
  slug: string
  name: string
  version: string
  source: string
  source_ref: string
  isolation: string
  install_dir: string
  enabled: boolean
  pinned: boolean
  checksum: string
  description: string
  author: string
  tags: string
  capabilities: string
  config_keys: string
  secret_keys: string
  requires_bins: string
  requires_env: string
  allowed_tools: string
  has_code: boolean
  effective_mode: string
  install_warnings: string
  installed_at: string
  updated_at?: string
}

export interface ExternalSkillResult {
  slug: string
  name: string
  version: string
  description: string
  author: string
  tags: string[]
  source: string
  source_ref: string
  downloads?: number
  stars?: number
  updated_at?: number
}

export interface ExternalSkillDetail {
  slug: string
  name: string
  version: string
  description: string
  author: string
  owner_display_name?: string
  tags: string[]
  source: string
  source_ref: string
  download_url?: string
  downloads?: number
  stars?: number
  updated_at?: number
}

// Config schema types
export interface ConfigSchemaField {
  key: string
  label: string
  description: string
  type: 'string' | 'int' | 'bool' | 'select' | 'secret' | 'url'
  default?: string
  options?: string[]
  secret: boolean
  required: boolean
  section: string
  model_source?: '' | 'openai' | 'ollama'
  // Enriched by backend:
  value?: string
  has_value?: boolean
  has_override?: boolean
}

export interface ConfigSchemaSection {
  name: string
  label: string
  description: string
  fields: ConfigSchemaField[]
}

export interface ConfigSchemaResponse {
  sections: ConfigSchemaSection[]
  encryption_enabled: boolean
}

export interface ConfigDebugRow {
  key: string
  label: string
  section: string
  secret: boolean
  base: string
  has_base: boolean
  db: string
  has_db: boolean
  effective: string
  source: 'default' | 'config' | 'database'
}

export interface ConfigDebugPaths {
  config_dir: string
  data_dir: string
  cache_dir: string
  state_dir: string
  config_file: string
  config_exists: boolean
  database_file: string
  models_dir: string
  log_file: string
  sentinel_file: string
  sentinel_exists: boolean
  encryption_key: string
}

export interface ConfigDebugResponse {
  rows: ConfigDebugRow[]
  paths: ConfigDebugPaths
  env_vars: { name: string; value: string }[] | null
  encryption_enabled: boolean
}

export interface ModelsResponse {
  models: string[]
  source: 'static' | 'dynamic'
}

export interface WizardStatus {
  wizard_completed: boolean
  setup_mode: boolean
  encryption_enabled: boolean
  has_llm_provider: boolean
}

export interface WizardSchemaField {
  key: string
  label: string
  description: string
  type: string
  default?: string
  options?: string[]
  secret: boolean
  required: boolean
  section: string
  model_source?: string
  value?: string
  has_value?: boolean
  has_override?: boolean
}

export interface WizardSchemaSection {
  name: string
  label: string
  description: string
  fields: WizardSchemaField[]
  optional: boolean
  is_llm: boolean
}

export interface WizardSchemaResponse {
  sections: WizardSchemaSection[]
  encryption_enabled: boolean
}

export interface WizardCompleteResponse {
  status: string
  message: string
}

export interface ImportTOMLResponse {
  imported: number
  skipped: number
  status: 'ok' | 'partial'
  errors?: string[]
  sentinel_error?: string
}

export interface AgentJob {
  id: number
  name: string
  prompt: string
  model: string
  cron_expr: string
  interval: string
  delivery_chat_id: string
  enabled: boolean
  last_run?: string
  next_run?: string
  created_at: string
  updated_at: string
}

// Todo items (user-facing tasks)
export interface TodoProvider {
  id: string
  name: string
  available: boolean
  is_default: boolean
}

export interface TodoItem {
  id: number
  user_id: string
  provider: string
  external_id: string
  title: string
  notes: string
  due_date: string | null
  completed_at: string | null
  priority: number
  labels: string
  project_name: string
  url: string
  synced_at: string | null
  created_at: string
  updated_at: string
}

export interface CreateTodoRequest {
  title: string
  notes?: string
  due_date?: string
  priority?: number
  provider?: string
}

export interface TodoCountsResponse {
  today: number
  overdue: number
}

// --- Token management ---

const TOKEN_KEY = 'iulita_access_token'
const REFRESH_KEY = 'iulita_refresh_token'

export function getAccessToken(): string | null {
  return localStorage.getItem(TOKEN_KEY)
}

export function getRefreshToken(): string | null {
  return localStorage.getItem(REFRESH_KEY)
}

export function setTokens(access: string, refresh: string) {
  localStorage.setItem(TOKEN_KEY, access)
  localStorage.setItem(REFRESH_KEY, refresh)
}

const logoutCallbacks: (() => void)[] = []

export function onLogout(cb: () => void) {
  logoutCallbacks.push(cb)
}

export function clearTokens() {
  localStorage.removeItem(TOKEN_KEY)
  localStorage.removeItem(REFRESH_KEY)
  for (const cb of logoutCallbacks) cb()
}

export function isLoggedIn(): boolean {
  return !!getAccessToken()
}

// Parse JWT payload without verification (for reading role/username client-side).
export function parseToken(token: string): { user_id: string; username: string; role: string; exp: number } | null {
  try {
    const payload = token.split('.')[1]
    return JSON.parse(atob(payload))
  } catch {
    return null
  }
}

export function currentUser(): { user_id: string; username: string; role: string } | null {
  const token = getAccessToken()
  if (!token) return null
  const parsed = parseToken(token)
  if (!parsed) return null
  // Check expiry
  if (parsed.exp * 1000 < Date.now()) {
    clearTokens()
    return null
  }
  return { user_id: parsed.user_id, username: parsed.username, role: parsed.role }
}

export function isAdmin(): boolean {
  return currentUser()?.role === 'admin'
}

// --- HTTP helpers with auth ---

function authHeaders(): Record<string, string> {
  const token = getAccessToken()
  if (token) return { Authorization: `Bearer ${token}` }
  return {}
}

async function handleResponse<T>(res: Response): Promise<T> {
  if (res.status === 401) {
    // Try refresh
    const refreshed = await tryRefresh()
    if (!refreshed) {
      clearTokens()
      window.location.href = '/login'
      throw new Error('Session expired')
    }
    // Retry is handled by caller
    throw new Error('TOKEN_REFRESHED')
  }
  if (!res.ok) {
    const body = await res.text()
    // Try to extract "error" field from JSON response.
    try {
      const parsed = JSON.parse(body)
      if (parsed.error) throw new Error(parsed.error)
    } catch (e: any) {
      if (e.message && e.message !== body) throw e
    }
    throw new Error(`API error ${res.status}: ${body}`)
  }
  return res.json()
}

let refreshPromise: Promise<boolean> | null = null

async function tryRefresh(): Promise<boolean> {
  // Deduplicate concurrent refresh attempts
  if (refreshPromise) return refreshPromise
  refreshPromise = (async () => {
    const rt = getRefreshToken()
    if (!rt) return false
    try {
      const res = await fetch('/api/auth/refresh', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ refresh_token: rt }),
      })
      if (!res.ok) return false
      const data = await res.json()
      localStorage.setItem(TOKEN_KEY, data.access_token)
      return true
    } catch {
      return false
    } finally {
      refreshPromise = null
    }
  })()
  return refreshPromise
}

async function get<T>(url: string): Promise<T> {
  const res = await fetch(url, { headers: authHeaders() })
  try {
    return await handleResponse<T>(res)
  } catch (e: any) {
    if (e.message === 'TOKEN_REFRESHED') {
      const retry = await fetch(url, { headers: authHeaders() })
      return handleResponse<T>(retry)
    }
    throw e
  }
}

async function post<T>(url: string, body?: unknown): Promise<T> {
  const headers: Record<string, string> = { ...authHeaders() }
  if (body) headers['Content-Type'] = 'application/json'
  const res = await fetch(url, {
    method: 'POST',
    headers,
    body: body ? JSON.stringify(body) : undefined,
  })
  try {
    return await handleResponse<T>(res)
  } catch (e: any) {
    if (e.message === 'TOKEN_REFRESHED') {
      const retry = await fetch(url, { method: 'POST', headers: { ...authHeaders(), 'Content-Type': 'application/json' }, body: body ? JSON.stringify(body) : undefined })
      return handleResponse<T>(retry)
    }
    throw e
  }
}

async function put<T>(url: string, body: unknown): Promise<T> {
  const res = await fetch(url, {
    method: 'PUT',
    headers: { ...authHeaders(), 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  })
  try {
    return await handleResponse<T>(res)
  } catch (e: any) {
    if (e.message === 'TOKEN_REFRESHED') {
      const retry = await fetch(url, { method: 'PUT', headers: { ...authHeaders(), 'Content-Type': 'application/json' }, body: JSON.stringify(body) })
      return handleResponse<T>(retry)
    }
    throw e
  }
}

async function del<T>(url: string): Promise<T> {
  const res = await fetch(url, { method: 'DELETE', headers: authHeaders() })
  try {
    return await handleResponse<T>(res)
  } catch (e: any) {
    if (e.message === 'TOKEN_REFRESHED') {
      const retry = await fetch(url, { method: 'DELETE', headers: authHeaders() })
      return handleResponse<T>(retry)
    }
    throw e
  }
}

// Public (no auth) post for login/refresh
async function publicPost<T>(url: string, body: unknown): Promise<T> {
  const res = await fetch(url, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  })
  if (!res.ok) {
    const text = await res.text()
    throw new Error(`API error ${res.status}: ${text}`)
  }
  return res.json()
}

export const api = {
  // Auth
  login: (username: string, password: string) =>
    publicPost<LoginResponse>('/api/auth/login', { username, password }),
  refreshToken: (refresh_token: string) =>
    publicPost<{ access_token: string }>('/api/auth/refresh', { refresh_token }),
  changePassword: (old_password: string, new_password: string) =>
    post<{ status: string }>('/api/auth/change-password', { old_password, new_password }),
  getMe: () => get<UserInfo>('/api/auth/me'),

  // System
  getSystem: () => get<SystemInfo>('/api/system'),
  getStats: (chatId?: string) => get<Stats>(`/api/stats${chatId ? `?chat_id=${chatId}` : ''}`),
  getChats: () => get<ChatInfo[]>('/api/chats'),
  getFacts: (params?: { chat_id?: string; user_id?: string; q?: string; limit?: number }) => {
    const p = new URLSearchParams()
    if (params?.chat_id) p.set('chat_id', params.chat_id)
    if (params?.user_id) p.set('user_id', params.user_id)
    if (params?.q) p.set('q', params.q)
    if (params?.limit) p.set('limit', String(params.limit))
    const qs = p.toString()
    return get<Fact[]>(`/api/facts${qs ? `?${qs}` : ''}`)
  },
  getInsights: (params?: { chat_id?: string; limit?: number }) => {
    const p = new URLSearchParams()
    if (params?.chat_id) p.set('chat_id', params.chat_id)
    if (params?.limit) p.set('limit', String(params.limit))
    const qs = p.toString()
    return get<Insight[]>(`/api/insights${qs ? `?${qs}` : ''}`)
  },
  getReminders: (chatId?: string) => get<Reminder[]>(`/api/reminders${chatId ? `?chat_id=${chatId}` : ''}`),
  getDirective: (chatId: string) => get<Directive | { directive: null }>(`/api/directives?chat_id=${chatId}`),
  getMessages: (chatId: string, params?: { limit?: number; before_id?: number }) => {
    const p = new URLSearchParams({ chat_id: chatId })
    if (params?.limit) p.set('limit', String(params.limit))
    if (params?.before_id) p.set('before_id', String(params.before_id))
    return get<Message[]>(`/api/messages?${p}`)
  },
  getSkills: () => get<SkillInfo[]>('/api/skills'),
  toggleSkill: (name: string, enabled: boolean) =>
    put<{ status: string; skill: string; enabled: boolean }>(`/api/skills/${name}/toggle`, { enabled }),
  getSkillConfig: (name: string) =>
    get<SkillConfigResponse>(`/api/skills/${name}/config`),
  setSkillConfig: (name: string, key: string, value: string) =>
    put<{ status: string; key: string }>(`/api/skills/${name}/config/${key}`, { value }),
  getTechFacts: (chatId?: string) => get<TechFactGroup>(`/api/techfacts${chatId ? `?chat_id=${chatId}` : ''}`),

  updateFact: (id: number, content: string) =>
    put<Fact>(`/api/facts/${id}`, { content }),
  deleteFact: (id: number) =>
    del<{ status: string }>(`/api/facts/${id}`),

  // Scheduler
  getSchedulers: () => get<JobInfo[]>('/api/schedulers'),
  triggerJob: (name: string) => post<{ status: string }>(`/api/schedulers/${name}/trigger`),

  // Tasks
  getTasks: (params?: { status?: string; type?: string; limit?: number }) => {
    const p = new URLSearchParams()
    if (params?.status) p.set('status', params.status)
    if (params?.type) p.set('type', params.type)
    if (params?.limit) p.set('limit', String(params.limit))
    const qs = p.toString()
    return get<Task[]>(`/api/tasks${qs ? `?${qs}` : ''}`)
  },
  getTaskCounts: () => get<TaskCounts>('/api/tasks/counts'),

  // Config (admin)
  getConfigDebug: () => get<ConfigDebugResponse>('/api/config/debug'),
  getConfig: () => get<ConfigListResponse>('/api/config/'),
  getConfigDecrypted: () => get<ConfigListResponse>('/api/config/decrypted'),
  getConfigSchema: () => get<ConfigSchemaResponse>('/api/config/schema'),
  getModels: (provider: string) => get<ModelsResponse>(`/api/config/models/${provider}`),
  setConfig: (key: string, value: string, encrypt: boolean) =>
    put<{ status: string; key: string }>(`/api/config/${key}`, { value, encrypt }),
  deleteConfig: (key: string) =>
    del<{ status: string; key: string }>(`/api/config/${key}`),

  // Users (admin)
  listUsers: () => get<UserInfo[]>('/api/users/'),
  getUser: (id: string) => get<UserInfo>(`/api/users/${id}`),
  createUser: (data: { username: string; password: string; role?: string; display_name?: string; timezone?: string }) =>
    post<UserInfo>('/api/users/', data),
  updateUser: (id: string, data: { username?: string; password?: string; role?: string; display_name?: string; timezone?: string }) =>
    put<UserInfo>(`/api/users/${id}`, data),
  deleteUser: (id: string) => del<{ status: string }>(`/api/users/${id}`),
  listUserChannels: (id: string) => get<UserChannel[]>(`/api/users/${id}/channels`),
  bindChannel: (id: string, data: { channel_type: string; channel_id: string; channel_user_id: string; channel_username?: string }) =>
    post<UserChannel>(`/api/users/${id}/channels`, data),
  unbindChannel: (userId: string, channelId: number) =>
    del<{ status: string }>(`/api/users/${userId}/channels/${channelId}`),

  // Channel instances (admin — communication bots/integrations)
  listChannelInstances: () => get<ChannelInstance[]>('/api/channels/'),
  getChannelInstance: (id: string) => get<ChannelInstance>(`/api/channels/${id}`),
  createChannelInstance: (data: { id: string; type: string; name: string; config?: string }) =>
    post<ChannelInstance>('/api/channels/', data),
  updateChannelInstance: (id: string, data: { name?: string; config?: string; enabled?: boolean }) =>
    put<ChannelInstance>(`/api/channels/${id}`, data),
  deleteChannelInstance: (id: string) => del<{ status: string }>(`/api/channels/${id}`),
  listChannelBindings: (id: string) => get<ChannelBinding[]>(`/api/channels/${id}/bindings`),

  // Agent jobs (admin)
  listAgentJobs: () => get<AgentJob[]>('/api/agent-jobs/'),
  getAgentJob: (id: number) => get<AgentJob>(`/api/agent-jobs/${id}`),
  createAgentJob: (data: { name: string; prompt: string; model?: string; cron_expr?: string; interval?: string; delivery_chat_id?: string; enabled?: boolean }) =>
    post<AgentJob>('/api/agent-jobs/', data),
  updateAgentJob: (id: number, data: { name?: string; prompt?: string; model?: string; cron_expr?: string; interval?: string; delivery_chat_id?: string; enabled?: boolean }) =>
    put<AgentJob>(`/api/agent-jobs/${id}`, data),
  deleteAgentJob: (id: number) => del<{ status: string }>(`/api/agent-jobs/${id}`),

  // Wizard (admin)
  getWizardStatus: () => get<WizardStatus>('/api/wizard/status'),
  getWizardSchema: () => get<WizardSchemaResponse>('/api/wizard/schema'),
  completeWizard: () => post<WizardCompleteResponse>('/api/wizard/complete', {}),
  importTOML: () => post<ImportTOMLResponse>('/api/wizard/import-toml', {}),

  // External skills (marketplace)
  listExternalSkills: () => get<InstalledSkill[]>('/api/skills/external/'),
  getExternalSkill: (slug: string) => get<InstalledSkill>(`/api/skills/external/${slug}`),
  getMarketplaceSkillDetail: (slug: string) => get<ExternalSkillDetail>(`/api/skills/external/marketplace/${slug}`),
  installExternalSkill: (ref: string, source?: string) =>
    post<{ skill: InstalledSkill; warnings: string[] }>('/api/skills/external/install', { source: source || 'clawhub', ref }),
  uninstallExternalSkill: (slug: string) =>
    del<{ status: string; slug: string }>(`/api/skills/external/${slug}`),
  searchExternalSkills: (query: string, source?: string, limit?: number) =>
    post<{ results: ExternalSkillResult[]; count: number }>('/api/skills/external/search', { source: source || 'clawhub', query, limit: limit || 20 }),
  enableExternalSkill: (slug: string) =>
    put<{ status: string; slug: string; enabled: boolean }>(`/api/skills/external/${slug}/enable`, {}),
  disableExternalSkill: (slug: string) =>
    put<{ status: string; slug: string; enabled: boolean }>(`/api/skills/external/${slug}/disable`, {}),
  updateExternalSkill: (slug: string) =>
    post<{ skill: InstalledSkill; warnings: string[] }>(`/api/skills/external/${slug}/update`),

  // Google
  getGoogleStatus: () => get<GoogleCredentialStatus>('/api/google/status'),
  uploadGoogleCredentials: async (file: File) => {
    const form = new FormData()
    form.append('file', file)
    const res = await fetch('/api/google/upload-credentials', {
      method: 'POST',
      headers: authHeaders(),
      body: form,
    })
    try {
      return await handleResponse<{ status: string; credential_type: string; file_path: string; filename: string }>(res)
    } catch (e: any) {
      if (e.message === 'TOKEN_REFRESHED') {
        const retry = await fetch('/api/google/upload-credentials', {
          method: 'POST',
          headers: authHeaders(),
          body: form,
        })
        return handleResponse<{ status: string; credential_type: string; file_path: string; filename: string }>(retry)
      }
      throw e
    }
  },
  getGoogleAuthURL: (accountAlias?: string) => {
    const p = new URLSearchParams()
    if (accountAlias) p.set('account_alias', accountAlias)
    const qs = p.toString()
    return get<{ url: string }>(`/api/google/auth${qs ? `?${qs}` : ''}`)
  },
  listGoogleAccounts: () => get<GoogleAccount[]>('/api/google/accounts'),
  deleteGoogleAccount: (id: number) => del<{ status: string }>(`/api/google/accounts/${id}`),
  updateGoogleAccount: (id: number, data: { account_alias?: string; is_default?: boolean }) =>
    put<{ status: string }>(`/api/google/accounts/${id}`, data),

  // Todo items (user-facing tasks)
  getTodoProviders: () => get<{ providers: TodoProvider[]; default_provider: string }>('/api/todos/providers'),
  getTodosToday: () => get<{ items: TodoItem[]; count: number }>('/api/todos/today'),
  getTodosOverdue: () => get<{ items: TodoItem[]; count: number }>('/api/todos/overdue'),
  getTodosUpcoming: (days?: number) => get<{ items: TodoItem[]; count: number }>(`/api/todos/upcoming${days ? `?days=${days}` : ''}`),
  getTodosAll: (limit?: number) => get<{ items: TodoItem[]; count: number }>(`/api/todos/all${limit ? `?limit=${limit}` : ''}`),
  getTodoCounts: () => get<TodoCountsResponse>('/api/todos/counts'),
  createTodo: (data: CreateTodoRequest) => post<TodoItem>('/api/todos/', data),
  completeTodo: (id: number) => post<{ status: string; id: number }>(`/api/todos/${id}/complete`),
  deleteTodo: (id: number) => del<{ status: string; id: number }>(`/api/todos/${id}`),
  triggerTodoSync: () => post<{ status: string }>('/api/todos/sync'),
  setDefaultTodoProvider: (provider: string) => put<{ status: string }>('/api/todos/default-provider', { provider }),
}
