import { apiClient } from '../client'
import type { Account } from '@/types'

export type UsageAlertPlatform = 'all' | 'openai' | 'anthropic'
export type UsageAlertWindow = '5h' | '7d' | '7d_sonnet'
export type UsageAlertMetric = 'used_percent' | 'remaining_percent'
export type UsageAlertOperator = '>=' | '<='
export type UsageAlertWebhookType = 'json_post' | 'telegram'

export interface RealAccount {
  id: number
  name: string
  platform: Exclude<UsageAlertPlatform, 'all'>
  identifier?: string | null
  notes?: string | null
  accounts?: Account[]
  created_at: string
  updated_at: string
}

export interface UsageAlertWindowSnapshot {
  used_percent: number
  remaining_percent: number
  reset_at?: string | null
}

export interface UsageAlertSnapshot {
  account_id: number
  real_account_id: number
  platform: Exclude<UsageAlertPlatform, 'all'>
  source: string
  windows: Partial<Record<UsageAlertWindow, UsageAlertWindowSnapshot>>
  sampled_at: string
}

export interface UsageAlertRule {
  id: number
  name: string
  platform: UsageAlertPlatform
  window: UsageAlertWindow
  metric: UsageAlertMetric
  operator: UsageAlertOperator
  threshold: number
  min_reset_after_hours?: number | null
  cooldown_minutes: number
  enabled: boolean
  created_at: string
  updated_at: string
}

export interface UsageAlertWebhook {
  id: number
  name: string
  type: UsageAlertWebhookType
  url?: string | null
  config?: Record<string, unknown> | null
  enabled: boolean
  retry_count: number
  created_at: string
  updated_at: string
}

export interface UsageAlertBinding {
  id: number
  real_account_id: number
  webhook_id: number
  enabled: boolean
  real_account?: RealAccount
  webhook?: UsageAlertWebhook
  created_at: string
  updated_at: string
}

export type RealAccountPayload = Pick<RealAccount, 'name' | 'platform' | 'identifier' | 'notes'>
export type UsageAlertRulePayload = Omit<UsageAlertRule, 'id' | 'created_at' | 'updated_at'>
export type UsageAlertWebhookPayload = Omit<UsageAlertWebhook, 'id' | 'created_at' | 'updated_at'>
export type UsageAlertBindingPayload = Pick<UsageAlertBinding, 'real_account_id' | 'webhook_id' | 'enabled'>

const base = '/admin/usage-alert'

export async function listRealAccounts(): Promise<RealAccount[]> {
  const { data } = await apiClient.get<RealAccount[]>(`${base}/real-accounts`)
  return data
}

export async function createRealAccount(payload: RealAccountPayload): Promise<RealAccount> {
  const { data } = await apiClient.post<RealAccount>(`${base}/real-accounts`, payload)
  return data
}

export async function updateRealAccount(id: number, payload: RealAccountPayload): Promise<RealAccount> {
  const { data } = await apiClient.put<RealAccount>(`${base}/real-accounts/${id}`, payload)
  return data
}

export async function deleteRealAccount(id: number): Promise<{ message: string }> {
  const { data } = await apiClient.delete<{ message: string }>(`${base}/real-accounts/${id}`)
  return data
}

export async function attachAccounts(realAccountId: number, accountIds: number[]): Promise<{ message: string }> {
  const { data } = await apiClient.post<{ message: string }>(`${base}/real-accounts/${realAccountId}/accounts`, {
    account_ids: accountIds
  })
  return data
}

export async function detachAccount(realAccountId: number, accountId: number): Promise<{ message: string }> {
  const { data } = await apiClient.delete<{ message: string }>(`${base}/real-accounts/${realAccountId}/accounts/${accountId}`)
  return data
}

export async function getSnapshot(realAccountId: number): Promise<UsageAlertSnapshot | null> {
  const { data } = await apiClient.get<UsageAlertSnapshot | null>(`${base}/real-accounts/${realAccountId}/snapshot`)
  return data
}

export async function listRules(): Promise<UsageAlertRule[]> {
  const { data } = await apiClient.get<UsageAlertRule[]>(`${base}/rules`)
  return data
}

export async function createRule(payload: UsageAlertRulePayload): Promise<UsageAlertRule> {
  const { data } = await apiClient.post<UsageAlertRule>(`${base}/rules`, payload)
  return data
}

export async function updateRule(id: number, payload: UsageAlertRulePayload): Promise<UsageAlertRule> {
  const { data } = await apiClient.put<UsageAlertRule>(`${base}/rules/${id}`, payload)
  return data
}

export async function deleteRule(id: number): Promise<{ message: string }> {
  const { data } = await apiClient.delete<{ message: string }>(`${base}/rules/${id}`)
  return data
}

export async function listWebhooks(): Promise<UsageAlertWebhook[]> {
  const { data } = await apiClient.get<UsageAlertWebhook[]>(`${base}/webhooks`)
  return data
}

export async function createWebhook(payload: UsageAlertWebhookPayload): Promise<UsageAlertWebhook> {
  const { data } = await apiClient.post<UsageAlertWebhook>(`${base}/webhooks`, payload)
  return data
}

export async function updateWebhook(id: number, payload: UsageAlertWebhookPayload): Promise<UsageAlertWebhook> {
  const { data } = await apiClient.put<UsageAlertWebhook>(`${base}/webhooks/${id}`, payload)
  return data
}

export async function deleteWebhook(id: number): Promise<{ message: string }> {
  const { data } = await apiClient.delete<{ message: string }>(`${base}/webhooks/${id}`)
  return data
}

export async function testWebhook(payload: UsageAlertWebhookPayload): Promise<{ message: string }> {
  const { data } = await apiClient.post<{ message: string }>(`${base}/webhooks/test`, payload)
  return data
}

export async function listBindings(): Promise<UsageAlertBinding[]> {
  const { data } = await apiClient.get<UsageAlertBinding[]>(`${base}/bindings`)
  return data
}

export async function createBinding(payload: UsageAlertBindingPayload): Promise<UsageAlertBinding> {
  const { data } = await apiClient.post<UsageAlertBinding>(`${base}/bindings`, payload)
  return data
}

export async function updateBinding(id: number, payload: UsageAlertBindingPayload): Promise<UsageAlertBinding> {
  const { data } = await apiClient.put<UsageAlertBinding>(`${base}/bindings/${id}`, payload)
  return data
}

export async function deleteBinding(id: number): Promise<{ message: string }> {
  const { data } = await apiClient.delete<{ message: string }>(`${base}/bindings/${id}`)
  return data
}

export const usageAlertAPI = {
  listRealAccounts,
  createRealAccount,
  updateRealAccount,
  deleteRealAccount,
  attachAccounts,
  detachAccount,
  getSnapshot,
  listRules,
  createRule,
  updateRule,
  deleteRule,
  listWebhooks,
  createWebhook,
  updateWebhook,
  deleteWebhook,
  testWebhook,
  listBindings,
  createBinding,
  updateBinding,
  deleteBinding
}

export default usageAlertAPI
