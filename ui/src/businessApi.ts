import { clearTokens, getAccessToken } from './api'

export type BusinessMode = 'off' | 'draft_only' | 'confirmed_send' | 'auto'
export interface BusinessCapability { method: string; right: string; supported: boolean; enabled: boolean; reason_code?: string }
export interface BusinessAutoRules { classes: string[]; timezone: string; start_hour: number; end_hour: number; max_replies_per_day: number }
export const defaultBusinessAutoRules = (): BusinessAutoRules => ({ classes: ['greeting'], timezone: 'UTC', start_hour: 9, end_hour: 18, max_replies_per_day: 5 })
export function copyBusinessAutoRules(rules?: BusinessAutoRules): BusinessAutoRules { return rules ? { ...rules, classes: [...rules.classes] } : defaultBusinessAutoRules() }
export function validBusinessAutoRules(rules: BusinessAutoRules): boolean {
  if (!rules.classes.length || new Set(rules.classes).size !== rules.classes.length || rules.classes.some(value => !['greeting','acknowledgement','short_question'].includes(value)) || !Number.isInteger(rules.start_hour) || rules.start_hour < 0 || rules.start_hour > 23 || !Number.isInteger(rules.end_hour) || rules.end_hour < 0 || rules.end_hour > 24 || rules.start_hour === rules.end_hour || !Number.isInteger(rules.max_replies_per_day) || rules.max_replies_per_day < 1 || rules.max_replies_per_day > 10) return false
  try { new Intl.DateTimeFormat('en', {timeZone:rules.timezone}).format(); return true } catch { return false }
}
export interface BusinessBudget { remaining_usd?: number; daily_usd?: number; input_tokens?: number; output_tokens?: number; exhausted?: boolean }
export interface BusinessDraft {
  id: string; revision: number; connection_id: string; peer_id: string; text: string; state: string
  account_label?: string; recipient_label?: string; recipient_ref?: string; expires_at?: string
  approval_id?: string; digest?: string; operation_id?: string; reason_code?: string
}
export interface BusinessConnection {
  id: string; revision: number; account_label: string; account_ref: string; bot_username?: string; bot_ref?: string
  state: string; is_enabled: boolean; storage_ready?: boolean; rights: Record<string, boolean>; allowed_methods?: string[]; mode: BusinessMode; processing_consent: boolean; token_only?: boolean; auto_reply?: boolean; auto_rules?: BusinessAutoRules; media_consent?: boolean; media_provider?: string; media_available?: boolean; requires_token_only?: boolean
  provider_available?: boolean; provider_label?: string; policy_version?: string; retention_days?: number
  verified_at?: string; error_code?: string; paused?: boolean; budget?: BusinessBudget; actions?: BusinessAction[]; drafts?: BusinessDraft[]
}
export interface BusinessPeer {
  id: string; revision: number; label: string; recipient_ref: string; mode?: BusinessMode; paused?: boolean
  processing_consent?: boolean; token_only?: boolean; auto_reply?: boolean; auto_rules?: BusinessAutoRules; media_consent?: boolean; media_provider?: string; media_available?: boolean; allowed_methods?: string[]; actions?: BusinessAction[]; context_revision?: number; last_processing_state?: string; last_processing_reason?: string; last_processed_at?: string; reply_until?: string; reply_available?: boolean; last_message_at?: string
}
export interface BusinessMessage { id: string; revision?: number; direction: string; text: string; created_at: string; deleted?: boolean; extracted_text?: string; extracted_provider?: string }
export interface BusinessOverview { connections: BusinessConnection[]; live_writes_enabled: boolean; capabilities: BusinessCapability[] }
export interface BusinessPeers { peers: BusinessPeer[]; drafts?: BusinessDraft[] }
export interface BusinessMessages { messages: BusinessMessage[]; drafts?: BusinessDraft[]; context_revision?: number; context?: BusinessContext; media_omitted?: number }
export interface BusinessAction { id: string; method: string; params: Record<string, unknown>; digest: string; state: string; required_steps: number; approval_steps: number; expires_at: string; receipt?: { message_ids?: number[]; story_id?: number; result?: unknown } }
export interface BusinessContext { changed: boolean; before_bytes: number; after_bytes: number; messages: number }
export interface BusinessPolicy { expected_revision: number; mode: BusinessMode; processing_consent: boolean; token_only?: boolean; auto_reply?: boolean; auto_rules?: BusinessAutoRules; media_consent?: boolean; media_provider?: string; media_available?: boolean; policy_version?: string; allowed_methods?: string[] }

