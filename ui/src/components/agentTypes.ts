export interface AgentInfo {
  id: string
  type: string
  status: 'running' | 'completed' | 'failed'
  turn?: number
  error?: string
  durationMs?: number
  tokens?: number
}
