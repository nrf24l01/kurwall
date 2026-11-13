<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { RouterLink, RouterView, useRoute, useRouter } from 'vue-router'
import { LogOut, Menu, Search } from '@lucide/vue'
import ConsoleDialog from '@/components/console/ConsoleDialog.vue'
import KeyLine from '@/components/console/KeyLine.vue'
import KeysDialog from '@/components/console/KeysDialog.vue'
import SearchDialog from '@/components/console/SearchDialog.vue'
import StatusMark from '@/components/console/StatusMark.vue'
import { api, type Node } from '@/lib/api'
import { liveNodeStatus, startLive, stopLive } from '@/lib/live'
import { apiToken, clearApiToken, setApiToken, unauthorized } from '@/lib/auth'
import { loadMeta, meta } from '@/lib/meta'
import { currentUser, signOutSession } from '@/lib/session'
import { requestRefresh } from '@/lib/ui'

const route = useRoute()
const router = useRouter()

const nodes = ref<Node[]>([])
const headerError = ref(false)
const tokenOpen = ref(false)
const tokenDraft = ref(apiToken.value)
const isPublic = computed(() => Boolean(route.meta.public))
let shellStarted = false
const searchOpen = ref(false)
const searchMode = ref<'all' | 'actions'>('all')
const keysOpen = ref(false)
const whichKey = ref(false)
const drawerOpen = ref(false)
let refreshTimer: number | undefined

const groups = [
  {
    label: 'Monitor',
    items: [
      { to: '/', label: 'Overview', key: 'd' },
      { to: '/events', label: 'Events', key: 'e' },
    ],
  },
  {
    label: 'Control',
    items: [
      { to: '/nodes', label: 'Nodes', key: 'n' },
      { to: '/policies', label: 'Policies', key: 'p' },
      { to: '/asns', label: 'ASNs', key: 'a' },
    ],
  },
  {
    label: 'System',
    items: [{ to: '/settings', label: 'Settings', key: 's' }],
  },
]

const online = computed(() => nodes.value.filter((node) => node.status === 'online').length)
const total = computed(() => nodes.value.length)
const coordinatorState = computed<'ok' | 'look' | 'broken' | 'unknown'>(() => {
  if (unauthorized.value || headerError.value) return 'broken'
  if (!meta.value) return 'broken'
  if (total.value === 0) return 'unknown'
  if (online.value < total.value) return 'look'
  return 'ok'
})
const nodeWord = computed(() => {
  if (total.value === 0) return 'no nodes'
  if (online.value === total.value) return 'all online'
  return `${online.value}/${total.value} online`
})
const warning = computed(() => {
  if (unauthorized.value) return meta.value?.authMode === 'users' ? 'sign-in required' : 'API token required'
  if (headerError.value || !meta.value) return 'coordinator unreachable'
  return ''
})
const keylineLeft = computed(() =>
  whichKey.value ? 'g then a key from the list · esc cancel' : ((route.meta.keys as string | undefined) ?? 'j k move · ↵ open'),
)
const liveText = computed(() => {
  if (unauthorized.value) return 'unauthorized'
  if (headerError.value || !meta.value) return 'coordinator offline'
  return 'coordinator online'
})

function isActive(to: string) {
  if (to === '/') return route.path === '/'
  return route.path === to || route.path.startsWith(`${to}/`)
}

function openToken() {
  tokenDraft.value = apiToken.value
  tokenOpen.value = true
}

function saveToken() {
  setApiToken(tokenDraft.value)
  tokenOpen.value = false
  window.location.reload()
}

async function signOut() {
  if (meta.value?.authMode === 'users') {
    await signOutSession()
    stopShell()
    await router.replace('/login')
    return
  }
  clearApiToken()
  window.location.reload()
}

function openSearch(mode: 'all' | 'actions') {
  searchMode.value = mode
  searchOpen.value = true
}

function openTokenFromSearch() {
  searchOpen.value = false
  openToken()
}

