<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import ConsoleDialog from '@/components/console/ConsoleDialog.vue'
import PageHead from '@/components/console/PageHead.vue'
import StatusMark from '@/components/console/StatusMark.vue'
import {
  api,
  errorMessage,
  type AsnCountry,
  type AsnEntry,
  type Policy,
} from '@/lib/api'
import { countryFlag, formatNumber, formatTime } from '@/lib/utils'
import { canWrite } from '@/lib/session'
import { useRefresh, useRowCursor } from '@/lib/ui'

const entries = ref<AsnEntry[]>([])
const countries = ref<AsnCountry[]>([])
const total = ref(0)
const catalogUpdatedAt = ref<string | undefined>()
const loading = ref(true)
const error = ref('')

const country = ref('')
const query = ref('')
const limit = 50
const offset = ref(0)

const selected = ref<Map<number, AsnEntry>>(new Map())
const dragSelecting = ref(false)

const addOpen = ref(false)
const selectedEntry = ref<AsnEntry | null>(null)
const policies = ref<Policy[]>([])
const policyId = ref('')
const note = ref('')
const enabled = ref(true)
const saving = ref(false)
const dialogError = ref('')
const notice = ref('')
const noticePolicyId = ref('')
const noticeTone = ref<'pine' | 'gold'>('pine')

const bulkOpen = ref(false)
const bulkNote = ref('')
const bulkPolicyId = ref('')
const bulkEnabled = ref(true)
const bulkSaving = ref(false)
const bulkError = ref('')
const bulkLimit = 200

let searchTimer: number | undefined
const regionNames = (() => {
  try {
    return new Intl.DisplayNames(['en'], { type: 'region' })
  } catch {
    return null
  }
})()

const subtitle = computed(() => {
  const parts = [`${formatNumber(total.value)} ASNs`]
  parts.push(country.value ? countryName(country.value) : 'all countries')
  if (catalogUpdatedAt.value) {
    parts.push(`catalog updated ${formatTime(catalogUpdatedAt.value)}`)
  }
  return parts.join(' · ')
})

const selectedCount = computed(() => selected.value.size)
const selectedList = computed(() =>
  [...selected.value.values()].sort((a, b) => a.asn - b.asn),
)
const pageSelectedCount = computed(
  () => entries.value.filter((entry) => selected.value.has(entry.asn)).length,
)
const allPageSelected = computed(
  () => entries.value.length > 0 && pageSelectedCount.value === entries.value.length,
)
const somePageSelected = computed(() => pageSelectedCount.value > 0 && !allPageSelected.value)

function countryName(code: string): string {
  if (!code) return ''
  try {
    return regionNames?.of(code) ?? code
  } catch {
    return code
  }
}

function companyInitials(name: string): string {
  const token = name.split(' - ')[0].replace(/[^A-Za-z0-9]/g, '')
  return (token.slice(0, 2) || 'AS').toUpperCase()
}

function isSelected(asn: number): boolean {
  return selected.value.has(asn)
}

function toggleRow(entry: AsnEntry, index?: number) {
  const next = new Map(selected.value)
  if (next.has(entry.asn)) next.delete(entry.asn)
  else next.set(entry.asn, entry)
  selected.value = next
  if (index !== undefined) anchorIndex = index
}

function selectRange(from: number, to: number, base: Map<number, AsnEntry>) {
  const next = new Map(base)
  const start = Math.min(from, to)
  const end = Math.max(from, to)
  for (let index = start; index <= end; index++) {
    const entry = entries.value[index]
    if (entry) next.set(entry.asn, entry)
  }
  selected.value = next
}

function clearSelection() {
  selected.value = new Map()
}

function togglePage() {
  const next = new Map(selected.value)
  if (allPageSelected.value) {
    for (const entry of entries.value) next.delete(entry.asn)
  } else {
    for (const entry of entries.value) next.set(entry.asn, entry)
  }
  selected.value = next
}

