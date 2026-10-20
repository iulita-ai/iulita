import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import RemoteAdvanced from './RemoteAdvanced.vue'
import { remoteRequest } from '../remoteAccessApi'

const terminals = vi.hoisted(() => [] as any[])
vi.mock('../remoteAccessApi', () => ({ remoteRequest: vi.fn() }))
vi.mock('@xterm/xterm', () => ({ Terminal: class {
  parser = { registerOscHandler: vi.fn() }
  write = vi.fn(); dispose = vi.fn(); focus = vi.fn()
  dataHandler = (_: string) => {}; resizeHandler = (_: any) => {}
  constructor() { terminals.push(this) }
  loadAddon() {} open() {}
  onData(handler: (data: string) => void) { this.dataHandler = handler }
  onResize(handler: (size: any) => void) { this.resizeHandler = handler }
} }))
vi.mock('@xterm/addon-fit', () => ({ FitAddon: class { fit() {} } }))
class FakeSocket {
  static OPEN = 1
  static instances: FakeSocket[] = []
  readyState = 1
  onmessage: ((e: {data: string}) => void) | null = null
  onclose: (() => void) | null = null
  onerror: (() => void) | null = null
  send = vi.fn(); close = vi.fn()
  constructor(public url: URL) { FakeSocket.instances.push(this) }
  message(frame: unknown) { this.onmessage?.({ data: JSON.stringify(frame) }) }
}
const snapshot = { enabled: true, servers: [], identities: [], grants: [], executions: [], routes: [{ id: 'r', label: 'Target', enabled: true, hops: [] }], runbooks: [{ id: 'b', label: 'Health', revision: 4, route_id: 'r', command: 'systemctl status {{service}}', enabled: true, parameters: {service: {values: ['nginx']}} }], jobs: [] }
const approval = { approval_id: 'single-use-nonce', plan_hash: 'frozen-hash', command: 'exact approved action', servers: [{ address: 'host', port: 22 }], identities: [{ username: 'deploy' }] }
beforeEach(() => {
  vi.clearAllMocks(); terminals.length = 0; FakeSocket.instances = []
  vi.stubGlobal('WebSocket', FakeSocket)
  vi.stubGlobal('ResizeObserver', class { observe() {} disconnect() {} })
  vi.mocked(remoteRequest).mockImplementation(async (path: string) => path.endsWith('/preview') ? approval : path === '/terminals/ticket' ? { ticket: 'one-time-ticket' } : {})
})
function render() { return mount(RemoteAdvanced, { props: { snapshot: structuredClone(snapshot) as any } }) }
function button(w: ReturnType<typeof render>, text: string) { return w.findAll('button').find(b => b.text() === text)! }
async function previewTerminal(w: ReturnType<typeof render>) { const form = w.findAll('form')[4]!; await form.find('select').setValue('r'); await form.trigger('submit'); await flushPromises() }
async function approveTerminal(w: ReturnType<typeof render>) { await previewTerminal(w); await button(w, 'Allow once').trigger('click'); await flushPromises() }

