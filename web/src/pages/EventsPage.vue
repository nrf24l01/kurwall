<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import ConsoleDialog from '@/components/console/ConsoleDialog.vue'
import TrafficConditionBuilder from '@/components/events/TrafficConditionBuilder.vue'
import PageHead from '@/components/console/PageHead.vue'
import StatusMark from '@/components/console/StatusMark.vue'
import { api, errorMessage, type DnsEvent, type Node, type Page, type TrafficCondition, type TrafficEvent, type TrafficMatch } from '@/lib/api'
import {
  backgroundFetchLimit,
  canApplyResponse,
  commitActiveWindow,
  DEFAULT_LIVE_WINDOW_MS,
  isCurrentPageResponse,
  isStaleResponse,
  LIVE_SCROLL_DEFER_PX,
  liveRefreshDecision,
  MAX_EVENTS_LIMIT,
  parseLocalDateTime,
  resolveQueryWindow,
  snapshotQueryWindow,
  windowDurationMs,
} from '@/lib/eventLive'
import { liveBlocks, liveConnected, liveDns, liveFlows } from '@/lib/live'
import { countryFlag, formatBytes, formatDuration, formatNumber, formatTime, protocolName } from '@/lib/utils'
import { fieldDefinition } from '@/lib/trafficFilters'
import { useRefresh } from '@/lib/ui'

type Tab = 'traffic' | 'dns'

const tab = ref<Tab>('traffic')
const nodeId = ref('all')
const search = ref('')
const container = ref('')
const ip = ref('')
const filter = ref('all')
const match = ref<TrafficMatch>('all')
const conditions = ref<TrafficCondition[]>([])
const excludeLocal = ref(false)
const limit = 50
const loadingMore = ref(false)
const live = ref(true)
const liveDurationMs = ref(DEFAULT_LIVE_WINDOW_MS)
const livePending = ref(false)
const liveRefreshing = ref(false)

const LIVE_REFRESH_MS = 5000
const LIVE_DEBOUNCE_MS = 1000
let disposed = false
let requestSeq = 0
let liveRefreshTimer: number | undefined
let liveDebounceTimer: number | undefined
let autoRefreshQueued = false

