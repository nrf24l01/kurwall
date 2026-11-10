<script setup lang="ts">
import StatusMark from '@/components/console/StatusMark.vue'

withDefaults(
  defineProps<{
    left?: string
    status?: 'ok' | 'look' | 'broken' | 'unknown'
    statusText?: string
  }>(),
  { left: '', status: 'unknown', statusText: 'coordinator unknown' },
)
</script>

<template>
  <footer class="pc-keys">
    <div class="keys-left">
      <span>{{ left || 'j k move · ↵ open' }}</span>
    </div>
    <div class="keys-mid pc-keys-only">
      <span class="hint"><kbd>/</kbd> search</span>
      <span class="hint"><kbd>:</kbd> actions</span>
      <span class="hint"><kbd>g</kbd> go</span>
      <span class="hint"><kbd>?</kbd> keys</span>
    </div>
    <div class="keys-right">
      <StatusMark :state="status" :word="statusText" />
    </div>
  </footer>
</template>