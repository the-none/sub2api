import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { defineComponent } from 'vue'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import EditAccountModal from '../EditAccountModal.vue'
import CodexTicketAccountPanel from '../CodexTicketAccountPanel.vue'
import { ticketDefaults, type TicketAccountView, type TicketPolicy } from '@/api/admin/codexTickets'

const mocks = vi.hoisted(() => ({ getTicket: vi.fn(), saveTicket: vi.fn(), updateAccount: vi.fn(), showError: vi.fn() }))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showError: mocks.showError, showSuccess: vi.fn(), showInfo: vi.fn() }) }))
vi.mock('@/stores/auth', () => ({ useAuthStore: () => ({ isSimpleMode: true }) }))
vi.mock('@/api/admin', () => ({ adminAPI: {
  accounts: { update: mocks.updateAccount, checkMixedChannelRisk: vi.fn().mockResolvedValue({ has_risk: false }) },
  settings: { getWebSearchEmulationConfig: vi.fn().mockResolvedValue({ enabled: false, providers: [] }), getSettings: vi.fn().mockResolvedValue({}) },
  tlsFingerprintProfiles: { list: vi.fn().mockResolvedValue([]) }
} }))
vi.mock('@/api/admin/accounts', () => ({ getAntigravityDefaultModelMapping: vi.fn() }))
vi.mock('@/api/admin/proxies', () => ({ getAll: vi.fn().mockResolvedValue([]) }))
vi.mock('@/api/admin/codexTickets', async () => ({
  ...await vi.importActual<typeof import('@/api/admin/codexTickets')>('@/api/admin/codexTickets'),
  getTicketAccount: mocks.getTicket, saveTicketPolicy: mocks.saveTicket
}))
vi.mock('vue-i18n', async () => ({
  ...await vi.importActual<typeof import('vue-i18n')>('vue-i18n'), useI18n: () => ({ t: (key: string) => key })
}))

const account = () => ({ id: 41, name: 'OAuth account', notes: '', platform: 'openai', type: 'oauth',
  credentials: { access_token: 'test' }, extra: {}, proxy_id: null, concurrency: 1, priority: 1,
  rate_multiplier: 1, status: 'active', group_ids: [], expires_at: null, auto_pause_on_expired: false })
let current: TicketAccountView
let renderErrors: unknown[]
const wrappers: VueWrapper[] = []
const BaseDialog = defineComponent({ props: ['show'], template: '<div v-if="show"><slot/><slot name="footer"/></div>' })

function mountEditor() {
  const wrapper = mount(EditAccountModal, { attachTo: document.body,
    props: { show: true, account: account() as any, groups: [], proxies: [] },
    global: { config: { errorHandler: error => { renderErrors.push(error) } },
      stubs: { BaseDialog, Select: true, Icon: true, ProxySelector: true, GroupSelector: true, ModelWhitelistSelector: true } }
  })
  wrappers.push(wrapper)
  return wrapper
}
const quickSave = (wrapper: VueWrapper) => wrapper.getComponent(CodexTicketAccountPanel).findAll('button').find(b => b.text().endsWith('saveAccount'))!
const bottomSave = (wrapper: VueWrapper) => wrapper.get('[data-tour="account-form-submit"]')

beforeEach(() => {
  vi.useFakeTimers(); vi.clearAllMocks(); renderErrors = []
  current = { revision: 'v1', policy: { enabled: true }, global_enabled: true, enabled: true, eligible: true,
    options: ticketDefaults(), proxy_configured: true, tickets: [],
    progress: [{ model: 'gpt-6-astra', state: 'waiting', attempts: 0, consecutive_failures: 0 }] }
  mocks.getTicket.mockImplementation(async () => JSON.parse(JSON.stringify(current)))
  mocks.saveTicket.mockImplementation(async (_id, policy: TicketPolicy) => {
    current = JSON.parse(JSON.stringify({ ...current, revision: current.revision + '-next', policy,
      enabled: current.global_enabled && policy.enabled !== false, tickets: !current.global_enabled || policy.enabled === false ? null : [],
      progress: [{ model: 'gpt-6-astra', state: policy.enabled === false ? 'disabled' : 'waiting', attempts: 0, consecutive_failures: 0 }] }))
    return JSON.parse(JSON.stringify(current))
  })
  mocks.updateAccount.mockImplementation(async () => ({ ...account(), extra: { codex_ticket_enabled: current.policy.enabled } }))
})
afterEach(() => { wrappers.splice(0).forEach(w => w.unmount()); document.body.innerHTML = ''; vi.useRealTimers() })

