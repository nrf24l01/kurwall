<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import ConsoleDialog from '@/components/console/ConsoleDialog.vue'
import PageHead from '@/components/console/PageHead.vue'
import StatusMark from '@/components/console/StatusMark.vue'
import { api, errorMessage, type BlockEvent, type DnsEvent, type Flow, type Node, type Page } from '@/lib/api'
import { liveBlocks, liveConnected, liveDns, liveFlows } from '@/lib/live'
import { formatBytes, formatDuration, formatNumber, formatTime, protocolName, rangeWindow } from '@/lib/utils'
import { useRefresh } from '@/lib/ui'

type Tab = 'flows' | 'dns' | 'blocks'

const tab = ref<Tab>('flows')
const range = ref('1h')
const nodeId = ref('all')
const search = ref('')
const container = ref('')
const ip = ref('')
const filter = ref('all')
const limit = 50
const offset = ref(0)
const live = ref(true)

const nodes = ref<Node[]>([])
const flows = ref<Page<Flow> | null>(null)
const dns = ref<Page<DnsEvent> | null>(null)
const blocks = ref<Page<BlockEvent> | null>(null)
const loading = ref(true)
const error = ref('')

const detailOpen = ref(false)
const detailTitle = ref('')
const detailFields = ref<{ label: string; value: string }[]>([])

const ranges = [
  { value: '1h', label: '1 h' },
  { value: '24h', label: '24 h' },
  { value: '7d', label: '7 d' },
]
const tabs: { value: Tab; label: string }[] = [
  { value: 'flows', label: 'Connections' },
  { value: 'dns', label: 'DNS' },
  { value: 'blocks', label: 'Blocks' },
]

const nodeNames = computed(() => new Map(nodes.value.map((node) => [node.id, node.name])))
const total = computed(() => (tab.value === 'flows' ? flows.value?.total : tab.value === 'dns' ? dns.value?.total : blocks.value?.total) ?? 0)
const page = computed(() => (tab.value === 'flows' ? flows.value : tab.value === 'dns' ? dns.value : blocks.value))
const tabLabel = computed(() => tabs.find((item) => item.value === tab.value)?.label ?? '')
const subtitle = computed(() => `${formatNumber(total.value)} ${tabLabel.value.toLowerCase()} · ${rangeLabel.value}`)
const rangeLabel = computed(() => ranges.find((item) => item.value === range.value)?.label ?? '')
const noFilters = computed(
  () =>
    nodeId.value === 'all' &&
    !search.value.trim() &&
    !container.value.trim() &&
    !ip.value.trim() &&
    filter.value === 'all',
)

const filterOptions = computed(() => {
  if (tab.value === 'flows') {
    return [
      { value: 'all', label: 'Any decision' },
      { value: 'allowed', label: 'Allowed' },
      { value: 'blocked', label: 'Blocked' },
    ]
  }
  if (tab.value === 'dns') {
    return [
      { value: 'all', label: 'Any result' },
      { value: 'allowed', label: 'Resolved' },
      { value: 'blocked', label: 'Blocked' },
    ]
  }
  return [
    { value: 'all', label: 'Any kind' },
    { value: 'ip', label: 'IP' },
    { value: 'cidr', label: 'CIDR' },
    { value: 'domain', label: 'Domain' },
    { value: 'asn', label: 'ASN' },
    { value: 'dns', label: 'DNS' },
  ]
})

function qtypeName(value: number): string {
  const names: Record<number, string> = { 1: 'A', 2: 'NS', 5: 'CNAME', 6: 'SOA', 12: 'PTR', 15: 'MX', 16: 'TXT', 28: 'AAAA', 33: 'SRV', 64: 'SVCB', 65: 'HTTPS' }
  return names[value] ?? String(value)
}

async function load() {
  loading.value = true
  error.value = ''
  const window = rangeWindow(range.value)
  const common = {
    nodeId: nodeId.value === 'all' ? undefined : nodeId.value,
    container: container.value || undefined,
    limit,
    offset: offset.value,
    from: window.from,
    to: window.to,
  }
  try {
    if (tab.value === 'flows') {
      flows.value = await api.flows({
        ...common,
        domain: search.value || undefined,
        dstIp: ip.value || undefined,
        decision: filter.value === 'all' ? undefined : filter.value,
      })
    } else if (tab.value === 'dns') {
      dns.value = await api.dns({
        ...common,
        domain: search.value || undefined,
        clientIp: ip.value || undefined,
        blocked: filter.value === 'all' ? undefined : filter.value === 'blocked',
      })
    } else {
      blocks.value = await api.blocks({
        ...common,
        target: search.value || undefined,
        dstIp: ip.value || undefined,
        kind: filter.value === 'all' ? undefined : filter.value,
      })
    }
  } catch (err) {
    error.value = errorMessage(err)
  } finally {
    loading.value = false
  }
}

function freshFlows(items: Flow[], previous: Flow[] | undefined): Flow[] {
  const seen = new Set((previous ?? []).map((item) => item.eventId))
  return items.filter((item) => !seen.has(item.eventId))
}