let press: { index: number; x: number; y: number; base: Map<number, AsnEntry>; moved: boolean } | null = null
let suppressClick = false
let anchorIndex: number | null = null

function beginSelect(event: PointerEvent, index: number) {
  if (event.button !== 0 || event.pointerType === 'touch') return
  const target = event.target as HTMLElement | null
  if (target?.closest('button, a, input, select, textarea')) return
  suppressClick = false
  const additive = event.shiftKey || event.metaKey || event.ctrlKey
  press = {
    index,
    x: event.clientX,
    y: event.clientY,
    base: additive ? new Map(selected.value) : new Map(),
    moved: false,
  }
  if (!event.shiftKey) anchorIndex = index
}

function rowIndexAt(x: number, y: number): number | null {
  const row = document.elementFromPoint(x, y)?.closest<HTMLElement>('tr[data-index]')
  const raw = row?.dataset.index
  return raw === undefined ? null : Number(raw)
}

function onPointerMove(event: PointerEvent) {
  if (!press) return
  if (!press.moved) {
    if (Math.abs(event.clientX - press.x) < 5 && Math.abs(event.clientY - press.y) < 5) return
    press.moved = true
    dragSelecting.value = true
  }
  const index = rowIndexAt(event.clientX, event.clientY)
  if (index === null) return
  selectRange(press.index, index, press.base)
}

function onPointerUp() {
  if (!press) return
  if (press.moved) {
    // A drag may end on a row and produce a click; let it pass, but do not
    // swallow the next genuine click if the pointer stopped outside the table.
    suppressClick = true
    window.setTimeout(() => {
      suppressClick = false
    }, 0)
  }
  dragSelecting.value = false
  press = null
}

function onRowClick(event: MouseEvent, entry: AsnEntry, index: number) {
  if (suppressClick) {
    suppressClick = false
    return
  }
  if (event.shiftKey && anchorIndex !== null) {
    selectRange(anchorIndex, index, selected.value)
    return
  }
  toggleRow(entry, index)
}

function onSelectKey(event: KeyboardEvent) {
  if (event.key !== ' ' || event.metaKey || event.ctrlKey || event.altKey) return
  if (document.querySelector('.pc-backdrop')) return
  const target = event.target as HTMLElement | null
  if (target?.closest('input, textarea, select, [contenteditable="true"], button')) return
  if (cursor.value < 0) return
  const entry = entries.value[cursor.value]
  if (!entry) return
  event.preventDefault()
  toggleRow(entry, cursor.value)
}

let loadSeq = 0

async function load() {
  // Live search can overlap requests; only the newest response may update the table.
  const seq = ++loadSeq
  loading.value = true
  error.value = ''
  try {
    const page = await api.asns({
      country: country.value || undefined,
      q: query.value.trim() || undefined,
      limit,
      offset: offset.value,
    })
    if (seq !== loadSeq) return
    entries.value = page.items
    total.value = page.total
    catalogUpdatedAt.value = page.catalogUpdatedAt
    anchorIndex = null
  } catch (err) {
    if (seq !== loadSeq) return
    error.value = errorMessage(err)
  } finally {
    if (seq === loadSeq) loading.value = false
  }
}

async function loadCountries() {
  try {
    countries.value = (await api.asnCountries()).items
  } catch {
    countries.value = []
  }
}

function cancelSearch() {
  if (searchTimer !== undefined) {
    window.clearTimeout(searchTimer)
    searchTimer = undefined
  }
}

function apply() {
  cancelSearch()
  offset.value = 0
  load()
}

function reset() {
  cancelSearch()
  country.value = ''
  query.value = ''
  offset.value = 0
  load()
}

async function openAdd(entry: AsnEntry) {
  selectedEntry.value = entry
  note.value = ''
  enabled.value = true
  dialogError.value = ''
  addOpen.value = true
  if (policies.value.length === 0) {
    try {
      policies.value = (await api.policies()).items
    } catch (err) {
      dialogError.value = errorMessage(err)
    }
  }
  const firstEnabled = policies.value.find((policy) => policy.enabled) ?? policies.value[0]
  policyId.value = firstEnabled?.id ?? ''
}

