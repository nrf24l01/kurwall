import { onBeforeUnmount, onMounted, ref, watch, type Ref } from 'vue'

export const refreshTick = ref(0)

export function requestRefresh() {
  refreshTick.value++
}

export function useRefresh(load: () => void) {
  watch(refreshTick, () => load())
}

/** Keyboard row cursor for a single table on a page: j/k, arrows, ⌃N/⌃P, ↵. */
export function useRowCursor<T>(rows: Ref<T[]>, open: (row: T) => void) {
  const cursor = ref(-1)

  function onKey(event: KeyboardEvent) {
    if (event.altKey || event.metaKey) return
    if (document.querySelector('.pc-backdrop') || document.querySelector('.whichkey')) return
    const target = event.target as HTMLElement | null
    if (target?.closest('input, textarea, select, [contenteditable="true"]')) return

    const down = event.key === 'j' || event.key === 'ArrowDown' || (event.ctrlKey && event.key.toLowerCase() === 'n')
    const up = event.key === 'k' || event.key === 'ArrowUp' || (event.ctrlKey && event.key.toLowerCase() === 'p')
    if (down || up) {
      event.preventDefault()
      if (rows.value.length === 0) return
      if (cursor.value < 0) cursor.value = down ? 0 : rows.value.length - 1
      else cursor.value = Math.min(rows.value.length - 1, Math.max(0, cursor.value + (down ? 1 : -1)))
      const node = document.querySelectorAll<HTMLElement>('.pc-table tbody tr.pc-row')[cursor.value]
      node?.scrollIntoView({ block: 'nearest' })
      return
    }
    if (event.key === 'Enter' && cursor.value >= 0) {
      const row = rows.value[cursor.value]
      if (row) {
        event.preventDefault()
        open(row)
      }
      return
    }
    if (event.key === 'Escape') cursor.value = -1
  }

  watch(rows, () => {
    cursor.value = Math.min(cursor.value, rows.value.length - 1)
  })

  onMounted(() => window.addEventListener('keydown', onKey))
  onBeforeUnmount(() => window.removeEventListener('keydown', onKey))

  return { cursor }
}
