<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import ConsoleDialog from '@/components/console/ConsoleDialog.vue'
import type { TrafficCondition, TrafficMatch } from '@/lib/api'
import {
  defaultCondition,
  expressionFor,
  fieldDefinition,
  operatorLabel,
  trafficFields,
} from '@/lib/trafficFilters'

const conditions = defineModel<TrafficCondition[]>('conditions', { required: true })
const match = defineModel<TrafficMatch>('match', { required: true })

const draft = ref('')
const error = ref('')
const guideOpen = ref(false)

const examples = [
  'ip.dst == 8.8.8.8',
  'domain contains "cloudflare" && decision == allowed',
  'ip.addr == 8.8.8.8 || ip.addr == 1.1.1.1',
  '!text contains "local"',
  'port == 443 && protocol == tcp',
]

const complete = computed(() =>
  conditions.value.filter((condition) => condition.field && condition.operator && condition.value.trim()),
)
const expression = computed(() => expressionFor(complete.value, match.value))
watch(expression, (value) => { draft.value = value }, { immediate: true })
watch(guideOpen, (open) => {
  if (!open) {
    draft.value = expression.value
    error.value = ''
  }
})

function openGuide() {
  draft.value = expression.value
  error.value = ''
  guideOpen.value = true
}

function closeGuide() {
  guideOpen.value = false
  draft.value = expression.value
  error.value = ''
}

function addCondition() {
  conditions.value = [...conditions.value, defaultCondition()]
}

function removeCondition(index: number) {
  conditions.value = conditions.value.filter((_, current) => current !== index)
}

function changeField(condition: TrafficCondition, field: string) {
  const def = fieldDefinition(field)
  condition.field = field
  condition.operator = def?.operators[0] ?? 'eq'
  condition.value = def?.type === 'select' ? (def.options?.[0]?.value ?? '') : ''
}

function splitExpression(raw: string): { terms: string[]; connector: '' | '&&' | '||' } {
  const terms: string[] = []
  let current = ''
  let quote = ''
  let connector: '' | '&&' | '||' = ''
  for (let index = 0; index < raw.length; index++) {
    const char = raw[index]
    if (quote) {
      current += char
      if (char === quote) quote = ''
      continue
    }
    if (char === '"' || char === "'") {
      quote = char
      current += char
      continue
    }
    const pair = raw.slice(index, index + 2)
    if (pair === '&&' || pair === '||') {
      if (connector && connector !== pair) {
        throw new Error('Mixed && and || is not supported; use condition rows for mixed logic.')
      }
      connector = pair
      if (current.trim()) terms.push(current.trim())
      current = ''
      index++
      continue
    }
    current += char
  }
  if (current.trim()) terms.push(current.trim())
  return { terms, connector }
}

