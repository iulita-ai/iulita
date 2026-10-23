<template>
  <n-space vertical :size="20" class="mcp-page">
    <n-page-header :title="t('mcp.title')" :subtitle="t('mcp.subtitle')">
      <template #extra
        ><n-space
          ><n-button :loading="loading" @click="load">{{
            t("mcp.refresh")
          }}</n-button
          ><n-button type="primary" @click="newDraft">{{
            t("mcp.add")
          }}</n-button></n-space
        ></template
      >
    </n-page-header>
    <n-alert v-if="error" type="error" role="alert">{{ error }}</n-alert>
    <n-alert v-if="loaded && !snapshot.enabled" type="warning">{{
      t("mcp.disabledHint")
    }}</n-alert>
    <n-space v-if="isAdmin()" align="center"
      ><n-switch
        :value="snapshot.enabled"
        :disabled="busy || !loaded"
        :aria-label="t('mcp.globalEnabled')"
        @update:value="setEnabled"
      /><span>{{ t("mcp.globalEnabled") }}</span></n-space
    >
    <n-empty
      v-if="loaded && !snapshot.connections.length"
      :description="t('mcp.empty')"
    />
    <div class="mcp-workspace">
      <nav
        v-if="snapshot.connections.length"
        class="mcp-connections"
        :aria-label="t('mcp.title')"
      >
        <button
          v-for="c in snapshot.connections"
          :key="c.id"
          class="mcp-row"
          :class="{ selected: selected?.id === c.id }"
          @click="select(c.id)"
        >
          <strong>{{ c.label }}</strong
          ><span>{{ t(`mcp.domain.${c.data_domain}`) }}</span>
          <span>{{ status(c) }}</span>
        </button>
      </nav>
      <section v-if="selected" class="mcp-details" :aria-label="selected.label">
        <n-space vertical :size="16">
          <div>
            <h2>{{ selected.label }}</h2>
            <n-text depth="3">{{ selected.endpoint }}</n-text>
          </div>
          <n-space
            ><n-tag
              :type="
                selected.desired_state === 'enabled' &&
                selected.runtime_state === 'ready'
                  ? 'success'
                  : 'default'
              "
              >{{ status(selected) }}</n-tag
            ><n-tag>{{ t(`mcp.domain.${selected.data_domain}`) }}</n-tag
            ><n-text depth="3">{{ t(`mcp.auth.${selected.auth_mode}`,selected.auth_mode) }}</n-text></n-space
          >
          <n-alert v-if="selected.error_code" type="warning"
            >{{ t("mcp.connectionFailed") }} ({{
              selected.error_code
            }})</n-alert
          >
          <n-space>
            <n-button :disabled="busy" @click="edit">{{
              t("mcp.edit")
            }}</n-button>
            <n-button
              :disabled="busy || !snapshot.enabled"
              @click="act('check')"
              >{{ t("mcp.check") }}</n-button
            >
            <n-button
              type="primary"
              :disabled="busy || !snapshot.enabled"
              @click="
                act(
                  selected.desired_state === 'enabled'
                    ? 'reconnect'
                    : 'connect',
                )
              "
              >{{
                t(
                  selected.desired_state === "enabled"
                    ? "mcp.reconnect"
                    : "mcp.connect",
                )
              }}</n-button
            >
            <n-button
              :disabled="busy || selected.desired_state !== 'enabled'"
              @click="act('disconnect')"
              >{{ t("mcp.disconnect") }}</n-button
            >
          </n-space>
          <n-tabs type="line">
            <n-tab-pane
              v-for="kind in ['tool', 'resource', 'prompt']"
              :key="kind"
              :name="kind"
              :tab="t(`mcp.kind.${kind}`)"
            >
              <n-space vertical>
                <n-alert type="info">{{ t("mcp.permissionsHint") }}</n-alert>
                <n-empty
                  v-if="!items(kind).length"
                  :description="t('mcp.catalogEmpty')"
                />
                <article
                  v-for="item in items(kind)"
                  :key="item.id"
                  class="mcp-item"
                >
                  <n-space justify="space-between" align="center"
                    ><strong>{{ item.title || item.name }}</strong
                    ><n-tag :type="item.allowed ? 'success' : 'warning'">{{
                      t(item.allowed ? "mcp.allowed" : "mcp.reviewRequired")
                    }}</n-tag></n-space
                  >
                  <p>{{ item.description }}</p>
                  <n-alert v-if="item.error_code" type="warning"
                    >{{ t("mcp.unsupported") }} ({{ item.error_code }})</n-alert
                  >
                  <details>
                    <summary>{{ t("mcp.descriptor") }}</summary>
                    <pre>{{ JSON.stringify(item.descriptor, null, 2) }}</pre>
                  </details>
                  <n-space align="center">
                    <n-select
                      :value="item.effect"
                      :options="effectOptions"
                      style="width: 160px"
                      :aria-label="t('mcp.effect')"
                      :disabled="busy"
                      @update:value="
                        (value) => review(item, item.allowed, value)
                      "
                    />
                    <n-button
                      :disabled="busy || !!item.error_code"
                      @click="review(item, !item.allowed, item.effect)"
                      >{{
                        t(item.allowed ? "mcp.forbid" : "mcp.allow")
                      }}</n-button
                    >
                    <n-button
                      :disabled="
                        busy ||
                        !item.allowed ||
                        selected.desired_state !== 'enabled' ||
                        !snapshot.enabled
                      "
                      @click="openCall(item)"
                      >{{ t("mcp.run") }}</n-button
                    >
                  </n-space>
                </article>
              </n-space>
            </n-tab-pane>
            <n-tab-pane name="auth" :tab="t('mcp.access')">
              <n-space vertical>
                <n-alert type="info">{{ t("mcp.secretHint") }}</n-alert>
                <form
                  @submit.prevent="saveCredential"
                  class="mcp-form"
                  autocomplete="off"
                >
                  <label
                    >{{ t("mcp.authMode")
                    }}<n-select
                      v-model:value="authMode"
                      :options="authOptions"
                      :aria-label="t('mcp.authMode')"
                      :placeholder="t('mcp.authMode')"
                  /></label>
                  <label v-if="authMode === 'bearer'"
                    >{{ t("mcp.token")
                    }}<n-input
                      v-model:value="token"
                      type="password"
                      autocomplete="new-password"
                      :aria-label="t('mcp.token')"
                      :placeholder="t('mcp.token')"
                  /></label>
                  <label v-if="authMode === 'headers'"
                    >{{ t("mcp.headers")
                    }}<n-input
                      v-model:value="headers"
                      type="textarea"
                      :aria-label="t('mcp.headers')"
                      :placeholder="t('mcp.headers')"
                  /></label>
                  <n-button attr-type="submit" :loading="busy">{{
                    t("mcp.saveAccess")
                  }}</n-button>
                </form>
                <details>
                  <summary>OAuth</summary>
                  <form class="mcp-form" @submit.prevent="startOAuth">
                    <label
                      >{{ t("mcp.issuer")
                      }}<n-input
                        v-model:value="issuer"
                        :aria-label="t('mcp.issuer')"
                        :placeholder="t('mcp.issuer')" /></label
                    ><label
                      >Client ID<n-input
                        v-model:value="clientID"
                        aria-label="Client ID" /></label
                    ><label
                      >{{ t("mcp.scopes")
                      }}<n-input
                        v-model:value="scopes"
                        :aria-label="t('mcp.scopes')"
                        :placeholder="t('mcp.scopes')" /></label
                    ><n-button
                      attr-type="submit"
                      :disabled="busy || !snapshot.enabled"
                      >{{ t("mcp.signIn") }}</n-button
                    >
                  </form>
                </details>
                <n-space
                  ><n-button :disabled="busy" @click="danger('revoke_auth')">{{
                    t("mcp.revoke")
                  }}</n-button
                  ><n-button
                    type="error"
                    :disabled="busy"
                    @click="danger('delete')"
                    >{{ t("mcp.delete") }}</n-button
                  ></n-space
                >
              </n-space>
            </n-tab-pane>
          </n-tabs>
        </n-space>
      </section>
    </div>
    <section v-if="plans.length" :aria-label="t('mcp.approvals')">
      <h2>{{ t("mcp.approvals") }}</h2>
      <n-space vertical
        ><n-card v-for="p in plans" :key="p.id" size="small">
          <pre>{{ p.preview }}</pre>
          <details open>
            <summary>{{ t("mcp.arguments") }}</summary>
            <pre>{{ JSON.stringify(p.call.arguments, null, 2) }}</pre>
          </details>
          <n-text depth="3"
            >{{ t("mcp.expires") }}
            {{ new Date(p.expires_at).toLocaleString(locale) }}</n-text
          ><n-space
            ><n-button :disabled="busy" @click="decision(p, false)">{{
              t("mcp.reject")
            }}</n-button
            ><n-button
              type="primary"
              :disabled="busy || !snapshot.enabled"
              @click="decision(p, true)"
              >{{ t("mcp.approve") }}</n-button
            ></n-space
          ></n-card
        ></n-space
      >
    </section>
    <section v-if="snapshot.executions.length">
      <h2>{{ t("mcp.history") }}</h2>
      <article v-for="x in snapshot.executions" :key="x.id" class="mcp-item">
        <n-space justify="space-between"
          ><strong>{{ connectionLabel(x.connection_id) }}</strong
          ><n-tag>{{ t(`mcp.execution.${x.state}`, x.state) }}</n-tag></n-space
        ><small>{{ new Date(x.created_at).toLocaleString(locale) }}</small
        ><n-alert v-if="x.state === 'unknown_outcome'" type="warning">{{
          t("mcp.unknown")
        }}</n-alert
        ><n-space
          ><n-button
            v-if="
              ['running', 'dispatching', 'cancel_requested'].includes(x.state)
            "
            :disabled="busy"
            @click="cancel(x.id)"
            >{{ t("mcp.cancelOperation") }}</n-button
          ><n-button
            v-if="x.artifact_id"
            :disabled="busy"
            @click="output(x.artifact_id)"
            >{{ t("mcp.output") }}</n-button
          ></n-space
        >
      </article>
    </section>
    <n-modal
      v-model:show="showCreate"
      preset="card"
      :title="t(editing ? 'mcp.edit' : 'mcp.add')"
      class="mcp-modal"
    >
      <form @submit.prevent="create" class="mcp-form">
        <label
          >{{ t("mcp.name")
          }}<n-input
            v-model:value="draft.label"
            maxlength="120"
            :aria-label="t('mcp.name')"
            :placeholder="t('mcp.name')"
        /></label>
        <label
          >{{ t("mcp.transport")
          }}<n-select
            v-model:value="draft.transport"
            :options="transportOptions"
            :aria-label="t('mcp.transport')"
            :placeholder="t('mcp.transport')"
        /></label>
        <label v-if="draft.transport === 'stdio'"
          >{{ t("mcp.profile")
          }}<n-input
            v-model:value="draft.profile"
            :aria-label="t('mcp.profile')"
            :placeholder="t('mcp.profile')"
        /></label>
        <label v-else
          >{{ t("mcp.endpoint")
          }}<n-input
            v-model:value="draft.endpoint"
            placeholder="https://example.org/mcp"
            :aria-label="t('mcp.endpoint')"
        /></label>
        <label
          >{{ t("mcp.dataDomain")
          }}<n-select
            v-model:value="draft.data_domain"
            :options="domainOptions"
            :aria-label="t('mcp.dataDomain')"
            :placeholder="t('mcp.dataDomain')"
        /></label>
        <label
          >{{ t("mcp.results")
          }}<n-select
            v-model:value="draft.result_mode"
            :options="resultOptions"
            :aria-label="t('mcp.results')"
            :placeholder="t('mcp.results')"
        /></label>
        <n-alert type="info">{{ t("mcp.draftHint") }}</n-alert>
        <n-button
          type="primary"
          attr-type="submit"
          :loading="busy"
          :disabled="
            !draft.label ||
            (draft.transport === 'http' ? !draft.endpoint : !draft.profile) ||
            !draft.data_domain
          "
          >{{ t(editing ? "mcp.save" : "mcp.saveDraft") }}</n-button
        >
      </form>
    </n-modal>
    <n-modal
      v-model:show="showCall"
      preset="card"
      :title="t('mcp.reviewCall')"
      class="mcp-modal"
      ><form class="mcp-form" @submit.prevent="prepare">
        <p>{{ callItem?.title || callItem?.name }}</p>
        <label
          >{{ t("mcp.arguments")
          }}<n-input
            v-model:value="argumentsJSON"
            type="textarea"
            :autosize="{ minRows: 6, maxRows: 18 }"
            :aria-label="t('mcp.arguments')"
            :placeholder="t('mcp.arguments')" /></label
        ><n-alert type="info">{{ t("mcp.prepareHint") }}</n-alert
        ><n-button attr-type="submit" type="primary" :loading="busy">{{
          t("mcp.prepare")
        }}</n-button>
      </form></n-modal
    >
    <n-modal
      v-model:show="showOutput"
      preset="card"
      :title="t('mcp.output')"
      class="mcp-modal"
    >
      <pre>{{ resultOutput }}</pre>
    </n-modal>
  </n-space>