const safeCodes = new Set(['unauthorized', 'forbidden', 'not_found', 'revision_conflict', 'expired', 'disabled', 'unsupported', 'unavailable', 'invalid_request', 'budget_exhausted', 'rate_limited', 'unknown_outcome', 'reply_window_closed'])
export class BusinessApiError extends Error {
  constructor(public status: number, public code: string) { super(code); this.name = 'BusinessApiError' }
}
const part = (value: string) => encodeURIComponent(value)
const connectionPath = (id: string) => `/connections/${part(id)}`
const peerPath = (id: string, peer: string) => `${connectionPath(id)}/peers/${part(peer)}`

// Account mutations are never replayed after refresh, timeout, network loss or 5xx.
// The server owns action admission and revision/approval validation independently.
export async function businessRequest<T>(path: string, method = 'GET', body?: unknown, signal?: AbortSignal): Promise<T> {
  const response = await businessFetch(path, method, body, signal)
  if (response.status === 204) return undefined as T
  try { return await response.json() }
  catch { throw new BusinessApiError(response.status, method === 'GET' ? 'unavailable' : 'unknown_outcome') }
}
async function businessFetch(path: string, method: string, body?: unknown, signal?: AbortSignal): Promise<Response> {
  const token = getAccessToken()
  if (!token) throw new BusinessApiError(401, 'unauthorized')
  let response: Response
  try {
    response = await fetch(`/api/telegram-business${path}`, {
      method, signal, cache: 'no-store', credentials: 'same-origin',
      headers: { Authorization: `Bearer ${token}`, ...(body === undefined ? {} : { 'Content-Type': 'application/json' }) },
      body: body === undefined ? undefined : JSON.stringify(body),
    })
  } catch (error) {
    if (signal?.aborted) throw error
    throw new BusinessApiError(0, method === 'GET' ? 'unavailable' : 'unknown_outcome')
  }
  if (response.status === 401 && getAccessToken() === token) clearTokens()
  if (!response.ok) {
    let code = ({ 401: 'unauthorized', 403: 'forbidden', 404: 'not_found', 409: 'revision_conflict', 410: 'expired', 429: 'rate_limited' } as Record<number, string>)[response.status] || 'unavailable'
    try {
      const data = await response.json()
      const candidate = data?.code || data?.error_code
      if (typeof candidate === 'string' && safeCodes.has(candidate)) code = candidate
    } catch { /* Never expose raw Telegram/provider/server response bodies. */ }
    throw new BusinessApiError(response.status, code)
  }
  return response
}

