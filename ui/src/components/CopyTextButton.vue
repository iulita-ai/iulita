<template>
  <div class="copy-action">
    <button
      type="button"
      class="copy-button"
      :aria-label="t('chat.copyMessage')"
      :aria-busy="state === 'copying'"
      :disabled="state === 'copying'"
      @click="copy"
    >{{ t('chat.copy') }}</button>
    <span role="status" aria-live="polite" class="copy-status">
      {{ state === 'copied' ? t('chat.copied') : state === 'error' ? t('chat.copyError') : state === 'unavailable' ? t('chat.copyUnavailable') : '' }}
    </span>
  </div>
</template>

<script setup lang="ts">
import { ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'

const props = defineProps<{ text: string }>()
const { t } = useI18n()
const state = ref<'idle' | 'copying' | 'copied' | 'error' | 'unavailable'>('idle')
watch(() => props.text, () => { state.value = 'idle' })

async function copy() {
  if (state.value === 'copying') return
  if (!navigator.clipboard?.writeText) {
    state.value = 'unavailable'
    return
  }
  state.value = 'copying'
  try {
    await navigator.clipboard.writeText(props.text)
    state.value = 'copied'
  } catch {
    // Never show browser errors or message contents in the error notification.
    state.value = 'error'
  }
}
</script>

<style scoped>
.copy-action {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 4px 8px;
  margin-top: 4px;
}
.copy-button {
  min-height: 36px;
  padding: 4px 10px;
  border: 1px solid currentColor;
  border-radius: 6px;
  background: transparent;
  color: inherit;
  cursor: pointer;
  font: inherit;
  font-size: 12px;
}
.copy-button:focus-visible {
  outline: 2px solid currentColor;
  outline-offset: 2px;
}
.copy-button:disabled { cursor: wait; }
.copy-status { font-size: 12px; }
</style>
