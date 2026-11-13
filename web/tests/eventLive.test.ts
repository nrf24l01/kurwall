import { describe, expect, test } from 'bun:test'
import {
  backgroundFetchLimit,
  canApplyResponse,
  commitActiveWindow,
  DEFAULT_LIVE_WINDOW_MS,
  isCurrentPageResponse,
  isStaleResponse,
  liveRefreshDecision,
  MAX_EVENTS_LIMIT,
  parseLocalDateTime,
  resolveQueryWindow,
  rollingWindow,
  shouldDeferLiveRefresh,
  snapshotQueryWindow,
  windowDurationMs,
} from '../src/lib/eventLive'

function local(value: string): Date {
  return new Date(value)
}

describe('windowDurationMs', () => {
  test('keeps the selected span', () => {
    expect(windowDurationMs(local('2026-11-13T10:00'), local('2026-11-13T12:30'))).toBe(150 * 60 * 1000)
  })

  test('falls back for missing, invalid or reversed bounds', () => {
    expect(windowDurationMs(null, local('2026-11-13T12:00'))).toBe(DEFAULT_LIVE_WINDOW_MS)
    expect(windowDurationMs(local('2026-11-13T12:00'), local('2026-11-13T12:00'))).toBe(DEFAULT_LIVE_WINDOW_MS)
    expect(windowDurationMs(local('2026-11-13T13:00'), local('2026-11-13T12:00'))).toBe(DEFAULT_LIVE_WINDOW_MS)
  })
})

describe('rollingWindow', () => {
  test('moves both bounds forward while preserving the width', () => {
    const first = rollingWindow(local('2026-11-13T18:00'), 2 * 60 * 60 * 1000)
    const second = rollingWindow(local('2026-11-13T18:05'), 2 * 60 * 60 * 1000)

    expect(first.to.getTime() - first.from.getTime()).toBe(2 * 60 * 60 * 1000)
    expect(second.to.getTime() - second.from.getTime()).toBe(2 * 60 * 60 * 1000)
    expect(second.to.getTime() - first.to.getTime()).toBe(5 * 60 * 1000)
    expect(second.from.getTime() - first.from.getTime()).toBe(5 * 60 * 1000)
  })
})

describe('resolveQueryWindow', () => {
  test('a live view restored from old storage does not freeze its To date', () => {
    const now = local('2026-11-13T18:00')
    const storedFrom = '2026-11-13T10:00'
    const storedTo = '2026-11-13T12:00'
    const durationMs = windowDurationMs(parseLocalDateTime(storedFrom), parseLocalDateTime(storedTo))
    const result = resolveQueryWindow({ live: true, fromInput: storedFrom, toInput: storedTo, durationMs, now })

    expect(result.ok).toBe(true)
    if (!result.ok) return
    expect(result.window.rolling).toBe(true)
    expect(result.window.durationMs).toBe(2 * 60 * 60 * 1000)
    expect(result.window.from?.getTime()).toBe(local('2026-11-13T16:00').getTime())
    expect(result.window.to.getTime()).toBe(now.getTime())
  })

  test('a live tick keeps the captured width even if the displayed bounds are edited', () => {
    const now = local('2026-11-13T18:00')
    const result = resolveQueryWindow({
      live: true,
      fromInput: '2026-11-13T09:00',
      toInput: '2026-11-13T13:00',
      durationMs: 60 * 60 * 1000,
      now,
    })

    expect(result.ok).toBe(true)
    if (!result.ok) return
    expect(result.window.from?.getTime()).toBe(local('2026-11-13T17:00').getTime())
    expect(result.window.to.getTime()).toBe(now.getTime())
  })

  test('a paused view honors its fixed bounds exactly', () => {
    const result = resolveQueryWindow({
      live: false,
      fromInput: '2026-11-13T10:00',
      toInput: '2026-11-13T12:00',
      durationMs: 2 * 60 * 60 * 1000,
      now: local('2026-11-13T18:00'),
    })

    expect(result.ok).toBe(true)
    if (!result.ok) return
    expect(result.window.rolling).toBe(false)
    expect(result.window.from?.getTime()).toBe(local('2026-11-13T10:00').getTime())
    expect(result.window.to.getTime()).toBe(local('2026-11-13T12:00').getTime())
  })

  test('rejects invalid or reversed fixed ranges', () => {
    const invalid = resolveQueryWindow({ live: false, fromInput: 'nope', toInput: '2026-11-13T12:00', durationMs: 1 })
    expect(invalid.ok).toBe(false)
    if (!invalid.ok) expect(invalid.error).toBe('From and To must be valid date/times.')

    const reversed = resolveQueryWindow({
      live: false,
      fromInput: '2026-11-13T13:00',
      toInput: '2026-11-13T12:00',
      durationMs: 1,
    })
    expect(reversed.ok).toBe(false)
    if (!reversed.ok) expect(reversed.error).toBe('From must be before To.')
  })
})

