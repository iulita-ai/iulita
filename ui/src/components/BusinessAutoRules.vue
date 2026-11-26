<template>
  <fieldset class="auto-rules"><legend>{{ t('secretary.autoRules') }}</legend>
    <p>{{ t('secretary.autoRulesHint') }}</p>
    <label v-for="value in classes" :key="value" class="check"><input type="checkbox" :checked="modelValue.classes.includes(value)" :disabled="disabled" @change="toggle(value, ($event.target as HTMLInputElement).checked)" />{{ t(`secretary.autoClasses.${value}`) }}</label>
    <label>{{ t('secretary.autoTimezone') }}<input :value="modelValue.timezone" maxlength="64" :disabled="disabled" @input="update('timezone', ($event.target as HTMLInputElement).value)" /></label>
    <div class="hours"><label>{{ t('secretary.autoStart') }}<input type="number" min="0" max="23" :value="modelValue.start_hour" :disabled="disabled" @input="update('start_hour', (($event.target as HTMLInputElement).value === '' ? Number.NaN : Number(($event.target as HTMLInputElement).value)))" /></label><label>{{ t('secretary.autoEnd') }}<input type="number" min="0" max="24" :value="modelValue.end_hour" :disabled="disabled" @input="update('end_hour', (($event.target as HTMLInputElement).value === '' ? Number.NaN : Number(($event.target as HTMLInputElement).value)))" /></label></div>
    <label>{{ t('secretary.autoDailyCap') }}<input type="number" min="1" max="10" :value="modelValue.max_replies_per_day" :disabled="disabled" @input="update('max_replies_per_day', (($event.target as HTMLInputElement).value === '' ? Number.NaN : Number(($event.target as HTMLInputElement).value)))" /></label>
    <p v-if="!validBusinessAutoRules(modelValue)" role="status">{{ t('secretary.autoRulesInvalid') }}</p>
  </fieldset>
</template>
<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { validBusinessAutoRules, type BusinessAutoRules } from '../businessApi'
const props = defineProps<{ modelValue: BusinessAutoRules; disabled?: boolean }>()
const emit = defineEmits<{ 'update:modelValue': [BusinessAutoRules] }>()
const { t } = useI18n()
const classes = ['greeting','acknowledgement','short_question']
function update(key: keyof BusinessAutoRules, value: string | number) { emit('update:modelValue', { ...props.modelValue, [key]: value }) }
function toggle(value: string, enabled: boolean) { emit('update:modelValue', { ...props.modelValue, classes: enabled ? [...new Set([...props.modelValue.classes, value])] : props.modelValue.classes.filter(item => item !== value) }) }
</script>
<style scoped>
.auto-rules { min-width: 0; border: 1px solid #ffffff30; border-radius: 8px; padding: 12px; }
label { display: grid; gap: 6px; margin-block: 12px; } .check { display: flex; gap: 10px; min-height: 44px; align-items: center; } .check input { min-width: 20px; min-height: 20px; }
input:not([type=checkbox]) { width: 100%; min-width: 0; box-sizing: border-box; min-height: 44px; background: #202024; color: inherit; border: 1px solid #ffffff40; border-radius: 7px; padding: 10px; font: inherit; }
p { font-size: 13px; color: #aaa; line-height: 1.5; }.hours { display: grid; grid-template-columns: repeat(2,minmax(0,1fr)); gap: 12px; }
</style>
