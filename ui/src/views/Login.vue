<template>
  <n-config-provider :theme="darkTheme">
    <n-message-provider>
      <div class="login-container">
        <n-card title="Iulita" style="width: 380px">
          <n-form @submit.prevent="handleLogin">
            <n-form-item :label="t('login.username')">
              <n-input v-model:value="username" :placeholder="t('login.username')" :disabled="loading" @keydown.enter="handleLogin" />
            </n-form-item>
            <n-form-item :label="t('login.password')">
              <n-input v-model:value="password" type="password" :placeholder="t('login.password')" :disabled="loading" show-password-on="click" @keydown.enter="handleLogin" />
            </n-form-item>
            <n-button type="primary" block :loading="loading" @click="handleLogin">
              {{ $t('login.signIn') }}
            </n-button>
            <n-alert v-if="error" type="error" style="margin-top: 12px" :title="error" />
          </n-form>

          <template v-if="mustChangePassword">
            <n-divider />
            <n-text type="warning">{{ $t('login.mustChangePassword') }}</n-text>
            <n-form style="margin-top: 12px" @submit.prevent="handleChangePassword">
              <n-form-item :label="t('login.newPassword')">
                <n-input v-model:value="newPassword" type="password" :placeholder="t('login.newPasswordHint')" show-password-on="click" />
              </n-form-item>
              <n-form-item :label="t('login.confirmPassword')">
                <n-input v-model:value="confirmPassword" type="password" :placeholder="t('login.confirmPasswordHint')" show-password-on="click" @keydown.enter="handleChangePassword" />
              </n-form-item>
              <n-button type="warning" block :loading="changingPassword" @click="handleChangePassword">
                {{ $t('login.changePassword') }}
              </n-button>
              <n-alert v-if="changeError" type="error" style="margin-top: 12px" :title="changeError" />
            </n-form>
          </template>
        </n-card>
      </div>
    </n-message-provider>
  </n-config-provider>
</template>

<script setup lang="ts">
import { ref } from 'vue'
import { useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { darkTheme, NConfigProvider, NMessageProvider, NCard, NForm, NFormItem, NInput, NButton, NAlert, NDivider, NText } from 'naive-ui'
import { api, setTokens, clearTokens } from '../api'

const { t } = useI18n()
const router = useRouter()

const username = ref('')
const password = ref('')
const loading = ref(false)
const error = ref('')
const mustChangePassword = ref(false)

const newPassword = ref('')
const confirmPassword = ref('')
const changingPassword = ref(false)
const changeError = ref('')

async function redirectAfterLogin() {
  try {
    const sys = await api.getSystem()
    if (sys.setup_mode || (sys.wizard_completed === false)) {
      router.push('/setup')
      return
    }
  } catch {
    // If system check fails, proceed to dashboard
  }
  router.push('/')
}

async function handleLogin() {
  if (!username.value || !password.value) {
    error.value = t('login.usernameRequired')
    return
  }
  loading.value = true
  error.value = ''
  try {
    const resp = await api.login(username.value, password.value)
    setTokens(resp.access_token, resp.refresh_token)
    if (resp.must_change_password) {
      mustChangePassword.value = true
    } else {
      await redirectAfterLogin()
    }
  } catch (e: any) {
    error.value = e.message?.includes('401') ? t('login.invalidCredentials') : t('login.loginFailed')
    clearTokens()
  } finally {
    loading.value = false
  }
}

async function handleChangePassword() {
  if (newPassword.value.length < 6) {
    changeError.value = t('login.passwordTooShort')
    return
  }
  if (newPassword.value !== confirmPassword.value) {
    changeError.value = t('login.passwordMismatch')
    return
  }
  changingPassword.value = true
  changeError.value = ''
  try {
    await api.changePassword(password.value, newPassword.value)
    // Re-login with new password to get fresh tokens
    const resp = await api.login(username.value, newPassword.value)
    setTokens(resp.access_token, resp.refresh_token)
    await redirectAfterLogin()
  } catch (e: any) {
    changeError.value = t('login.changePasswordFailed')
  } finally {
    changingPassword.value = false
  }
}
</script>

<style scoped>
.login-container {
  display: flex;
  align-items: center;
  justify-content: center;
  min-height: 100vh;
  background: #1a1a2e;
}
</style>
