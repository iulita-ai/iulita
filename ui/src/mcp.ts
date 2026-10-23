export interface MCPItem {
  id: string;
  kind: "tool" | "resource" | "resource_template" | "prompt";
  name: string;
  title?: string;
  description?: string;
  uri?: string;
  revision: string;
  descriptor: unknown;
  effect: "read" | "write" | "unknown";
  allowed: boolean;
  error_code?: string;
}
export interface MCPConnection {
  id: string;
  label: string;
  data_domain: string;
  endpoint?: string;
  transport: string;
  profile?: string;
  desired_state: string;
  runtime_state: string;
  auth_mode: string;
  auth_state: string;
  revision: number;
  generation: number;
  checked_at: string;
  error_code?: string;
  result_mode: string;
  items: MCPItem[] | null;
}
export interface MCPExecution {
  id: string;
  connection_id: string;
  item_id: string;
  state: string;
  error_code?: string;
  side_effect_state: string;
  created_at: string;
  artifact_id?: string;
}
export interface MCPPlan {
  id: string;
  hash: string;
  preview: string;
  expires_at: string;
  call: { connection_id: string; item_id: string; arguments: unknown };
}
export interface MCPSnapshot {
  stdio_enabled?: boolean;
  enabled: boolean;
  connections: MCPConnection[];
  executions: MCPExecution[];
}
