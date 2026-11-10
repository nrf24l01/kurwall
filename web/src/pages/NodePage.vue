<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRoute } from 'vue-router'
import ConsoleDialog from '@/components/console/ConsoleDialog.vue'
import PageHead from '@/components/console/PageHead.vue'
import StatusMark from '@/components/console/StatusMark.vue'
import { api, errorMessage, type Node, type Policy, type Snapshot } from '@/lib/api'
import { formatNumber, formatTime } from '@/lib/utils'
import { canWrite } from '@/lib/session'
import { useRefresh } from '@/lib/ui'

const route = useRoute()
const nodeId = route.params.id as string

const node = ref<Node | null>(null)
const policies = ref<Policy[]>([])
const snapshot = ref<Snapshot | null>(null)
const loading = ref(true)
const error = ref('')
const snapshotError = ref('')

const renameOpen = ref(false)
const nameDraft = ref('')
const labelsOpen = ref(false)
const labelsDraft = ref('')
const policiesOpen = ref(false)
const selectedPolicies = ref<string[]>([])
const saving = ref(false)
const dialogError = ref('')

const subtitle = computed(() => {
  if (!node.value) return error.value || 'Loading node…'
  return `${node.value.status} · ${node.value.hostname || node.value.id} · agent ${node.value.agentVersion || '—'} · revision ${formatNumber(node.value.policyVersion)}`
})
const policyNames = computed(() => new Map(policies.value.map((policy) => [policy.id, policy.name])))
const policyById = computed(() => new Map(policies.value.map((policy) => [policy.id, policy])))
const rules = computed(() => snapshot.value?.rules ?? [])
const state = computed(() => {
  if (!node.value) return 'unknown' as const
  if (node.value.status === 'online') return 'ok' as const
  if (node.value.status === 'offline') return 'broken' as const
  return 'unknown' as const
})

async function load() {
  loading.value = true
  error.value = ''
  snapshotError.value = ''
  try {
    const [loadedNode, loadedPolicies] = await Promise.all([api.node(nodeId), api.policies()])
    node.value = loadedNode
    policies.value = loadedPolicies.items
    nameDraft.value = loadedNode.name
    labelsDraft.value = JSON.stringify(loadedNode.labels, null, 2)
    selectedPolicies.value = [...loadedNode.policyIds]
  } catch (err) {
    error.value = errorMessage(err)
  } finally {
    loading.value = false
  }
  try {
    snapshot.value = await api.nodeSnapshot(nodeId)
  } catch (err) {
    snapshot.value = null
    snapshotError.value = errorMessage(err)
  }
}

function openRename() {
  nameDraft.value = node.value?.name ?? ''
  dialogError.value = ''
  renameOpen.value = true
}

async function saveName() {
  saving.value = true
  dialogError.value = ''
  try {
    await api.updateNode(nodeId, { name: nameDraft.value })
    renameOpen.value = false
    await load()
  } catch (err) {
    dialogError.value = errorMessage(err)
  } finally {
    saving.value = false
  }
}

function openLabels() {
  labelsDraft.value = JSON.stringify(node.value?.labels ?? {}, null, 2)
  dialogError.value = ''
  labelsOpen.value = true
}

async function saveLabels() {
  let labels: Record<string, string>
  try {
    const parsed = JSON.parse(labelsDraft.value) as unknown
    if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) throw new Error('use a JSON object')
    labels = {}
    for (const [key, value] of Object.entries(parsed as Record<string, unknown>)) {
      labels[key] = String(value)
    }
  } catch (err) {
    dialogError.value = `Labels must be a JSON object. ${errorMessage(err)}`
    return
  }
  saving.value = true
  dialogError.value = ''
  try {
    await api.updateNode(nodeId, { labels })
    labelsOpen.value = false
    await load()
  } catch (err) {
    dialogError.value = errorMessage(err)
  } finally {
    saving.value = false
  }
}

function togglePolicy(policyId: string) {
  if (selectedPolicies.value.includes(policyId)) {
    selectedPolicies.value = selectedPolicies.value.filter((id) => id !== policyId)
  } else {
    selectedPolicies.value = [...selectedPolicies.value, policyId]
  }
}

