<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import PageHead from '@/components/console/PageHead.vue'
import StatusMark from '@/components/console/StatusMark.vue'
import { api, errorMessage, type Node, type Stats } from '@/lib/api'
import { liveBlocks, liveConnected, liveDns, liveFlows, liveLastAt } from '@/lib/live'
import { formatBytes, formatNumber, formatRelative, formatTime, rangeWindow } from '@/lib/utils'
import { useRefresh } from '@/lib/ui'

const router = useRouter()
const range = ref('24h')
const stats = ref<Stats | null>(null)
const nodes = ref<Node[]>([])
const loading = ref(true)
const error = ref('')
let timer: number | undefined

const ranges = [
  { value: '1h', label: '1 h' },
  { value: '24h', label: '24 h' },
  { value: '7d', label: '7 d' },
]

const online = computed(() => nodes.value.filter((node) => node.status === 'online').length)
const subtitle = computed(() => {
  if (!stats.value) return error.value || 'Loading coordinator data…'
  return `${online.value} of ${nodes.value.length} nodes online · ${formatNumber(stats.value.flows)} connections · ${formatNumber(stats.value.blockedFlows)} blocked`
})
const nodeNames = computed(() => new Map(nodes.value.map((node) => [node.id, node])))
const liveRows = computed(() => {
  const rows = [
    ...liveFlows.value.slice(0, 30).map((flow) => ({
      id: `flow-${flow.eventId}`, at: flow.startedAt, nodeId: flow.nodeId, kind: 'connection',
      target: flow.sni || flow.domain || `${flow.dstIp}:${flow.dstPort}`,
      detail: `${flow.srcIp} → ${flow.dstIp}${flow.containerName ? ` · ${flow.containerName}` : ''}`,
      state: flow.blocked ? 'blocked' as const : 'ok' as const,
      word: flow.blocked ? 'blocked' : 'allowed',
    })),
    ...liveDns.value.slice(0, 30).map((event) => ({
      id: `dns-${event.eventId}`, at: event.at, nodeId: event.nodeId, kind: 'dns',
      target: event.qname,
      detail: `${event.clientIp}${event.containerName ? ` · ${event.containerName}` : ''}`,
      state: event.blocked ? 'blocked' as const : 'ok' as const,
      word: event.blocked ? 'blocked' : `rcode ${event.rcode}`,
    })),
    ...liveBlocks.value.slice(0, 30).map((event) => ({
      id: `block-${event.eventId}`, at: event.at, nodeId: event.nodeId, kind: event.kind,
      target: event.target || event.domain || event.dstIp,
      detail: `${event.srcIp} → ${event.dstIp}${event.containerName ? ` · ${event.containerName}` : ''}`,
      state: 'blocked' as const,
      word: 'blocked',
    })),
  ]
  return rows.sort((a, b) => Date.parse(b.at) - Date.parse(a.at)).slice(0, 8)
})
const counts = computed(() => [
  {
    label: 'Connections',
    state: 'ok' as const,
    value: formatNumber(stats.value?.flows),
    note: `${formatNumber(stats.value?.blockedFlows)} blocked`,
    zero: (stats.value?.flows ?? 0) === 0,
  },
  {
    label: 'Blocked',
    state: 'blocked' as const,
    value: formatNumber(stats.value?.blockedFlows),
    note: `${formatNumber(stats.value?.blockEvents)} blocking events`,
    zero: (stats.value?.blockedFlows ?? 0) === 0,
  },
  {
    label: 'DNS queries',
    state: 'ok' as const,
    value: formatNumber(stats.value?.dnsQueries),
    note: `${formatNumber(stats.value?.blockedDns)} blocked`,
    zero: (stats.value?.dnsQueries ?? 0) === 0,
  },
  {
    label: 'Egress',
    state: 'unknown' as const,
    value: formatBytes(stats.value?.bytesOut),
    note: `${formatBytes(stats.value?.bytesIn)} ingress`,
    zero: (stats.value?.bytesOut ?? 0) === 0,
  },
])

