<script setup lang="ts">
import ConsoleDialog from '@/components/console/ConsoleDialog.vue'

defineProps<{ open: boolean }>()
const emit = defineEmits<{ 'update:open': [value: boolean] }>()

const sections = [
  {
    title: 'Anywhere',
    rows: [
      ['/ search', 'Search nodes, policies, pages and actions'],
      [': actions', 'Search actions only'],
      ['g then a key', 'Go to a page'],
      ['g g', 'Top of the page'],
      ['? keys', 'This sheet'],
      ['i', 'First field on the page'],
      ['r', 'Refresh the current page'],
      ['esc', 'Close, leave a field or cancel'],
    ],
  },
  {
    title: 'Pages',
    rows: [
      ['g d', 'Overview'],
      ['g n', 'Nodes'],
      ['g p', 'Policies'],
      ['g a', 'ASNs'],
      ['g e', 'Events'],
      ['g s', 'Settings'],
    ],
  },
  {
    title: 'In a list',
    rows: [
      ['↵', 'Open the row (click also works)'],
      ['j k', 'Move through rows'],
      ['n', 'New item, where the page has one'],
    ],
  },
]
</script>

<template>
  <ConsoleDialog :open="open" title="Keys" width="min(620px, 100%)" @update:open="emit('update:open', $event)">
    <div class="stack">
      <section v-for="section in sections" :key="section.title">
        <div class="pc-label" style="margin-bottom: 6px">{{ section.title }}</div>
        <dl class="kv">
          <template v-for="[key, description] in section.rows" :key="key">
            <dt><kbd>{{ key }}</kbd></dt>
            <dd>{{ description }}</dd>
          </template>
        </dl>
      </section>
    </div>
    <template #footer>
      <button class="pc-btn pc-ghost" type="button" @click="emit('update:open', false)">Close</button>
    </template>
  </ConsoleDialog>
</template>