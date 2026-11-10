<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import ConsoleDialog from '@/components/console/ConsoleDialog.vue'
import PageHead from '@/components/console/PageHead.vue'
import StatusMark from '@/components/console/StatusMark.vue'
import { api, errorMessage, type AsnLookup, type Policy, type Rule, type RuleKind } from '@/lib/api'
import { formatNumber, formatTime } from '@/lib/utils'
import { canWrite } from '@/lib/session'
import { useRefresh } from '@/lib/ui'

const route = useRoute()
const router = useRouter()
const policyId = route.params.id as string

const policy = ref<Policy | null>(null)
const loading = ref(true)
const error = ref('')
const saving = ref(false)
const dialogError = ref('')

const editOpen = ref(false)
const name = ref('')
const description = ref('')
const enabled = ref(true)

const ruleOpen = ref(false)
const editingRule = ref<Rule | null>(null)
const ruleKind = ref<RuleKind>('domain')
const ruleValue = ref('')
const ruleNote = ref('')
const ruleEnabled = ref(true)

const deleteRuleTarget = ref<Rule | null>(null)
const deletePolicyOpen = ref(false)
const deleteName = ref('')
const asnInfo = ref<AsnLookup | null>(null)
const asnLoading = ref(false)
const asnError = ref('')

const rules = computed(() => policy.value?.rules ?? [])
const nodes = computed(() => policy.value?.nodes ?? [])
const subtitle = computed(() => {
  if (!policy.value) return error.value || 'Loading policy…'
  return `${policy.value.enabled ? 'enabled' : 'disabled'} · ${rules.value.length} rules · ${nodes.value.length} nodes · updated ${formatTime(policy.value.updatedAt)}`
})

function nodeState(status: string): 'ok' | 'off' | 'unknown' {
  if (status === 'online') return 'ok'
  if (status === 'offline') return 'off'
  return 'unknown'
}
const ruleHint = computed(() => {
  if (ruleKind.value === 'domain') return 'example.com blocks the domain and subdomains. *.example.com blocks subdomains only.'
  if (ruleKind.value === 'ip') return 'A single IPv4 or IPv6 address, for example 203.0.113.10.'
  if (ruleKind.value === 'asn') return 'AS15169 blocks every prefix currently announced by that autonomous system. Prefixes refresh automatically.'
  return 'An IPv4 or IPv6 range, for example 203.0.113.0/24.'
})

async function load() {
  loading.value = true
  error.value = ''
  try {
    policy.value = await api.policy(policyId)
  } catch (err) {
    error.value = errorMessage(err)
  } finally {
    loading.value = false
  }
}

function openEdit() {
  if (!policy.value) return
  name.value = policy.value.name
  description.value = policy.value.description
  enabled.value = policy.value.enabled
  dialogError.value = ''
  editOpen.value = true
}

async function savePolicy() {
  saving.value = true
  dialogError.value = ''
  try {
    policy.value = await api.updatePolicy(policyId, { name: name.value, description: description.value, enabled: enabled.value })
    editOpen.value = false
  } catch (err) {
    dialogError.value = errorMessage(err)
  } finally {
    saving.value = false
  }
}

function openNewRule() {
  editingRule.value = null
  ruleKind.value = 'domain'
  ruleValue.value = ''
  ruleNote.value = ''
  ruleEnabled.value = true
  asnInfo.value = null
  asnError.value = ''
  dialogError.value = ''
  ruleOpen.value = true
}

function openEditRule(rule: Rule) {
  editingRule.value = rule
  ruleKind.value = rule.kind
  ruleValue.value = rule.value
  ruleNote.value = rule.note
  ruleEnabled.value = rule.enabled
  asnInfo.value = rule.kind === 'asn' ? {
    asn: rule.value, number: 0, holder: '', prefixCount: rule.prefixCount ?? 0,
    prefixes: [], refreshedAt: '',
  } : null
  asnError.value = ''
  dialogError.value = ''
  ruleOpen.value = true
}