function parseValue(raw: string): string {
  const value = raw.trim()
  if ((value.startsWith('"') && value.endsWith('"')) || (value.startsWith("'") && value.endsWith("'"))) {
    return value.slice(1, -1).replace(/\\(["'\\])/g, '$1')
  }
  return value
}

function applyExpression() {
  error.value = ''
  const raw = draft.value.trim()
  if (!raw) {
    conditions.value = []
    match.value = 'all'
    guideOpen.value = false
    return
  }
  try {
    const { terms, connector } = splitExpression(raw)
    const pattern = /^(!)?\s*([a-zA-Z0-9.]+)\s*(==|!=|>=|<=|>|<|not\s+contains|contains|starts_with|starts\s+with|ends_with|ends\s+with)\s*(.+)$/
    const parsed: TrafficCondition[] = []
    for (const term of terms) {
      const matchResult = term.match(pattern)
      if (!matchResult) throw new Error(`Cannot parse condition: ${term}`)
      const negate = Boolean(matchResult[1])
      const field = matchResult[2].toLowerCase()
      const def = fieldDefinition(field)
      if (!def) throw new Error(`Unknown field: ${field}`)
      const rawOperator = matchResult[3].toLowerCase().replace(/\s+/g, ' ')
      const operator =
        rawOperator === '==' ? 'eq' :
        rawOperator === '!=' ? 'ne' :
        rawOperator === '>=' ? 'gte' :
        rawOperator === '<=' ? 'lte' :
        rawOperator === '>' ? 'gt' :
        rawOperator === '<' ? 'lt' :
        rawOperator === 'contains' ? 'contains' :
        rawOperator === 'not contains' ? 'not_contains' :
        rawOperator === 'starts with' || rawOperator === 'starts_with' ? 'starts_with' :
        rawOperator === 'ends with' || rawOperator === 'ends_with' ? 'ends_with' : ''
      if (!operator || !def.operators.includes(operator)) {
        throw new Error(`Operator ${rawOperator} is not supported for ${field}`)
      }
      let value = parseValue(matchResult[4])
      if (def.type === 'select') value = value.toLowerCase()
      if (!value) throw new Error(`Value is required for ${field}`)
      parsed.push({ field, operator, value, negate })
    }
    conditions.value = parsed
    match.value = connector === '||' ? 'any' : 'all'
    guideOpen.value = false
  } catch (err) {
    error.value = err instanceof Error ? err.message : 'Cannot parse the filter'
  }
}
</script>

<template>
  <div class="filter-builder">
    <div class="filter-builder-head">
      <span class="filter-label">Wireshark filter</span>
      <button
        id="wireshark-filter"
        class="filter-input filter-filter-button mono"
        type="button"
        aria-haspopup="dialog"
        :title="draft || 'Click to write a Wireshark filter'"
        @click="openGuide"
      >
        {{ draft || 'Click to write a Wireshark filter…' }}
      </button>
      <select v-model="match" class="pc-inp filter-match" aria-label="Match mode">
        <option value="all">Match all (AND)</option>
        <option value="any">Match any (OR)</option>
      </select>
      <button class="pc-btn pc-sm" type="button" @click="addCondition">Add condition</button>
    </div>
    <div v-if="conditions.length === 0" class="filter-empty">
      Add a condition to filter by IP, port, protocol, domain, decision, block kind or size. Use
      “Text (any field)” to search domain, target, reason and container together.
    </div>
    <div v-for="(condition, index) in conditions" :key="index" class="filter-row">
      <span class="filter-connector">{{ index === 0 ? '' : match === 'all' ? 'AND' : 'OR' }}</span>
      <button
        class="filter-negate"
        type="button"
        :aria-pressed="condition.negate"
        :title="condition.negate ? 'Include this condition' : 'Exclude this condition'"
        @click="condition.negate = !condition.negate"
      >
        !
      </button>
      <select
        class="pc-inp filter-field"
        :value="condition.field"
        aria-label="Filter field"
        @change="changeField(condition, ($event.target as HTMLSelectElement).value)"
      >
        <option v-for="field in trafficFields" :key="field.value" :value="field.value">{{ field.label }}</option>
      </select>
      <select v-model="condition.operator" class="pc-inp filter-operator" aria-label="Filter operator">
        <option v-for="operator in fieldDefinition(condition.field)?.operators ?? []" :key="operator" :value="operator">
          {{ operatorLabel(operator) }}
        </option>
      </select>
      <select
        v-if="fieldDefinition(condition.field)?.type === 'select'"
        v-model="condition.value"
        class="pc-inp filter-value"
        aria-label="Filter value"
      >
        <option v-for="option in fieldDefinition(condition.field)?.options ?? []" :key="option.value" :value="option.value">
          {{ option.label }}
        </option>
      </select>
      <input
        v-else
        v-model="condition.value"
        class="pc-inp filter-value"
        :type="fieldDefinition(condition.field)?.type === 'number' ? 'number' : 'text'"
        :min="fieldDefinition(condition.field)?.type === 'number' ? 0 : undefined"
        :placeholder="fieldDefinition(condition.field)?.type === 'ip' ? '8.8.8.8' : fieldDefinition(condition.field)?.type === 'number' ? '443' : 'value'"
        aria-label="Filter value"
      />
      <button class="pc-btn pc-sm pc-ghost filter-remove" type="button" title="Remove condition" @click="removeCondition(index)">×</button>
    </div>

    <ConsoleDialog v-model:open="guideOpen" title="Wireshark filter" width="min(720px, 100%)" @submit="applyExpression">
      <div class="stack">
        <p class="field-hint">
          Type an expression, then Apply. It is converted into the condition rows below, which remain editable.
        </p>
        <div class="field">
          <label for="wireshark-expression">Expression</label>
          <input
            id="wireshark-expression"
            v-model="draft"
            class="pc-inp mono"
            placeholder='ip.dst == 8.8.8.8 &amp;&amp; !domain contains "ads"'
          />
        </div>
        <p v-if="error" class="err">{{ error }}</p>

        <div class="filter-guide">
          <h3>Fields</h3>
          <p class="field-hint mono">
            text · ip.addr · ip.src · ip.dst · port · src.port · dst.port · protocol ·
            event.kind · decision · domain · sni · target · block.kind · container ·
            bytes.out · bytes.in · duration · reason
          </p>
          <p class="field-hint">
            Select values: <span class="mono">protocol</span> tcp/udp/icmp/icmpv6,
            <span class="mono">event.kind</span> connection/block,
            <span class="mono">decision</span> allowed/blocked,
            <span class="mono">block.kind</span> ip/cidr/domain/asn/dns.
          </p>

          <h3>Operators</h3>
          <p class="field-hint mono">== · != · contains · not contains · starts_with · ends_with · &gt; · &gt;= · &lt; · &lt;=</p>

          <h3>Combine</h3>
          <p class="field-hint">
            <span class="mono">&amp;&amp;</span> all conditions, <span class="mono">||</span> any condition;
            prefix a condition with <span class="mono">!</span> to exclude it. Use one connector type per
            expression; parentheses are not supported.
          </p>

          <h3>Examples</h3>
          <button
            v-for="example in examples"
            :key="example"
            class="filter-example mono"
            type="button"
            @click="draft = example"
          >
            {{ example }}
          </button>
        </div>
      </div>
      <template #footer>
        <button class="pc-btn pc-pri" type="button" @click="applyExpression">Apply</button>
        <button class="pc-btn pc-ghost" type="button" @click="closeGuide">Close</button>
      </template>
    </ConsoleDialog>
  </div>
</template>