describe('Advanced remote access consent and human terminal', () => {
  it('previews SFTP only and executes the frozen path with one nonce/hash without retry', async () => {
    const w = render(); const form = w.findAll('form')[2]!
    await form.find('select').setValue('r'); const inputs = form.findAll('input'); await inputs[0]!.setValue('/home/deploy/private'); await inputs[1]!.setValue('report.txt')
    await form.trigger('submit'); await flushPromises()
    const expected = { route_id: 'r', operation: 'file_get', file: {root: '/home/deploy/private', path: 'report.txt', sha256: ''}, timeout_seconds: 60 }
    expect(remoteRequest).toHaveBeenCalledExactlyOnceWith('/operations/preview', 'POST', expected)
    await inputs[1]!.setValue('changed-after-preview.txt')
    vi.mocked(remoteRequest).mockRejectedValueOnce(new Error('connection lost'))
    await button(w, 'Allow once').trigger('click'); await flushPromises()
    expect(remoteRequest).toHaveBeenLastCalledWith('/operations', 'POST', {...expected, approval_id: 'single-use-nonce', expected_plan_hash: 'frozen-hash'})
    expect(remoteRequest).toHaveBeenCalledTimes(2); expect(w.text()).toContain('No automatic retry'); w.unmount()
  })
  it('sends exact version-independent runbook invocation and schedule payloads', async () => {
    const w = render(); const form = w.findAll('form')[1]!
    await form.find('select').setValue('b'); await form.find('textarea').setValue('{"service":"nginx"}'); await form.trigger('submit'); await flushPromises()
    const request = {runbook_id: 'b', parameters: {service: 'nginx'}, timeout_seconds: 60}
    expect(remoteRequest).toHaveBeenLastCalledWith('/runbooks/execute', 'POST', request)
    await form.find('input').setValue('1h'); await button(w, 'Create schedule').trigger('click'); await flushPromises()
    expect(remoteRequest).toHaveBeenLastCalledWith('/schedules', 'POST', {name: 'Health', interval: '1h', request}); w.unmount()
  })
  it('requires explicit PTY consent and uses a single-use ticket without bearer tokens', async () => {
    localStorage.setItem('access_token', 'secret-bearer-token')
    const w = render(); await previewTerminal(w)
    expect(FakeSocket.instances).toHaveLength(0); expect(remoteRequest).not.toHaveBeenCalledWith('/terminals/ticket', expect.anything(), expect.anything())
    await button(w, 'Allow once').trigger('click'); await flushPromises()
    expect(remoteRequest).toHaveBeenLastCalledWith('/terminals/ticket', 'POST', {route_id: 'r', options: {rows: 24, cols: 80, term: 'xterm-256color', timeout_seconds: 3600}, approval_id: 'single-use-nonce', expected_plan_hash: 'frozen-hash'})
    expect(FakeSocket.instances).toHaveLength(1)
    const url = FakeSocket.instances[0]!.url
    expect(url.pathname).toBe('/api/remote-access/terminals/connect/one-time-ticket'); expect(url.search).toBe(''); expect(url.toString()).not.toContain('secret-bearer-token')
    const t = terminals[0]; expect(t.parser.registerOscHandler.mock.calls.map((x: any[]) => x[0])).toEqual([52, 8]); for (const [, handler] of t.parser.registerOscHandler.mock.calls) expect(handler()).toBe(true)
    w.unmount(); localStorage.clear()
  })
  it('ignores old terminal output and callbacks after disconnect and a separately approved reopen', async () => {
    const w = render(); await approveTerminal(w); const old = FakeSocket.instances[0]!
    old.message({kind: 'ready', execution_id: 'old'}); await flushPromises()
    const staleMessage = old.onmessage!, staleClose = old.onclose!
    await button(w, 'Disconnect terminal').trigger('click'); await flushPromises(); expect(old.close).toHaveBeenCalledOnce()
    await approveTerminal(w); const current = FakeSocket.instances[1]!
    current.message({kind: 'ready', execution_id: 'current'}); await flushPromises()
    staleMessage({data: JSON.stringify({kind: 'output', data: btoa('old-secret-output')})}); staleClose(); await flushPromises()
    expect(terminals[1].write).not.toHaveBeenCalled(); expect(button(w, 'Disconnect terminal').exists()).toBe(true)
    current.message({kind: 'output', data: btoa('current')}); expect(terminals[1].write).toHaveBeenCalledWith(Uint8Array.from(new TextEncoder().encode('current')))
    terminals[0].dataHandler('stale command'); expect(current.send).not.toHaveBeenCalled(); terminals[1].dataHandler('uptime\n'); expect(current.send).toHaveBeenCalledOnce(); w.unmount()
  })
  it.each(['background_status', 'background_cancel'])('allows explicitly approved %s recovery while global execution is disabled', async operation => {
    const w = render(); await w.setProps({snapshot: {...structuredClone(snapshot), enabled: false} as any})
    const form = w.findAll('form')[3]!, selects = form.findAll('select')
    await selects[0]!.setValue('r'); await selects[1]!.setValue(operation)
    const inputs = form.findAll('input'); await inputs[0]!.setValue('/home/deploy/private'); await inputs[1]!.setValue('abcdefghijklmnop')
    await form.trigger('submit'); await flushPromises()
    expect(remoteRequest).toHaveBeenLastCalledWith('/operations/preview', 'POST', {route_id: 'r', operation, background: {root: '/home/deploy/private', job_id: 'abcdefghijklmnop'}, timeout_seconds: 60})
    expect(button(w, 'Preview transfer').attributes('disabled')).toBeDefined(); expect(button(w, 'Preview PTY').attributes('disabled')).toBeDefined()
    expect(button(w, 'Allow once').attributes('disabled')).toBeUndefined()
    await selects[1]!.setValue('background_start')
    await button(w, 'Allow once').trigger('click'); await flushPromises()
    expect(remoteRequest).toHaveBeenLastCalledWith('/operations', 'POST', {route_id: 'r', operation, background: {root: '/home/deploy/private', job_id: 'abcdefghijklmnop'}, timeout_seconds: 60, approval_id: 'single-use-nonce', expected_plan_hash: 'frozen-hash'})
    w.unmount()
  })
  it('keeps helper installation approval blocked while global execution is disabled', async () => {
    const w = render(); const form = w.findAll('form')[3]!
    await form.find('select').setValue('r'); await form.trigger('submit'); await flushPromises()
    await w.setProps({snapshot: {...structuredClone(snapshot), enabled: false} as any})
    expect(button(w, 'Allow once').attributes('disabled')).toBeDefined()
    expect(remoteRequest).toHaveBeenCalledTimes(1); w.unmount()
  })

})