async function lookupASN() {
  if (ruleKind.value !== 'asn' || !ruleValue.value.trim()) return
  asnLoading.value = true
  asnError.value = ''
  try {
    asnInfo.value = await api.lookupASN(ruleValue.value.trim())
  } catch (err) {
    asnInfo.value = null
    asnError.value = errorMessage(err)
  } finally {
    asnLoading.value = false
  }
}

function resetASN() {
  asnInfo.value = null
  asnError.value = ''
}

async function saveRule() {
  saving.value = true
  dialogError.value = ''
  const body = {
    kind: ruleKind.value,
    value: ruleValue.value,
    action: 'block' as const,
    note: ruleNote.value,
    enabled: ruleEnabled.value,
  }
  try {
    if (editingRule.value) await api.updateRule(policyId, editingRule.value.id, body)
    else await api.createRule(policyId, body)
    ruleOpen.value = false
    await load()
  } catch (err) {
    dialogError.value = errorMessage(err)
  } finally {
    saving.value = false
  }
}

async function deleteRule() {
  if (!deleteRuleTarget.value) return
  saving.value = true
  dialogError.value = ''
  try {
    await api.deleteRule(policyId, deleteRuleTarget.value.id)
    deleteRuleTarget.value = null
    await load()
  } catch (err) {
    dialogError.value = errorMessage(err)
  } finally {
    saving.value = false
  }
}

function openDeletePolicy() {
  deleteName.value = ''
  dialogError.value = ''
  deletePolicyOpen.value = true
}