async function savePolicies() {
  saving.value = true
  dialogError.value = ''
  try {
    await api.assignPolicies(nodeId, selectedPolicies.value)
    policiesOpen.value = false
    await load()
  } catch (err) {
    dialogError.value = errorMessage(err)
  } finally {
    saving.value = false
  }
}

useRefresh(load)
onMounted(load)
</script>

<template>
  <div class="page">
    <RouterLink class="backlink" to="/nodes">‹ Nodes</RouterLink>
    <PageHead :title="node?.name ?? 'Node'" :subtitle="subtitle">
      <template #actions>
        <StatusMark v-if="node" :state="state" :word="node.status" />
        <button class="pc-btn pc-sm" type="button" :disabled="loading" @click="load">Refresh</button>
        <button v-if="canWrite" class="pc-btn pc-sm" type="button" @click="openRename">Rename</button>
        <button v-if="canWrite" class="pc-btn pc-sm" type="button" @click="openLabels">Labels</button>
        <button v-if="canWrite" class="pc-btn pc-sm pc-pri" type="button" @click="policiesOpen = true">Manage policies</button>
      </template>
    </PageHead>

    <div v-if="error" class="band band-love">
      <div class="band-head">
        <StatusMark state="broken" word="request failed" />
        <span class="band-actions"><button class="pc-btn pc-sm" type="button" @click="load">Retry</button></span>
      </div>
      <p>{{ error }}</p>
    </div>

    <div v-if="node" class="areas grid-2">
      <section class="area">
        <div class="pc-title"><h2>Node</h2><span class="area-title-note">mTLS identity {{ node.id }}</span></div>
        <div class="area-body">
          <dl class="kv">
            <dt>Node ID</dt><dd class="mono">{{ node.id }}</dd>
            <dt>Hostname</dt><dd>{{ node.hostname || '—' }}</dd>
            <dt>Addresses</dt><dd class="mono">{{ node.addresses.join(', ') || '—' }}</dd>
            <dt>Agent</dt><dd class="mono">{{ node.agentVersion || '—' }}</dd>
            <dt>Platform</dt><dd>{{ node.os || '—' }} <span class="mono subtle">{{ node.kernel }}</span></dd>
            <dt>Last seen</dt><dd>{{ formatTime(node.lastSeenAt) }}</dd>
            <dt>Applied revision</dt><dd class="mono">{{ formatNumber(node.policyVersion) }}</dd>
            <dt>Labels</dt>
            <dd>
              <span v-if="Object.keys(node.labels).length === 0" class="subtle">—</span>
              <span v-else class="tags">
                <span v-for="(value, key) in node.labels" :key="key" class="tag">{{ key }}={{ value }}</span>
              </span>
            </dd>
          </dl>
        </div>
      </section>

      <section class="area">
        <div class="pc-title">
          <h2>Policies</h2>
          <span class="area-title-note">{{ node.policyIds.length }} assigned</span>
          <span v-if="canWrite" class="area-title-actions"><button class="pc-btn pc-sm" type="button" @click="policiesOpen = true">Manage</button></span>
        </div>
        <div class="area-body flush">
          <div class="list">
            <RouterLink v-for="policyId in node.policyIds" :key="policyId" class="list-row" :to="`/policies/${policyId}`">
              <span class="name">{{ policyNames.get(policyId) ?? policyId }}</span>
              <span class="value">
                <StatusMark
                  v-if="policyById.get(policyId)"
                  :state="policyById.get(policyId)?.enabled ? 'ok' : 'off'"
                  :word="policyById.get(policyId)?.enabled ? 'enabled' : 'disabled'"
                />
              </span>
            </RouterLink>
            <div v-if="node.policyIds.length === 0" class="empty">No policies assigned. The node records traffic only.</div>
          </div>
        </div>
        <div class="foot">Saving a new selection publishes a revision and pushes it to this node.</div>
      </section>
    </div>

    <div class="areas">
      <section class="area">
        <div class="pc-title">
          <h2>Effective rules</h2>
          <span class="area-title-note">{{ rules.length }} enabled</span>
        </div>
        <div class="area-body flush">
          <div v-if="snapshotError" class="empty">{{ snapshotError }}</div>
          <div v-else class="tbl">
            <table class="pc-table">
              <thead>
                <tr>
                  <th>Kind</th>
                  <th>Value</th>
                  <th>Policy</th>
                  <th>Note</th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="rule in rules" :key="rule.id">
                  <td><span class="tag">{{ rule.kind }}</span></td>
                  <td class="mono">
                    {{ rule.value }}
                    <span v-if="rule.kind === 'asn' && rule.prefixCount" class="muted">· {{ formatNumber(rule.prefixCount) }} prefixes</span>
                  </td>
                  <td class="subtle">{{ rule.policyName }}</td>
                  <td class="subtle">{{ rule.note || '—' }}</td>
                </tr>
                <tr v-if="rules.length === 0">
                  <td colspan="4" class="empty">No rules are distributed to this node.</td>
                </tr>
              </tbody>
            </table>
          </div>
        </div>
        <div v-if="snapshot" class="foot">
          Inspection: DNS {{ snapshot.settings.dnsInspection ? 'on' : 'off' }}, TLS {{ snapshot.settings.tlsInspection ? 'on' : 'off' }},
          ports {{ snapshot.settings.tlsInspectorPorts.join(', ') || '—' }}. Snapshot revision {{ snapshot.version }}.
        </div>
      </section>
    </div>

    <ConsoleDialog v-model:open="renameOpen" title="Rename node" @submit="saveName">
      <div class="stack">
        <div class="field">
          <label for="node-name">Name</label>
          <input id="node-name" v-model="nameDraft" class="pc-inp" />
        </div>
        <p v-if="dialogError" class="err">{{ dialogError }}</p>
      </div>
      <template #footer>
        <button class="pc-btn pc-pri" type="button" :disabled="saving || !nameDraft.trim()" @click="saveName">Save</button>
        <button class="pc-btn pc-ghost" type="button" @click="renameOpen = false">Cancel</button>
      </template>
    </ConsoleDialog>

    <ConsoleDialog v-model:open="labelsOpen" title="Edit labels" @submit="saveLabels">
      <div class="stack">
        <p class="field-hint">A JSON object of free-form metadata, for example {"role":"edge","site":"fra"}.</p>
        <div class="field">
          <label for="node-labels">JSON</label>
          <textarea id="node-labels" v-model="labelsDraft" class="pc-inp mono" />
        </div>
        <p v-if="dialogError" class="err">{{ dialogError }}</p>
      </div>
      <template #footer>
        <button class="pc-btn pc-pri" type="button" :disabled="saving" @click="saveLabels">Save</button>
        <button class="pc-btn pc-ghost" type="button" @click="labelsOpen = false">Cancel</button>
      </template>
    </ConsoleDialog>

    <ConsoleDialog v-model:open="policiesOpen" title="Assign policies" width="min(560px, 100%)" @submit="savePolicies">
      <div class="stack">
        <p class="field-hint">Only enabled rules in the selected policies reach the agent.</p>
        <div class="dialog-list">
          <label v-for="policy in policies" :key="policy.id" class="dialog-list-row">
            <input
              type="checkbox"
              :checked="selectedPolicies.includes(policy.id)"
              @change="togglePolicy(policy.id)"
            />
            <span class="row-main">
              <b>{{ policy.name }}</b>
              <span>{{ policy.description || `${policy.ruleCount} rules` }}</span>
            </span>
          </label>
          <div v-if="policies.length === 0" class="empty">Create a policy first.</div>
        </div>
        <p v-if="dialogError" class="err">{{ dialogError }}</p>
      </div>
      <template #footer>
        <button class="pc-btn pc-pri" type="button" :disabled="saving" @click="savePolicies">Save</button>
        <button class="pc-btn pc-ghost" type="button" @click="policiesOpen = false">Cancel</button>
      </template>
    </ConsoleDialog>
  </div>
</template>