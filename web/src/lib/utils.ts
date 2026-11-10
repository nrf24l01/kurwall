export function formatNumber(value: number | undefined | null): string {
  if (value === undefined || value === null) return '0'
  return new Intl.NumberFormat().format(value)
}

export function formatBytes(value: number | undefined | null): string {
  const bytes = value ?? 0
  if (bytes < 1024) return `${bytes} B`
  const units = ['KB', 'MB', 'GB', 'TB', 'PB']
  let amount = bytes / 1024
  let unit = 0
  while (amount >= 1024 && unit < units.length - 1) {
    amount /= 1024
    unit++
  }
  return `${amount.toFixed(amount >= 10 ? 0 : 1)} ${units[unit]}`
}

export function formatTime(value: string | undefined | null): string {
  if (!value) return '—'
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return '—'
  return date.toLocaleString(undefined, { hour12: false })
}

export function formatRelative(value: string | undefined | null): string {
  if (!value) return 'never'
  const date = new Date(value)
  const diff = Date.now() - date.getTime()
  if (Number.isNaN(diff)) return 'never'
  const seconds = Math.round(diff / 1000)
  if (seconds < 10) return 'just now'
  if (seconds < 60) return `${seconds}s ago`
  const minutes = Math.round(seconds / 60)
  if (minutes < 60) return `${minutes}m ago`
  const hours = Math.round(minutes / 60)
  if (hours < 48) return `${hours}h ago`
  return `${Math.round(hours / 24)}d ago`
}

export function toRFC3339(date: Date): string {
  return date.toISOString()
}

export function protocolName(protocol: number): string {
  switch (protocol) {
    case 6:
      return 'TCP'
    case 17:
      return 'UDP'
    case 1:
      return 'ICMP'
    case 58:
      return 'ICMPv6'
    default:
      return String(protocol)
  }
}
export function rangeWindow(range: string): { from: string; to: string } {
  const to = new Date()
  const hours = range === '1h' ? 1 : range === '6h' ? 6 : range === '24h' ? 24 : 168
  const from = new Date(to.getTime() - hours * 3600 * 1000)
  return { from: from.toISOString(), to: to.toISOString() }
}

export function formatDuration(ms: number | undefined | null): string {
  const value = ms ?? 0
  if (value < 1000) return `${value} ms`
  const seconds = value / 1000
  if (seconds < 60) return `${seconds.toFixed(1)} s`
  const minutes = seconds / 60
  if (minutes < 60) return `${minutes.toFixed(1)} min`
  return `${(minutes / 60).toFixed(1)} h`
}