async function deletePolicy() {
  if (!policy.value || deleteName.value !== policy.value.name) return
  saving.value = true
  dialogError.value = ''
  try {
    await api.deletePolicy(policyId)
    deletePolicyOpen.value = false
    await router.push('/policies')
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
    <RouterLink class="backlink" to="/policies">‹ Policies</RouterLink>
    <PageHead :title="policy?.name ?? 'Policy'" :subtitle="subtitle">
      <template #actions>
        <StatusMark v-if="policy" :state="policy.enabled ? 'ok' : 'off'" :word="policy.enabled ? 'enabled' : 'disabled'" />
        <button class="pc-btn pc-sm" type="button" :disabled="loading" @click="load">Refresh</button>
        <button v-if="canWrite" class="pc-btn pc-sm" type="button" @click="openEdit">Edit</button>
        <button v-if="canWrite" class="pc-btn pc-sm pc-pri" type="button" @click="openNewRule">Add rule</button>
        <button v-if="canWrite" class="pc-btn pc-sm pc-dng" type="button" @click="openDeletePolicy">Delete…</button>
      </template>
    </PageHead>

    <div v-if="error" class="band band-love">
      <div class="band-head">
        <StatusMark state="broken" word="request failed" />
        <span class="band-actions"><button class="pc-btn pc-sm" type="button" @click="load">Retry</button></span>
      </div>
      <p>{{ error }}</p>
    </div>

    <div v-if="policy && !policy.enabled" class="band band-gold">
      <div class="band-head">
        <StatusMark state="off" word="disabled" />
        <span>This policy is not distributed to any node. Enable it to publish its rules.</span>
        <span v-if="canWrite" class="band-actions"><button class="pc-btn pc-sm" type="button" @click="openEdit">Edit policy</button></span>
      </div>
    </div>

    <div class="areas">
      <section class="area">
        <div class="pc-title">
          <h2>Nodes</h2>
          <span class="area-title-note">{{ nodes.length }} assigned · distributed only while the policy is enabled</span>
        </div>
        <div class="area-body flush">
          <div class="tbl">
            <table class="pc-table tbl-narrow">
              <thead>
                <tr>
                  <th>Node</th>
                  <th>Status</th>
                  <th>Agent</th>
                  <th class="num">Revision</th>
                </tr>
              </thead>
              <tbody>
                <tr
                  v-for="node in nodes"
                  :key="node.id"
                  class="pc-row"
                  tabindex="0"
                  @click="router.push(`/nodes/${node.id}`)"
                  @keydown.enter="router.push(`/nodes/${node.id}`)"
                >
                  <td>
                    {{ node.name }}
                    <span class="muted mono">{{ node.id.slice(0, 8) }}</span>
                  </td>
                  <td><StatusMark :state="nodeState(node.status)" :word="node.status" /></td>
                  <td class="mono">{{ node.agentVersion || '—' }}</td>
                  <td class="num mono">{{ formatNumber(node.policyVersion) }}</td>
                </tr>
                <tr v-if="!loading && nodes.length === 0">
                  <td colspan="4" class="empty">
                    This policy is not assigned to any node. Assign it from a node page.
                  </td>
                </tr>
              </tbody>
            </table>
          </div>
        </div>
        <div class="foot">Each assigned node receives this policy's enabled rules with the next revision.</div>
      </section>
    </div>

    <div class="areas">
      <section class="area">
        <div class="pc-title">
          <h2>Rules</h2>
          <span class="area-title-note">{{ rules.length }} configured · allow by default</span>
        </div>
        <div class="area-body flush">
          <div class="tbl">
            <table class="pc-table">
              <thead>
                <tr>
                  <th>Kind</th>
                  <th>Value</th>
                  <th>Note</th>
                  <th>Status</th>
                  <th class="num">Actions</th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="rule in rules" :key="rule.id">
                  <td><span class="tag">{{ rule.kind }}</span></td>
                  <td class="mono">
                    {{ rule.value }}
                    <span v-if="rule.kind === 'asn' && rule.prefixCount" class="muted">· {{ formatNumber(rule.prefixCount) }} prefixes</span>
                  </td>
                  <td class="subtle">{{ rule.note || '—' }}</td>
                  <td><StatusMark :state="rule.enabled ? 'ok' : 'off'" :word="rule.enabled ? 'enabled' : 'disabled'" /></td>
                  <td class="num">
                    <span v-if="canWrite" class="actions-line" style="justify-content: flex-end">
                      <button class="pc-btn pc-sm" type="button" @click="openEditRule(rule)">Edit</button>
                      <button class="pc-btn pc-sm pc-dng" type="button" @click="deleteRuleTarget = rule">Delete…</button>
                    </span>
                    <span v-else class="muted">—</span>
                  </td>
                </tr>
                <tr v-if="!loading && rules.length === 0">
                  <td colspan="5" class="empty">This policy has no rules yet.</td>
                </tr>
              </tbody>
            </table>
          </div>
        </div>
        <div class="foot">Domain rules block DNS answers and TLS SNI. IP and CIDR rules are enforced in nftables.</div>
      </section>
    </div>

    <ConsoleDialog v-model:open="editOpen" title="Edit policy" @submit="savePolicy">
      <div class="stack">
        <div class="field">
          <label for="policy-name">Name</label>
          <input id="policy-name" v-model="name" class="pc-inp" />
        </div>
        <div class="field">
          <label for="policy-description">Description</label>
          <textarea id="policy-description" v-model="description" class="pc-inp" />
        </div>
        <div class="spread">
          <span class="field-hint">Enabled</span>
          <button class="pc-sw" type="button" role="switch" :aria-checked="enabled" @click="enabled = !enabled" />
        </div>
        <p v-if="dialogError" class="err">{{ dialogError }}</p>
      </div>
      <template #footer>
        <button class="pc-btn pc-pri" type="button" :disabled="saving || !name.trim()" @click="savePolicy">Save</button>
        <button class="pc-btn pc-ghost" type="button" @click="editOpen = false">Cancel</button>
      </template>
    </ConsoleDialog>

    <ConsoleDialog
      v-model:open="ruleOpen"
      :title="editingRule ? 'Edit rule' : 'Add rule'"
      width="min(620px, 100%)"
      @submit="saveRule"
    >
      <div class="stack">
        <div class="form-grid two">
          <div class="field">
            <label for="rule-kind">Kind</label>
            <select id="rule-kind" v-model="ruleKind" class="pc-inp" @change="resetASN">
              <option value="domain">Domain</option>
              <option value="ip">IP address</option>
              <option value="cidr">CIDR range</option>
              <option value="asn">Autonomous system</option>
            </select>
          </div>
          <div class="field">
            <label for="rule-value">Value</label>
            <input
              id="rule-value"
              v-model="ruleValue"
              class="pc-inp mono"
              :placeholder="ruleKind === 'asn' ? 'AS15169' : 'example.com'"
              @input="resetASN"
            />
          </div>
        </div>
        <p class="field-hint">{{ ruleHint }}</p>
        <div v-if="ruleKind === 'asn'" class="actions-line">
          <button class="pc-btn pc-sm" type="button" :disabled="asnLoading || !ruleValue.trim()" @click="lookupASN">
            Look up ASN
          </button>
          <StatusMark v-if="asnLoading" state="running" word="resolving" />
          <template v-else-if="asnInfo">
            <StatusMark :state="asnInfo.error ? 'look' : 'ok'" :word="`${formatNumber(asnInfo.prefixCount)} prefixes`" />
            <span class="field-hint">{{ asnInfo.holder || asnInfo.asn }}</span>
            <span v-if="asnInfo.error" class="err">{{ asnInfo.error }}</span>
          </template>
          <span v-if="asnError" class="err">{{ asnError }}</span>
        </div>
        <div class="field">
          <label for="rule-note">Note</label>
          <input id="rule-note" v-model="ruleNote" class="pc-inp" placeholder="Optional" />
        </div>
        <div class="spread">
          <span class="field-hint">Enabled</span>
          <button class="pc-sw" type="button" role="switch" :aria-checked="ruleEnabled" @click="ruleEnabled = !ruleEnabled" />
        </div>
        <p v-if="dialogError" class="err">{{ dialogError }}</p>
      </div>
      <template #footer>
        <button class="pc-btn pc-pri" type="button" :disabled="saving || !ruleValue.trim()" @click="saveRule">Save</button>
        <button class="pc-btn pc-ghost" type="button" @click="ruleOpen = false">Cancel</button>
      </template>
    </ConsoleDialog>

    <ConsoleDialog
      :open="!!deleteRuleTarget"
      title="Delete rule"
      @update:open="deleteRuleTarget = $event ? deleteRuleTarget : null"
      @submit="deleteRule"
    >
      <div class="stack">
        <p class="field-hint">
          Remove <span class="mono">{{ deleteRuleTarget?.value }}</span> from this policy? Assigned nodes receive a new
          revision.
        </p>
        <p v-if="dialogError" class="err">{{ dialogError }}</p>
      </div>
      <template #footer>
        <button class="pc-btn pc-dng" type="button" :disabled="saving" @click="deleteRule">Delete rule</button>
        <button class="pc-btn pc-ghost" type="button" @click="deleteRuleTarget = null">Cancel</button>
      </template>
    </ConsoleDialog>

    <ConsoleDialog v-model:open="deletePolicyOpen" title="Delete policy" @submit="deletePolicy">
      <div class="stack">
        <p class="field-hint">
          This removes <b>{{ policy?.name }}</b> and its rules. Node assignments are detached and every affected node
          receives a new revision. This cannot be undone.
        </p>
        <div class="field">
          <label for="delete-policy-name">Type the policy name to confirm</label>
          <input id="delete-policy-name" v-model="deleteName" class="pc-inp mono" autocomplete="off" />
        </div>
        <p v-if="dialogError" class="err">{{ dialogError }}</p>
      </div>
      <template #footer>
        <button class="pc-btn pc-dng" type="button" :disabled="saving || deleteName !== policy?.name" @click="deletePolicy">
          Delete policy
        </button>
        <button class="pc-btn pc-ghost" type="button" @click="deletePolicyOpen = false">Cancel</button>
      </template>
    </ConsoleDialog>
  </div>
</template>