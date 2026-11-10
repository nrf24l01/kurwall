<script setup lang="ts">
withDefaults(
  defineProps<{
    state: 'ok' | 'look' | 'blocked' | 'broken' | 'unknown' | 'off' | 'paused' | 'running' | 'gone'
    word?: string
  }>(),
  { word: '' },
)
</script>

<template>
  <span class="state" :class="`state-${state}`">
    <span class="st" :class="`st-${state}`" aria-hidden="true" />
    <span v-if="word" class="st-word">{{ word }}</span>
    <span v-else class="sr-only">{{ state }}</span>
  </span>
</template>