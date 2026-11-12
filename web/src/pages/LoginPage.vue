<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { api, errorMessage } from '@/lib/api'
import { unauthorized } from '@/lib/auth'
import { currentUser } from '@/lib/session'

const route = useRoute()
const router = useRouter()
const username = ref('')
const password = ref('')
const error = ref('')
const saving = ref(false)
const userInput = ref<HTMLInputElement | null>(null)
const insecure = location.protocol === 'http:' && !['localhost', '127.0.0.1'].includes(location.hostname)

function nextPath(): string {
  const next = typeof route.query.next === 'string' ? route.query.next : ''
  if (!next.startsWith('/') || next.startsWith('//') || next === '/login' || next.startsWith('/login?')) return '/'
  return next
}

async function submit() {
  if (saving.value || !username.value.trim() || !password.value) return
  saving.value = true
  error.value = ''
  try {
    const result = await api.login(username.value.trim(), password.value)
    currentUser.value = result.user
    unauthorized.value = false
    await router.replace(nextPath())
  } catch (err) {
    error.value = errorMessage(err)
    password.value = ''
  } finally {
    saving.value = false
  }
}

onMounted(() => userInput.value?.focus())
</script>

<template>
  <div class="auth">
    <form class="auth-panel" @submit.prevent="submit">
      <div class="auth-brand">
        <img class="logo-mark auth-logo" src="/logo-192.png" alt="" width="56" height="56" />
        <span class="brand"><b>Kurwall</b><span>Traffic control</span></span>
      </div>

      <div class="auth-head">
        <h1>Sign in</h1>
        <p class="field-hint">Use an account created on the coordinator.</p>
      </div>

      <div class="field">
        <label for="login-username">Username</label>
        <input
          id="login-username"
          ref="userInput"
          v-model="username"
          class="pc-inp"
          autocomplete="username"
          autocapitalize="none"
          spellcheck="false"
        />
      </div>
      <div class="field">
        <label for="login-password">Password</label>
        <input
          id="login-password"
          v-model="password"
          class="pc-inp"
          type="password"
          autocomplete="current-password"
        />
      </div>

      <p v-if="insecure" class="err">This page is served over HTTP. Credentials are not encrypted in transit.</p>
      <p v-if="error" class="err">{{ error }}</p>

      <button class="pc-btn pc-pri auth-submit" type="submit" :disabled="saving || !username.trim() || !password">
        {{ saving ? 'Signing in…' : 'Sign in' }}
      </button>
    </form>
  </div>
</template>
