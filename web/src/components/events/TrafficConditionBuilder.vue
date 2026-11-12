<script setup lang="ts">
import { computed } from 'vue'
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

const complete = computed(() =>
  conditions.value.filter((condition) => condition.field && condition.operator && condition.value.trim()),
)
const expression = computed(() => expressionFor(complete.value, match.value))

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
</script>

<template>
  <div class="filter-builder">
    <div class="filter-builder-head">
      <span class="filter-expr mono">{{ expression || 'No conditions' }}</span>
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
  </div>
</template>
