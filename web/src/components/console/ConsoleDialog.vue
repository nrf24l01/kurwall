<script setup lang="ts">
import { nextTick, onBeforeUnmount, ref, watch } from 'vue'

const props = withDefaults(
  defineProps<{
    open: boolean
    title: string
    width?: string
  }>(),
  { width: '' },
)
const emit = defineEmits<{ 'update:open': [value: boolean]; submit: [] }>()

const box = ref<HTMLElement | null>(null)
const titleId = `dialog-title-${Math.random().toString(36).slice(2, 9)}`

function close() {
  emit('update:open', false)
}

function onKeydown(event: KeyboardEvent) {
  if (!props.open) return
  if (event.key === 'Escape') {
    event.preventDefault()
    event.stopPropagation()
    close()
    return
  }
  if ((event.metaKey || event.ctrlKey) && event.key === 'Enter') {
    event.preventDefault()
    event.stopPropagation()
    emit('submit')
    return
  }
  const target = event.target as HTMLElement | null
  if (event.key === 'Enter' && target?.tagName === 'INPUT' && target.getAttribute('type') !== 'search') {
    event.preventDefault()
    event.stopPropagation()
    emit('submit')
  }
}

watch(
  () => props.open,
  async (open) => {
    if (open) {
      document.body.style.overflow = 'hidden'
      await nextTick()
      const first = box.value?.querySelector<HTMLElement>('input, select, textarea, button:not(.pc-close)')
      first?.focus()
    } else {
      document.body.style.overflow = ''
    }
  },
)

onBeforeUnmount(() => {
  document.body.style.overflow = ''
})

// Capture so dialogs win over the page's global shortcuts.
if (typeof window !== 'undefined') window.addEventListener('keydown', onKeydown, true)
onBeforeUnmount(() => window.removeEventListener('keydown', onKeydown, true))
</script>

<template>
  <Teleport to="body">
    <div v-if="open" class="pc-backdrop" @click.self="close">
      <div
        ref="box"
        class="pc-dialog"
        role="dialog"
        aria-modal="true"
        :aria-labelledby="titleId"
        :style="width ? { width } : undefined"
      >
        <div class="pc-dialog-head">
          <h2 :id="titleId">{{ title }}</h2>
          <button class="pc-close" type="button" aria-label="Close" @click="close">×</button>
        </div>
        <div class="pc-dialog-body">
          <slot />
        </div>
        <div v-if="$slots.footer" class="pc-dialog-foot">
          <slot name="footer" />
          <span class="keys pc-keys-only"><kbd>⌘</kbd><kbd>↵</kbd> save · <kbd>esc</kbd> close</span>
        </div>
      </div>
    </div>
  </Teleport>
</template>