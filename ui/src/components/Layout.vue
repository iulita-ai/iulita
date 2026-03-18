<template>
  <n-config-provider :theme="darkTheme">
    <n-message-provider>
    <n-layout has-sider style="height: 100vh">
      <n-layout-sider
        bordered
        :width="220"
        :collapsed-width="64"
        collapse-mode="width"
        show-trigger
        :native-scrollbar="false"
        @update:collapsed="(val: boolean) => { siderCollapsed = val }"
      >
        <div style="display:flex;align-items:center;gap:10px;padding:12px 16px;min-height:64px;">
          <img :src="logoUrl" alt="Iulita" style="width:36px;height:36px;flex-shrink:0;" />
          <n-text
            v-show="!siderCollapsed"
            :depth="1"
            style="font-size:18px;font-weight:700;white-space:nowrap;overflow:hidden;"
          >
            Iulita.ai
          </n-text>
        </div>
        <n-menu
          :value="currentRoute"
          :options="menuOptions"
          @update:value="handleMenuSelect"
        />
        <div style="position: absolute; bottom: 16px; left: 0; right: 0; padding: 0 16px;">
          <div style="margin-bottom: 8px;">
            <LanguagePicker />
          </div>
          <n-text :depth="3" style="font-size: 12px; display: block; margin-bottom: 8px; text-align: center;">
            {{ user?.username }}
          </n-text>
          <n-button size="small" block quaternary @click="handleLogout">
            {{ t('nav.logout') }}
          </n-button>
        </div>
      </n-layout-sider>
      <n-layout-content content-style="padding: 24px;" :native-scrollbar="false">
        <div :dir="direction">
          <slot />
        </div>
      </n-layout-content>
    </n-layout>
    </n-message-provider>
  </n-config-provider>
</template>

<script setup lang="ts">
import { computed, h, ref } from 'vue'
import { useRouter, useRoute } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { useLocale } from '../composables/useLocale'
import { darkTheme, NConfigProvider, NLayout, NLayoutSider, NLayoutContent, NMenu, NText, NIcon, NMessageProvider, NButton } from 'naive-ui'
import type { MenuOption } from 'naive-ui'
import {
  HomeOutline,
  BulbOutline,
  NuclearOutline,
  AlarmOutline,
  PersonOutline,
  SettingsOutline,
  PeopleOutline,
  GitNetworkOutline,
  ChatbubblesOutline,
  TimerOutline,
  ExtensionPuzzleOutline,
  CheckmarkDoneOutline,
  BarChartOutline,
} from '@vicons/ionicons5'
import { currentUser, clearTokens, isAdmin } from '../api'
import { useSkillStatus } from '../composables/useSkillStatus'
import LanguagePicker from './LanguagePicker.vue'
import logoUrl from '../assets/logo.svg'

const { t } = useI18n()
const { direction } = useLocale()
const siderCollapsed = ref(false)
const router = useRouter()
const route = useRoute()

const { isSkillEnabled } = useSkillStatus()
const user = computed(() => currentUser())
const currentRoute = computed(() => route.name as string)

function renderIcon(icon: any) {
  return () => h(NIcon, null, { default: () => h(icon) })
}

const menuOptions = computed<MenuOption[]>(() => {
  const items: MenuOption[] = [
    { label: t('nav.dashboard'), key: 'dashboard', icon: renderIcon(HomeOutline) },
    { label: t('nav.chat'), key: 'chat', icon: renderIcon(ChatbubblesOutline) },
    { label: t('nav.reminders'), key: 'reminders', icon: renderIcon(AlarmOutline) },
  ]
  if (isSkillEnabled('tasks')) {
    items.push({ label: t('nav.tasks'), key: 'tasks', icon: renderIcon(CheckmarkDoneOutline) })
  }
  items.push(
    { label: t('nav.facts'), key: 'facts', icon: renderIcon(BulbOutline) },
    { label: t('nav.insights'), key: 'insights', icon: renderIcon(NuclearOutline) },
    { label: t('nav.profile'), key: 'profile', icon: renderIcon(PersonOutline) },
    { label: t('nav.settings'), key: 'settings', icon: renderIcon(SettingsOutline) },
  )
  if (isAdmin()) {
    items.push({ key: 'admin-divider', type: 'divider' } as any)
    items.push({ label: t('nav.usage'), key: 'usage', icon: renderIcon(BarChartOutline) })
    items.push({ label: t('nav.skills'), key: 'skills', icon: renderIcon(ExtensionPuzzleOutline) })
    items.push({ label: t('nav.agentJobs'), key: 'agent-jobs', icon: renderIcon(TimerOutline) })
    items.push({ label: t('nav.channels'), key: 'channels', icon: renderIcon(GitNetworkOutline) })
    items.push({ label: t('nav.users'), key: 'users', icon: renderIcon(PeopleOutline) })
  }
  return items
})

function handleMenuSelect(key: string) {
  router.push({ name: key })
}

function handleLogout() {
  clearTokens()
  router.push({ name: 'login' })
}
</script>