function freshDns(items: DnsEvent[], previous: DnsEvent[] | undefined): DnsEvent[] {
  const seen = new Set((previous ?? []).map((item) => item.eventId))
  return items.filter((item) => !seen.has(item.eventId))
}

function freshBlocks(items: BlockEvent[], previous: BlockEvent[] | undefined): BlockEvent[] {
  const seen = new Set((previous ?? []).map((item) => item.eventId))
  return items.filter((item) => !seen.has(item.eventId))
}

watch(liveFlows, (items, previous) => {
  if (!live.value || tab.value !== 'flows' || offset.value !== 0 || !noFilters.value || !flows.value) return
  const incoming = freshFlows(items, previous)
  if (incoming.length === 0) return
  const existing = new Set(flows.value.items.map((item) => item.eventId))
  const add = incoming.filter((item) => !existing.has(item.eventId))
  if (add.length === 0) return
  flows.value = { ...flows.value, items: [...add, ...flows.value.items].slice(0, limit), total: flows.value.total + add.length }
})

watch(liveDns, (items, previous) => {
  if (!live.value || tab.value !== 'dns' || offset.value !== 0 || !noFilters.value || !dns.value) return
  const incoming = freshDns(items, previous)
  if (incoming.length === 0) return
  const existing = new Set(dns.value.items.map((item) => item.eventId))
  const add = incoming.filter((item) => !existing.has(item.eventId))
  if (add.length === 0) return
  dns.value = { ...dns.value, items: [...add, ...dns.value.items].slice(0, limit), total: dns.value.total + add.length }
})

watch(liveBlocks, (items, previous) => {
  if (!live.value || tab.value !== 'blocks' || offset.value !== 0 || !noFilters.value || !blocks.value) return
  const incoming = freshBlocks(items, previous)
  if (incoming.length === 0) return
  const existing = new Set(blocks.value.items.map((item) => item.eventId))
  const add = incoming.filter((item) => !existing.has(item.eventId))
  if (add.length === 0) return
  blocks.value = { ...blocks.value, items: [...add, ...blocks.value.items].slice(0, limit), total: blocks.value.total + add.length }
})

function applyFilters() {
  offset.value = 0
  load()
}

function resetFilters() {
  search.value = ''
  container.value = ''
  ip.value = ''
  filter.value = 'all'
  nodeId.value = 'all'
  range.value = '1h'
  offset.value = 0
  load()
}