function toLocalInput(date: Date): string {
  const pad = (value: number) => String(value).padStart(2, '0')
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}T${pad(date.getHours())}:${pad(date.getMinutes())}`
}

const now = new Date()
const fromInput = ref(toLocalInput(new Date(now.getTime() - DEFAULT_LIVE_WINDOW_MS)))
const toInput = ref(toLocalInput(now))
const activeFrom = ref('')
const activeTo = ref('')

function defaultWindow() {
  const to = new Date()
  fromInput.value = toLocalInput(new Date(to.getTime() - DEFAULT_LIVE_WINDOW_MS))
  toInput.value = toLocalInput(to)
}

/** Freeze the From→To width before a live window starts rolling. */
function captureLiveDuration() {
  liveDurationMs.value = windowDurationMs(parseLocalDateTime(fromInput.value), parseLocalDateTime(toInput.value))
}

const nodes = ref<Node[]>([])
const traffic = ref<Page<TrafficEvent> | null>(null)
const dns = ref<Page<DnsEvent> | null>(null)
const loading = ref(true)
const error = ref('')

const detailOpen = ref(false)
const detailTitle = ref('')
const detailFields = ref<{ label: string; value: string }[]>([])

const tabs: { value: Tab; label: string }[] = [
  { value: 'traffic', label: 'Traffic' },
  { value: 'dns', label: 'DNS' },
]

const nodeNames = computed(() => new Map(nodes.value.map((node) => [node.id, node.name])))
const total = computed(() => (tab.value === 'traffic' ? traffic.value?.total : dns.value?.total) ?? 0)
const page = computed(() => (tab.value === 'traffic' ? traffic.value : dns.value))
const hasMore = computed(() => {
  const current = page.value
  return Boolean(current && current.items.length < current.total)
})
const tabLabel = computed(() => tabs.find((item) => item.value === tab.value)?.label ?? '')
const subtitle = computed(() => `${formatNumber(total.value)} ${tabLabel.value.toLowerCase()} · ${windowLabel.value}`)
const windowLabel = computed(() => {
  const from = fromInput.value.replace('T', ' ')
  const to = toInput.value.replace('T', ' ')
  return from && to ? `${from} → ${to}` : 'custom range'
})
const completeConditions = computed(() =>
  conditions.value.filter((condition) => condition.field && condition.operator && condition.value.trim()),
)
const conditionSignature = computed(() =>
  JSON.stringify({ match: match.value, conditions: completeConditions.value }),
)
const loadedCount = computed(() => (tab.value === 'traffic' ? traffic.value?.items.length : dns.value?.items.length) ?? 0)
const liveNotice = computed(() => {
  if (!livePending.value) return ''
  if (loadedCount.value > MAX_EVENTS_LIMIT) {
    return `Live updates paused because ${loadedCount.value} rows are loaded; one refresh can preserve at most ${MAX_EVENTS_LIMIT}.`
  }
  return 'New events are available. Scroll to the top or refresh to load them.'
})

const filterOptions = [
  { value: 'all', label: 'Any result' },
  { value: 'allowed', label: 'Resolved' },
  { value: 'blocked', label: 'Blocked' },
]

const EVENTS_STORAGE_KEY = 'kurwall.events.filters.v1'

interface StoredEventsFilters {
  from: string
  to: string
  nodeId: string
  match: TrafficMatch
  conditions: TrafficCondition[]
  excludeLocal: boolean
  live: boolean
  tab: Tab
  search: string
  container: string
  ip: string
  filter: string
}

function restoreFilters() {
  try {
    const raw = localStorage.getItem(EVENTS_STORAGE_KEY)
    if (!raw) return
    const data = JSON.parse(raw) as Partial<StoredEventsFilters>
    if (typeof data.from === 'string' && data.from) fromInput.value = data.from
    if (typeof data.to === 'string' && data.to) toInput.value = data.to
    if (typeof data.nodeId === 'string') nodeId.value = data.nodeId
    if (data.match === 'all' || data.match === 'any') match.value = data.match
    if (Array.isArray(data.conditions)) {
      conditions.value = data.conditions
        .filter((condition) => {
          if (!condition || typeof condition.field !== 'string' || typeof condition.operator !== 'string') return false
          const definition = fieldDefinition(condition.field)
          return Boolean(definition && definition.operators.includes(condition.operator) && typeof condition.value === 'string')
        })
        .map((condition) => ({
          field: condition.field,
          operator: condition.operator,
          value: condition.value,
          negate: Boolean(condition.negate),
        }))
    }
    if (typeof data.excludeLocal === 'boolean') excludeLocal.value = data.excludeLocal
    if (typeof data.live === 'boolean') live.value = data.live
    if (data.tab === 'traffic' || data.tab === 'dns') tab.value = data.tab
    if (typeof data.search === 'string') search.value = data.search
    if (typeof data.container === 'string') container.value = data.container
    if (typeof data.ip === 'string') ip.value = data.ip
    if (typeof data.filter === 'string') filter.value = data.filter
  } catch {
    // Ignore a corrupt or stale stored filter; the defaults still work.
  }
}

function saveFilters() {
  const state: StoredEventsFilters = {
    from: fromInput.value,
    to: toInput.value,
    nodeId: nodeId.value,
    match: match.value,
    conditions: conditions.value,
    excludeLocal: excludeLocal.value,
    live: live.value,
    tab: tab.value,
    search: search.value,
    container: container.value,
    ip: ip.value,
    filter: filter.value,
  }
  try {
    localStorage.setItem(EVENTS_STORAGE_KEY, JSON.stringify(state))
  } catch {
    // Storage can be unavailable in private modes; filtering still works.
  }
}

restoreFilters()
// Restored live pages keep the stored From→To width but roll it up to now.
if (live.value) captureLiveDuration()

function qtypeName(value: number): string {
  const names: Record<number, string> = { 1: 'A', 2: 'NS', 5: 'CNAME', 6: 'SOA', 12: 'PTR', 15: 'MX', 16: 'TXT', 28: 'AAAA', 33: 'SRV', 64: 'SVCB', 65: 'HTTPS' }
  return names[value] ?? String(value)
}

function endpoint(ip: string, port: number): string {
  const host = ip.includes(':') ? `[${ip}]` : ip
  return `${host}${port > 0 ? `:${port}` : ''}`
}

function targetLabel(event: TrafficEvent): string {
  return event.target || event.sni || event.domain || '—'
}

function asnLabel(event: TrafficEvent): string {
  if (event.dstAsnName) return event.dstAsnName
  if (event.dstAsn) return `AS${event.dstAsn}`
  return '—'
}

function egressLabel(event: TrafficEvent): string {
  if (event.kind !== 'connection' || event.bytesOut === undefined) return '—'
  return event.bytesOut > 0 ? formatBytes(event.bytesOut) : '0 B'
}

let searchTimer: number | undefined
let conditionTimer: number | undefined
let skipSearchWatch = false

function cancelSearch() {
  if (searchTimer !== undefined) {
    window.clearTimeout(searchTimer)
    searchTimer = undefined
  }
}

function cancelConditions() {
  if (conditionTimer !== undefined) {
    window.clearTimeout(conditionTimer)
    conditionTimer = undefined
  }
}

function maybeLoadMore() {
  if (disposed || loading.value || loadingMore.value || liveRefreshing.value || !hasMore.value) return
  if (window.innerHeight + window.scrollY >= document.documentElement.scrollHeight - 400) {
    void loadMore()
  }
}

interface LoadOptions {
  /** Keep existing rows on screen and fetch at least as many from the server. */
  background?: boolean
}

async function load(options: LoadOptions = {}) {
  if (disposed) return
  const background = options.background === true
  const seq = ++requestSeq
  const resolved = resolveQueryWindow({
    live: live.value,
    fromInput: fromInput.value,
    toInput: toInput.value,
    durationMs: liveDurationMs.value,
  })
  if (!resolved.ok) {
    error.value = resolved.error
    loading.value = false
    liveRefreshing.value = false
    return
  }

  const range = resolved.window
  // Build the request from a local snapshot; the active snapshot only moves
  // forward with data that is actually accepted below.
  const requested = snapshotQueryWindow(range)
  error.value = ''
  loading.value = !background
  liveRefreshing.value = background

  const requestedTab = tab.value
  const loaded = requestedTab === 'traffic' ? traffic.value?.items.length ?? 0 : dns.value?.items.length ?? 0
  const fetchLimit = background ? backgroundFetchLimit(loaded, limit) : limit
  const common = {
    nodeId: nodeId.value === 'all' ? undefined : nodeId.value,
    limit: fetchLimit,
    offset: 0,
    from: requested.from || undefined,
    to: requested.to,
  }
  let nextTraffic: Page<TrafficEvent> | null = null
  let nextDns: Page<DnsEvent> | null = null
  let succeeded = false
  try {
    if (requestedTab === 'traffic') {
      const result = await api.traffic({
        ...common,
        excludeLocal: excludeLocal.value || undefined,
        match: match.value,
        filter: completeConditions.value.length > 0 ? JSON.stringify(completeConditions.value) : undefined,
      })
      if (!canApplyResponse(disposed, seq, requestSeq)) return
      nextTraffic = result
    } else {
      const result = await api.dns({
        ...common,
        container: container.value || undefined,
        domain: search.value || undefined,
        clientIp: ip.value || undefined,
        blocked: filter.value === 'all' ? undefined : filter.value === 'blocked',
      })
      if (!canApplyResponse(disposed, seq, requestSeq)) return
      nextDns = result
    }
    succeeded = true
  } catch (err) {
    if (!canApplyResponse(disposed, seq, requestSeq)) return
    error.value = errorMessage(err)
    if (background) livePending.value = true
  } finally {
    if (!disposed && !isStaleResponse(seq, requestSeq)) {
      loading.value = false
      liveRefreshing.value = false
      if (succeeded && background) livePending.value = false
      if (autoRefreshQueued) scheduleLiveRefresh()
      void nextTick(() => {
        if (!disposed) maybeLoadMore()
      })
    }
  }

  const committed = commitActiveWindow(
    { from: activeFrom.value, to: activeTo.value },
    requested,
    succeeded,
  )
  activeFrom.value = committed.from
  activeTo.value = committed.to
  if (succeeded) {
    if (range.rolling) {
      liveDurationMs.value = range.durationMs
      if (range.from) fromInput.value = toLocalInput(range.from)
      toInput.value = toLocalInput(range.to)
    }
    if (nextTraffic !== null) traffic.value = nextTraffic
    if (nextDns !== null) dns.value = nextDns
  }
}

async function loadMore() {
  if (disposed || loading.value || loadingMore.value || liveRefreshing.value) return
  const active = page.value
  if (!active || active.items.length >= active.total) return
  const requestedTab = tab.value
  const seq = requestSeq
  const expectedOffset = active.items.length
  const expectedFrom = activeFrom.value
  const expectedTo = activeTo.value
  loadingMore.value = true
  error.value = ''
  const common = {
    nodeId: nodeId.value === 'all' ? undefined : nodeId.value,
    limit,
    from: activeFrom.value || undefined,
    to: activeTo.value,
  }
  try {
    if (requestedTab === 'traffic') {
      const current = traffic.value
      if (!current || current.items.length >= current.total) return
      const next = await api.traffic({
        ...common,
        offset: expectedOffset,
        excludeLocal: excludeLocal.value || undefined,
        match: match.value,
        filter: completeConditions.value.length > 0 ? JSON.stringify(completeConditions.value) : undefined,
      })
      if (!sameLoadedPage(requestedTab, seq, expectedOffset, expectedFrom, expectedTo)) return
      const latest = traffic.value
      if (!latest) return
      const seen = new Set(latest.items.map((item) => item.eventId))
      const items = [...latest.items, ...next.items.filter((item) => !seen.has(item.eventId))]
      traffic.value = { ...next, items }
    } else {
      const current = dns.value
      if (!current || current.items.length >= current.total) return
      const next = await api.dns({
        ...common,
        container: container.value || undefined,
        offset: expectedOffset,
        domain: search.value || undefined,
        clientIp: ip.value || undefined,
        blocked: filter.value === 'all' ? undefined : filter.value === 'blocked',
      })
      if (!sameLoadedPage(requestedTab, seq, expectedOffset, expectedFrom, expectedTo)) return
      const latest = dns.value
      if (!latest) return
      const seen = new Set(latest.items.map((item) => item.eventId))
      const items = [...latest.items, ...next.items.filter((item) => !seen.has(item.eventId))]
      dns.value = { ...next, items }
    }
  } catch (err) {
    if (canApplyResponse(disposed, seq, requestSeq)) error.value = errorMessage(err)
  } finally {
    if (!disposed) {
      loadingMore.value = false
      if (!isStaleResponse(seq, requestSeq)) {
        void nextTick(() => {
          if (!disposed) maybeLoadMore()
        })
        if (autoRefreshQueued) scheduleLiveRefresh()
      }
    }
  }
}

/** Reject a page append if a newer load, tab change or window won the race. */
function sameLoadedPage(tabAtStart: Tab, seq: number, offset: number, from: string, to: string): boolean {
  return isCurrentPageResponse({
    requestSeq: seq,
    currentSeq: requestSeq,
    requestedTab: tabAtStart,
    currentTab: tab.value,
    expectedFrom: from,
    currentFrom: activeFrom.value,
    expectedTo: to,
    currentTo: activeTo.value,
    expectedOffset: offset,
    loaded: loadedCount.value,
    disposed,
  })
}

function runLiveRefresh() {
  if (disposed) return
  autoRefreshQueued = false
  void load({ background: true })
}

/**
 * Coalesce websocket batches. The trailing timer fires once per burst instead
 * of restarting for every event, so a busy node cannot starve the refresh.
 */
function scheduleLiveRefresh() {
  if (disposed || !live.value || liveDebounceTimer !== undefined) return
  liveDebounceTimer = window.setTimeout(() => {
    liveDebounceTimer = undefined
    requestLiveRefresh()
  }, LIVE_DEBOUNCE_MS)
}

function requestLiveRefresh(force = false) {
  if (disposed) return
  const decision = liveRefreshDecision({
    live: live.value,
    visible: document.visibilityState === 'visible',
    loaded: loadedCount.value,
    scrollY: window.scrollY,
    force,
  })
  if (decision === 'skip') return
  if (decision === 'defer') {
    livePending.value = true
    return
  }
  if (loading.value || loadingMore.value || liveRefreshing.value) {
    autoRefreshQueued = true
    return
  }
  runLiveRefresh()
}

function refreshLiveNow() {
  if (disposed) return
  livePending.value = false
  autoRefreshQueued = false
  if (loading.value || loadingMore.value || liveRefreshing.value) {
    autoRefreshQueued = true
    return
  }
  void load({ background: true })
}

function stopLiveTimers() {
  if (liveRefreshTimer !== undefined) {
    window.clearInterval(liveRefreshTimer)
    liveRefreshTimer = undefined
  }
  if (liveDebounceTimer !== undefined) {
    window.clearTimeout(liveDebounceTimer)
    liveDebounceTimer = undefined
  }
}

function startLiveTimers() {
  if (disposed || !live.value || liveRefreshTimer !== undefined || document.visibilityState !== 'visible') return
  liveRefreshTimer = window.setInterval(() => requestLiveRefresh(), LIVE_REFRESH_MS)
}

function onVisibilityChange() {
  if (disposed || !live.value) return
  if (document.visibilityState === 'visible') {
    startLiveTimers()
    requestLiveRefresh()
  } else {
    stopLiveTimers()
  }
}

function onScroll() {
  if (disposed) return
  maybeLoadMore()
  if (livePending.value && live.value && window.scrollY <= LIVE_SCROLL_DEFER_PX) {
    requestLiveRefresh()
  }
}

// Websocket traffic is only a notification now: every row comes back from the
// event API with the active node, conditions and time window applied.
watch(liveDns, () => {
  if (!live.value || tab.value !== 'dns') return
  scheduleLiveRefresh()
})

watch([liveFlows, liveBlocks], () => {
  if (!live.value || tab.value !== 'traffic') return
  scheduleLiveRefresh()
})

function applyFilters() {
  cancelSearch()
  cancelConditions()
  if (live.value) captureLiveDuration()
  livePending.value = false
  load()
}

function resetFilters() {
  cancelSearch()
  cancelConditions()
  if (search.value !== '') {
    skipSearchWatch = true
    search.value = ''
  }
  container.value = ''
  ip.value = ''
  filter.value = 'all'
  match.value = 'all'
  conditions.value = []
  excludeLocal.value = false
  nodeId.value = 'all'
  defaultWindow()
  if (live.value) captureLiveDuration()
  livePending.value = false
  load()
}

function changeTab(value: Tab) {
  if (tab.value === value) return
  cancelSearch()
  cancelConditions()
  tab.value = value
  filter.value = 'all'
  livePending.value = false
  load()
}

function openDetail(title: string, fields: { label: string; value: string }[]) {
  detailTitle.value = title
  detailFields.value = fields
  detailOpen.value = true
}

function showTraffic(event: TrafficEvent) {
  const fields: { label: string; value: string }[] = [
    { label: 'Node', value: nodeNames.value.get(event.nodeId) ?? event.nodeId },
    { label: 'Time', value: formatTime(event.at) },
    { label: 'Kind', value: event.kind },
    { label: 'Source', value: endpoint(event.srcIp, event.srcPort) },
    { label: 'Destination', value: endpoint(event.dstIp, event.dstPort) },
    { label: 'Target', value: targetLabel(event) },
    { label: 'Block kind', value: event.blockKind || '—' },
    { label: 'Protocol', value: protocolName(event.protocol) },
    { label: 'Domain / SNI', value: [event.domain, event.sni].filter(Boolean).join(' · ') || '—' },
    { label: 'Container', value: event.containerName || event.containerId || '—' },
  ]
  if (event.kind === 'connection') {
    fields.push(
      { label: 'Bytes', value: `${formatBytes(event.bytesOut ?? 0)} out / ${formatBytes(event.bytesIn ?? 0)} in` },
      { label: 'Packets', value: `${formatNumber(event.packetsOut ?? 0)} out / ${formatNumber(event.packetsIn ?? 0)} in` },
      { label: 'Duration', value: formatDuration(event.durationMs ?? 0) },
      { label: 'Network', value: event.network || '—' },
    )
  }
  const asn = event.dstAsn ? `AS${event.dstAsn}` : ''
  const asnName = event.dstAsnName || asn
  const country = event.dstCountry ? `${countryFlag(event.dstCountry)} ${event.dstCountry}`.trim() : ''
  fields.push(
    { label: 'ASN / country', value: [asnName, asnName !== asn ? asn : '', country].filter(Boolean).join(' · ') || '—' },
  )
  if (event.kind === 'block' && event.blockKind === 'dns') {
    fields.push({ label: 'ASN source', value: 'Resolver IP; the blocked domain itself has no ASN.' })
  }
  fields.push(
    { label: 'Rule', value: event.ruleId || '—' },
    { label: 'Reason', value: event.reason || '—' },
    { label: 'Event ID', value: event.eventId },
  )
  openDetail(event.kind === 'block' ? 'Blocking event' : 'Connection', fields)
}

function showDNS(event: DnsEvent) {
  openDetail('DNS request', [
    { label: 'Node', value: nodeNames.value.get(event.nodeId) ?? event.nodeId },
    { label: 'Time', value: formatTime(event.at) },
    { label: 'Client', value: `${event.clientIp}${event.clientPort ? `:${event.clientPort}` : ''}` },
    { label: 'Name', value: event.qname },
    { label: 'Type', value: qtypeName(event.qtype) },
    { label: 'RCODE', value: String(event.rcode) },
    { label: 'Answers', value: event.answers.join(', ') || '—' },
    { label: 'Latency', value: `${event.latencyMs} ms` },
    { label: 'Container', value: event.containerName || event.containerId || '—' },
    { label: 'Blocked', value: event.blocked ? 'yes' : 'no' },
    { label: 'Rule', value: event.ruleId || '—' },
    { label: 'Event ID', value: event.eventId },
  ])
}

watch([nodeId, filter, excludeLocal], () => {
  livePending.value = false
  load()
})
watch(conditionSignature, () => {
  if (tab.value !== 'traffic') return
  cancelConditions()
  conditionTimer = window.setTimeout(() => {
    conditionTimer = undefined
    livePending.value = false
    load()
  }, 300)
})
watch(search, () => {
  if (skipSearchWatch) {
    skipSearchWatch = false
    return
  }
  if (tab.value !== 'dns') return
  cancelSearch()
  searchTimer = window.setTimeout(() => {
    searchTimer = undefined
    livePending.value = false
    load()
  }, 300)
})
// Enabling live captures the visible From→To width, then the API window rolls
// to now. Pausing leaves the last rolling frame as a fixed range.
watch(live, (on) => {
  livePending.value = false
  if (on) {
    captureLiveDuration()
    startLiveTimers()
    load()
  } else {
    stopLiveTimers()
    autoRefreshQueued = false
  }
})
watch(
  [fromInput, toInput, nodeId, match, conditions, excludeLocal, live, tab, search, container, ip, filter],
  () => saveFilters(),
  { deep: true },
)
useRefresh(load)
onMounted(async () => {
  try {
    const result = await api.nodes()
    if (disposed) return
    nodes.value = result.items
  } catch {
    if (disposed) return
    nodes.value = []
  }
  if (disposed) return
  window.addEventListener('scroll', onScroll, { passive: true })
  document.addEventListener('visibilitychange', onVisibilityChange)
  startLiveTimers()
  load()
})
onBeforeUnmount(() => {
  disposed = true
  requestSeq++
  autoRefreshQueued = false
  livePending.value = false
  liveRefreshing.value = false
  loadingMore.value = false
  loading.value = false
  cancelSearch()
  cancelConditions()
  stopLiveTimers()
  window.removeEventListener('scroll', onScroll)
  document.removeEventListener('visibilitychange', onVisibilityChange)
})
</script>

<template>
  <div class="page">
    <PageHead title="Events" :subtitle="subtitle">
      <template #actions>
        <button class="pc-btn pc-sm" type="button" :disabled="loading" @click="load()">Refresh</button>
      </template>
    </PageHead>

    <div class="tabs" role="tablist" aria-label="Event type">
      <button
        v-for="item in tabs"
        :key="item.value"
        class="tab"
        type="button"
        role="tab"
        :aria-selected="tab === item.value"
        @click="changeTab(item.value)"
      >
        {{ item.label }}
      </button>
    </div>

    <div v-if="error" class="band band-love">
      <div class="band-head">
        <StatusMark state="broken" word="request failed" />
        <span class="band-actions"><button class="pc-btn pc-sm" type="button" @click="load()">Retry</button></span>
      </div>
      <p>{{ error }}</p>
    </div>

    <form class="filters" @submit.prevent="applyFilters">
      <div style="width: 190px">
        <select v-model="nodeId" class="pc-inp" aria-label="Node">
          <option value="all">All nodes</option>
          <option v-for="node in nodes" :key="node.id" :value="node.id">{{ node.name }}</option>
        </select>
      </div>
      <div style="width: 210px">
        <input v-model="fromInput" class="pc-inp" type="datetime-local" aria-label="From date and time" title="From" @change="applyFilters" />
      </div>
      <div style="width: 210px">
        <input v-model="toInput" class="pc-inp" type="datetime-local" aria-label="To date and time" title="To" @change="applyFilters" />
      </div>
      <template v-if="tab === 'dns'">
        <div class="grow">
          <input v-model="search" class="pc-inp" placeholder="Domain" type="search" />
        </div>
        <div style="width: 160px">
          <input v-model="container" class="pc-inp" placeholder="Container" type="search" />
        </div>
        <div style="width: 190px">
          <input v-model="ip" class="pc-inp" placeholder="IP address" type="search" />
        </div>
        <div style="width: 160px">
          <select v-model="filter" class="pc-inp" aria-label="Filter">
            <option v-for="option in filterOptions" :key="option.value" :value="option.value">{{ option.label }}</option>
          </select>
        </div>
        <button class="pc-btn" type="submit" :disabled="loading">Search</button>
      </template>
      <button class="pc-btn pc-ghost" type="button" @click="resetFilters">Reset</button>
      <button
        v-if="tab === 'traffic'"
        class="pc-chip"
        type="button"
        :aria-pressed="excludeLocal"
        title="Hide events where both source and destination are local, private, CGNAT, link-local or multicast"
        @click="excludeLocal = !excludeLocal"
      >
        {{ excludeLocal ? 'Local hidden' : 'Exclude local' }}
      </button>
      <button class="pc-chip" type="button" :aria-pressed="live" @click="live = !live">
        <StatusMark :state="liveConnected ? 'running' : 'broken'" />
        {{ live ? (liveRefreshing ? 'Live · updating' : 'Live') : 'Paused' }}
      </button>
    </form>

    <TrafficConditionBuilder
      v-if="tab === 'traffic'"
      v-model:conditions="conditions"
      v-model:match="match"
    />

    <div v-if="livePending" class="band band-gold">
      <div class="band-head">
        <StatusMark state="running" word="live" />
        <span>{{ liveNotice }}</span>
        <span class="band-actions">
          <button class="pc-btn pc-sm" type="button" :disabled="liveRefreshing" @click="refreshLiveNow">
            {{ liveRefreshing ? 'Refreshing…' : 'Refresh now' }}
          </button>
        </span>
      </div>
    </div>

    <div class="areas">
      <section class="area">
        <div class="pc-title">
          <h2>{{ tabLabel }}</h2>
          <span class="area-title-note">{{ formatNumber(total) }} matching</span>
        </div>
        <div class="area-body flush">
          <div class="tbl tbl-wide">
            <table v-if="tab === 'traffic'" class="pc-table">
              <thead>
                <tr>
                  <th>Time</th>
                  <th>Node</th>
                  <th>Source</th>
                  <th>Destination</th>
                  <th>Target / domain</th>
                  <th>ASN name</th>
                  <th>Country</th>
                  <th class="num">Egress</th>
                  <th class="num">Decision</th>
                </tr>
              </thead>
              <tbody>
                <tr v-if="loading"><td colspan="9" class="empty">Loading traffic…</td></tr>
                <template v-else>
                  <tr
                    v-for="event in traffic?.items ?? []"
                    :key="event.eventId"
                    class="pc-row"
                    :class="{ 'pc-row-blocked': event.blocked }"
                    @click="showTraffic(event)"
                  >
                    <td class="muted">{{ formatTime(event.at) }}</td>
                    <td>{{ nodeNames.get(event.nodeId) ?? event.nodeId }}</td>
                    <td class="mono subtle">{{ endpoint(event.srcIp, event.srcPort) }}</td>
                    <td class="mono subtle">{{ endpoint(event.dstIp, event.dstPort) }}</td>
                    <td>
                      <span v-if="event.blocked && event.blockKind" class="tag">{{ event.blockKind }}</span>
                      <span class="ell" :title="targetLabel(event)">{{ targetLabel(event) }}</span>
                    </td>
                    <td class="subtle"><span class="ell" :title="asnLabel(event)">{{ asnLabel(event) }}</span></td>
                    <td>
                      <template v-if="event.dstCountry">
                        <span class="flag" aria-hidden="true">{{ countryFlag(event.dstCountry) }}</span> {{ event.dstCountry }}
                      </template>
                      <template v-else>—</template>
                    </td>
                    <td class="num">{{ egressLabel(event) }}</td>
                    <td class="num">
                      <StatusMark :state="event.blocked ? 'blocked' : 'ok'" :word="event.blocked ? 'blocked' : 'allowed'" />
                    </td>
                  </tr>
                  <tr v-if="(traffic?.items.length ?? 0) === 0"><td colspan="9" class="empty">No traffic in this window.</td></tr>
                </template>
              </tbody>
            </table>

            <table v-else class="pc-table">
              <thead>
                <tr>
                  <th>Time</th>
                  <th>Node</th>
                  <th>Client</th>
                  <th>Name</th>
                  <th>Type</th>
                  <th>Answers</th>
                  <th>Container</th>
                  <th class="num">Result</th>
                </tr>
              </thead>
              <tbody>
                <tr v-if="loading"><td colspan="8" class="empty">Loading DNS requests…</td></tr>
                <template v-else>
                  <tr
                    v-for="event in dns?.items ?? []"
                    :key="event.eventId"
                    class="pc-row"
                    :class="{ 'pc-row-blocked': event.blocked }"
                    @click="showDNS(event)"
                  >
                    <td class="muted">{{ formatTime(event.at) }}</td>
                    <td>{{ nodeNames.get(event.nodeId) ?? event.nodeId }}</td>
                    <td class="mono subtle">{{ event.clientIp }}</td>
                    <td><span class="ell" :title="event.qname">{{ event.qname }}</span></td>
                    <td>{{ qtypeName(event.qtype) }}</td>
                    <td class="mono muted"><span class="ell" :title="event.answers.join(', ') || '—'">{{ event.answers.join(', ') || '—' }}</span></td>
                    <td class="subtle">{{ event.containerName || '—' }}</td>
                    <td class="num">
                      <StatusMark :state="event.blocked ? 'blocked' : 'ok'" :word="event.blocked ? 'blocked' : `rcode ${event.rcode}`" />
                    </td>
                  </tr>
                  <tr v-if="(dns?.items.length ?? 0) === 0"><td colspan="8" class="empty">No DNS requests in this window.</td></tr>
                </template>
              </tbody>
            </table>
          </div>
        </div>
        <div class="pager">
          <span>
            {{ page ? `${formatNumber(page.items.length)} shown · ${formatNumber(page.total)} total` : '—' }}
            <template v-if="live"> · {{ liveRefreshing ? 'updating from server…' : 'server live' }}</template>
          </span>
          <span class="pages">
            <button v-if="hasMore" class="pc-btn pc-sm" type="button" :disabled="loadingMore" @click="loadMore">
              {{ loadingMore ? 'Loading…' : 'Load more' }}
            </button>
          </span>
        </div>
        <div class="foot">
          Scroll to load more. Click a row for every field. Filters search the selected window only.
          <template v-if="live"> Live refreshes with every filter applied; loaded rows are kept while you scroll.</template>
        </div>
      </section>
    </div>

    <ConsoleDialog v-model:open="detailOpen" :title="detailTitle" width="min(620px, 100%)">
      <dl class="detail-list">
        <template v-for="field in detailFields" :key="field.label">
          <dt>{{ field.label }}</dt>
          <dd class="mono">{{ field.value }}</dd>
        </template>
      </dl>
      <template #footer>
        <button class="pc-btn pc-ghost" type="button" @click="detailOpen = false">Close</button>
      </template>
    </ConsoleDialog>
  </div>
</template>