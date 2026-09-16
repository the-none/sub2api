import type { RealAccount, UsageAlertBinding, UsageAlertRule } from '@/api/admin/usageAlert'

export interface AccountDeletionImpact {
  realAccountID: number
  name: string
  ruleCount: number
  bindingCount: number
  remainingAccounts: number
}

export function accountDeletionImpacts(
  accountIDs: readonly number[],
  realAccounts: RealAccount[],
  rules: UsageAlertRule[],
  bindings: UsageAlertBinding[]
): AccountDeletionImpact[] {
  const deleting = new Set(accountIDs)
  // Deleting a parent also deletes its Spark shadows, including unselected ones.
  for (const real of realAccounts) {
    for (const account of real.accounts || []) {
      if (account.parent_account_id && deleting.has(account.parent_account_id) && account.quota_dimension === 'spark') {
        deleting.add(account.id)
      }
    }
  }
  return realAccounts.flatMap((real) => {
    const linked = real.accounts || []
    if (!linked.some((account) => deleting.has(account.id))) return []
    const ruleCount = rules.filter((rule) => rule.real_account_id === real.id).length
    const bindingCount = bindings.filter((binding) => binding.real_account_id === real.id).length
    if (!ruleCount && !bindingCount) return []
    return [{
      realAccountID: real.id,
      name: real.name,
      ruleCount,
      bindingCount,
      remainingAccounts: linked.filter((account) => !deleting.has(account.id)).length
    }]
  })
}