async function saveRule() {
  if (!selectedEntry.value || !policyId.value) return
  saving.value = true
  dialogError.value = ''
  try {
    await api.createRule(policyId.value, {
      kind: 'asn',
      value: `AS${selectedEntry.value.asn}`,
      action: 'block',
      note: note.value,
      enabled: enabled.value,
    })
    const policy = policies.value.find((item) => item.id === policyId.value)
    noticeTone.value = 'pine'
    notice.value = `AS${selectedEntry.value.asn} added to ${policy?.name ?? 'the policy'}`
    noticePolicyId.value = policyId.value
    addOpen.value = false
    await load()
  } catch (err) {
    dialogError.value = errorMessage(err)
  } finally {
    saving.value = false
  }
}

async function openBulk() {
  if (selectedCount.value === 0) return
  bulkNote.value = ''
  bulkEnabled.value = true
  bulkError.value = ''
  bulkPolicyId.value = ''
  bulkOpen.value = true
  if (policies.value.length === 0) {
    try {
      policies.value = (await api.policies()).items
    } catch (err) {
      bulkError.value = errorMessage(err)
    }
  }
  const firstEnabled = policies.value.find((policy) => policy.enabled) ?? policies.value[0]
  bulkPolicyId.value = firstEnabled?.id ?? ''
}

async function saveBulk() {
  if (!bulkPolicyId.value || selectedCount.value === 0 || selectedCount.value > bulkLimit) return
  bulkSaving.value = true
  bulkError.value = ''
  try {
    const result = await api.createRules(
      bulkPolicyId.value,
      selectedList.value.map((entry) => ({
        kind: 'asn' as const,
        value: `AS${entry.asn}`,
        action: 'block' as const,
        note: bulkNote.value,
        enabled: bulkEnabled.value,
      })),
    )
    const policy = policies.value.find((item) => item.id === bulkPolicyId.value)
    const parts: string[] = []
    if (result.items.length > 0) {
      parts.push(
        `${formatNumber(result.items.length)} ASN${result.items.length === 1 ? '' : 's'} added to ${policy?.name ?? 'the policy'}`,
      )
    }
    if (result.skipped.length > 0) {
      parts.push(`${formatNumber(result.skipped.length)} already present`)
    }
    if (result.failed.length > 0) {
      const values = result.failed.slice(0, 3).map((item) => item.value).join(', ')
      const more = result.failed.length > 3 ? ` and ${formatNumber(result.failed.length - 3)} more` : ''
      parts.push(`${formatNumber(result.failed.length)} could not be resolved (${values}${more})`)
    }
    noticeTone.value = result.items.length === 0 && result.skipped.length === 0 ? 'gold' : 'pine'
    notice.value = parts.join(' · ') || 'No rules were created'
    noticePolicyId.value = bulkPolicyId.value

    const done = new Set([...result.items.map((rule) => rule.value), ...result.skipped])
    const remaining = new Map<number, AsnEntry>()
    for (const [asn, entry] of selected.value) {
      if (!done.has(`AS${asn}`)) remaining.set(asn, entry)
    }
    selected.value = remaining
    bulkOpen.value = false
    await load()
  } catch (err) {
    bulkError.value = errorMessage(err)
  } finally {
    bulkSaving.value = false
  }
}