async function loadHeader() {
  if (isPublic.value) return
  try {
    const [nodeList, metaResult] = await Promise.all([api.nodes(), loadMeta()])
    nodes.value = nodeList.items
    if (metaResult) meta.value = metaResult
    headerError.value = false
    unauthorized.value = false
  } catch {
    headerError.value = true
  }
}

function startShell() {
  if (isPublic.value || shellStarted) return
  shellStarted = true
  startLive()
  void loadHeader()
  refreshTimer = window.setInterval(() => {
    if (document.visibilityState === 'visible') void loadHeader()
  }, 30000)
}

function stopShell() {
  shellStarted = false
  if (refreshTimer) {
    window.clearInterval(refreshTimer)
    refreshTimer = undefined
  }
  stopLive()
}

function go(letter: string) {
  const map: Record<string, string> = { d: '/', n: '/nodes', p: '/policies', a: '/asns', e: '/events', s: '/settings' }
  if (letter === 'g') {
    window.scrollTo({ top: 0, behavior: 'smooth' })
    return
  }
  const target = map[letter]
  if (target) void router.push(target)
}

function isField(target: EventTarget | null) {
  const element = target as HTMLElement | null
  if (!element) return false
  return Boolean(element.closest('input, textarea, select, [contenteditable="true"]'))
}

function onGlobalKey(event: KeyboardEvent) {
  if (isPublic.value) return
  if (event.metaKey || event.ctrlKey || event.altKey) return
  if (document.querySelector('.pc-backdrop')) return

  if (event.key === 'Escape') {
    if (isField(event.target)) {
      const field = event.target as HTMLElement
      field.blur()
      return
    }
    whichKey.value = false
    drawerOpen.value = false
    return
  }

  if (whichKey.value) {
    event.preventDefault()
    whichKey.value = false
    go(event.key.toLowerCase())
    return
  }

  if (isField(event.target)) return

  if (event.key === 'g') {
    event.preventDefault()
    whichKey.value = true
    return
  }
  if (event.key === '/') {
    event.preventDefault()
    openSearch('all')
    return
  }
  if (event.key === ':') {
    event.preventDefault()
    openSearch('actions')
    return
  }
  if (event.key === '?') {
    event.preventDefault()
    keysOpen.value = true
    return
  }
  if (event.key === 'i') {
    event.preventDefault()
    document.querySelector<HTMLElement>('.page input, .page select, .page textarea')?.focus()
    return
  }
  if (event.key === 'r') {
    event.preventDefault()
    requestRefresh()
  }
}

watch(unauthorized, async (value) => {
  if (!value) return
  // The cached mode may be stale after the first account was created.
  // Re-read it before deciding between the login page and the token dialog.
  const fresh = await loadMeta(true)
  if (fresh) meta.value = fresh
  const mode = fresh?.authMode ?? meta.value?.authMode ?? 'users'
  if (mode === 'users') {
    stopShell()
    if (route.path !== '/login') {
      await router.replace({ path: '/login', query: { next: route.fullPath } })
    }
    return
  }
  if (mode === 'token') openToken()
})
watch(
  () => route.path,
  () => {
    drawerOpen.value = false
    whichKey.value = false
    if (!isPublic.value) startShell()
  },
)
watch(
  liveNodeStatus,
  (statuses) => {
    const allowed = new Set(['online', 'offline', 'pending'])
    nodes.value = nodes.value.map((node) => {
      const status = statuses[node.id]
      return status && allowed.has(status) ? { ...node, status: status as Node['status'] } : node
    })
  },
  { deep: true },
)

onMounted(() => {
  startShell()
  window.addEventListener('keydown', onGlobalKey)
})
onBeforeUnmount(() => {
  stopShell()
  window.removeEventListener('keydown', onGlobalKey)
})
</script>

