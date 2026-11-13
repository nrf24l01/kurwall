/** Shared, dependency-free rules for the server-authoritative Events live view. */

export const DEFAULT_LIVE_WINDOW_MS = 60 * 60 * 1000
export const MAX_EVENTS_LIMIT = 500
export const LIVE_SCROLL_DEFER_PX = 240

export interface QueryWindow {
  from: Date | null
  to: Date
  durationMs: number
  rolling: boolean
}

export interface QueryWindowSnapshot {
  from: string
  to: string
}

export type QueryWindowResult = { ok: true; window: QueryWindow } | { ok: false; error: string }

export function parseLocalDateTime(value: string): Date | null {
  if (!value) return null
  const parsed = new Date(value)
  return Number.isNaN(parsed.getTime()) ? null : parsed
}

/** Returns a positive duration, preferring the explicit span over the fallback. */
export function windowDurationMs(from: Date | null, to: Date | null, fallback = DEFAULT_LIVE_WINDOW_MS): number {
  if (!from || !to) return fallback
  const span = to.getTime() - from.getTime()
  return Number.isFinite(span) && span > 0 ? span : fallback
}

/** A live window always ends at `now` and keeps its width. */
export function rollingWindow(now: Date, durationMs: number): { from: Date; to: Date } {
  const span = Number.isFinite(durationMs) && durationMs > 0 ? durationMs : DEFAULT_LIVE_WINDOW_MS
  return { from: new Date(now.getTime() - span), to: new Date(now.getTime()) }
}

/**
 * Resolves the window sent to the API.
 *
 * Live mode ignores the stored/displayed upper bound so a page restored from
 * localStorage cannot freeze its To date. Non-live mode honors both fixed
 * bounds exactly.
 */
export function resolveQueryWindow(input: {
  live: boolean
  fromInput: string
  toInput: string
  durationMs: number
  now?: Date
}): QueryWindowResult {
  const now = input.now ?? new Date()
  const fromDate = input.fromInput ? parseLocalDateTime(input.fromInput) : null
  const toDate = input.toInput ? parseLocalDateTime(input.toInput) : new Date(now.getTime())

  if (input.live) {
    // Use the width captured when live started (or was restored). Re-reading
    // the inputs here would let an in-progress edit or a frozen To date change
    // the rolling window on the next timer tick.
    const duration =
      Number.isFinite(input.durationMs) && input.durationMs > 0
        ? input.durationMs
        : windowDurationMs(fromDate, toDate)
    const range = rollingWindow(now, duration)
    return { ok: true, window: { ...range, durationMs: duration, rolling: true } }
  }

  if (input.fromInput && !fromDate) {
    return { ok: false, error: 'From and To must be valid date/times.' }
  }
  if (!toDate) {
    return { ok: false, error: 'From and To must be valid date/times.' }
  }
  if (fromDate && fromDate >= toDate) {
    return { ok: false, error: 'From must be before To.' }
  }
  return {
    ok: true,
    window: {
      from: fromDate,
      to: toDate,
      durationMs: windowDurationMs(fromDate, toDate, input.durationMs),
      rolling: false,
    },
  }
}

/** Freezes the exact query bounds a request will use. */
export function snapshotQueryWindow(window: QueryWindow): QueryWindowSnapshot {
  return {
    from: window.from ? window.from.toISOString() : '',
    to: window.to.toISOString(),
  }
}

/**
 * The active snapshot is the source of truth for load-more offsets. It only
 * advances when the response is accepted; a failed request keeps both the
 * existing rows and their matching window.
 */
export function commitActiveWindow(
  current: QueryWindowSnapshot,
  requested: QueryWindowSnapshot,
  accepted: boolean,
): QueryWindowSnapshot {
  return accepted ? requested : current
}

/** Background refreshes must not shrink the rows a user already loaded. */
export function backgroundFetchLimit(loaded: number, base: number, max = MAX_EVENTS_LIMIT): number {
  const floor = Number.isFinite(base) && base > 0 ? Math.floor(base) : 50
  if (!Number.isFinite(loaded) || loaded <= floor) return floor
  return Math.min(Math.floor(loaded), max)
}

/**
 * While the reader is scrolled into loaded rows, replacing the table would
 * move content under the pointer. A page larger than one API request also
 * cannot be refreshed without dropping rows, so both cases defer with UX.
 */
export function shouldDeferLiveRefresh(input: {
  loaded: number
  scrollY: number
  max?: number
  scrollThreshold?: number
}): boolean {
  const max = input.max ?? MAX_EVENTS_LIMIT
  const threshold = input.scrollThreshold ?? LIVE_SCROLL_DEFER_PX
  return input.scrollY > threshold || input.loaded > max
}

export type LiveRefreshDecision = 'skip' | 'defer' | 'run'

/**
 * Decides whether a websocket/timer tick should query now, defer behind the
 * explicit "new events" banner, or do nothing because Live is paused.
 */
export function liveRefreshDecision(input: {
  live: boolean
  visible: boolean
  loaded: number
  scrollY: number
  force?: boolean
  max?: number
  scrollThreshold?: number
}): LiveRefreshDecision {
  if (!input.live) return 'skip'
  if (!input.visible) return 'defer'
  if (!input.force && shouldDeferLiveRefresh(input)) return 'defer'
  return 'run'
}

/** A response is stale once a newer load/filter/tab change has a ticket. */
export function isStaleResponse(ticket: number, current: number): boolean {
  return ticket !== current
}

/** No response may mutate state or schedule work after the view is disposed. */
export function canApplyResponse(disposed: boolean, ticket: number, current: number): boolean {
  return !disposed && !isStaleResponse(ticket, current)
}

/** Guards an appended page against a newer load, tab change or window. */
export function isCurrentPageResponse(input: {
  requestSeq: number
  currentSeq: number
  requestedTab: string
  currentTab: string
  expectedFrom: string
  currentFrom: string
  expectedTo: string
  currentTo: string
  expectedOffset: number
  loaded: number
  disposed?: boolean
}): boolean {
  return (
    !input.disposed &&
    !isStaleResponse(input.requestSeq, input.currentSeq) &&
    input.requestedTab === input.currentTab &&
    input.expectedFrom === input.currentFrom &&
    input.expectedTo === input.currentTo &&
    input.expectedOffset === input.loaded
  )
}
