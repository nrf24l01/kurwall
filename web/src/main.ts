import { createApp } from 'vue'
import App from './App.vue'
import { router } from './router'
import { loadMeta, meta } from './lib/meta'
import { currentUser, ensureSession } from './lib/session'
import './style.css'

router.beforeEach(async (to) => {
  await loadMeta()
  await ensureSession()
  const mode = meta.value?.authMode ?? 'users'
  if (to.meta.public) {
    return mode === 'users' && currentUser.value ? '/' : true
  }
  if ((mode === 'users' || mode === 'unavailable') && !currentUser.value) {
    return { path: '/login', query: { next: to.fullPath } }
  }
  return true
})

const app = createApp(App).use(router)
router.isReady().then(() => app.mount('#app'))
