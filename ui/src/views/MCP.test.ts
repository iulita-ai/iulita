import { beforeEach, describe, expect, it, vi } from "vitest";
import { flushPromises, mount } from "@vue/test-utils";
import MCP from "./MCP.vue";
import { mcpAPI } from "../api";
import type { MCPConnection } from "../mcp";
const { warning } = vi.hoisted(() => ({ warning: vi.fn() }));
vi.mock("naive-ui", async () => {
  const actual = await vi.importActual("naive-ui");
  return { ...actual, useDialog: () => ({ warning }) };
});
vi.mock("../api", () => ({
  isAdmin: vi.fn(() => true),
  mcpAPI: {
    snapshot: vi.fn(),
    plans: vi.fn(),
    connection: vi.fn(),
    create: vi.fn(),
    action: vi.fn(),
    credentials: vi.fn(),
    review: vi.fn(),
    prepare: vi.fn(),
    decide: vi.fn(),
    cancel: vi.fn(),
    artifact: vi.fn(),
    enabled: vi.fn(),
    oauth: vi.fn(),
  },
}));
const connection: MCPConnection = {
  id: "c",
  label: "Private archive",
  data_domain: "personal",
  endpoint: "https://fixture.example/mcp",
  transport: "http",
  desired_state: "enabled",
  runtime_state: "ready",
  auth_mode: "bearer",
  auth_state: "valid",
  revision: 4,
  generation: 2,
  checked_at: "2026-10-23T10:00:00Z",
  result_mode: "private",
  items: [
    {
      id: "i",
      kind: "tool",
      name: "lookup",
      description: "<script>foreign descriptor</script>",
      revision: "hash",
      descriptor: { inputSchema: { type: "object" } },
      effect: "unknown",
      allowed: false,
    },
  ],
};
const button = (w: ReturnType<typeof mount>, text: string) =>
  w.findAll("button").find((b) => b.text() === text)!;
beforeEach(() => {
  vi.clearAllMocks();
  vi.mocked(mcpAPI.snapshot).mockResolvedValue({
    enabled: true,
    connections: [structuredClone(connection)],
    executions: [],
  });
  vi.mocked(mcpAPI.plans).mockResolvedValue([]);
  vi.mocked(mcpAPI.connection).mockResolvedValue(structuredClone(connection));
});
describe("MCP owner connection controls", () => {
  it("shows unreviewed descriptors as escaped text without executing or granting anything", async () => {
    const w = mount(MCP);
    await flushPromises();
    expect(w.text()).toContain("Review required");
    expect(w.text()).toContain("<script>foreign descriptor</script>");
    expect(w.find("script").exists()).toBe(false);
    expect(mcpAPI.review).not.toHaveBeenCalled();
    expect(mcpAPI.prepare).not.toHaveBeenCalled();
    w.unmount();
  });
  it("requires a concrete review before disconnecting", async () => {
    const w = mount(MCP);
    await flushPromises();
    await button(w, "Disconnect").trigger("click");
    expect(mcpAPI.action).not.toHaveBeenCalled();
    expect(warning).toHaveBeenCalled();
    await warning.mock.calls[0]![0].onPositiveClick();
    await flushPromises();
    expect(mcpAPI.action).toHaveBeenCalledWith("c", "disconnect", 4);
    w.unmount();
  });
  it("shows unknown outcome without a retry control", async () => {
    vi.mocked(mcpAPI.snapshot).mockResolvedValue({
      enabled: true,
      connections: [connection],
      executions: [
        {
          id: "x",
          connection_id: "c",
          item_id: "i",
          state: "unknown_outcome",
          side_effect_state: "unknown",
          created_at: "2026-10-23T10:00:00Z",
        },
      ],
    });
    const w = mount(MCP);
    await flushPromises();
    expect(w.text()).toContain("Repeating it may perform the action again");
    expect(
      w.findAll("button").some((b) => /retry|repeat/i.test(b.text())),
    ).toBe(false);
    w.unmount();
  });
  it("keeps execution separate from preparing a plan", async () => {
    vi.mocked(mcpAPI.plans).mockResolvedValue([
      {
        id: "plan",
        hash: "bound-hash",
        preview: "Exact operation",
        expires_at: "2026-10-23T11:00:00Z",
        call: {
          connection_id: "c",
          item_id: "i",
          arguments: { value: "visible to owner" },
        },
      },
    ]);
    const w = mount(MCP);
    await flushPromises();
    expect(w.text()).toContain("visible to owner");
    expect(mcpAPI.decide).not.toHaveBeenCalled();
    await button(w, "Approve exact request").trigger("click");
    await flushPromises();
    expect(mcpAPI.decide).toHaveBeenCalledWith(
      expect.objectContaining({ id: "plan", hash: "bound-hash" }),
      true,
    );
    w.unmount();
  });
});
