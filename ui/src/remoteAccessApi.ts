import { getAccessToken } from './api'

export interface SSHServer { id: string; label: string; address: string; port: number; enabled: boolean; host_keys: string[]; trust_source: string; revision?: number }
export interface SSHIdentity { id: string; username: string; auth_type: string; enabled: boolean; revision?: number; public_key?: string; public_fingerprint?: string }
export interface SSHHop { server_id: string; identity_id: string }
export interface SSHRoute { id: string; label: string; hops: SSHHop[]; enabled: boolean; revision?: number }
export interface SSHGrant { id: string; caller_id: string; route_id: string; capability: string; route_revision: number; enabled: boolean; expires_at?: string; revision?: number }
export interface SSHExecution { id: string; route_id?: string; state: string; plan_hash?: string; command?: string; created_at?: string; exit_code?: number; error_code?: string; artifact_id?: string; stdout_bytes?: number; stderr_bytes?: number; truncated?: boolean }
export interface RemoteSnapshot { enabled: boolean; servers: SSHServer[]; identities: SSHIdentity[]; routes: SSHRoute[]; grants: SSHGrant[]; executions: SSHExecution[]; installations?: SSHInstallation[]; runbooks?: any[]; jobs?: any[] }
export interface SSHKeyOperation {id:string;action:string;state:string;error_code?:string;installation_id?:string;created_at:string}
export interface SSHInstallation {id:string;server_id:string;identity_id:string;fingerprint:string;state:string}
export interface SSHApproval { execution_id:string; approval_id: string; plan_hash: string; command: string; servers: SSHServer[]; identities: SSHIdentity[]; expires_at: string }
export type RemoteCollection = 'servers' | 'identities' | 'routes' | 'grants'

// SSH mutations must never replay automatically after a timeout or authentication failure.
export async function remoteRequest<T>(path: string, method = 'GET', body?: unknown): Promise<T> {
  const token = getAccessToken()
  const res = await fetch(`/api/remote-access${path}`, {
    method, headers: { ...(token ? { Authorization: `Bearer ${token}` } : {}), ...(body === undefined ? {} : { 'Content-Type': 'application/json' }) },
    body: body === undefined ? undefined : JSON.stringify(body),
  })
  if (!res.ok) throw new Error(`Remote access request failed (${res.status})`)
  if (res.status === 204) return undefined as T
  return res.json()
}
export const remoteAccessApi = {
  snapshot: async () => { const raw = await remoteRequest<any>(''); return { ...raw, servers: Object.values(raw.servers || {}), identities: Object.values(raw.identities || {}), routes: Object.values(raw.routes || {}), grants: Object.values(raw.grants || {}), executions: Object.values(raw.executions || {}), runbooks:Object.values(raw.runbooks||{}),jobs:Object.values(raw.jobs||{}), installations: Object.values(raw.installations || {}) } as RemoteSnapshot },
  enabled: (enabled: boolean) => remoteRequest('/enabled', 'POST', { enabled }),
  save: (kind: RemoteCollection, data: unknown, id?: string) => remoteRequest(`/${kind}${id ? `/${encodeURIComponent(id)}` : ''}`, id ? 'PUT' : 'POST', data),
  keyOperations: () => remoteRequest<SSHKeyOperation[]>('/key-operations'),
  approvals: () => remoteRequest<SSHApproval[]>('/approvals'),
  decide: (id:string, plan_hash:string, approved:boolean) => remoteRequest(`/approvals/${encodeURIComponent(id)}`, 'POST', {plan_hash,approved}),
  keyOperation: (data:unknown) => remoteRequest('/key-operations','POST',data),
  generate: (username: string) => remoteRequest<SSHIdentity>('/identities/generate', 'POST', {username,enabled:true}),
  preview: (data: unknown) => remoteRequest<SSHApproval>('/executions/preview', 'POST', data),
  execute: (data: unknown) => remoteRequest<SSHExecution>('/executions', 'POST', data),
  cancel: (id: string) => remoteRequest(`/executions/${encodeURIComponent(id)}/cancel`, 'POST', {}),
  artifact: (id: string) => remoteRequest<{ stdout: string; stderr: string }>(`/artifacts/${encodeURIComponent(id)}`),
}
