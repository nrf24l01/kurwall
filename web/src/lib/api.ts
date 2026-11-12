import { apiToken, unauthorized } from '@/lib/auth'

const baseURL = (import.meta.env.VITE_API_BASE_URL as string | undefined)?.replace(/\/$/, '') ?? '/api/v1'

export interface FieldError {
  field: string
  issue: string
  rejectedValue?: string
}

export interface Envelope<T> {
  status: number
  code: string
  message: string
  traceId: string
  timestamp: string
  path: string
  data?: T
  fieldErrors?: FieldError[]
}

export class ApiError extends Error {
  status: number
  code: string
  traceId: string
  fieldErrors: FieldError[]

  constructor(envelope: Partial<Envelope<unknown>>, status: number) {
    super(envelope.message || 'Request failed')
    this.name = 'ApiError'
    this.status = status
    this.code = envelope.code || 'REQUEST_FAILED'
    this.traceId = envelope.traceId || ''
    this.fieldErrors = envelope.fieldErrors || []
  }
}

type RequestOptions = Omit<RequestInit, 'body'> & { body?: unknown; skipAuth?: boolean }

function cookieValue(name: string): string {
  const match = document.cookie.match(new RegExp(`(?:^|;\\s*)${name}=([^;]+)`))
  return match ? decodeURIComponent(match[1]) : ''
}

async function request<T>(path: string, options: RequestOptions = {}): Promise<T> {
  const { body, skipAuth, headers: optionHeaders, ...rest } = options
  const headers = new Headers(optionHeaders)
  if (body !== undefined) headers.set('Content-Type', 'application/json')
  if (apiToken.value) headers.set('Authorization', `Bearer ${apiToken.value}`)
  const method = (rest.method ?? 'GET').toUpperCase()
  if (method !== 'GET' && method !== 'HEAD' && method !== 'OPTIONS') {
    const csrf = cookieValue('kurwall_csrf')
    if (csrf) headers.set('X-CSRF-Token', csrf)
  }
  const response = await fetch(`${baseURL}${path}`, {
    ...rest,
    credentials: 'same-origin',
    headers,
    body: body === undefined ? undefined : JSON.stringify(body),
  })
  let envelope: Envelope<T>
  try {
    envelope = (await response.json()) as Envelope<T>
  } catch {
    throw new ApiError({ message: `Unexpected response (${response.status})` }, response.status)
  }
  if (response.status === 401 && !skipAuth) unauthorized.value = true
  if (!response.ok || envelope.status >= 400) {
    throw new ApiError(envelope, response.status)
  }
  return envelope.data as T
}

export type AuthRole = 'admin' | 'viewer'
export type AuthMode = 'open' | 'token' | 'users' | 'unavailable'

export interface AuthUser {
  id: string
  username: string
  role: AuthRole
  kind: 'open' | 'token' | 'session'
}

export interface SessionInfo {
  authenticated: boolean
  authMode: AuthMode
  user: AuthUser
}

export interface DeployKey {
  id: string
  nodeName: string
  policyIds: string[]
  createdBy: string
  createdAt: string
  expiresAt: string
  usedAt?: string
  usedByIp?: string
}

export interface DeployKeyResult {
  key: DeployKey
  token: string
  installUrl: string
  installCommand: string
}

export function errorMessage(error: unknown): string {
  if (error instanceof ApiError) {
    if (error.fieldErrors.length > 0) {
      return error.fieldErrors.map((field) => `${field.field}: ${field.issue}`).join(', ')
    }
    return error.message
  }
  if (error instanceof Error) return error.message
  return 'Request failed'
}

export interface Node {
  id: string
  name: string
  hostname: string
  agentVersion: string
  os: string
  kernel: string
  addresses: string[]
  status: 'online' | 'offline' | 'pending'
  lastSeenAt?: string
  labels: Record<string, string>
  policyIds: string[]
  policyVersion: number
  createdAt: string
  updatedAt: string
}

export interface PolicyNode {
  id: string
  name: string
  status: Node['status']
  agentVersion: string
  policyVersion: number
}

export interface Policy {
  id: string
  name: string
  description: string
  enabled: boolean
  ruleCount: number
  rules?: Rule[]
  nodes?: PolicyNode[]
  createdAt: string
  updatedAt: string
}

