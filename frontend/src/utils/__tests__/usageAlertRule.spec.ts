import { describe, expect, it } from 'vitest'

import { defaultUsageAlertOperator, isStandardUsageAlertCondition } from '../usageAlertRule'

describe('defaultUsageAlertOperator', () => {
  it('uses an increasing threshold for used percentage', () => {
    expect(defaultUsageAlertOperator('used_percent')).toBe('>=')
  })

  it('uses a decreasing threshold for remaining percentage', () => {
    expect(defaultUsageAlertOperator('remaining_percent')).toBe('<=')
  })

  it('detects non-standard metric and operator combinations', () => {
    expect(isStandardUsageAlertCondition('used_percent', '>=')).toBe(true)
    expect(isStandardUsageAlertCondition('remaining_percent', '<=')).toBe(true)
    expect(isStandardUsageAlertCondition('used_percent', '<=')).toBe(false)
    expect(isStandardUsageAlertCondition('remaining_percent', '>=')).toBe(false)
  })
})
