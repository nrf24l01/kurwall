import type { TrafficCondition, TrafficMatch } from '@/lib/api'

export interface FilterOption {
  value: string
  label: string
}

export interface TrafficField {
  value: string
  label: string
  type: 'text' | 'ip' | 'number' | 'select'
  options?: FilterOption[]
  operators: string[]
}

const textOperators = ['eq', 'ne', 'contains', 'not_contains', 'starts_with', 'ends_with']
const numberOperators = ['eq', 'ne', 'gt', 'gte', 'lt', 'lte']
const choiceOperators = ['eq', 'ne']

export const trafficFields: TrafficField[] = [
  { value: 'text', label: 'Text (any field)', type: 'text', operators: textOperators },
  { value: 'ip.addr', label: 'IP address', type: 'ip', operators: choiceOperators },
  { value: 'ip.src', label: 'Source IP', type: 'ip', operators: choiceOperators },
  { value: 'ip.dst', label: 'Destination IP', type: 'ip', operators: choiceOperators },
  { value: 'src.port', label: 'Source port', type: 'number', operators: numberOperators },
  { value: 'dst.port', label: 'Destination port', type: 'number', operators: numberOperators },
  { value: 'port', label: 'Either port', type: 'number', operators: numberOperators },
  { value: 'protocol', label: 'Protocol', type: 'select', operators: choiceOperators, options: [
    { value: 'tcp', label: 'TCP' },
    { value: 'udp', label: 'UDP' },
    { value: 'icmp', label: 'ICMP' },
    { value: 'icmpv6', label: 'ICMPv6' },
  ] },
  { value: 'event.kind', label: 'Event kind', type: 'select', operators: choiceOperators, options: [
    { value: 'connection', label: 'Connection' },
    { value: 'block', label: 'Block' },
  ] },
  { value: 'decision', label: 'Decision', type: 'select', operators: choiceOperators, options: [
    { value: 'allowed', label: 'Allowed' },
    { value: 'blocked', label: 'Blocked' },
  ] },
  { value: 'domain', label: 'Domain / SNI', type: 'text', operators: textOperators },
  { value: 'sni', label: 'SNI', type: 'text', operators: textOperators },
  { value: 'target', label: 'Block target', type: 'text', operators: textOperators },
  { value: 'block.kind', label: 'Block kind', type: 'select', operators: choiceOperators, options: [
    { value: 'ip', label: 'IP' },
    { value: 'cidr', label: 'CIDR' },
    { value: 'domain', label: 'Domain' },
    { value: 'asn', label: 'ASN' },
    { value: 'dns', label: 'DNS' },
  ] },
  { value: 'container', label: 'Container', type: 'text', operators: textOperators },
  { value: 'bytes.out', label: 'Egress bytes', type: 'number', operators: numberOperators },
  { value: 'bytes.in', label: 'Ingress bytes', type: 'number', operators: numberOperators },
  { value: 'duration', label: 'Duration (ms)', type: 'number', operators: numberOperators },
  { value: 'reason', label: 'Reason', type: 'text', operators: textOperators },
]

export const operatorLabels: Record<string, string> = {
  eq: 'is',
  ne: 'is not',
  contains: 'contains',
  not_contains: 'does not contain',
  starts_with: 'starts with',
  ends_with: 'ends with',
  gt: 'greater than',
  gte: 'at least',
  lt: 'less than',
  lte: 'at most',
}

const operatorSymbols: Record<string, string> = {
  eq: '==',
  ne: '!=',
  contains: 'contains',
  not_contains: 'not contains',
  starts_with: 'starts_with',
  ends_with: 'ends_with',
  gt: '>',
  gte: '>=',
  lt: '<',
  lte: '<=',
}

export function fieldDefinition(field: string): TrafficField | undefined {
  return trafficFields.find((item) => item.value === field)
}

export function operatorLabel(operator: string): string {
  return operatorLabels[operator] ?? operator
}

export function conditionExpression(condition: TrafficCondition): string {
  const def = fieldDefinition(condition.field)
  const symbol = operatorSymbols[condition.operator] ?? condition.operator
  const value = def?.type === 'text' ? JSON.stringify(condition.value) : condition.value
  return `${condition.negate ? '!' : ''}${condition.field} ${symbol} ${value}`
}

export function expressionFor(conditions: TrafficCondition[], match: TrafficMatch): string {
  if (conditions.length === 0) return ''
  return conditions.map(conditionExpression).join(match === 'all' ? ' && ' : ' || ')
}

export function defaultCondition(): TrafficCondition {
  return { field: 'ip.addr', operator: 'eq', value: '', negate: false }
}