export type RuleKind = 'ip' | 'cidr' | 'domain' | 'asn'

export interface Rule {
  id: string
  policyId: string
  policyName?: string
  kind: RuleKind
  value: string
  action: 'block'
  note: string
  enabled: boolean
  createdAt: string
  updatedAt: string
  prefixCount?: number
  prefixes?: string[]
}

export interface AsnEntry {
  asn: number
  name: string
  country: string
  prefixCount: number
}

export interface AsnCountry {
  code: string
  count: number
}

export interface AsnPage {
  items: AsnEntry[]
  total: number
  limit: number
  offset: number
  catalogUpdatedAt?: string
}

export interface AsnLookup {
  asn: string
  number: number
  holder: string
  prefixCount: number
  prefixes: string[]
  updatedAt?: string
  refreshedAt: string
  error?: string
}

export interface Flow {
  nodeId: string
  eventId: string
  startedAt: string
  endedAt: string
  durationMs: number
  srcIp: string
  dstIp: string
  srcPort: number
  dstPort: number
  protocol: number
  domain: string
  sni: string
  containerId: string
  containerName: string
  bytesIn: number
  bytesOut: number
  packetsIn: number
  packetsOut: number
  blocked: boolean
  ruleId: string
  reason: string
  network: string
  dstAsn?: number
  dstAsnName?: string
  dstCountry?: string
}

export interface DnsEvent {
  nodeId: string
  eventId: string
  at: string
  clientIp: string
  clientPort: string
  qname: string
  qtype: number
  rcode: number
  answers: string[]
  blocked: boolean
  ruleId: string
  containerId: string
  containerName: string
  latencyMs: number
}

export interface BlockEvent {
  nodeId: string
  eventId: string
  at: string
  kind: string
  target: string
  srcIp: string
  dstIp: string
  srcPort: number
  dstPort: number
  protocol: number
  domain: string
  containerId: string
  containerName: string
  ruleId: string
  reason: string
  dstAsn?: number
  dstAsnName?: string
  dstCountry?: string
}

export interface TrafficEvent {
  kind: 'connection' | 'block'
  eventId: string
  nodeId: string
  at: string
  endedAt?: string
  durationMs?: number
  srcIp: string
  srcPort: number
  dstIp: string
  dstPort: number
  protocol: number
  domain?: string
  sni?: string
  target?: string
  blockKind?: string
  containerId?: string
  containerName?: string
  bytesIn?: number
  bytesOut?: number
  packetsIn?: number
  packetsOut?: number
  blocked: boolean
  ruleId?: string
  reason?: string
  network?: string
  dstAsn?: number
  dstAsnName?: string
  dstCountry?: string
}

export interface Page<T> {
  items: T[]
  total: number
  limit: number
  offset: number
}

export interface CountItem {
  name: string
  count: number
}

export interface NodeActivity {
  nodeId: string
  flows: number
  blockedFlows: number
  dnsQueries: number
  bytesOut: number
  lastSeenAt: string
}

export interface Stats {
  window: { from: string; to: string }
  flows: number
  blockedFlows: number
  dnsQueries: number
  blockedDns: number
  blockEvents: number
  bytesOut: number
  bytesIn: number
  topDomains: CountItem[]
  topDestinations: CountItem[]
  topContainers: CountItem[]
  nodes: NodeActivity[]
}

export interface AgentSettings {
  tlsInspectorPorts: number[]
  dnsInspection: boolean
  tlsInspection: boolean
  batchMaxEvents: number
  flushIntervalMs: number
}

export interface Snapshot {
  version: number
  nodeId: string
  generatedAt: string
  rules: Rule[]
  policies: { id: string; name: string }[]
  settings: AgentSettings
}

export interface PolicyInput {
  name: string
  description: string
  enabled: boolean
}

export interface RuleInput {
  kind: RuleKind
  value: string
  action: 'block'
  note: string
  enabled: boolean
}

export interface RuleFailure {
  value: string
  message: string
}

export interface CreateRulesResult {
  items: Rule[]
  skipped: string[]
  failed: RuleFailure[]
}

type Params = Record<string, string | number | boolean | undefined | null>

