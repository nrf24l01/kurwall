import { computed, ref } from 'vue'
import { api, type AuthUser } from '@/lib/api'
import { unauthorized } from '@/lib/auth'
import { meta } from '@/lib/meta'

export const currentUser = ref<AuthUser | null>(null)
export const sessionReady = ref(false)
let loading: Promise<void> | null = null

export async function ensureSession(force = false): Promise<void> {
  if (loading && !force) return loading
  loading = (async () => {
    if (!meta.value) return
    if (meta.value.authMode === 'open') {
      currentUser.value = { id: '', username: 'operator', role: 'admin', kind: 'open' }
      sessionReady.value = true
      return
    }
    try {
      const result = await api.authMe()
      currentUser.value = result.user
      unauthorized.value = false
      if (result.authMode && meta.value) meta.value = { ...meta.value, authMode: result.authMode }
    } catch {
      currentUser.value = null
    } finally {
      sessionReady.value = true
    }
  })()
  try {
    await loading
  } finally {
    loading = null
  }
}

export async function signOutSession(): Promise<void> {
  try {
    await api.logout()
  } catch {
    // A missing session is still a successful local sign-out.
  }
  currentUser.value = null
}

export const canWrite = computed(
  () => currentUser.value?.role === 'admin' || meta.value?.authMode === 'open',
)
