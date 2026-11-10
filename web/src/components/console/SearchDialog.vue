<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { Search } from '@lucide/vue'
import { api, type Node, type Policy } from '@/lib/api'
import { requestRefresh } from '@/lib/ui'
import { clearApiToken } from '@/lib/auth'

const props = withDefaults(
  defineProps<{
    open: boolean
    mode?: 'all' | 'actions'
  }>(),
  { mode: 'all' },
)
const emit = defineEmits<{ 'update:open': [value: boolean]; token: [] }>()

const router = useRouter()
const input = ref<HTMLInputElement | null>(null)
const query = ref('')
const scope = ref<'all' | 'actions'>(props.mode)
const active = ref(0)
const nodes = ref<Node[]>([])
const policies = ref<Policy[]>([])

const pageResults = [
  { id: 'page-overview', group: 'Go to', label: 'Overview', kind: 'page', keys: 'g d', to: '/' },
  { id: 'page-nodes', group: 'Go to', label: 'Nodes', kind: 'page', keys: 'g n', to: '/nodes' },
  { id: 'page-policies', group: 'Go to', label: 'Policies', kind: 'page', keys: 'g p', to: '/policies' },
  { id: 'page-asns', group: 'Go to', label: 'ASNs', kind: 'page', keys: 'g a', to: '/asns' },
  { id: 'page-events', group: 'Go to', label: 'Events', kind: 'page', keys: 'g e', to: '/events' },
  { id: 'page-settings', group: 'Go to', label: 'Settings', kind: 'page', keys: 'g s', to: '/settings' },
] as const

type Result = {
  id: string
  group: 'Go to' | 'Actions'
  label: string
  kind: string
  keys?: string
  to?: string
  run?: () => void
}

const results = computed<Result[]>(() => {
  const needle = query.value.trim().toLowerCase()
  const matches = (value: string) => !needle || value.toLowerCase().includes(needle)
  const found: Result[] = []

  if (scope.value === 'all') {
    for (const page of pageResults) if (matches(page.label)) found.push({ ...page })
    for (const node of nodes.value) {
      if (!matches(`${node.name} ${node.id} ${node.hostname}`)) continue
      found.push({
        id: `node-${node.id}`,
        group: 'Go to',
        label: node.name,
        kind: node.status === 'online' ? 'node · online' : `node · ${node.status}`,
        to: `/nodes/${encodeURIComponent(node.id)}`,
      })
    }
    for (const policy of policies.value) {
      if (!matches(`${policy.name} ${policy.description}`)) continue
      found.push({
        id: `policy-${policy.id}`,
        group: 'Go to',
        label: policy.name,
        kind: policy.enabled ? 'policy · enabled' : 'policy · disabled',
        to: `/policies/${encodeURIComponent(policy.id)}`,
      })
    }
  }

  const actions: Result[] = [
    {
      id: 'action-refresh',
      group: 'Actions',
      label: 'Refresh current page',
      kind: 'action',
      keys: 'r',
      run: () => requestRefresh(),
    },
    {
      id: 'action-token',
      group: 'Actions',
      label: 'Set API token',
      kind: 'action',
      run: () => emit('token'),
    },
    {
      id: 'action-clear-token',
      group: 'Actions',
      label: 'Clear API token',
      kind: 'action',
      run: () => {
        clearApiToken()
        window.location.reload()
      },
    },
    {
      id: 'action-settings',
      group: 'Actions',
      label: 'Open settings',
      kind: 'page',
      to: '/settings',
    },
  ]
  for (const action of actions) if (matches(action.label)) found.push(action)
  return found
})

const groups = computed(() => {
  const map = new Map<string, Result[]>()
  for (const result of results.value) {
    const list = map.get(result.group) ?? []
    list.push(result)
    map.set(result.group, list)
  }
  return [...map.entries()]
})

const flat = computed(() => results.value)

function close() {
  emit('update:open', false)
}

