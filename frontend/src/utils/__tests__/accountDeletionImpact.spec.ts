import { describe, expect, it } from 'vitest'
import type { RealAccount, RealAccountLinkedAccount, UsageAlertBinding, UsageAlertRule } from '@/api/admin/usageAlert'
import { accountDeletionImpacts } from '../accountDeletionImpact'

const account = (id: number, parent?: number): RealAccountLinkedAccount => ({
  id, name: `Account ${id}`, platform: 'openai', type: 'oauth', status: 'active',
  parent_account_id: parent, quota_dimension: parent ? 'spark' : 'global'
})
const real = (id: number, accounts: RealAccountLinkedAccount[]): RealAccount => ({
  id, name: `Source ${id}`, platform: 'openai', accounts, created_at: '', updated_at: ''
})
const rule: UsageAlertRule = {
  id: 7, name: 'Weekly', real_account_id: 3, platform: 'openai', usage_type: 'overall',
  window: '7d', metric: 'used_percent', operator: '>=', threshold: 20,
  cooldown_minutes: 240, enabled: true, created_at: '', updated_at: ''
}
const binding: UsageAlertBinding = { id: 1, real_account_id: 3, webhook_id: 1, enabled: true, created_at: '', updated_at: '' }

describe('accountDeletionImpacts', () => {
  it('distinguishes deleting one shared account from deleting the last account in a batch', () => {
    const sources = [real(3, [account(4), account(5)]), real(9, [account(13)])]
    expect(accountDeletionImpacts([4], sources, [rule], [binding])).toEqual([
      { realAccountID: 3, name: 'Source 3', ruleCount: 1, bindingCount: 1, remainingAccounts: 1 }
    ])
    expect(accountDeletionImpacts([4, 5, 13], sources, [rule], [binding])[0].remainingAccounts).toBe(0)
  })

  it('includes implicit Spark cascade deletion but not an unselected ordinary account', () => {
    const sources = [real(3, [account(4), account(5, 4)])]
    expect(accountDeletionImpacts([4], sources, [rule], [])[0].remainingAccounts).toBe(0)
    expect(accountDeletionImpacts([5], sources, [rule], [])[0].remainingAccounts).toBe(1)
  })

  it('finds cascaded shadows even when attached to another real account', () => {
    const sources = [real(3, [account(5, 4)]), real(9, [account(4)])]
    expect(accountDeletionImpacts([4], sources, [rule], [])[0].remainingAccounts).toBe(0)
  })

  it('warns about retained disabled configuration and bindings without rules', () => {
    const sources = [real(3, [account(4)])]
    expect(accountDeletionImpacts([4], sources, [{ ...rule, enabled: false }], [])).toHaveLength(1)
    expect(accountDeletionImpacts([4], sources, [], [{ ...binding, enabled: false }])[0].bindingCount).toBe(1)
  })

  it('does not warn for unrelated or unconfigured accounts', () => {
    expect(accountDeletionImpacts([13], [real(3, [account(4)])], [rule], [binding])).toEqual([])
    expect(accountDeletionImpacts([4], [real(3, [account(4)])], [], [])).toEqual([])
  })
})
