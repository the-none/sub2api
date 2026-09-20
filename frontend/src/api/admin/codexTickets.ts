import { apiClient } from '../client'

export interface TicketOptions {
  models: string[]
  instructions: string
  user_prompt: string
  retry_seconds: number
  timeout_seconds: number
  ttl_seconds: number
  refresh_before_seconds: number
  max_backoff_seconds: number
  fail_closed: boolean
}

export interface TicketPolicy {
  enabled: boolean | null
  options?: TicketOptions | null
  proxy_id?: number | null
}

export interface TicketConfig {
  enabled: boolean
  default_account_enabled?: boolean
  target_length: number
  ttl_seconds: number
  refresh_before_seconds: number
  harvest_proxy_url: string
  harvest_probe_interval_seconds: number
  harvest_attempt_timeout_seconds: number
  fail_closed: boolean
  models: string[]
  instructions: string
  user_prompt: string
  max_concurrency: number
  max_backoff_seconds: number
}

export interface TicketProgress {
  model: string
  state: string
  attempts: number
  consecutive_failures: number
  last_attempt?: string
  last_success?: string
  next_attempt?: string
  reason?: string
  http_status?: number
  ticket_length?: number
}

export interface TicketAccountView {
  policy: TicketPolicy
  global_enabled: boolean
  enabled: boolean
  eligible: boolean
  options: TicketOptions
  proxy_configured: boolean
  tickets: Array<{ model: string; ready: boolean; blocked: boolean; expires_at?: string }>
  progress: TicketProgress[]
}

const base = '/admin/accounts'
export const getTicketConfig = async () => (await apiClient.get<TicketConfig>(`${base}/codex-ticket/settings`)).data
export const saveTicketConfig = async (config: TicketConfig, clearProxy = false) =>
  (await apiClient.put<TicketConfig>(`${base}/codex-ticket/settings`, { ...config, clear_proxy: clearProxy })).data
export const getTicketAccount = async (id: number) => (await apiClient.get<TicketAccountView>(`${base}/${id}/codex-ticket`)).data
export const saveTicketPolicy = async (id: number, policy: TicketPolicy) =>
  (await apiClient.put<TicketAccountView>(`${base}/${id}/codex-ticket`, policy)).data
export const harvestTicket = async (id: number, model: string) =>
  (await apiClient.post<{ state: string }>(`${base}/${id}/codex-ticket/harvest`, { model })).data

export const ticketDefaults = (): TicketOptions => ({
  models: ['gpt-6-astra', 'gpt-5.6-sol'], instructions: 'Reply with exactly: pong', user_prompt: 'ping',
  retry_seconds: 6, timeout_seconds: 25, ttl_seconds: 3600, refresh_before_seconds: 600,
  max_backoff_seconds: 300, fail_closed: true
})

export function configToOptions(config: TicketConfig): TicketOptions {
  return { models: [...config.models], instructions: config.instructions, user_prompt: config.user_prompt,
    retry_seconds: config.harvest_probe_interval_seconds, timeout_seconds: config.harvest_attempt_timeout_seconds,
    ttl_seconds: config.ttl_seconds, refresh_before_seconds: config.refresh_before_seconds,
    max_backoff_seconds: config.max_backoff_seconds, fail_closed: config.fail_closed }
}

export function optionsToConfig(config: TicketConfig, options: TicketOptions): TicketConfig {
  return { ...config, models: [...options.models], instructions: options.instructions, user_prompt: options.user_prompt,
    harvest_probe_interval_seconds: options.retry_seconds, harvest_attempt_timeout_seconds: options.timeout_seconds,
    ttl_seconds: options.ttl_seconds, refresh_before_seconds: options.refresh_before_seconds,
    max_backoff_seconds: options.max_backoff_seconds, fail_closed: options.fail_closed }
}