</template>

<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted } from "vue";
import { useI18n } from "vue-i18n";
import {
  NAlert,
  NButton,
  NCard,
  NEmpty,
  NInput,
  NModal,
  NPageHeader,
  NSelect,
  NSpace,
  NSwitch,
  NTabPane,
  NTabs,
  NTag,
  NText,
  useDialog,
} from "naive-ui";
import { isAdmin, mcpAPI } from "../api";
import type { MCPConnection, MCPItem, MCPPlan, MCPSnapshot } from "../mcp";
const { t, locale } = useI18n();
const dialog = useDialog();
const snapshot = ref<MCPSnapshot>({
  enabled: false,
  connections: [],
  executions: [],
});
const selected = ref<MCPConnection | null>(null),
  plans = ref<MCPPlan[]>([]);
const error = ref(""),
  loading = ref(false),
  loaded = ref(false),
  busy = ref(false);
const showCreate = ref(false),
  showCall = ref(false),
  showOutput = ref(false),
  callItem = ref<MCPItem | null>(null);
const argumentsJSON = ref("{}"),
  resultOutput = ref(""),
  token = ref(""),
  headers = ref("{}"),
  authMode = ref("none"),
  issuer = ref(""),
  clientID = ref(""),
  scopes = ref("");
const editing = ref<string | null>(null);
const editRevision = ref(0);
const draft = ref({
  label: "",
  endpoint: "",
  data_domain: null as string | null,
  result_mode: "private",
  transport: "http",
  profile: "",
});
const transportOptions = computed(() => [
  { value: "http", label: t("mcp.remote") },
  { value: "stdio", label: t("mcp.local") },
]);
function newDraft() {
  editing.value = null;
  draft.value = {
    label: "",
    endpoint: "",
    data_domain: null,
    result_mode: "private",
    transport: "http",
    profile: "",
  };
  showCreate.value = true;
}
function edit() {
  const c = selected.value!;
  editing.value = c.id;
  editRevision.value = c.revision;
  draft.value = {
    label: c.label,
    endpoint: c.endpoint || "",
    data_domain: c.data_domain,
    result_mode: c.result_mode,
    transport: c.transport,
    profile: c.profile || "",
  };
  showCreate.value = true;
}
const domainOptions = computed(() =>
  ["personal", "work", "other"].map((value) => ({
    value,
    label: t(`mcp.domain.${value}`),
  })),
);
const resultOptions = computed(() =>
  ["private", "model"].map((value) => ({
    value,
    label: t(`mcp.result.${value}`),
  })),
);
const effectOptions = computed(() =>
  ["read", "write", "unknown"].map((value) => ({
    value,
    label: t(`mcp.effects.${value}`),
  })),
);
const authOptions = computed(() =>
  ["none", "bearer", "headers"].map((value) => ({
    value,
    label: t(`mcp.auth.${value}`),
  })),
);
function status(c: MCPConnection) {
  return c.desired_state === "enabled"
    ? t(`mcp.runtime.${c.runtime_state}`, c.runtime_state)
    : t(`mcp.desired.${c.desired_state}`, c.desired_state);
}
function items(kind: string) {
  return (
    selected.value?.items?.filter(
      (i) =>
        i.kind === kind ||
        (kind === "resource" && i.kind === "resource_template"),
    ) || []
  );
}
function connectionLabel(id: string) {
  return snapshot.value.connections.find((c) => c.id === id)?.label || id;
}
async function load() {
  loading.value = true;
  error.value = "";
  try {
    const [s, p] = await Promise.all([mcpAPI.snapshot(), mcpAPI.plans()]);
    snapshot.value = s;
    plans.value = p;
    loaded.value = true;
    if (
      selected.value &&
      s.connections.some((c) => c.id === selected.value?.id)
    ) {
      const next = await mcpAPI.connection(selected.value.id);
      if (next.revision !== selected.value.revision) {
        token.value = "";
        headers.value = "{}";
        authMode.value = next.auth_mode === "oauth" ? "none" : next.auth_mode;
      }
      selected.value = next;
    } else if (s.connections.length) await select(s.connections[0]!.id);
    else selected.value = null;
  } catch (e) {
    error.value = e instanceof Error ? e.message : t("mcp.loadFailed");
  } finally {
    loading.value = false;
  }
}
async function select(id: string) {
  try {
    selected.value = await mcpAPI.connection(id);
    token.value = "";
    headers.value = "{}";
    authMode.value =
      selected.value.auth_mode === "oauth" ? "none" : selected.value.auth_mode;
  } catch (e) {
    error.value = String(e);
  }
}
async function work(fn: () => Promise<unknown>) {
  if (busy.value) return;
  busy.value = true;
  error.value = "";
  try {
    await fn();
    await load();
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e);
  } finally {
    busy.value = false;
  }
}
async function create() {
  await work(async () => {
    const d = {
      ...draft.value,
      endpoint: draft.value.transport === "stdio" ? "" : draft.value.endpoint,
    };
    const c = editing.value
      ? await mcpAPI.update(editing.value, {
          revision: editRevision.value,
          draft: d,
        })
      : await mcpAPI.create(d);
    showCreate.value = false;
    selected.value = c;
    draft.value = {
      label: "",
      endpoint: "",
      data_domain: null,
      result_mode: "private",
      transport: "http",
      profile: "",
    };
  });
}
async function act(action: string) {
  if (!selected.value) return;
  if (action === "disconnect" || action === "reconnect") {
    dialog.warning({
      title: t(action === "disconnect" ? "mcp.disconnect" : "mcp.reconnect"),
      content: t(
        action === "disconnect" ? "mcp.disconnectHint" : "mcp.reconnectHint",
      ),
      positiveText: t(
        action === "disconnect" ? "mcp.disconnect" : "mcp.reconnect",
      ),
      negativeText: t("mcp.keep"),
      onPositiveClick: () =>
        work(() =>
          mcpAPI.action(selected.value!.id, action, selected.value!.revision),
        ),
    });
    return;
  }
  await work(() =>
    mcpAPI.action(selected.value!.id, action, selected.value!.revision),
  );
}
async function review(item: MCPItem, allowed: boolean, effect: string) {
  dialog.warning({
    title: t("mcp.reviewAccess"),
    content: t("mcp.reviewAccessHint", {
      name: item.name,
      effect: t(`mcp.effects.${effect}`),
    }),
    positiveText: t("mcp.save"),
    negativeText: t("mcp.keep"),
    onPositiveClick: () =>
      work(() =>
        mcpAPI.review(selected.value!.id, {
          revision: selected.value!.revision,
          review: {
            item_id: item.id,
            revision: item.revision,
            allowed,
            effect,
          },
        }),
      ),
  });
}
async function saveCredential() {
  await work(async () => {
    await mcpAPI.credentials(selected.value!.id, {
      revision: selected.value!.revision,
      mode: authMode.value,
      token: token.value,
      headers: authMode.value === "headers" ? JSON.parse(headers.value) : null,
    });
    token.value = "";
    headers.value = "{}";
  });
}
async function startOAuth() {
  await work(async () => {
    const v = await mcpAPI.oauth(selected.value!.id, {
      revision: selected.value!.revision,
      issuer: issuer.value,
      client_id: clientID.value,
      scopes: scopes.value,
    });
    const url = new URL(v.url);
    if (url.protocol !== "https:") throw new Error(t("mcp.invalidURL"));
    window.location.assign(url.href);
  });
}
function danger(action: string) {
  dialog.warning({
    title: t(action === "delete" ? "mcp.delete" : "mcp.revoke"),
    content: t("mcp.deleteHint"),
    positiveText: t("mcp.confirm"),
    negativeText: t("mcp.keep"),
    onPositiveClick: () =>
      work(() =>
        mcpAPI.action(selected.value!.id, action, selected.value!.revision),
      ),
  });
}
function setEnabled(value: boolean) {
  dialog.warning({
    title: t("mcp.globalEnabled"),
    content: t(value ? "mcp.enableHint" : "mcp.disableGlobalHint"),
    positiveText: t("mcp.confirm"),
    negativeText: t("mcp.keep"),
    onPositiveClick: () => work(() => mcpAPI.enabled(value)),
  });
}
function openCall(item: MCPItem) {
  callItem.value = item;
  argumentsJSON.value = "{}";
  showCall.value = true;
}
async function prepare() {
  await work(async () => {
    await mcpAPI.prepare({
      connection_id: selected.value!.id,
      item_id: callItem.value!.id,
      descriptor_revision: callItem.value!.revision,
      arguments: JSON.parse(argumentsJSON.value),
    });
    showCall.value = false;
  });
}
async function decision(p: MCPPlan, approved: boolean) {
  await work(() => mcpAPI.decide(p, approved));
}
async function cancel(id: string) {
  await work(() => mcpAPI.cancel(id));
}
async function output(id: string) {
  await work(async () => {
    resultOutput.value = JSON.stringify(await mcpAPI.artifact(id), null, 2);
    showOutput.value = true;
  });
}
let poller: ReturnType<typeof setInterval> | undefined;
onMounted(() => {
  void load();
  poller = setInterval(() => {
    if (
      !busy.value &&
      !loading.value &&
      (plans.value.length > 0 ||
        snapshot.value.executions.some((x) =>
          ["running", "dispatching", "cancel_requested"].includes(x.state),
        ))
    )
      void load();
  }, 3000);
});
onUnmounted(() => {
  if (poller) clearInterval(poller);
});
</script>