function changeTab(value: Tab) {
  if (tab.value === value) return
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

function showFlow(flow: Flow) {
  openDetail('Connection', [
    { label: 'Node', value: nodeNames.value.get(flow.nodeId) ?? flow.nodeId },
    { label: 'Started', value: formatTime(flow.startedAt) },
    { label: 'Ended', value: formatTime(flow.endedAt) },
    { label: 'Duration', value: formatDuration(flow.durationMs) },
    { label: 'Source', value: `${flow.srcIp}:${flow.srcPort}` },
    { label: 'Destination', value: `${flow.dstIp}:${flow.dstPort}` },
    { label: 'Protocol', value: protocolName(flow.protocol) },
    { label: 'Domain', value: flow.domain || '—' },
    { label: 'SNI', value: flow.sni || '—' },
    { label: 'Container', value: flow.containerName || flow.containerId || '—' },
    { label: 'Network', value: flow.network || '—' },
    { label: 'Egress / ingress', value: `${formatBytes(flow.bytesOut)} / ${formatBytes(flow.bytesIn)}` },
    { label: 'Packets', value: `${formatNumber(flow.packetsOut)} out / ${formatNumber(flow.packetsIn)} in` },
    { label: 'Decision', value: flow.blocked ? `blocked — ${flow.reason}` : flow.reason || 'allowed' },
    { label: 'Rule', value: flow.ruleId || '—' },
    { label: 'Event ID', value: flow.eventId },
  ])
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

function showBlock(event: BlockEvent) {
  openDetail('Blocking event', [
    { label: 'Node', value: nodeNames.value.get(event.nodeId) ?? event.nodeId },
    { label: 'Time', value: formatTime(event.at) },
    { label: 'Kind', value: event.kind },
    { label: 'Target', value: event.target },
    { label: 'Source', value: `${event.srcIp}${event.srcPort ? `:${event.srcPort}` : ''}` },
    { label: 'Destination', value: `${event.dstIp}${event.dstPort ? `:${event.dstPort}` : ''}` },
    { label: 'Protocol', value: protocolName(event.protocol) },
    { label: 'Domain', value: event.domain || '—' },
    { label: 'Container', value: event.containerName || event.containerId || '—' },
    { label: 'Rule', value: event.ruleId || '—' },
    { label: 'Reason', value: event.reason || '—' },
    { label: 'Event ID', value: event.eventId },
  ])
}

watch([range, nodeId, filter], () => {
  offset.value = 0
  load()
})
watch(offset, load)
useRefresh(load)
onMounted(async () => {
  try {
    nodes.value = (await api.nodes()).items
  } catch {
    nodes.value = []
  }
  load()
})
</script>

<template>
  <div class="page">
    <PageHead title="Events" :subtitle="subtitle">
      <template #actions>
        <div class="seg-row">
          <button
            v-for="option in ranges"
            :key="option.value"
            class="pc-sb"
            type="button"
            :aria-pressed="range === option.value"
            @click="range = option.value"
          >
            {{ option.label }}
          </button>
        </div>
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
      <div class="grow">
        <input v-model="search" class="pc-inp" :placeholder="tab === 'blocks' ? 'Target' : 'Domain'" type="search" />
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
      <button class="pc-chip" type="button" :aria-pressed="live" @click="live = !live">
        <StatusMark :state="liveConnected ? 'running' : 'broken'" />
        {{ live ? 'Live' : 'Paused' }}
      </button>
    </form>

    <div class="areas">
      <section class="area">
        <div class="pc-title">
          <h2>{{ tabLabel }}</h2>
          <span class="area-title-note">{{ formatNumber(total) }} matching · {{ rangeLabel }}</span>
        </div>
        <div class="area-body flush">
          <div class="tbl" :class="tab === 'dns' ? 'tbl-wide' : 'tbl-wide'">
            <table v-if="tab === 'flows'" class="pc-table">
              <thead>
                <tr>
                  <th>Time</th>
                  <th>Node</th>
                  <th>Source → destination</th>
                  <th>Domain / SNI</th>
                  <th>Container</th>
                  <th class="num">Egress</th>
                  <th class="num">Decision</th>
                </tr>
              </thead>
              <tbody>
                <tr v-if="loading"><td colspan="7" class="empty">Loading connections…</td></tr>
                <template v-else>
                  <tr v-for="flow in flows?.items ?? []" :key="flow.eventId" class="pc-row" @click="showFlow(flow)">
                    <td class="muted">{{ formatTime(flow.startedAt) }}</td>
                    <td>{{ nodeNames.get(flow.nodeId) ?? flow.nodeId }}</td>
                    <td class="mono subtle">{{ flow.srcIp }}:{{ flow.srcPort }} → {{ flow.dstIp }}:{{ flow.dstPort }}</td>
                    <td><span class="ell" :title="flow.sni || flow.domain || '—'">{{ flow.sni || flow.domain || '—' }}</span></td>
                    <td class="subtle">{{ flow.containerName || '—' }}</td>
                    <td class="num">{{ formatBytes(flow.bytesOut) }}</td>
                    <td class="num">
                      <StatusMark :state="flow.blocked ? 'blocked' : 'ok'" :word="flow.blocked ? 'blocked' : 'allowed'" />
                    </td>
                  </tr>
                  <tr v-if="(flows?.items.length ?? 0) === 0"><td colspan="7" class="empty">No connections in this window.</td></tr>
                </template>
              </tbody>
            </table>

            <table v-else-if="tab === 'dns'" class="pc-table">
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
                  <tr v-for="event in dns?.items ?? []" :key="event.eventId" class="pc-row" @click="showDNS(event)">
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

            <table v-else class="pc-table">
              <thead>
                <tr>
                  <th>Time</th>
                  <th>Node</th>
                  <th>Kind</th>
                  <th>Target</th>
                  <th>Source → destination</th>
                  <th>Container</th>
                  <th class="num">Reason</th>
                </tr>
              </thead>
              <tbody>
                <tr v-if="loading"><td colspan="7" class="empty">Loading blocking events…</td></tr>
                <template v-else>
                  <tr v-for="event in blocks?.items ?? []" :key="event.eventId" class="pc-row" @click="showBlock(event)">
                    <td class="muted">{{ formatTime(event.at) }}</td>
                    <td>{{ nodeNames.get(event.nodeId) ?? event.nodeId }}</td>
                    <td><span class="tag">{{ event.kind }}</span></td>
                    <td class="mono"><span class="ell" :title="event.target">{{ event.target }}</span></td>
                    <td class="mono subtle">{{ event.srcIp }} → {{ event.dstIp }}</td>
                    <td class="subtle">{{ event.containerName || '—' }}</td>
                    <td class="num muted"><span class="ell" :title="event.reason">{{ event.reason }}</span></td>
                  </tr>
                  <tr v-if="(blocks?.items.length ?? 0) === 0"><td colspan="7" class="empty">No blocking events in this window.</td></tr>
                </template>
              </tbody>
            </table>
          </div>
        </div>
        <div class="pager">
          <span>{{ page ? `${formatNumber(page.items.length)} shown · ${formatNumber(page.total)} total` : '—' }}</span>
          <span class="pages">
            <button class="pc-btn pc-sm" type="button" :disabled="offset === 0" @click="offset = Math.max(0, offset - limit)">‹ Previous</button>
            <button class="pc-btn pc-sm" type="button" :disabled="!page || offset + limit >= page.total" @click="offset = offset + limit">Next ›</button>
          </span>
        </div>
        <div class="foot">Click a row for every field. Filters search the selected window only.</div>
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