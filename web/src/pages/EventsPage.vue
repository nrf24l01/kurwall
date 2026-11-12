<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import ConsoleDialog from '@/components/console/ConsoleDialog.vue'
import PageHead from '@/components/console/PageHead.vue'
import StatusMark from '@/components/console/StatusMark.vue'
import { api, errorMessage, type BlockEvent, type DnsEvent, type Flow, type Node, type Page, type TrafficEvent } from '@/lib/api'
import { liveBlocks, liveConnected, liveDns, liveFlows } from '@/lib/live'
import { countryFlag, formatBytes, formatDuration, formatNumber, formatTime, protocolName } from '@/lib/utils'
import { useRefresh } from '@/lib/ui'

type Tab = 'traffic' | 'dns'

const tab = ref<Tab>('traffic')
const nodeId = ref('all')
const search = ref('')
const container = ref('')
const ip = ref('')
const filter = ref('all')
const excludeLocal = ref(false)
const limit = 50
const offset = ref(0)
const loadingMore = ref(false)
const live = ref(true)

function toLocalInput(date: Date): string {
  const pad = (value: number) => String(value).padStart(2, '0')
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}T${pad(date.getHours())}:${pad(date.getMinutes())}`
}

const now = new Date()
const fromInput = ref(toLocalInput(new Date(now.getTime() - 3600 * 1000)))
const toInput = ref(toLocalInput(now))
const activeFrom = ref('')
const activeTo = ref('')

function defaultWindow() {
  const to = new Date()
  fromInput.value = toLocalInput(new Date(to.getTime() - 3600 * 1000))
  toInput.value = toLocalInput(to)
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
const noFilters = computed(
  () =>
    nodeId.value === 'all' &&
    !search.value.trim() &&
    !container.value.trim() &&
    !ip.value.trim() &&
    filter.value === 'all' &&
    !excludeLocal.value,
)

const filterOptions = computed(() => {
  if (tab.value === 'traffic') {
    return [
      { value: 'all', label: 'Any decision' },
      { value: 'allowed', label: 'Allowed' },
      { value: 'blocked', label: 'Blocked' },
    ]
  }
  return [
    { value: 'all', label: 'Any result' },
    { value: 'allowed', label: 'Resolved' },
    { value: 'blocked', label: 'Blocked' },
  ]
})

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
let skipSearchWatch = false

function cancelSearch() {
  if (searchTimer !== undefined) {
    window.clearTimeout(searchTimer)
    searchTimer = undefined
  }
}

function maybeLoadMore() {
  if (loading.value || loadingMore.value || !hasMore.value) return
  if (window.innerHeight + window.scrollY >= document.documentElement.scrollHeight - 400) {
    void loadMore()
  }
}

async function load() {
  offset.value = 0
  loading.value = true
  error.value = ''
  const fromDate = fromInput.value ? new Date(fromInput.value) : null
  const toDate = toInput.value ? new Date(toInput.value) : new Date()
  if ((fromDate && Number.isNaN(fromDate.getTime())) || Number.isNaN(toDate.getTime())) {
    error.value = 'From and To must be valid date/times.'
    loading.value = false
    return
  }
  if (fromDate && fromDate >= toDate) {
    error.value = 'From must be before To.'
    loading.value = false
    return
  }
  activeFrom.value = fromDate ? fromDate.toISOString() : ''
  activeTo.value = toDate.toISOString()
  const common = {
    nodeId: nodeId.value === 'all' ? undefined : nodeId.value,
    container: container.value || undefined,
    limit,
    offset: offset.value,
    from: activeFrom.value || undefined,
    to: activeTo.value,
  }
  try {
    if (tab.value === 'traffic') {
      traffic.value = await api.traffic({
        ...common,
        domain: search.value || undefined,
        ip: ip.value || undefined,
        decision: filter.value === 'all' ? undefined : filter.value,
        excludeLocal: excludeLocal.value || undefined,
      })
    } else {
      dns.value = await api.dns({
        ...common,
        domain: search.value || undefined,
        clientIp: ip.value || undefined,
        blocked: filter.value === 'all' ? undefined : filter.value === 'blocked',
      })
    }
  } catch (err) {
    error.value = errorMessage(err)
  } finally {
    loading.value = false
    void nextTick(() => maybeLoadMore())
  }
}

async function loadMore() {
  if (loading.value || loadingMore.value) return
  const active = page.value
  if (!active || active.items.length >= active.total) return
  loadingMore.value = true
  error.value = ''
  const common = {
    nodeId: nodeId.value === 'all' ? undefined : nodeId.value,
    container: container.value || undefined,
    limit,
    from: activeFrom.value || undefined,
    to: activeTo.value,
  }
  try {
    if (tab.value === 'traffic') {
      const current = traffic.value
      if (!current || current.items.length >= current.total) return
      const next = await api.traffic({
        ...common,
        offset: current.items.length,
        domain: search.value || undefined,
        ip: ip.value || undefined,
        decision: filter.value === 'all' ? undefined : filter.value,
        excludeLocal: excludeLocal.value || undefined,
      })
      const seen = new Set(current.items.map((item) => item.eventId))
      const items = [...current.items, ...next.items.filter((item) => !seen.has(item.eventId))]
      traffic.value = { ...next, items }
      offset.value = items.length
    } else {
      const current = dns.value
      if (!current || current.items.length >= current.total) return
      const next = await api.dns({
        ...common,
        offset: current.items.length,
        domain: search.value || undefined,
        clientIp: ip.value || undefined,
        blocked: filter.value === 'all' ? undefined : filter.value === 'blocked',
      })
      const seen = new Set(current.items.map((item) => item.eventId))
      const items = [...current.items, ...next.items.filter((item) => !seen.has(item.eventId))]
      dns.value = { ...next, items }
      offset.value = items.length
    }
  } catch (err) {
    error.value = errorMessage(err)
  } finally {
    loadingMore.value = false
    void nextTick(() => maybeLoadMore())
  }
}

function freshDns(items: DnsEvent[], previous: DnsEvent[] | undefined): DnsEvent[] {
  const seen = new Set((previous ?? []).map((item) => item.eventId))
  return items.filter((item) => !seen.has(item.eventId))
}

watch(liveDns, (items, previous) => {
  if (!live.value || tab.value !== 'dns' || offset.value !== 0 || !noFilters.value || !dns.value) return
  const incoming = freshDns(items, previous)
  if (incoming.length === 0) return
  const existing = new Set(dns.value.items.map((item) => item.eventId))
  const add = incoming.filter((item) => !existing.has(item.eventId))
  if (add.length === 0) return
  dns.value = { ...dns.value, items: [...add, ...dns.value.items].slice(0, limit), total: dns.value.total + add.length }
})

function flowToTraffic(flow: Flow): TrafficEvent {
  return {
    kind: 'connection',
    eventId: flow.eventId,
    nodeId: flow.nodeId,
    at: flow.startedAt,
    endedAt: flow.endedAt,
    durationMs: flow.durationMs,
    srcIp: flow.srcIp,
    srcPort: flow.srcPort,
    dstIp: flow.dstIp,
    dstPort: flow.dstPort,
    protocol: flow.protocol,
    domain: flow.domain,
    sni: flow.sni,
    containerId: flow.containerId,
    containerName: flow.containerName,
    bytesIn: flow.bytesIn,
    bytesOut: flow.bytesOut,
    packetsIn: flow.packetsIn,
    packetsOut: flow.packetsOut,
    blocked: false,
    ruleId: flow.ruleId,
    reason: flow.reason,
    network: flow.network,
    dstAsn: flow.dstAsn,
    dstAsnName: flow.dstAsnName,
    dstCountry: flow.dstCountry,
  }
}

function blockToTraffic(block: BlockEvent): TrafficEvent {
  return {
    kind: 'block',
    eventId: block.eventId,
    nodeId: block.nodeId,
    at: block.at,
    srcIp: block.srcIp,
    srcPort: block.srcPort,
    dstIp: block.dstIp,
    dstPort: block.dstPort,
    protocol: block.protocol,
    domain: block.domain,
    target: block.target,
    blockKind: block.kind,
    containerId: block.containerId,
    containerName: block.containerName,
    blocked: true,
    ruleId: block.ruleId,
    reason: block.reason,
    dstAsn: block.dstAsn,
    dstAsnName: block.dstAsnName,
    dstCountry: block.dstCountry,
  }
}

watch([liveFlows, liveBlocks], ([flows, blocks], [prevFlows, prevBlocks]) => {
  const current = traffic.value
  if (!live.value || tab.value !== 'traffic' || offset.value !== 0 || !noFilters.value || !current) return
  // Diff against the previous buffer so rows that already fell off the capped
  // table are never replayed; only genuinely new live events are prepended.
  const seenFlows = new Set((prevFlows ?? []).map((flow) => flow.eventId))
  const seenBlocks = new Set((prevBlocks ?? []).map((block) => block.eventId))
  const rows = [
    ...flows.filter((flow) => !flow.blocked && !seenFlows.has(flow.eventId)).map(flowToTraffic),
    ...blocks.filter((block) => !seenBlocks.has(block.eventId)).map(blockToTraffic),
  ].sort((a, b) => Date.parse(b.at) - Date.parse(a.at))
  const existing = new Set(current.items.map((item) => item.eventId))
  const add: TrafficEvent[] = []
  for (const row of rows) {
    if (existing.has(row.eventId)) continue
    existing.add(row.eventId)
    add.push(row)
  }
  if (add.length === 0) return
  traffic.value = { ...current, items: [...add, ...current.items].slice(0, limit), total: current.total + add.length }
})

function applyFilters() {
  cancelSearch()
  offset.value = 0
  load()
}

function resetFilters() {
  cancelSearch()
  if (search.value !== '') {
    skipSearchWatch = true
    search.value = ''
  }
  container.value = ''
  ip.value = ''
  filter.value = 'all'
  excludeLocal.value = false
  nodeId.value = 'all'
  defaultWindow()
  offset.value = 0
  load()
}

function changeTab(value: Tab) {
  if (tab.value === value) return
  cancelSearch()
  tab.value = value
  filter.value = 'all'
  offset.value = 0
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
  offset.value = 0
  load()
})
watch(search, () => {
  if (skipSearchWatch) {
    skipSearchWatch = false
    return
  }
  cancelSearch()
  searchTimer = window.setTimeout(() => {
    searchTimer = undefined
    offset.value = 0
    load()
  }, 300)
})
// Resuming live mode replays history so events missed while paused do not
// leave a silent gap until the next manual refresh.
watch(live, (on) => {
  if (on) load()
})
useRefresh(load)
onMounted(async () => {
  try {
    nodes.value = (await api.nodes()).items
  } catch {
    nodes.value = []
  }
  window.addEventListener('scroll', maybeLoadMore, { passive: true })
  load()
})
onBeforeUnmount(() => {
  cancelSearch()
  window.removeEventListener('scroll', maybeLoadMore)
})
</script>

<template>
  <div class="page">
    <PageHead title="Events" :subtitle="subtitle">
      <template #actions>
        <button class="pc-btn pc-sm" type="button" :disabled="loading" @click="load">Refresh</button>
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
        <span class="band-actions"><button class="pc-btn pc-sm" type="button" @click="load">Retry</button></span>
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
      <div class="grow">
        <input v-model="search" class="pc-inp" :placeholder="tab === 'traffic' ? 'Domain / target' : 'Domain'" type="search" />
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
        {{ live ? 'Live' : 'Paused' }}
      </button>
    </form>

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
          <span>{{ page ? `${formatNumber(page.items.length)} shown · ${formatNumber(page.total)} total` : '—' }}</span>
          <span class="pages">
            <button v-if="hasMore" class="pc-btn pc-sm" type="button" :disabled="loadingMore" @click="loadMore">
              {{ loadingMore ? 'Loading…' : 'Load more' }}
            </button>
          </span>
        </div>
        <div class="foot">Scroll to load more. Click a row for every field. Filters search the selected window only.</div>
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