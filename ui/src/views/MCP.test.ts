import { beforeEach, describe, expect, it, vi } from "vitest";
import { flushPromises, mount } from "@vue/test-utils";
import MCP from "./MCP.vue";
import { NSelect } from "naive-ui";
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
  it.each([false, undefined])(
    "offers only remote transport when stdio capability is %s",
    async (stdio_enabled) => {
      vi.mocked(mcpAPI.snapshot).mockResolvedValue({
        enabled: true,
        stdio_enabled,
        connections: [structuredClone(connection)],
        executions: [],
      });
      const w = mount(MCP);
      await flushPromises();
      await button(w, "Add connection").trigger("click");
      await flushPromises();
      const transport = w
        .findAllComponents(NSelect)
        .find((select) =>
          select.props("options")?.some((option) => option.value === "http"),
        );
      expect(transport?.props("options")).toEqual([
        expect.objectContaining({ value: "http" }),
      ]);
      w.unmount();
    },
  );
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
  it.each(["unknown_outcome", "result_unavailable"])(
    "shows %s without a retry control",
    async (state) => {
      vi.mocked(mcpAPI.snapshot).mockResolvedValue({
        enabled: true,
        connections: [connection],
        executions: [
          {
            id: "x",
            connection_id: "c",
            item_id: "i",
            state,
            side_effect_state: "unknown",
            created_at: "2026-10-23T10:00:00Z",
          },
        ],
      });
      const w = mount(MCP);
      await flushPromises();
      expect(w.text()).toContain(
        state === "unknown_outcome"
          ? "Repeating it may perform the action again"
          : "the result could not be saved",
      );
      expect(
        w.findAll("button").some((b) => /retry|repeat/i.test(b.text())),
      ).toBe(false);
      w.unmount();
    },
  );
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

it("keeps a confirmation bound to the connection that opened it", async () => {
  const other = {
    ...structuredClone(connection),
    id: "other",
    label: "Work tracker",
    revision: 9,
  };
  vi.mocked(mcpAPI.snapshot).mockResolvedValue({
    enabled: true,
    connections: [connection, other],
    executions: [],
  });
  vi.mocked(mcpAPI.connection).mockImplementation(async (id) =>
    structuredClone(id === "c" ? connection : other),
  );
  const w = mount(MCP);
  await flushPromises();
  await button(w, "Disconnect").trigger("click");
  const confirmation = warning.mock.calls[0]![0];
  await w.findAll(".mcp-row")[1]!.trigger("click");
  await flushPromises();
  expect(w.find("h2").text()).toBe("Work tracker");
  await confirmation.onPositiveClick();
  await flushPromises();
  expect(mcpAPI.action).toHaveBeenCalledWith("c", "disconnect", 4);
  w.unmount();
});

it("ignores a late connection response after selecting a newer target", async () => {
  const other = {
    ...structuredClone(connection),
    id: "other",
    label: "Work tracker",
  };
  vi.mocked(mcpAPI.snapshot).mockResolvedValue({
    enabled: true,
    connections: [connection, other],
    executions: [],
  });
  const w = mount(MCP);
  await flushPromises();
  let resolveFirst!: (c: MCPConnection) => void;
  vi.mocked(mcpAPI.connection).mockImplementation((id) =>
    id === "c"
      ? new Promise((resolve) => {
          resolveFirst = resolve;
        })
      : Promise.resolve(structuredClone(other)),
  );
  await w.findAll(".mcp-row")[0]!.trigger("click");
  await w.findAll(".mcp-row")[1]!.trigger("click");
  await flushPromises();
  resolveFirst(structuredClone(connection));
  await flushPromises();
  expect(w.find("h2").text()).toBe("Work tracker");
  expect(w.findAll(".mcp-row")[1]!.attributes("aria-current")).toBe("true");
  w.unmount();
});
