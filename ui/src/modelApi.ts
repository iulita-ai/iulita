import { getAccessToken } from './api'

export interface ModelProfile {
  id: string; name: string; connection: string; model: string
  max_output_tokens: number; thinking: 'enabled' | 'disabled'; reasoning_effort?: string; clear_thinking: boolean
}
export interface ModelSettings {
  schema_version: number; profiles: ModelProfile[]
  policy: {
    everyday: string; complex: string; vision: string; background: string
    classifier: { enabled: boolean; profile?: string }
    legacy_hints?: Record<string, string>; legacy_profile_hints?: Record<string, string>
    fallbacks?: Record<string, string[]>; forbidden_providers?: string[]
  }
}
export interface ModelDefinition {
  provider: string; model: string; name: string; context_tokens: number; max_output_tokens: number
  images: boolean; tools: boolean; streaming: boolean; thinking_required: boolean; source_url: string; catalog_version: string
}
export interface ModelConnection {
  provider: string; endpoint: string; generation: string; source: string; credential_set: boolean; availability: string
}
export interface ConnectionMutation { provider: string; endpoint?: string; api_key_action: 'keep' | 'replace'; api_key?: string }
export interface ModelEvidence {
  kind: string; passed: boolean; checked_at: string; requested_model: string; served_model: string; error_code?: string
}
export interface ProfileEvidence { id: string; eligibility: string; fingerprint: string; evidence: ModelEvidence[] }
export interface ModelStage {
  id: string; base_revision: number; config_hash: string; created_at: string; expires_at: string
  settings: ModelSettings; connections: ModelConnection[]; effective_profiles: ProfileEvidence[]
}
export interface ModelSettingsView {
  revision: number; active_revision: number; activation_status?: string; settings: ModelSettings; connections: ModelConnection[]
  effective_profiles: ProfileEvidence[]; legacy_settings?: ModelSettings; legacy_classifier_active?: boolean; stage?: ModelStage; health: string; affected_roles: string[]; encryption_available: boolean
}
export interface ModelProbe {
  id: string; profile_id: string; stage_id?: string; kind: string; status: string
  started_at: string; deadline: string; result?: ModelEvidence; error_code?: string
}
export interface ModelHistoryEntry { revision: number; settings: ModelSettings; created_at: string }
export interface ProbeRequest { expected_revision: number; stage_id?: string; profile_id: string; kind: string; idempotency_key: string }
export interface ModelFieldError { path: string; code: string; message: string }
export class ModelAPIError extends Error {
  constructor(public code: string, public status: number, public fields: ModelFieldError[] = []) { super(code) }
}

// Do not replay paid actions after a lost response or authentication change.
// The page retains the original operation key and offers an explicit retry.
async function request<T>(path: string, method = 'GET', body?: unknown): Promise<T> {
  const token = getAccessToken()
  const res = await fetch(`/api/models${path}`, {
    method, headers: { ...(token ? { Authorization: `Bearer ${token}` } : {}), ...(body !== undefined ? { 'Content-Type': 'application/json' } : {}) },
    body: body !== undefined ? JSON.stringify(body) : undefined,
  })
  if (!res.ok) {
    let data: { code?: string; field_errors?: ModelFieldError[] } = {}
    try { data = await res.json() } catch { /* Never display an upstream body. */ }
    throw new ModelAPIError(data.code || (res.status === 401 ? 'unauthenticated' : 'request_failed'), res.status, data.field_errors || [])
  }
  if (res.status === 204) return {} as T
  return res.json()
}
export const modelsApi = {
  history: () => request<{ history: ModelHistoryEntry[] }>('/history'),
  restore: (revision: number, expected_revision: number) => request<ModelStage>(`/history/${revision}/stage`, 'POST', { expected_revision }),
  settings: () => request<ModelSettingsView>('/settings'),
  catalog: async () => {
    const data = await request<ModelDefinition[] | { models?: ModelDefinition[]; catalog?: ModelDefinition[] }>('/catalog')
    return Array.isArray(data) ? data : (data.models || data.catalog || [])
  },
  validate: (settings: ModelSettings, connections: ConnectionMutation[]) => request<{ field_errors?: ModelFieldError[] }>('/settings/validate', 'POST', { settings, connections }),
  stage: (expected_revision: number, settings: ModelSettings, connections: ConnectionMutation[]) => request<ModelStage>('/settings/stage', 'POST', { expected_revision, settings, connections }),
  activate: (expected_revision: number, stage_id: string) => request<ModelSettingsView>('/settings', 'PUT', { expected_revision, stage_id }),
  discard: (id: string, expected_revision: number) => request<void>(`/settings/stage/${encodeURIComponent(id)}?expected_revision=${expected_revision}`, 'DELETE'),
  probe: (requestBody: ProbeRequest) => request<ModelProbe>(`/profiles/${encodeURIComponent(requestBody.profile_id)}/probe`, 'POST', requestBody),
  probeStatus: (id: string) => request<ModelProbe>(`/probes/${encodeURIComponent(id)}`),
  cancelProbe: (id: string) => request<ModelProbe>(`/probes/${encodeURIComponent(id)}/cancel`, 'POST', {}),
  revoke: (provider: string, expected_revision: number, generation: string) => request<ModelSettingsView>(`/connections/${encodeURIComponent(provider)}/revoke`, 'POST', { expected_revision, generation }),
}