function nodeState(status: string): 'ok' | 'broken' | 'unknown' {
  if (status === 'online') return 'ok'
  if (status === 'offline') return 'broken'
  return 'unknown'
}

async function load() {
  loading.value = true
  error.value = ''
  try {
    const window = rangeWindow(range.value)
    const [nextStats, nextNodes] = await Promise.all([api.stats(window.from, window.to), api.nodes()])
    stats.value = nextStats
    nodes.value = nextNodes.items
  } catch (err) {
    error.value = errorMessage(err)
  } finally {
    loading.value = false
  }
}

watch(range, load)
useRefresh(load)
onMounted(() => {
  load()
  timer = window.setInterval(() => {
    if (document.visibilityState === 'visible') load()
  }, 15000)
})
onBeforeUnmount(() => {
  if (timer) window.clearInterval(timer)
})
</script>

<template>
  <div class="page">
    <PageHead title="Overview" :subtitle="subtitle">
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
        <RouterLink class="pc-btn pc-sm pc-pri" to="/events">View events</RouterLink>
      </template>
    </PageHead>

    <div v-if="error" class="band band-love">
      <div class="band-head">
        <StatusMark state="broken" word="request failed" />
        <span class="band-actions"><button class="pc-btn pc-sm" type="button" @click="load">Retry</button></span>
      </div>
      <p>{{ error }}</p>
    </div>

    <section class="counts" aria-label="Counts">
      <div v-for="cell in counts" :key="cell.label" class="count" :class="{ zero: cell.zero }">
        <div class="count-label"><StatusMark :state="cell.state" />{{ cell.label }}</div>
        <div class="count-num">{{ cell.value }}</div>
        <div class="count-note">{{ cell.note }}</div>
      </div>
    </section>

    <div class="areas">
      <section class="area">
        <div class="pc-title">
          <h2>Live traffic</h2>
          <span class="area-title-note">
            WebSocket · {{ liveLastAt ? `last event ${formatRelative(liveLastAt)}` : 'waiting for the first event' }}
          </span>
          <span class="area-title-actions">
            <StatusMark :state="liveConnected ? 'running' : 'broken'" :word="liveConnected ? 'live' : 'reconnecting'" />
          </span>
        </div>
        <div class="area-body flush">
          <div class="tbl">
            <table class="pc-table">
              <thead>
                <tr>
                  <th>Time</th>
                  <th>Node</th>
                  <th>Kind</th>
                  <th>Target</th>
                  <th>Detail</th>
                  <th class="num">Decision</th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="row in liveRows" :key="row.id">
                  <td class="muted">{{ formatTime(row.at) }}</td>
                  <td>{{ nodeNames.get(row.nodeId)?.name ?? row.nodeId }}</td>
                  <td><span class="tag">{{ row.kind }}</span></td>
                  <td><span class="ell" :title="row.target">{{ row.target }}</span></td>
                  <td class="subtle mono"><span class="ell">{{ row.detail }}</span></td>
                  <td class="num"><StatusMark :state="row.state" :word="row.word" /></td>
                </tr>
                <tr v-if="liveRows.length === 0">
                  <td colspan="6" class="empty">
                    {{ liveConnected ? 'Waiting for the next event…' : 'Live stream is reconnecting.' }}
                  </td>
                </tr>
              </tbody>
            </table>
          </div>
        </div>
        <div class="foot">Live frames are best-effort. The full history remains in ClickHouse and is queried below and on Events.</div>
      </section>
    </div>

    <div class="areas grid-13">
      <section class="area">
        <div class="pc-title">
          <h2>Node activity</h2>
          <span class="area-title-note">connections in this window</span>
          <span class="area-title-actions"><RouterLink class="pc-btn pc-sm" to="/nodes">Nodes →</RouterLink></span>
        </div>
        <div class="area-body flush">
          <div class="tbl">
            <table class="pc-table tbl-narrow">
              <thead>
                <tr>
                  <th>Node</th>
                  <th>Status</th>
                  <th class="num">Flows</th>
                  <th class="num">DNS</th>
                  <th class="num">Egress</th>
                  <th class="num">Seen</th>
                </tr>
              </thead>
              <tbody>
                <tr
                  v-for="activity in stats?.nodes ?? []"
                  :key="activity.nodeId"
                  class="pc-row"
                  @click="router.push(`/nodes/${activity.nodeId}`)"
                >
                  <td>{{ nodeNames.get(activity.nodeId)?.name ?? activity.nodeId }}</td>
                  <td>
                    <StatusMark
                      :state="nodeState(nodeNames.get(activity.nodeId)?.status ?? 'offline')"
                      :word="nodeNames.get(activity.nodeId)?.status ?? 'offline'"
                    />
                  </td>
                  <td class="num">{{ formatNumber(activity.flows) }}</td>
                  <td class="num">{{ formatNumber(activity.dnsQueries) }}</td>
                  <td class="num">{{ formatBytes(activity.bytesOut) }}</td>
                  <td class="num muted">{{ formatRelative(nodeNames.get(activity.nodeId)?.lastSeenAt ?? activity.lastSeenAt) }}</td>
                </tr>
                <tr v-if="!loading && (stats?.nodes.length ?? 0) === 0">
                  <td colspan="6" class="empty">No activity in this window.</td>
                </tr>
                <tr v-if="loading">
                  <td colspan="6" class="empty">Loading…</td>
                </tr>
              </tbody>
            </table>
          </div>
        </div>
        <div class="foot">Click a node to open its page. The window is {{ ranges.find((r) => r.value === range)?.label }}.</div>
      </section>

      <section class="area">
        <div class="pc-title">
          <h2>Top domains</h2>
          <span class="area-title-note">by connections</span>
        </div>
        <div class="area-body flush">
          <div class="list">
            <div v-for="(item, index) in stats?.topDomains ?? []" :key="item.name" class="list-row">
              <span class="value">{{ String(index + 1).padStart(2, '0') }}</span>
              <span class="name">{{ item.name }}</span>
              <span class="value num">{{ formatNumber(item.count) }}</span>
            </div>
            <div v-if="(stats?.topDomains.length ?? 0) === 0" class="empty">No domain data. DNS inspection may be off.</div>
          </div>
        </div>
      </section>
    </div>

    <div class="areas grid-2">
      <section class="area">
        <div class="pc-title">
          <h2>Top destinations</h2>
          <span class="area-title-note">by connections</span>
        </div>
        <div class="area-body flush">
          <div class="list">
            <div v-for="item in stats?.topDestinations ?? []" :key="item.name" class="list-row">
              <span class="name mono">{{ item.name }}</span>
              <span class="value num">{{ formatNumber(item.count) }}</span>
            </div>
            <div v-if="(stats?.topDestinations.length ?? 0) === 0" class="empty">No connections recorded yet.</div>
          </div>
        </div>
      </section>

      <section class="area">
        <div class="pc-title">
          <h2>Top containers</h2>
          <span class="area-title-note">Docker bridge traffic</span>
        </div>
        <div class="area-body flush">
          <div class="list">
            <div v-for="item in stats?.topContainers ?? []" :key="item.name" class="list-row">
              <span class="name">{{ item.name }}</span>
              <span class="value num">{{ formatNumber(item.count) }}</span>
            </div>
            <div v-if="(stats?.topContainers.length ?? 0) === 0" class="empty">No container-attributed traffic.</div>
          </div>
        </div>
      </section>
    </div>

    <div class="foot above">
      Window {{ stats ? `${new Date(stats.window.from).toLocaleString()} → ${new Date(stats.window.to).toLocaleString()}` : '—' }}.
      {{ formatNumber(stats?.blockEvents) }} blocking events, including {{ formatNumber(stats?.blockedDns) }} DNS answers.
    </div>
  </div>
</template>