export const businessApi = {
  connections: (signal?: AbortSignal) => businessRequest<BusinessOverview>('/connections', 'GET', undefined, signal),
  peers: (id: string, signal?: AbortSignal) => businessRequest<BusinessPeers>(`${connectionPath(id)}/peers`, 'GET', undefined, signal),
  messages: (id: string, peer: string, signal?: AbortSignal) => businessRequest<BusinessMessages>(`${peerPath(id, peer)}/messages?limit=20`, 'GET', undefined, signal),
  policy: (id: string, policy: BusinessPolicy) => businessRequest<{ connection: BusinessConnection }>(`${connectionPath(id)}/policy`, 'PUT', policy),
  peerPolicy: (id: string, peer: string, policy: BusinessPolicy) => businessRequest<{ peer: BusinessPeer }>(`${peerPath(id, peer)}/policy`, 'PUT', policy),
  rejectDraft: (draft: BusinessDraft) => businessRequest<{ rejected: boolean }>(`/drafts/${part(draft.id)}/reject`, 'POST', { expected_revision: draft.revision }),
  cancelAction: (id: string, peer: string | undefined, action: BusinessAction) => businessRequest<{ action: BusinessAction }>(`${peer ? peerPath(id, peer) : connectionPath(id)}/actions/${part(action.id)}/cancel`, 'POST', { digest: action.digest }),
  compact: (id: string, peer: string, revision: number) => businessRequest<{ context: BusinessContext }>(`${peerPath(id, peer)}/compact`, 'POST', { expected_revision: revision }),
  createDraft: (id: string, peer: string, revision: number, text: string, generate = false) => businessRequest<{ draft: BusinessDraft }>(`${peerPath(id, peer)}/drafts`, 'POST', { expected_revision: revision, text, generate }),
  editDraft: (id: string, revision: number, text: string) => businessRequest<{ draft: BusinessDraft }>(`/drafts/${part(id)}`, 'PUT', { expected_revision: revision, text }),
  approveDraft: (draft: BusinessDraft) => businessRequest<{ draft: BusinessDraft }>(`/drafts/${part(draft.id)}/approve`, 'POST', { expected_revision: draft.revision, ...(draft.digest ? { expected_digest: draft.digest } : {}) }),
  executeDraft: (draft: BusinessDraft) => businessRequest<{ draft: BusinessDraft }>(`/drafts/${part(draft.id)}/execute`, 'POST', { expected_revision: draft.revision, ...(draft.approval_id ? { approval_id: draft.approval_id } : {}) }),
  pause: (id: string, revision: number, paused: boolean, peer?: string) => businessRequest<{ connection?: BusinessConnection; peer?: BusinessPeer }>(`${peer ? peerPath(id, peer) : connectionPath(id)}/pause`, 'POST', { expected_revision: revision, paused }),
  listActions: (id: string, peer?: string) => businessRequest<{ actions: BusinessAction[] }>(`${peer ? peerPath(id, peer) : connectionPath(id)}/actions?limit=20`),
  prepareAction: (id: string, peer: string | undefined, revision: number, method: string, params: Record<string, unknown>) => businessRequest<{ action: BusinessAction }>(`${peer ? peerPath(id, peer) : connectionPath(id)}/actions`, 'POST', { expected_revision: revision, method, params }),
  getAction: (id: string, peer: string | undefined, action: string) => businessRequest<{ action: BusinessAction }>(`${peer ? peerPath(id, peer) : connectionPath(id)}/actions/${part(action)}`),
  approveAction: (id: string, peer: string | undefined, action: BusinessAction) => businessRequest<{ action: BusinessAction }>(`${peer ? peerPath(id, peer) : connectionPath(id)}/actions/${part(action.id)}/approve`, 'POST', { digest: action.digest, step: action.approval_steps + 1 }),
  executeAction: (id: string, peer: string | undefined, action: BusinessAction) => businessRequest<{ action: BusinessAction }>(`${peer ? peerPath(id, peer) : connectionPath(id)}/actions/${part(action.id)}/execute`, 'POST', { digest: action.digest }),
  uploadMedia: (id: string, peer: string | undefined, filename: string, mime_type: string, data: string) => businessRequest<{ media_ref: string }>(`${peer ? peerPath(id, peer) : connectionPath(id)}/media`, 'POST', { filename, mime_type, data }),
  rebind: (id: string, revision: number) => businessRequest<{ connection: BusinessConnection }>(`${connectionPath(id)}/rebind`, 'POST', { expected_revision: revision }),
  purge: (id: string, revision: number, peer?: string) => businessRequest<{ purged: boolean }>(`${peer ? peerPath(id, peer) : connectionPath(id)}/purge`, 'POST', { expected_revision: revision }),
  export: async (id: string, peer?: string): Promise<Blob> => {
    const response = await businessFetch(`${peer ? peerPath(id, peer) : connectionPath(id)}/export`, 'GET')
    if (Number(response.headers.get('content-length')) > 10 * 1024 * 1024) throw new BusinessApiError(413, 'invalid_request')
    const blob = await response.blob()
    if (blob.size > 10 * 1024 * 1024 || !response.headers.get('content-type')?.includes('application/json')) throw new BusinessApiError(413, 'invalid_request')
    return blob
  },
}
