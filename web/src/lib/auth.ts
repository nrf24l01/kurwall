import { ref } from 'vue'

const storageKey = 'kurwall.token'

export const apiToken = ref(localStorage.getItem(storageKey) ?? '')
export const unauthorized = ref(false)

export function setApiToken(value: string) {
  apiToken.value = value.trim()
  if (apiToken.value) localStorage.setItem(storageKey, apiToken.value)
  else localStorage.removeItem(storageKey)
  unauthorized.value = false
}

export function clearApiToken() {
  setApiToken('')
}