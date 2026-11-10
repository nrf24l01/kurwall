import { ref } from 'vue'
import { api, type AuthMode } from '@/lib/api'

export interface Meta {
  version: string
  clickhouseRetentionDays: number
  authRequired: boolean
  publicUrl?: string
  grpcPublicAddr?: string
  agentInstallUrl?: string
  agentManifestUrl?: string
  agentVersion?: string
  agentDistAvailable?: boolean
  authMode?: AuthMode
  enrollmentAvailable?: boolean
  agentSettings: {
    tlsInspectorPorts: number[]
    dnsInspection: boolean
    tlsInspection: boolean
    batchMaxEvents: number
    flushIntervalMs: number
  }
}

export const meta = ref<Meta | null>(null)

let loading: Promise<Meta | null> | null = null

export async function loadMeta(force = false): Promise<Meta | null> {
  if (meta.value && !force) return meta.value
  if (loading) return loading
  loading = api
    .meta()
    .then((value) => {
      meta.value = value
      return value
    })
    .catch(() => null)
    .finally(() => {
      loading = null
    })
  return loading
}