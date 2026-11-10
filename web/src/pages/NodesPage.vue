<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import ConsoleDialog from '@/components/console/ConsoleDialog.vue'
import PageHead from '@/components/console/PageHead.vue'
import StatusMark from '@/components/console/StatusMark.vue'
import { api, errorMessage, type DeployKey, type DeployKeyResult, type Node, type Policy } from '@/lib/api'
import { copyText } from '@/lib/clipboard'
import { meta } from '@/lib/meta'
import { canWrite } from '@/lib/session'
import { formatNumber, formatRelative, formatTime } from '@/lib/utils'
import { useRefresh, useRowCursor } from '@/lib/ui'

const router = useRouter()
const nodes = ref<Node[]>([])
const loading = ref(true)
const error = ref('')
const filter = ref<'all' | 'online' | 'offline' | 'pending'>('all')
const search = ref('')

const installOpen = ref(false)
const installName = ref('')
const installTtl = ref(60)
const installPolicyIds = ref<string[]>([])
const installPolicies = ref<Policy[]>([])
const installResult = ref<DeployKeyResult | null>(null)
const installError = ref('')
const installSaving = ref(false)
const copied = ref(false)
const deployKeys = ref<DeployKey[]>([])
const pendingKeys = computed(() => deployKeys.value.filter((key) => keyState(key) === 'pending'))

const filters = [
  { value: 'all', label: 'All' },
  { value: 'online', label: 'Online' },
  { value: 'offline', label: 'Offline' },
  { value: 'pending', label: 'Pending' },
] as const

const online = computed(() => nodes.value.filter((node) => node.status === 'online').length)
const subtitle = computed(() => `${nodes.value.length} enrolled · ${online.value} online`)
const visible = computed(() => {
  const needle = search.value.trim().toLowerCase()
  return nodes.value.filter((node) => {
    if (filter.value !== 'all' && node.status !== filter.value) return false
    if (!needle) return true
    return `${node.name} ${node.id} ${node.hostname} ${node.os}`.toLowerCase().includes(needle)
  })
})

function stateOf(status: string): 'ok' | 'broken' | 'unknown' {
  if (status === 'online') return 'ok'
  if (status === 'offline') return 'broken'
  return 'unknown'
}

function open(node: Node) {
  void router.push(`/nodes/${encodeURIComponent(node.id)}`)
}

async function load() {
  loading.value = true
  error.value = ''
  try {
    nodes.value = (await api.nodes()).items
    if (canWrite.value) await loadKeys()
  } catch (err) {
    error.value = errorMessage(err)
  } finally {
    loading.value = false
  }
}

async function loadKeys() {
  try {
    deployKeys.value = (await api.deployKeys()).items
  } catch {
    deployKeys.value = []
  }
}

function openInstall() {
  installName.value = ''
  installTtl.value = 60
  installPolicyIds.value = []
  installResult.value = null
  installError.value = ''
  copied.value = false
  installOpen.value = true
  if (installPolicies.value.length === 0) {
    api
      .policies()
      .then((result) => (installPolicies.value = result.items))
      .catch((err) => (installError.value = errorMessage(err)))
  }
}

function toggleInstallPolicy(policyId: string) {
  if (installPolicyIds.value.includes(policyId)) {
    installPolicyIds.value = installPolicyIds.value.filter((id) => id !== policyId)
  } else {
    installPolicyIds.value = [...installPolicyIds.value, policyId]
  }
}

async function createInstall() {
  if (!installName.value.trim() || installSaving.value) return
  installSaving.value = true
  installError.value = ''
  try {
    installResult.value = await api.createDeployKey({
      nodeName: installName.value.trim(),
      ttlMinutes: installTtl.value,
      policyIds: installPolicyIds.value,
    })
    await loadKeys()
  } catch (err) {
    installError.value = errorMessage(err)
  } finally {
    installSaving.value = false
  }
}

async function copyInstall() {
  if (!installResult.value) return
  copied.value = await copyText(installResult.value.installCommand)
  window.setTimeout(() => (copied.value = false), 2500)
}

async function revokeKey(key: DeployKey) {
  try {
    await api.deleteDeployKey(key.id)
    if (installResult.value?.key.id === key.id) installOpen.value = false
    await loadKeys()
  } catch (err) {
    installError.value = errorMessage(err)
  }
}

function keyState(key: DeployKey): 'pending' | 'used' | 'expired' {
  if (key.usedAt) return 'used'
  if (Date.parse(key.expiresAt) <= Date.now()) return 'expired'
  return 'pending'
}

function keyStateMark(key: DeployKey): 'look' | 'ok' | 'off' {
  const state = keyState(key)
  if (state === 'used') return 'ok'
  if (state === 'expired') return 'off'
  return 'look'
}

