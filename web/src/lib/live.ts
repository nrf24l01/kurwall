import { ref } from 'vue'
import { api, type BlockEvent, type DnsEvent, type Flow } from '@/lib/api'

interface LiveEvent {
  id: string
  kind: 'flow' | 'dns' | 'block'
  flow?: Flow
  dns?: DnsEvent
  block?: BlockEvent
}

interface LiveMessage {
  type: 'hello' | 'events' | 'node'
  nodeId?: string
  status?: string
  events?: LiveEvent[]
}

export const liveConnected = ref(false)
export const liveFlows = ref<Flow[]>([])
export const liveDns = ref<DnsEvent[]>([])
export const liveBlocks = ref<BlockEvent[]>([])
export const liveNodeStatus = ref<Record<string, string>>({})
export const liveLastAt = ref('')

const maxLive = 200
let socket: WebSocket | null = null
let retryTimer: number | undefined
let attempt = 0
let started = false

function schedule() {
  if (retryTimer !== undefined || !started) return
  const delay = Math.min(30000, 1000 * 2 ** Math.min(attempt, 5))
  attempt++
  retryTimer = window.setTimeout(() => {
    retryTimer = undefined
    void connect()
  }, delay)
}

async function connect() {
  if (!started) return
  try {
    const { ticket } = await api.streamTicket()
    const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
    const url = `${protocol}//${window.location.host}/api/v1/stream?ticket=${encodeURIComponent(ticket)}`
    const next = new WebSocket(url)
    socket = next
    next.onopen = () => {
      liveConnected.value = true
      attempt = 0
    }
    next.onmessage = (event) => handle(String(event.data))
    next.onclose = () => {
      if (socket === next) socket = null
      liveConnected.value = false
      schedule()
    }
    next.onerror = () => next.close()
  } catch {
    liveConnected.value = false
    schedule()
  }
}

function handle(raw: string) {
  let message: LiveMessage
  try {
    message = JSON.parse(raw) as LiveMessage
  } catch {
    return
  }
  if (message.type === 'hello') {
    liveConnected.value = true
    attempt = 0
    return
  }
  if (message.type === 'node' && message.nodeId && message.status) {
    liveNodeStatus.value = { ...liveNodeStatus.value, [message.nodeId]: message.status }
    return
  }
  if (message.type !== 'events') return
  for (const event of message.events ?? []) {
    const at = event.flow?.startedAt ?? event.dns?.at ?? event.block?.at
    if (at) liveLastAt.value = at
    if (event.kind === 'flow' && event.flow) {
      liveFlows.value = [event.flow, ...liveFlows.value].slice(0, maxLive)
    } else if (event.kind === 'dns' && event.dns) {
      liveDns.value = [event.dns, ...liveDns.value].slice(0, maxLive)
    } else if (event.kind === 'block' && event.block) {
      liveBlocks.value = [event.block, ...liveBlocks.value].slice(0, maxLive)
    }
  }
}

export function startLive() {
  if (started) return
  started = true
  void connect()
}

export function stopLive() {
  started = false
  if (retryTimer !== undefined) {
    window.clearTimeout(retryTimer)
    retryTimer = undefined
  }
  if (socket) {
    socket.onclose = null
    socket.close()
    socket = null
  }
  liveConnected.value = false
}
