import { computed, ref } from 'vue'
import { api, type AuthUser } from '@/lib/api'
import { unauthorized } from '@/lib/auth'
import { loadMeta, meta } from '@/lib/meta'

export const currentUser = ref<AuthUser | null>(null)
export const sessionReady = ref(false)
let loading: Promise<void> | null = null

export async function ensureSession(force = false): Promise<void> {
  if (loading && !force) return loading
  loading = (async () => {
    try {
      // Always ask the coordinator. Cached meta can still say there are no
      // accounts after the first one was created in another terminal.
      const result = await api.authMe()
      currentUser.value = result.user
      unauthorized.value = false
      if (result.authMode && meta.value) {
        meta.value = { ...meta.value, authMode: result.authMode }
      } else if (result.authMode) {
        const fresh = await loadMeta(true)
        if (fresh) meta.value = fresh
      }
    } catch {
      currentUser.value = null
      // Refresh the auth mode so the router and the 401 handler know whether
      // this deployment requires users or a token.
      const fresh = await loadMeta(true)
      if (fresh) meta.value = fresh
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

export const canWrite = computed(() => currentUser.value?.role === 'admin')