function run(result: Result | undefined) {
  if (!result) return
  close()
  if (result.to) {
    void router.push(result.to)
  } else {
    result.run?.()
  }
}

function move(delta: number) {
  if (flat.value.length === 0) return
  active.value = (active.value + delta + flat.value.length) % flat.value.length
}

function onInputKey(event: KeyboardEvent) {
  if (event.key === 'ArrowDown' || (event.ctrlKey && event.key.toLowerCase() === 'n')) {
    event.preventDefault()
    move(1)
    return
  }
  if (event.key === 'ArrowUp' || (event.ctrlKey && event.key.toLowerCase() === 'p')) {
    event.preventDefault()
    move(-1)
    return
  }
  if (event.key === 'Enter') {
    event.preventDefault()
    run(flat.value[active.value])
    return
  }
  if (event.key === 'Escape') {
    event.preventDefault()
    close()
    return
  }
  if (event.key === ':' && query.value === '') {
    event.preventDefault()
    scope.value = 'actions'
    active.value = 0
    return
  }
  if (event.key === 'Backspace' && query.value === '' && scope.value === 'actions') {
    event.preventDefault()
    scope.value = 'all'
    active.value = 0
  }
}

watch(query, () => {
  active.value = 0
})
watch(
  () => props.mode,
  (mode) => {
    scope.value = mode
    query.value = ''
    active.value = 0
  },
)
watch(
  () => props.open,
  async (open) => {
    if (!open) return
    scope.value = props.mode
    query.value = ''
    active.value = 0
    try {
      const [nodeList, policyList] = await Promise.all([api.nodes(), api.policies()])
      nodes.value = nodeList.items
      policies.value = policyList.items
    } catch {
      nodes.value = []
      policies.value = []
    }
    await nextTick()
    input.value?.focus()
  },
)

function onWindowKey(event: KeyboardEvent) {
  if (!props.open) return
  if (event.key === 'Escape') {
    event.preventDefault()
    close()
  }
}
if (typeof window !== 'undefined') window.addEventListener('keydown', onWindowKey, true)
onBeforeUnmount(() => window.removeEventListener('keydown', onWindowKey, true))
</script>

<template>
  <Teleport to="body">
    <div v-if="open" class="pc-backdrop" @click.self="close">
      <div class="pc-dialog search" role="dialog" aria-modal="true" aria-label="Search">
        <div class="search-head">
          <Search :size="16" aria-hidden="true" />
          <input
            ref="input"
            v-model="query"
            type="search"
            placeholder="Search nodes, policies, pages and actions"
            autocomplete="off"
            spellcheck="false"
            @keydown="onInputKey"
          />
          <span class="mode">{{ scope === 'actions' ? 'actions only' : 'everything' }}</span>
        </div>
        <div class="search-results">
          <template v-for="[group, items] in groups" :key="group">
            <div class="search-group">{{ group }}</div>
            <a
              v-for="result in items"
              :key="result.id"
              class="search-item"
              :class="{ active: flat.indexOf(result) === active }"
              href="#"
              @click.prevent="run(result)"
              @mouseenter="active = flat.indexOf(result)"
            >
              <span class="si-main">{{ result.label }}</span>
              <span class="si-kind">{{ result.kind }}</span>
              <span v-if="result.keys" class="si-key">{{ result.keys }}</span>
            </a>
          </template>
          <div v-if="flat.length === 0" class="empty">Nothing matches.</div>
        </div>
        <div class="search-foot pc-keys-only">
          <span><kbd>↑</kbd><kbd>↓</kbd> move</span>
          <span><kbd>⌃N</kbd><kbd>⌃P</kbd></span>
          <span><kbd>↵</kbd> open or run</span>
          <span><kbd>:</kbd> actions only</span>
          <span><kbd>esc</kbd> close</span>
        </div>
      </div>
    </div>
  </Teleport>
</template>