function keyWord(key: DeployKey): string {
  const state = keyState(key)
  if (state === 'used') return 'used'
  if (state === 'expired') return 'expired'
  return 'pending'
}

function policyName(policyId: string): string {
  return installPolicies.value.find((policy) => policy.id === policyId)?.name ?? policyId.slice(0, 8)
}

useRefresh(load)
const { cursor } = useRowCursor(visible, open)
onMounted(load)
</script>

<template>
  <div class="page">
    <PageHead title="Nodes" :subtitle="subtitle">
      <template #actions>
        <button class="pc-btn pc-sm" type="button" :disabled="loading" @click="load">Refresh</button>
        <button v-if="canWrite" class="pc-btn pc-sm pc-pri" type="button" @click="openInstall">Install node</button>
      </template>
    </PageHead>

    <div v-if="error" class="band band-love">
      <div class="band-head">
        <StatusMark state="broken" word="request failed" />
        <span class="band-actions"><button class="pc-btn pc-sm" type="button" @click="load">Retry</button></span>
      </div>
      <p>{{ error }}</p>
    </div>

    <div class="filters">
      <button
        v-for="option in filters"
        :key="option.value"
        class="pc-chip"
        type="button"
        :aria-pressed="filter === option.value"
        @click="filter = option.value"
      >
        {{ option.label }}
      </button>
      <div class="grow">
        <input v-model="search" class="pc-inp" type="search" placeholder="Filter by name, id, host or platform" />
      </div>
    </div>

    <div class="areas">
      <section class="area">
        <div class="pc-title">
          <h2>Enrolled nodes</h2>
          <span class="area-title-note">{{ visible.length }} shown · agents enroll with their mTLS identity</span>
        </div>
        <div class="area-body flush">
          <div class="tbl">
            <table class="pc-table">
              <thead>
                <tr>
                  <th>Node</th>
                  <th>Status</th>
                  <th>Agent</th>
                  <th>Platform</th>
                  <th class="num">Policies</th>
                  <th class="num">Revision</th>
                  <th class="num">Last seen</th>
                </tr>
              </thead>
              <tbody>
                <tr v-if="loading">
                  <td colspan="7" class="empty">Loading nodes…</td>
                </tr>
                <template v-else>
                <tr
                  v-for="node in visible"
                  :key="node.id"
                  class="pc-row"
                  :class="{ 'pc-cur': visible.indexOf(node) === cursor }"
                  tabindex="0"
                  @click="open(node)"
                  @keydown.enter="open(node)"
                >
                  <td>
                    <span class="ell" :title="node.id">{{ node.name }}</span>
                  </td>
                  <td><StatusMark :state="stateOf(node.status)" :word="node.status" /></td>
                  <td class="mono subtle">{{ node.agentVersion || '—' }}</td>
                  <td class="subtle">{{ node.os || '—' }} <span class="muted mono">{{ node.kernel }}</span></td>
                  <td class="num">{{ formatNumber(node.policyIds.length) }}</td>
                  <td class="num">{{ formatNumber(node.policyVersion) }}</td>
                  <td class="num muted">{{ formatRelative(node.lastSeenAt) }}</td>
                </tr>
                <tr v-if="visible.length === 0">
                  <td colspan="7" class="empty">
                    {{ nodes.length === 0 ? 'No agents have connected yet.' : 'No node matches the filters.' }}
                  </td>
                </tr>
                </template>
              </tbody>
            </table>
          </div>
        </div>
        <div class="foot">
          New agents enroll with a one-time deploy key, then register on first connection. Assign policies on the node
          page or when creating the deploy key.
        </div>
      </section>
    </div>

    <div v-if="canWrite && pendingKeys.length > 0" class="areas">
      <section class="area">
        <div class="pc-title">
          <h2>Pending installs</h2>
          <span class="area-title-note">{{ pendingKeys.length }} pending · one-time enrollment</span>
        </div>
        <div class="area-body flush">
          <div class="tbl">
            <table class="pc-table">
              <thead>
                <tr>
                  <th>Node</th>
                  <th>Policies</th>
                  <th>Created</th>
                  <th>Expires</th>
                  <th>Status</th>
                  <th class="num">Action</th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="key in pendingKeys" :key="key.id">
                  <td class="mono">{{ key.nodeName }}</td>
                  <td class="subtle">
                    <span v-if="key.policyIds.length === 0" class="muted">—</span>
                    <span v-else class="tags">
                      <span v-for="policyId in key.policyIds" :key="policyId" class="tag">{{ policyName(policyId) }}</span>
                    </span>
                  </td>
                  <td class="muted">{{ formatTime(key.createdAt) }}</td>
                  <td class="muted">{{ formatTime(key.expiresAt) }}</td>
                  <td><StatusMark :state="keyStateMark(key)" :word="keyWord(key)" /></td>
                  <td class="num">
                    <button
                      v-if="keyState(key) === 'pending'"
                      class="pc-btn pc-sm pc-dng"
                      type="button"
                      @click="revokeKey(key)"
                    >
                      Revoke
                    </button>
                    <span v-else class="muted">—</span>
                  </td>
                </tr>
              </tbody>
            </table>
          </div>
        </div>
        <div class="foot">The install link contains a one-time deploy key. It never stores the key in plaintext on the coordinator.</div>
      </section>
    </div>

    <ConsoleDialog
      v-model:open="installOpen"
      :title="installResult ? `Install ${installResult.key.nodeName}` : 'Install node'"
      width="min(680px, 100%)"
      @submit="installResult ? undefined : createInstall()"
    >
      <div v-if="installResult" class="stack">
        <dl class="kv">
          <dt>Node</dt><dd class="mono">{{ installResult.key.nodeName }}</dd>
          <dt>Expires</dt><dd>{{ formatTime(installResult.key.expiresAt) }}</dd>
          <dt>Key</dt><dd class="mono">one-time · shown once</dd>
        </dl>
        <div class="field">
          <label for="install-command">Run on the node as root</label>
          <code id="install-command" class="cmd">{{ installResult.installCommand }}</code>
        </div>
        <div class="actions-line">
          <button class="pc-btn pc-sm" type="button" @click="copyInstall">Copy command</button>
          <StatusMark v-if="copied" state="ok" word="copied" />
        </div>
        <p class="field-hint">
          The installer generates the private key on the node, exchanges the deploy key for a signed certificate, and
          starts the agent. The key cannot be reused.
        </p>
      </div>
      <div v-else class="stack">
        <div v-if="meta?.publicUrl?.startsWith('http://')" class="band band-gold" style="margin: 0">
          <div class="band-head">
            <StatusMark state="look" word="insecure link" />
            <span>Deploy keys are sent in clear text over HTTP. Use HTTPS for production.</span>
          </div>
        </div>
        <div v-if="meta && !meta.enrollmentAvailable" class="band band-gold" style="margin: 0">
          <div class="band-head">
            <StatusMark state="look" word="enrollment disabled" />
            <span>Node enrollment is disabled on the coordinator.</span>
          </div>
        </div>
        <div class="form-grid two">
          <div class="field">
            <label for="install-name">Node name</label>
            <input
              id="install-name"
              v-model="installName"
              class="pc-inp mono"
              placeholder="edge-1"
              autocomplete="off"
              spellcheck="false"
            />
            <span class="field-hint">Becomes the certificate CN. It must not already exist.</span>
          </div>
          <div class="field">
            <label for="install-ttl">Link expires</label>
            <select id="install-ttl" v-model.number="installTtl" class="pc-inp">
              <option :value="15">15 minutes</option>
              <option :value="60">1 hour</option>
              <option :value="360">6 hours</option>
              <option :value="1440">24 hours</option>
            </select>
          </div>
        </div>
        <div class="field">
          <span class="field-label">Policies</span>
          <div class="dialog-list">
            <label v-for="policy in installPolicies" :key="policy.id" class="dialog-list-row">
              <input
                type="checkbox"
                :checked="installPolicyIds.includes(policy.id)"
                @change="toggleInstallPolicy(policy.id)"
              />
              <span class="row-main">
                <b>{{ policy.name }}</b>
                <span>{{ policy.description || `${policy.ruleCount} rules` }}</span>
              </span>
            </label>
            <div v-if="installPolicies.length === 0" class="empty">No policies exist yet.</div>
          </div>
          <span class="field-hint">Assigned when the node enrolls. You can change this later on the node page.</span>
        </div>
        <p v-if="installError" class="err">{{ installError }}</p>
      </div>
      <template #footer>
        <template v-if="installResult">
          <button class="pc-btn pc-pri" type="button" @click="installOpen = false">Done</button>
          <button class="pc-btn pc-dng" type="button" @click="revokeKey(installResult.key)">Revoke key</button>
        </template>
        <template v-else>
          <button
            class="pc-btn pc-pri"
            type="button"
            :disabled="installSaving || !installName.trim() || !meta?.enrollmentAvailable"
            @click="createInstall"
          >
            {{ installSaving ? 'Creating…' : 'Create install link' }}
          </button>
          <button class="pc-btn pc-ghost" type="button" @click="installOpen = false">Cancel</button>
        </template>
      </template>
    </ConsoleDialog>
  </div>
</template>