describe('active query snapshot', () => {
  test('freezes the exact ISO bounds used by the request', () => {
    const snapshot = snapshotQueryWindow({
      from: local('2026-11-13T10:00:00'),
      to: local('2026-11-13T12:00:00'),
      durationMs: 2 * 60 * 60 * 1000,
      rolling: false,
    })

    expect(snapshot).toEqual({
      from: local('2026-11-13T10:00:00').toISOString(),
      to: local('2026-11-13T12:00:00').toISOString(),
    })
  })

  test('a failed response preserves the previous active window', () => {
    const previous = { from: '2026-11-13T08:00:00.000Z', to: '2026-11-13T09:00:00.000Z' }
    const requested = { from: '2026-11-13T10:00:00.000Z', to: '2026-11-13T11:00:00.000Z' }

    expect(commitActiveWindow(previous, requested, false)).toEqual(previous)
    expect(commitActiveWindow(previous, requested, true)).toEqual(requested)
  })
})

describe('backgroundFetchLimit', () => {
  test('preserves the number of loaded rows up to the API maximum', () => {
    expect(backgroundFetchLimit(0, 50)).toBe(50)
    expect(backgroundFetchLimit(50, 50)).toBe(50)
    expect(backgroundFetchLimit(120, 50)).toBe(120)
    expect(backgroundFetchLimit(250, 50)).toBe(250)
    expect(backgroundFetchLimit(MAX_EVENTS_LIMIT, 50)).toBe(MAX_EVENTS_LIMIT)
    expect(backgroundFetchLimit(MAX_EVENTS_LIMIT + 1, 50)).toBe(MAX_EVENTS_LIMIT)
  })
})

describe('shouldDeferLiveRefresh', () => {
  test('defers while scrolled or when one request cannot preserve all rows', () => {
    expect(shouldDeferLiveRefresh({ loaded: 50, scrollY: 0 })).toBe(false)
    expect(shouldDeferLiveRefresh({ loaded: 120, scrollY: 0 })).toBe(false)
    expect(shouldDeferLiveRefresh({ loaded: 50, scrollY: 400 })).toBe(true)
    expect(shouldDeferLiveRefresh({ loaded: MAX_EVENTS_LIMIT + 1, scrollY: 0 })).toBe(true)
  })
})

describe('liveRefreshDecision', () => {
  test('skips when Live is paused', () => {
    expect(liveRefreshDecision({ live: false, visible: true, loaded: 50, scrollY: 0 })).toBe('skip')
  })

  test('defers in a hidden tab, even when the caller forces a refresh', () => {
    expect(liveRefreshDecision({ live: true, visible: false, loaded: 50, scrollY: 0 })).toBe('defer')
    expect(liveRefreshDecision({ live: true, visible: false, loaded: 50, scrollY: 0, force: true })).toBe('defer')
  })

  test('defers behind explicit UX while scrolled, then runs at the top', () => {
    expect(liveRefreshDecision({ live: true, visible: true, loaded: 120, scrollY: 400 })).toBe('defer')
    expect(liveRefreshDecision({ live: true, visible: true, loaded: 120, scrollY: 0 })).toBe('run')
  })

  test('defers when one request cannot preserve the loaded rows', () => {
    expect(
      liveRefreshDecision({ live: true, visible: true, loaded: MAX_EVENTS_LIMIT + 1, scrollY: 0 }),
    ).toBe('defer')
    expect(
      liveRefreshDecision({ live: true, visible: true, loaded: MAX_EVENTS_LIMIT + 1, scrollY: 0, force: true }),
    ).toBe('run')
  })
})

describe('stale response guards', () => {
  test('a newer request ticket wins over an older response', () => {
    expect(isStaleResponse(4, 5)).toBe(true)
    expect(isStaleResponse(5, 5)).toBe(false)
  })

  test('no response may apply or schedule work after disposal', () => {
    expect(canApplyResponse(false, 5, 5)).toBe(true)
    expect(canApplyResponse(false, 4, 5)).toBe(false)
    expect(canApplyResponse(true, 5, 5)).toBe(false)
  })

  test('a page append is rejected after a newer load, tab, window or row-count change', () => {
    const base = {
      requestSeq: 7,
      currentSeq: 7,
      requestedTab: 'traffic',
      currentTab: 'traffic',
      expectedFrom: '2026-11-13T10:00:00.000Z',
      currentFrom: '2026-11-13T10:00:00.000Z',
      expectedTo: '2026-11-13T12:00:00.000Z',
      currentTo: '2026-11-13T12:00:00.000Z',
      expectedOffset: 100,
      loaded: 100,
    }

    expect(isCurrentPageResponse(base)).toBe(true)
    expect(isCurrentPageResponse({ ...base, currentSeq: 8 })).toBe(false)
    expect(isCurrentPageResponse({ ...base, currentTab: 'dns' })).toBe(false)
    expect(isCurrentPageResponse({ ...base, currentFrom: '2026-11-13T09:00:00.000Z' })).toBe(false)
    expect(isCurrentPageResponse({ ...base, currentTo: '2026-11-13T13:00:00.000Z' })).toBe(false)
    expect(isCurrentPageResponse({ ...base, loaded: 50 })).toBe(false)
    expect(isCurrentPageResponse({ ...base, disposed: true })).toBe(false)
  })
})