function query(params: Params): string {
  const search = new URLSearchParams()
  for (const [key, value] of Object.entries(params)) {
    if (value === undefined || value === null || value === '') continue
    search.set(key, String(value))
  }
  const encoded = search.toString()
  return encoded ? `?${encoded}` : ''
}

export const api = {
  meta: () => request<import('./meta').Meta>('/meta'),
  health: () => request<Record<string, unknown>>('/health'),

  authMe: () => request<SessionInfo>('/auth/me'),
  login: (username: string, password: string) =>
    request<{ user: AuthUser }>('/auth/login', { method: 'POST', body: { username, password }, skipAuth: true }),
  logout: () => request<{ loggedOut: boolean }>('/auth/logout', { method: 'POST' }),

  deployKeys: () => request<{ items: DeployKey[] }>('/deploy-keys'),
  createDeployKey: (body: { nodeName: string; ttlMinutes: number; policyIds: string[] }) =>
    request<DeployKeyResult>('/deploy-keys', { method: 'POST', body }),
  deleteDeployKey: (id: string) =>
    request<{ deleted: boolean }>(`/deploy-keys/${encodeURIComponent(id)}`, { method: 'DELETE' }),

  nodes: () => request<{ items: Node[] }>('/nodes'),
  node: (id: string) => request<Node>(`/nodes/${encodeURIComponent(id)}`),
  updateNode: (id: string, body: { name?: string; labels?: Record<string, string> }) =>
    request<Node>(`/nodes/${encodeURIComponent(id)}`, { method: 'PATCH', body }),
  assignPolicies: (id: string, policyIds: string[]) =>
    request<Node>(`/nodes/${encodeURIComponent(id)}/policies`, { method: 'PUT', body: { policyIds } }),
  nodeSnapshot: (id: string) => request<Snapshot>(`/nodes/${encodeURIComponent(id)}/snapshot`),

  policies: () => request<{ items: Policy[] }>('/policies'),
  policy: (id: string) => request<Policy>(`/policies/${encodeURIComponent(id)}`),
  createPolicy: (body: PolicyInput) => request<Policy>('/policies', { method: 'POST', body }),
  updatePolicy: (id: string, body: PolicyInput) =>
    request<Policy>(`/policies/${encodeURIComponent(id)}`, { method: 'PATCH', body }),
  deletePolicy: (id: string) =>
    request<{ deleted: boolean }>(`/policies/${encodeURIComponent(id)}`, { method: 'DELETE' }),

  createRule: (policyId: string, body: RuleInput) =>
    request<Rule>(`/policies/${encodeURIComponent(policyId)}/rules`, { method: 'POST', body }),
  createRules: (policyId: string, rules: RuleInput[]) =>
    request<CreateRulesResult>(`/policies/${encodeURIComponent(policyId)}/rules/batch`, {
      method: 'POST',
      body: { rules },
    }),
  updateRule: (policyId: string, ruleId: string, body: RuleInput) =>
    request<Rule>(`/policies/${encodeURIComponent(policyId)}/rules/${encodeURIComponent(ruleId)}`, {
      method: 'PATCH',
      body,
    }),
  deleteRule: (policyId: string, ruleId: string) =>
    request<{ deleted: boolean }>(
      `/policies/${encodeURIComponent(policyId)}/rules/${encodeURIComponent(ruleId)}`,
      { method: 'DELETE' },
    ),

  flows: (params: Params) => request<Page<Flow>>(`/events/flows${query(params)}`),
  dns: (params: Params) => request<Page<DnsEvent>>(`/events/dns${query(params)}`),
  blocks: (params: Params) => request<Page<BlockEvent>>(`/events/blocks${query(params)}`),
  traffic: (params: Params) => request<Page<TrafficEvent>>(`/events/traffic${query(params)}`),
  streamTicket: () => request<{ ticket: string; expiresAt: string }>('/stream-ticket', { method: 'POST' }),
  asns: (params: Params) => request<AsnPage>(`/asns${query(params)}`),
  asnCountries: () => request<{ items: AsnCountry[] }>('/asns/countries'),
  lookupASN: (value: string) => request<AsnLookup>(`/asns/${encodeURIComponent(value)}`),

  stats: (from: string, to: string) => request<Stats>(`/stats/overview${query({ from, to })}`),
}