<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import ConsoleDialog from '@/components/console/ConsoleDialog.vue'
import PageHead from '@/components/console/PageHead.vue'
import StatusMark from '@/components/console/StatusMark.vue'
import { api, errorMessage, type Policy } from '@/lib/api'
import { formatNumber, formatTime } from '@/lib/utils'
import { canWrite } from '@/lib/session'
import { useRefresh, useRowCursor } from '@/lib/ui'

const router = useRouter()
const policies = ref<Policy[]>([])
const loading = ref(true)
const error = ref('')
const createOpen = ref(false)
const name = ref('')
const description = ref('')
const enabled = ref(true)
const saving = ref(false)
const dialogError = ref('')

const subtitle = computed(
  () => `${policies.value.length} policies · ${policies.value.reduce((sum, policy) => sum + policy.ruleCount, 0)} rules`,
)

async function load() {
  loading.value = true
  error.value = ''
  try {
    policies.value = (await api.policies()).items
  } catch (err) {
    error.value = errorMessage(err)
  } finally {
    loading.value = false
  }
}

function openCreate() {
  if (!canWrite.value) return
  name.value = ''
  description.value = ''
  enabled.value = true
  dialogError.value = ''
  createOpen.value = true
}

async function create() {
  saving.value = true
  dialogError.value = ''
  try {
    const policy = await api.createPolicy({ name: name.value, description: description.value, enabled: enabled.value })
    createOpen.value = false
    await router.push(`/policies/${policy.id}`)
  } catch (err) {
    dialogError.value = errorMessage(err)
  } finally {
    saving.value = false
  }
}

function open(policy: Policy) {
  void router.push(`/policies/${policy.id}`)
}

function assignedNodes(policy: Policy) {
  return policy.nodes ?? []
}

function onNewKey(event: KeyboardEvent) {
  if (event.metaKey || event.ctrlKey || event.altKey) return
  if (event.key !== 'n') return
  if (document.querySelector('.pc-backdrop') || document.querySelector('.whichkey')) return
  const target = event.target as HTMLElement | null
  if (target?.closest('input, textarea, select, [contenteditable="true"]')) return
  event.preventDefault()
  openCreate()
}

useRefresh(load)
const { cursor } = useRowCursor(policies, open)
onMounted(() => {
  load()
  window.addEventListener('keydown', onNewKey)
})
onBeforeUnmount(() => window.removeEventListener('keydown', onNewKey))
</script>

<template>
  <div class="page">
    <PageHead title="Policies" :subtitle="subtitle">
      <template #actions>
        <button class="pc-btn pc-sm" type="button" :disabled="loading" @click="load">Refresh</button>
        <button v-if="canWrite" class="pc-btn pc-sm pc-pri" type="button" @click="openCreate">New policy</button>
      </template>
    </PageHead>

    <div v-if="error" class="band band-love">
      <div class="band-head">
        <StatusMark state="broken" word="request failed" />
        <span class="band-actions"><button class="pc-btn pc-sm" type="button" @click="load">Retry</button></span>
      </div>
      <p>{{ error }}</p>
    </div>

    <div class="areas">
      <section class="area">
        <div class="pc-title">
          <h2>Policies</h2>
          <span class="area-title-note">reusable rule sets assigned to nodes</span>
        </div>
        <div class="area-body flush">
          <div class="tbl">
            <table class="pc-table tbl-narrow">
              <thead>
                <tr>
                  <th>Policy</th>
                  <th>Nodes</th>
                  <th>Status</th>
                  <th class="num">Rules</th>
                  <th class="num">Updated</th>
                </tr>
              </thead>
              <tbody>
                <tr v-if="loading"><td colspan="5" class="empty">Loading policies…</td></tr>
                <template v-else>
                <tr
                  v-for="policy in policies"
                  :key="policy.id"
                  class="pc-row"
                  :class="{ 'pc-cur': policies.indexOf(policy) === cursor }"
                  tabindex="0"
                  @click="open(policy)"
                  @keydown.enter="open(policy)"
                >
                  <td>
                    <span class="ell" :title="policy.description || policy.name">{{ policy.name }}</span>
                  </td>
                  <td>
                    <span v-if="assignedNodes(policy).length" class="tags">
                      <span v-for="node in assignedNodes(policy).slice(0, 2)" :key="node.id" class="tag">{{ node.name }}</span>
                      <span v-if="assignedNodes(policy).length > 2" class="tag">+{{ assignedNodes(policy).length - 2 }}</span>
                    </span>
                    <span v-else class="muted">—</span>
                  </td>
                  <td>
                    <StatusMark :state="policy.enabled ? 'ok' : 'off'" :word="policy.enabled ? 'enabled' : 'disabled'" />
                  </td>
                  <td class="num">{{ formatNumber(policy.ruleCount) }}</td>
                  <td class="num muted">{{ formatTime(policy.updatedAt) }}</td>
                </tr>
                <tr v-if="policies.length === 0">
                  <td colspan="5" class="empty">No policies yet. Create one to start blocking traffic.</td>
                </tr>
                </template>
              </tbody>
            </table>
          </div>
        </div>
        <div class="foot">A disabled policy stays in the list but its rules are not distributed. Assign policies to nodes from a node page.</div>
      </section>
    </div>

    <ConsoleDialog v-model:open="createOpen" title="New policy" @submit="create">
      <div class="stack">
        <div class="field">
          <label for="policy-name">Name</label>
          <input id="policy-name" v-model="name" class="pc-inp" placeholder="Baseline egress blocks" />
        </div>
        <div class="field">
          <label for="policy-description">Description</label>
          <textarea id="policy-description" v-model="description" class="pc-inp" placeholder="Optional" />
        </div>
        <div class="spread">
          <span class="field-hint">Enabled</span>
          <button class="pc-sw" type="button" role="switch" :aria-checked="enabled" @click="enabled = !enabled" />
        </div>
        <p v-if="dialogError" class="err">{{ dialogError }}</p>
      </div>
      <template #footer>
        <button class="pc-btn pc-pri" type="button" :disabled="saving || !name.trim()" @click="create">Create</button>
        <button class="pc-btn pc-ghost" type="button" @click="createOpen = false">Cancel</button>
      </template>
    </ConsoleDialog>
  </div>
</template>