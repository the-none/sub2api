import type { UsageAlertMetric, UsageAlertOperator } from '@/api/admin/usageAlert'

export function defaultUsageAlertOperator(metric: UsageAlertMetric): UsageAlertOperator {
  return metric === 'used_percent' ? '>=' : '<='
}

export function isStandardUsageAlertCondition(
  metric: UsageAlertMetric,
  operator: UsageAlertOperator
): boolean {
  return operator === defaultUsageAlertOperator(metric)
}