const { cursor } = useRowCursor(entries, openAdd)
watch(country, () => {
  offset.value = 0
  load()
})
watch(query, () => {
  if (searchTimer !== undefined) window.clearTimeout(searchTimer)
  searchTimer = window.setTimeout(() => {
    searchTimer = undefined
    offset.value = 0
    load()
  }, 300)
})
watch(offset, load)
useRefresh(load)
onMounted(async () => {
  window.addEventListener('pointermove', onPointerMove)
  window.addEventListener('pointerup', onPointerUp)
  window.addEventListener('pointercancel', onPointerUp)
  window.addEventListener('keydown', onSelectKey)
  await Promise.all([load(), loadCountries()])
})
onBeforeUnmount(() => {
  if (searchTimer !== undefined) window.clearTimeout(searchTimer)
  window.removeEventListener('pointermove', onPointerMove)
  window.removeEventListener('pointerup', onPointerUp)
  window.removeEventListener('pointercancel', onPointerUp)
  window.removeEventListener('keydown', onSelectKey)
})
</script>

<template>
  <div class="page">
    <PageHead title="Autonomous systems" :subtitle="subtitle">
      <template #actions>
        <button class="pc-btn pc-sm" type="button" :disabled="loading" @click="load">Refresh</button>
      </template>
    </PageHead>

    <div v-if="error" class="band band-love">
      <div class="band-head">
        <StatusMark state="broken" word="request failed" />
        <span class="band-actions"><button class="pc-btn pc-sm" type="button" @click="load">Retry</button></span>
      </div>
      <p>{{ error }}</p>
    </div>

    <div v-if="notice" class="band" :class="noticeTone === 'gold' ? 'band-gold' : 'band-pine'">
      <div class="band-head">
        <StatusMark :state="noticeTone === 'gold' ? 'look' : 'ok'" :word="noticeTone === 'gold' ? 'nothing changed' : 'policy updated'" />
        <span>{{ notice }}</span>
        <span class="band-actions">
          <RouterLink class="pc-btn pc-sm" :to="`/policies/${noticePolicyId}`">Open policy →</RouterLink>
        </span>
      </div>
    </div>

    <form class="filters" @submit.prevent="apply">
      <div style="width: 270px">
        <select v-model="country" class="pc-inp" aria-label="Country">
          <option value="">All countries</option>
          <option v-for="item in countries" :key="item.code" :value="item.code">
            {{ countryFlag(item.code) }} {{ item.code }} · {{ countryName(item.code) }} · {{ formatNumber(item.count) }}
          </option>
        </select>
      </div>
      <div class="grow">
        <input
          v-model="query"
          class="pc-inp"
          type="search"
          placeholder="Search by ASN or name"
          aria-label="Search by ASN or name"
        />
      </div>
      <button class="pc-btn" type="submit" :disabled="loading">Search</button>
      <button class="pc-btn pc-ghost" type="button" @click="reset">Reset</button>
    </form>

    <div v-if="selectedCount > 0" class="bulkbar">
      <span class="bulk-count"><b>{{ formatNumber(selectedCount) }}</b> selected</span>
      <span class="bulk-note">Drag across rows to select; Shift-click extends the range; ⌘/Ctrl-drag adds.</span>
      <span class="bulk-actions">
        <button class="pc-btn pc-sm pc-ghost" type="button" @click="clearSelection">Clear</button>
        <button v-if="canWrite" class="pc-btn pc-sm" type="button" @click="openBulk">Block selected</button>
      </span>
    </div>

    <div class="areas">
      <section class="area">
        <div class="pc-title">
          <h2>ASN directory</h2>
          <span class="area-title-note">{{ formatNumber(total) }} matching · {{ formatNumber(pageSelectedCount) }} selected on this page</span>
        </div>
        <div class="area-body flush">
          <div class="tbl">
            <table class="pc-table tbl-wide selectable" :class="{ 'drag-selecting': dragSelecting }">
              <thead>
                <tr>
                  <th class="sel">
                    <button
                      class="pc-check"
                      type="button"
                      role="checkbox"
                      :aria-checked="allPageSelected ? 'true' : somePageSelected ? 'mixed' : 'false'"
                      :aria-label="allPageSelected ? 'Deselect all ASNs on this page' : 'Select all ASNs on this page'"
                      @click="togglePage"
                    />
                  </th>
                  <th>ASN</th>
                  <th>Name</th>
                  <th>Country</th>
                  <th class="num">Cached prefixes</th>
                  <th class="num">Action</th>
                </tr>
              </thead>
              <tbody>
                <tr v-if="loading"><td colspan="6" class="empty">Loading the ASN directory…</td></tr>
                <template v-else>
                  <tr
                    v-for="(entry, index) in entries"
                    :key="entry.asn"
                    class="pc-row"
                    :class="{ 'pc-cur': index === cursor, 'pc-sel': isSelected(entry.asn) }"
                    :data-index="index"
                    tabindex="0"
                    @pointerdown="beginSelect($event, index)"
                    @click="onRowClick($event, entry, index)"
                    @keydown.enter.stop="openAdd(entry)"
                    @keydown.space.prevent.stop="toggleRow(entry, index)"
                  >
                    <td class="sel">
                      <button
                        class="pc-check"
                        type="button"
                        role="checkbox"
                        :aria-checked="isSelected(entry.asn)"
                        :aria-label="`Select AS${entry.asn}`"
                        @pointerdown.stop
                        @click.stop="toggleRow(entry, index)"
                        @keydown.stop
                      />
                    </td>
                    <td class="mono">AS{{ entry.asn }}</td>
                    <td>
                      <span class="company">
                        <span class="company-mark" aria-hidden="true">{{ companyInitials(entry.name) }}</span>
                        <span class="ell" :title="entry.name">{{ entry.name }}</span>
                      </span>
                    </td>
                    <td class="subtle">
                      <span class="flag" aria-hidden="true">{{ countryFlag(entry.country) }}</span>
                      {{ entry.country }} <span class="muted">{{ countryName(entry.country) }}</span>
                    </td>
                    <td class="num mono">{{ entry.prefixCount ? formatNumber(entry.prefixCount) : '—' }}</td>
                    <td class="num">
                      <button v-if="canWrite" class="pc-btn pc-sm" type="button" @click.stop="openAdd(entry)">Add rule</button>
                    </td>
                  </tr>
                  <tr v-if="entries.length === 0">
                    <td colspan="6" class="empty">
                      {{ total === 0 ? 'No ASN matches this filter. The directory may still be loading.' : 'No ASN matches this filter.' }}
                    </td>
                  </tr>
                </template>
              </tbody>
            </table>
          </div>
        </div>
        <div class="pager">
          <span>{{ formatNumber(total) }} ASNs · {{ formatNumber(entries.length) }} shown</span>
          <span class="pages">
            <button class="pc-btn pc-sm" type="button" :disabled="offset === 0" @click="offset = Math.max(0, offset - limit)">‹ Previous</button>
            <button class="pc-btn pc-sm" type="button" :disabled="offset + limit >= total" @click="offset = offset + limit">Next ›</button>
          </span>
        </div>
        <div class="foot">
          Drag across rows to select a range, or use the checkboxes. Selected ASNs are blocked together in one policy
          revision. ASN metadata is cached from the configured directory; adding a rule resolves current prefixes.
        </div>
      </section>
    </div>

    <ConsoleDialog
      v-model:open="addOpen"
      :title="selectedEntry ? `Add AS${selectedEntry.asn}` : 'Add ASN rule'"
      width="min(540px, 100%)"
      @submit="saveRule"
    >
      <div v-if="selectedEntry" class="stack">
        <dl class="kv">
          <dt>ASN</dt><dd class="mono">AS{{ selectedEntry.asn }}</dd>
          <dt>Name</dt><dd>{{ selectedEntry.name }}</dd>
          <dt>Country</dt><dd><span class="flag">{{ countryFlag(selectedEntry.country) }}</span> {{ selectedEntry.country }} · {{ countryName(selectedEntry.country) }}</dd>
        </dl>
        <div class="field">
          <label for="asn-policy">Policy</label>
          <select id="asn-policy" v-model="policyId" class="pc-inp" :disabled="policies.length === 0">
            <option v-for="policy in policies" :key="policy.id" :value="policy.id">
              {{ policy.name }} · {{ policy.ruleCount }} rules
            </option>
          </select>
          <span v-if="policies.length === 0" class="field-hint">
            No policies exist yet. <RouterLink to="/policies">Create one first.</RouterLink>
          </span>
        </div>
        <div class="field">
          <label for="asn-note">Note</label>
          <input id="asn-note" v-model="note" class="pc-inp" placeholder="Optional" />
        </div>
        <div class="spread">
          <span class="field-hint">Enabled</span>
          <button class="pc-sw" type="button" role="switch" :aria-checked="enabled" @click="enabled = !enabled" />
        </div>
        <p class="field-hint">The coordinator resolves the ASN to its current announced prefixes when the rule is saved.</p>
        <p v-if="dialogError" class="err">{{ dialogError }}</p>
      </div>
      <template #footer>
        <button
          class="pc-btn pc-pri"
          type="button"
          :disabled="saving || !policyId || !selectedEntry"
          @click="saveRule"
        >
          Add rule
        </button>
        <button class="pc-btn pc-ghost" type="button" @click="addOpen = false">Cancel</button>
      </template>
    </ConsoleDialog>

    <ConsoleDialog
      v-model:open="bulkOpen"
      :title="`Block ${formatNumber(selectedCount)} ASN${selectedCount === 1 ? '' : 's'}`"
      width="min(620px, 100%)"
      @submit="saveBulk"
    >
      <div class="stack">
        <dl class="kv">
          <dt>Selected</dt><dd class="mono">{{ formatNumber(selectedCount) }} ASN{{ selectedCount === 1 ? '' : 's' }}</dd>
          <dt>Action</dt><dd>Block all announced prefixes</dd>
        </dl>
        <div class="field">
          <label for="bulk-policy">Policy</label>
          <select id="bulk-policy" v-model="bulkPolicyId" class="pc-inp" :disabled="policies.length === 0">
            <option v-for="policy in policies" :key="policy.id" :value="policy.id">
              {{ policy.name }} · {{ policy.ruleCount }} rules
            </option>
          </select>
          <span v-if="policies.length === 0" class="field-hint">
            No policies exist yet. <RouterLink to="/policies">Create one first.</RouterLink>
          </span>
        </div>
        <div class="field">
          <label for="bulk-note">Note</label>
          <input id="bulk-note" v-model="bulkNote" class="pc-inp" placeholder="Optional" />
        </div>
        <div class="spread">
          <span class="field-hint">Enabled</span>
          <button class="pc-sw" type="button" role="switch" :aria-checked="bulkEnabled" @click="bulkEnabled = !bulkEnabled" />
        </div>
        <div class="field">
          <span class="field-label">ASNs</span>
          <div class="bulk-list">
            <div v-for="entry in selectedList" :key="entry.asn" class="bulk-list-row">
              <span class="asn mono">AS{{ entry.asn }}</span>
              <span class="name">{{ entry.name }} · {{ entry.country }}</span>
            </div>
          </div>
        </div>
        <p class="field-hint">
          Prefixes are resolved now; ASNs that cannot be resolved are reported and stay selected for a retry.
        </p>
        <p v-if="selectedCount > bulkLimit" class="err">
          Select at most {{ formatNumber(bulkLimit) }} ASNs per action. Clear some rows or block them in smaller groups.
        </p>
        <p v-if="bulkError" class="err">{{ bulkError }}</p>
      </div>
      <template #footer>
        <button
          class="pc-btn pc-pri"
          type="button"
          :disabled="bulkSaving || !bulkPolicyId || selectedCount === 0 || selectedCount > bulkLimit"
          @click="saveBulk"
        >
          {{ bulkSaving ? 'Blocking…' : `Block ${formatNumber(selectedCount)} ASN${selectedCount === 1 ? '' : 's'}` }}
        </button>
        <button class="pc-btn pc-ghost" type="button" @click="bulkOpen = false">Cancel</button>
      </template>
    </ConsoleDialog>
  </div>
</template>