describe('account editor with the real ticket panel', () => {
  it('saves ticket changes from the bottom account save button', async () => {
    const wrapper = mountEditor(); await flushPromises()
    await wrapper.getComponent(CodexTicketAccountPanel).find('select').setValue('off')
    await bottomSave(wrapper).trigger('click'); await flushPromises()
    expect(mocks.saveTicket).toHaveBeenCalledWith(41, { enabled: false, options: null, proxy_id: null }, 'v1')
    expect(mocks.updateAccount).toHaveBeenCalledTimes(1)
    expect(current.policy.enabled).toBe(false)
    expect(wrapper.emitted('close')).toHaveLength(1)
    expect(renderErrors).toEqual([])
  })

  it('can re-enable after a disabled response returns tickets=null', async () => {
    const wrapper = mountEditor(); await flushPromises()
    await wrapper.getComponent(CodexTicketAccountPanel).find('select').setValue('off')
    await quickSave(wrapper).trigger('click'); await flushPromises()
    expect(renderErrors).toEqual([])
    expect(quickSave(wrapper).attributes('disabled')).toBeUndefined()
    await wrapper.getComponent(CodexTicketAccountPanel).find('select').setValue('on')
    await quickSave(wrapper).trigger('click'); await flushPromises()
    expect(mocks.saveTicket).toHaveBeenLastCalledWith(41, { enabled: true, options: null, proxy_id: null }, 'v1-next')
    expect(current.policy.enabled).toBe(true)
    expect(renderErrors).toEqual([])
  })

  it('does not rewrite unchanged ticket settings when saving account fields', async () => {
    const wrapper = mountEditor(); await flushPromises()
    await bottomSave(wrapper).trigger('click'); await flushPromises()
    expect(mocks.saveTicket).not.toHaveBeenCalled()
    expect(mocks.updateAccount).toHaveBeenCalledTimes(1)
    expect(wrapper.emitted('close')).toHaveLength(1)
  })

  it('keeps the dialog open and skips account writes when ticket saving conflicts', async () => {
    mocks.saveTicket.mockRejectedValue({ status: 409, message: 'Ticket configuration changed' })
    const wrapper = mountEditor(); await flushPromises()
    await wrapper.getComponent(CodexTicketAccountPanel).find('select').setValue('off')
    await bottomSave(wrapper).trigger('click'); await flushPromises()
    expect(mocks.updateAccount).not.toHaveBeenCalled()
    expect(wrapper.emitted('close')).toBeUndefined()
    expect(mocks.showError).toHaveBeenCalledWith('admin.accounts.ticket.saveBeforeAccountFailed')
    expect(wrapper.text()).toContain('admin.accounts.ticket.conflict')
    expect(bottomSave(wrapper).attributes('disabled')).toBeUndefined()
  })

  it('joins an in-flight quick save and prevents duplicate account submissions', async () => {
    let resolveSave!: (value: TicketAccountView) => void
    mocks.saveTicket.mockImplementationOnce(() => new Promise(resolve => { resolveSave = resolve }))
    const wrapper = mountEditor(); await flushPromises()
    await wrapper.getComponent(CodexTicketAccountPanel).find('select').setValue('off')
    await quickSave(wrapper).trigger('click'); await flushPromises()
    await bottomSave(wrapper).trigger('click'); await flushPromises()
    await wrapper.get('form#edit-account-form').trigger('submit.prevent')
    expect(mocks.saveTicket).toHaveBeenCalledTimes(1)
    expect(mocks.updateAccount).not.toHaveBeenCalled()
    expect(bottomSave(wrapper).attributes('disabled')).toBeDefined()
    current = { ...current, revision: 'v2', enabled: false, policy: { enabled: false }, tickets: null }
    resolveSave(current); await flushPromises()
    expect(mocks.saveTicket).toHaveBeenCalledTimes(1)
    expect(mocks.updateAccount).toHaveBeenCalledTimes(1)
    expect(wrapper.emitted('close')).toHaveLength(1)
    expect(renderErrors).toEqual([])
  })

  it('does not dismiss an unresolved conflict when the draft is changed back to its initial value', async () => {
    const wrapper = mountEditor(); await flushPromises()
    const mode = wrapper.getComponent(CodexTicketAccountPanel).find('select')
    await mode.setValue('off')
    current = { ...current, revision: 'external', policy: { enabled: false }, enabled: false, tickets: null }
    await vi.advanceTimersByTimeAsync(5000); await flushPromises()
    await mode.setValue('on')
    await bottomSave(wrapper).trigger('click'); await flushPromises()
    expect(mocks.saveTicket).not.toHaveBeenCalled()
    expect(mocks.updateAccount).not.toHaveBeenCalled()
    expect(wrapper.emitted('close')).toBeUndefined()
    expect(wrapper.text()).toContain('admin.accounts.ticket.conflict')
  })

  it('reports partial success and retries only the unsaved account fields', async () => {
    mocks.updateAccount.mockRejectedValueOnce({ message: 'Account update failed' })
    const wrapper = mountEditor(); await flushPromises()
    await wrapper.getComponent(CodexTicketAccountPanel).find('select').setValue('off')
    await bottomSave(wrapper).trigger('click'); await flushPromises()
    expect(current.policy.enabled).toBe(false)
    expect(wrapper.emitted('close')).toBeUndefined()
    expect(mocks.showError).toHaveBeenCalledWith('admin.accounts.ticket.partialAccountSave Account update failed')
    await bottomSave(wrapper).trigger('click'); await flushPromises()
    expect(mocks.saveTicket).toHaveBeenCalledTimes(1)
    expect(mocks.updateAccount).toHaveBeenCalledTimes(2)
    expect(wrapper.emitted('close')).toHaveLength(1)
  })

  it('can edit an initially disabled policy without overriding the global master switch', async () => {
    current = { ...current, global_enabled: false, enabled: false, policy: { enabled: false }, tickets: null }
    const wrapper = mountEditor(); await flushPromises()
    expect(renderErrors).toEqual([])
    expect(wrapper.text()).toContain('admin.accounts.ticket.masterOff')
    await wrapper.getComponent(CodexTicketAccountPanel).find('select').setValue('on')
    await bottomSave(wrapper).trigger('click'); await flushPromises()
    expect(current.policy.enabled).toBe(true)
    expect(current.enabled).toBe(false)
    expect(mocks.updateAccount).toHaveBeenCalledTimes(1)
    expect(renderErrors).toEqual([])
  })

  it('does not save or close a different account after an old ticket request finishes', async () => {
    let resolveSave!: (value: TicketAccountView) => void
    mocks.saveTicket.mockImplementationOnce(() => new Promise(resolve => { resolveSave = resolve }))
    const wrapper = mountEditor(); await flushPromises()
    await wrapper.getComponent(CodexTicketAccountPanel).find('select').setValue('off')
    await bottomSave(wrapper).trigger('click'); await flushPromises()
    await wrapper.setProps({ account: { ...account(), id: 42 } as any }); await flushPromises()
    resolveSave({ ...current, policy: { enabled: false }, enabled: false, tickets: null }); await flushPromises()
    expect(mocks.updateAccount).not.toHaveBeenCalled()
    expect(wrapper.emitted('close')).toBeUndefined()
    expect(bottomSave(wrapper).attributes('disabled')).toBeUndefined()
  })
})
