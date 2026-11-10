<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import PageHead from '@/components/console/PageHead.vue'
import StatusMark from '@/components/console/StatusMark.vue'
import { api, errorMessage } from '@/lib/api'
import { apiToken, clearApiToken, setApiToken } from '@/lib/auth'
import { meta } from '@/lib/meta'
import { currentUser, signOutSession } from '@/lib/session'
import { useRefresh } from '@/lib/ui'

const router = useRouter()
const tokenDraft = ref(apiToken.value)
const health = ref<Record<string, unknown> | null>(null)
const healthError = ref('')
const checking = ref(false)
const saved = ref(false)

const authLabel = computed(() => {
  if (meta.value?.authMode === 'users') return `${currentUser.value?.username ?? 'signed in'} · ${currentUser.value?.role ?? 'user'}`
  if (meta.value?.authMode === 'token') return 'static bearer token'
  return 'open API'
})

const subtitle = computed(() => {
  if (!meta.value) return 'Coordinator metadata unavailable'
  return `${meta.value.version} · ${authLabel.value} · 30-day event retention`
})

async function checkHealth() {
  checking.value = true
  healthError.value = ''
  try {
    health.value = await api.health()
  } catch (err) {
    health.value = null
    healthError.value = errorMessage(err)
  } finally {
    checking.value = false
  }
}

function save() {
  setApiToken(tokenDraft.value)
  saved.value = true
  window.setTimeout(() => (saved.value = false), 2000)
}

function clear() {
  tokenDraft.value = ''
  clearApiToken()
  saved.value = true
  window.setTimeout(() => (saved.value = false), 2000)
}

async function signOut() {
  if (meta.value?.authMode === 'users') {
    await signOutSession()
    await router.replace('/login')
    return
  }
  clearApiToken()
  window.location.reload()
}

useRefresh(checkHealth)
onMounted(checkHealth)
</script>

<template>
  <div class="page">
    <PageHead title="Settings" :subtitle="subtitle">
      <template #actions>
        <button class="pc-btn pc-sm" type="button" :disabled="checking" @click="checkHealth">Check health</button>
        <RouterLink class="pc-btn pc-sm pc-ghost" to="/nodes">Install nodes →</RouterLink>
      </template>
    </PageHead>

    <div v-if="healthError" class="band band-love">
      <div class="band-head">
        <StatusMark state="broken" word="dependency failed" />
        <span class="band-actions"><button class="pc-btn pc-sm" type="button" @click="checkHealth">Check again</button></span>
      </div>
      <p>{{ healthError }}</p>
    </div>

    <div class="areas grid-2">
      <section class="area">
        <div class="pc-title"><h2>Access</h2><span class="area-title-note">{{ meta?.authMode ?? 'open' }} mode</span></div>
        <div class="area-body">
          <div v-if="meta?.authMode === 'users'" class="stack">
            <dl class="kv">
              <dt>User</dt><dd>{{ currentUser?.username ?? '—' }}</dd>
              <dt>Role</dt><dd>{{ currentUser?.role ?? '—' }}</dd>
              <dt>Session</dt><dd>HttpOnly cookie · 12 hours</dd>
            </dl>
            <div class="actions-line">
              <button class="pc-btn" type="button" @click="signOut">Sign out</button>
            </div>
          </div>

          <div v-else-if="meta?.authMode === 'token'" class="stack">
            <div class="field">
              <label for="token">API token</label>
              <input id="token" v-model="tokenDraft" class="pc-inp" type="password" autocomplete="off" placeholder="Paste API token" />
              <span class="field-hint">Stored in this browser only. The coordinator expects a static bearer token.</span>
            </div>
            <div class="actions-line">
              <button class="pc-btn pc-pri" type="button" @click="save">Save token</button>
              <button class="pc-btn pc-ghost" type="button" @click="clear">Clear</button>
              <StatusMark v-if="saved" state="ok" word="saved" />
            </div>
          </div>

          <div v-else class="stack">
            <dl class="kv">
              <dt>Mode</dt><dd>Open API</dd>
              <dt>Accounts</dt><dd>none</dd>
            </dl>
          </div>
        </div>
        <div class="foot">
          Users are stored in PostgreSQL with bcrypt password hashes. Sessions are server-side and expire after 12
          hours.
        </div>
      </section>

      <section class="area">
        <div class="pc-title"><h2>Dependencies</h2><span class="area-title-note">readiness</span></div>
        <div class="area-body">
          <dl class="kv">
            <dt>PostgreSQL</dt>
            <dd>
              <StatusMark :state="health ? 'ok' : checking ? 'running' : 'unknown'" :word="health ? 'ok' : checking ? 'checking' : 'unknown'" />
            </dd>
            <dt>ClickHouse</dt>
            <dd>
              <StatusMark :state="health ? 'ok' : checking ? 'running' : 'unknown'" :word="health ? 'ok' : checking ? 'checking' : 'unknown'" />
            </dd>
            <dt>Coordinator</dt>
            <dd><StatusMark :state="meta ? 'ok' : 'broken'" :word="meta ? 'online' : 'unavailable'" /></dd>
            <dt>Version</dt>
            <dd class="mono">{{ meta?.version ?? '—' }}</dd>
            <dt>Public URL</dt>
            <dd class="mono">{{ meta?.publicUrl ?? '—' }}</dd>
            <dt>gRPC address</dt>
            <dd class="mono">{{ meta?.grpcPublicAddr ?? '—' }}</dd>
            <dt>Node enrollment</dt>
            <dd>
              <StatusMark
                :state="meta?.enrollmentAvailable ? 'ok' : 'off'"
                :word="meta?.enrollmentAvailable ? 'enabled' : 'disabled'"
              />
            </dd>
            <dt>Event retention</dt>
            <dd class="mono">{{ meta?.clickhouseRetentionDays ?? 30 }} days</dd>
          </dl>
        </div>
        <div class="foot">Liveness is /ping. Readiness checks PostgreSQL and ClickHouse.</div>
      </section>
    </div>

    <div class="areas">
      <section class="area">
        <div class="pc-title"><h2>Agent defaults</h2><span class="area-title-note">published with each policy snapshot</span></div>
        <div class="area-body">
          <div class="form-grid grid-4">
            <div class="field">
              <span class="field-label">DNS inspection</span>
              <StatusMark :state="meta?.agentSettings.dnsInspection ? 'ok' : 'off'" :word="meta?.agentSettings.dnsInspection ? 'enabled' : 'disabled'" />
            </div>
            <div class="field">
              <span class="field-label">TLS / SNI inspection</span>
              <StatusMark :state="meta?.agentSettings.tlsInspection ? 'ok' : 'off'" :word="meta?.agentSettings.tlsInspection ? 'enabled' : 'disabled'" />
            </div>
            <div class="field">
              <span class="field-label">TLS ports</span>
              <span class="mono">{{ meta?.agentSettings.tlsInspectorPorts.join(', ') || '—' }}</span>
            </div>
            <div class="field">
              <span class="field-label">Batch</span>
              <span class="mono">{{ meta?.agentSettings.batchMaxEvents ?? '—' }} events / {{ meta?.agentSettings.flushIntervalMs ?? '—' }} ms</span>
            </div>
          </div>
        </div>
        <div class="foot">
          Each agent exposes Prometheus metrics on its configured metrics address at /metrics. Conntrack accounting is
          enabled by the installer.
        </div>
      </section>
    </div>
  </div>
</template>