<template>
  <RouterView v-if="isPublic" />
  <div v-else class="app">
    <header class="hdr">
      <button class="hdr-menu" type="button" aria-label="Menu" @click="drawerOpen = true"><Menu :size="18" /></button>
      <RouterLink class="hdr-logo" to="/">
        <img class="logo-mark" src="/logo-192.png" alt="" width="40" height="40" />
        <span class="brand"><b>Kurwall</b><span>Traffic control</span></span>
      </RouterLink>

      <button class="hdr-search" type="button" @click="openSearch('all')">
        <Search :size="14" aria-hidden="true" />
        <span>Search nodes, policies and pages</span>
        <span class="search-hint pc-keys-only"><kbd>/</kbd></span>
      </button>
      <button class="hdr-search phone" type="button" aria-label="Search" @click="openSearch('all')">
        <Search :size="18" />
      </button>

      <div class="hdr-cell" :title="liveText">
        <StatusMark :state="coordinatorState" :word="nodeWord" />
      </div>
      <div v-if="warning" class="hdr-cell warn" :title="warning">
        <StatusMark state="broken" :word="warning" />
      </div>
      <div class="hdr-user" :title="currentUser?.role === 'admin' ? 'Administrator' : currentUser?.role === 'viewer' ? 'Viewer' : 'Operator'">
        {{ currentUser?.username ?? 'operator' }}
      </div>
      <button class="hdr-out" type="button" aria-label="Sign out" title="Sign out" @click="signOut">
        <LogOut :size="16" />
      </button>
    </header>

    <div class="workspace">
      <aside class="side" :class="{ open: drawerOpen }">
        <div class="side-scroll">
          <div v-for="group in groups" :key="group.label">
            <div class="side-group">{{ group.label }}</div>
            <RouterLink
              v-for="item in group.items"
              :key="item.to"
              :to="item.to"
              class="side-item"
              :class="{ active: isActive(item.to) }"
            >
              <span>{{ item.label }}</span>
            </RouterLink>
          </div>
        </div>
        <div class="side-ver">Kurwall {{ meta?.version ?? 'dev' }}</div>
      </aside>
      <div v-if="drawerOpen" class="drawer-backdrop" @click="drawerOpen = false" />

      <main class="main">
        <RouterView />
      </main>
    </div>

    <KeyLine :left="keylineLeft" :status="coordinatorState" :status-text="liveText" />

    <div v-if="whichKey" class="whichkey">
      <button type="button" @click="go('d')"><b>d</b> Overview</button>
      <button type="button" @click="go('n')"><b>n</b> Nodes</button>
      <button type="button" @click="go('p')"><b>p</b> Policies</button>
      <button type="button" @click="go('a')"><b>a</b> ASNs</button>
      <button type="button" @click="go('e')"><b>e</b> Events</button>
      <button type="button" @click="go('s')"><b>s</b> Settings</button>
      <button type="button" @click="go('g')"><b>g</b> Top</button>
      <button type="button" @click="whichKey = false"><b>esc</b> cancel</button>
    </div>

    <SearchDialog
      v-model:open="searchOpen"
      :mode="searchMode"
      @token="openTokenFromSearch"
    />
    <KeysDialog v-model:open="keysOpen" />

    <ConsoleDialog v-model:open="tokenOpen" title="API token" width="min(460px, 100%)">
      <div class="stack">
        <p class="field-hint">
          The token is stored locally and sent as a Bearer credential. Leave it empty when the coordinator does not
          require one.
        </p>
        <div class="field">
          <label for="api-token">Token</label>
          <input id="api-token" v-model="tokenDraft" class="pc-inp" type="password" autocomplete="off" placeholder="Paste API token" />
        </div>
      </div>
      <template #footer>
        <button class="pc-btn pc-pri" type="button" @click="saveToken">Save and reload</button>
        <button class="pc-btn pc-ghost" type="button" @click="tokenOpen = false">Cancel</button>
      </template>
    </ConsoleDialog>
  </div>
</template>