<style scoped>
.mcp-page {
  max-width: 1280px;
  margin: 0 auto;
}
.mcp-workspace {
  display: grid;
  grid-template-columns: minmax(200px, 280px) minmax(0, 1fr);
  gap: 24px;
}
.mcp-connections {
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.mcp-row {
  display: flex;
  flex-direction: column;
  gap: 5px;
  text-align: start;
  padding: 16px;
  background: transparent;
  color: inherit;
  border: 1px solid var(--n-border-color, #555);
  border-radius: 6px;
  cursor: pointer;
  min-height: 44px;
}
.mcp-row.selected {
  border-inline-start: 4px solid #63e2b7;
}
.mcp-row span {
  font-size: 13px;
  opacity: 0.8;
}
.mcp-details {
  min-width: 0;
}
.mcp-details h2 {
  margin: 0 0 8px;
}
.mcp-item {
  padding: 16px 0;
  border-bottom: 1px solid #555;
}
.mcp-item p {
  max-width: 75ch;
  white-space: pre-wrap;
  overflow-wrap: anywhere;
}
.mcp-modal {
  width: min(720px, calc(100vw - 32px));
  max-height: 90vh;
  overflow: auto;
}
.mcp-form {
  display: flex;
  flex-direction: column;
  gap: 16px;
}
.mcp-form label {
  display: flex;
  flex-direction: column;
  gap: 6px;
}
pre {
  white-space: pre-wrap;
  overflow-wrap: anywhere;
  max-height: 400px;
  overflow: auto;
}
summary {
  cursor: pointer;
  padding: 10px 0;
  min-height: 24px;
}
button:focus-visible,
summary:focus-visible {
  outline: 2px solid currentColor;
  outline-offset: 3px;
}
@media (max-width: 700px) {
  .mcp-page :deep(.n-button) {
    min-height: 44px;
  }
  .mcp-workspace {
    grid-template-columns: 1fr;
  }
  .mcp-connections {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(150px, 1fr));
  }
}
</style>
