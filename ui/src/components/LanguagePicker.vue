<script setup lang="ts">
import { NSelect } from 'naive-ui'
import { useLocale } from '../composables/useLocale'
import { useI18n } from 'vue-i18n'
import { computed } from 'vue'

const { locale, setLocale } = useLocale()
const { t } = useI18n()

const options = computed(() => [
  { label: t('locale.en'), value: 'en' },
  { label: t('locale.ru'), value: 'ru' },
  { label: t('locale.zh'), value: 'zh' },
  { label: t('locale.es'), value: 'es' },
  { label: t('locale.fr'), value: 'fr' },
  { label: t('locale.he'), value: 'he' },
])

function handleUpdate(val: string) {
  setLocale(val)
}
</script>

<template>
  <n-select
    :value="locale"
    :options="options"
    size="small"
    style="width: 130px"
    @update:value="handleUpdate"
  />
</template>
