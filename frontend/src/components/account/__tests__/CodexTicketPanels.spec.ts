import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { ticketDefaults, type TicketAccountView, type TicketConfig } from '@/api/admin/codexTickets'
import CodexTicketSettingsPanel from '../CodexTicketSettingsPanel.vue'
import CodexTicketAccountPanel from '../CodexTicketAccountPanel.vue'
import CodexTicketOptionsForm from '../CodexTicketOptionsForm.vue'

const mocks = vi.hoisted(() => ({ getConfig: vi.fn(), saveConfig: vi.fn(), getAccount: vi.fn(), savePolicy: vi.fn(), harvest: vi.fn() }))
vi.mock('@/api/admin/codexTickets', async () => ({
  ...await vi.importActual<typeof import('@/api/admin/codexTickets')>('@/api/admin/codexTickets'),
  getTicketConfig: mocks.getConfig, saveTicketConfig: mocks.saveConfig, getTicketAccount: mocks.getAccount,
  saveTicketPolicy: mocks.savePolicy, harvestTicket: mocks.harvest
}))
vi.mock('@/api/admin/proxies', () => ({ getAll: vi.fn().mockResolvedValue([{ id: 9, name: 'Harvest proxy', status: 'active' }]) }))
vi.mock('vue-i18n', async () => ({ ...await vi.importActual<typeof import('vue-i18n')>('vue-i18n'), useI18n: () => ({ t: (key: string) => key }) }))

const config = (): TicketConfig => ({ enabled: false, default_account_enabled: true, target_length: 292, ttl_seconds: 3600,
  refresh_before_seconds: 600, harvest_proxy_url: 'http://user:***@proxy.example:8080', harvest_probe_interval_seconds: 6,
  harvest_attempt_timeout_seconds: 25, fail_closed: true, models: ['gpt-6-astra'], instructions: 'Reply with exactly: pong',
  user_prompt: 'ping', max_concurrency: 4, max_backoff_seconds: 300 })
const account = (): TicketAccountView => ({ revision: 'v1', policy: { enabled: null }, global_enabled: true, enabled: true, eligible: true,
  options: ticketDefaults(), proxy_configured: true, tickets: [], progress: [{ model: 'gpt-6-astra', state: 'waiting', attempts: 0, consecutive_failures: 0 }] })
const wrappers: VueWrapper[] = []
const button = (wrapper: VueWrapper, suffix: string) => wrapper.findAll('button').find(b => b.text().endsWith(suffix))!

beforeEach(() => {
  vi.useFakeTimers(); vi.clearAllMocks()
  mocks.getConfig.mockResolvedValue(config())
  mocks.saveConfig.mockImplementation(async value => value)
  mocks.getAccount.mockImplementation(async () => account())
  mocks.savePolicy.mockImplementation(async (_id, policy) => ({ ...account(), policy }))
  mocks.harvest.mockResolvedValue({ state: 'started' })
})
afterEach(() => { wrappers.splice(0).forEach(w => w.unmount()); vi.useRealTimers() })

describe('Ticket settings', () => {
  it('saves the master switch, custom prompts and timing separately with masked proxy intact', async () => {
    const wrapper = mount(CodexTicketSettingsPanel); wrappers.push(wrapper); await flushPromises()
    await wrapper.findAll('input[type="checkbox"]')[0]!.setValue(true)
    const form = wrapper.getComponent(CodexTicketOptionsForm)
    form.vm.$emit('update:modelValue', { ...ticketDefaults(), retry_seconds: 30, user_prompt: '测试', instructions: '' })
    await button(wrapper, 'saveGlobal').trigger('click'); await flushPromises()
    expect(mocks.saveConfig).toHaveBeenCalledWith(expect.objectContaining({ enabled: true, harvest_probe_interval_seconds: 30,
      user_prompt: '测试', instructions: '', harvest_proxy_url: 'http://user:***@proxy.example:8080' }), false)
    expect(wrapper.find('[role="status"]').exists()).toBe(true)
  })

  it('requires an explicit clear action for the proxy', async () => {
    const wrapper = mount(CodexTicketSettingsPanel); wrappers.push(wrapper); await flushPromises()
    await wrapper.findAll('input[type="checkbox"]')[2]!.setValue(true)
    await button(wrapper, 'saveGlobal').trigger('click'); await flushPromises()
    expect(mocks.saveConfig).toHaveBeenCalledWith(expect.anything(), true)
  })

  it('resets only prompts and previews quoted text as valid JSON', async () => {
    const options = { ...ticketDefaults(), user_prompt: '"quoted"\n中文', retry_seconds: 77 }
    const wrapper = mount(CodexTicketOptionsForm, { props: { modelValue: options } }); wrappers.push(wrapper)
    expect(JSON.parse(wrapper.get('pre').text()).input[0].content[0].text).toBe(options.user_prompt)
    await button(wrapper, 'resetPrompts').trigger('click')
    expect(wrapper.emitted('update:modelValue')?.[0]?.[0]).toEqual({ ...options, user_prompt: 'ping', instructions: 'Reply with exactly: pong' })
  })
})

describe('Account ticket panel', () => {
  it('preserves unsaved controls during polling and saves participation plus proxy override', async () => {
    const wrapper = mount(CodexTicketAccountPanel, { props: { accountId: 41 } }); wrappers.push(wrapper); await flushPromises()
    await wrapper.findAll('select')[0]!.setValue('off')
    await wrapper.findAll('select')[1]!.setValue('9')
    await vi.advanceTimersByTimeAsync(5000); await flushPromises()
    expect((wrapper.findAll('select')[0]!.element as HTMLSelectElement).value).toBe('off')
    await button(wrapper, 'saveAccount').trigger('click'); await flushPromises()
    expect(mocks.savePolicy).toHaveBeenCalledWith(41, { enabled: false, options: null, proxy_id: 9 }, 'v1')
    expect(wrapper.emitted('saved')?.[0]?.[0]).toMatchObject({ accountId: 41, policy: { enabled: false } })
  })

  it('refreshes untouched inherited controls before switching to an override', async () => {
    const wrapper = mount(CodexTicketAccountPanel, { props: { accountId: 41 } }); wrappers.push(wrapper); await flushPromises()
    const newer = { ...account(), revision: 'v2', options: { ...ticketDefaults(), user_prompt: 'new global prompt', retry_seconds: 77 } }
    mocks.getAccount.mockResolvedValue(newer)
    await vi.advanceTimersByTimeAsync(5000); await flushPromises()
    await wrapper.get('input[type="checkbox"]').setValue(true)
    const editor = wrapper.findAllComponents(CodexTicketOptionsForm).find(c => !c.props('disabled'))!
    expect(editor.props('modelValue')).toMatchObject({ user_prompt: 'new global prompt', retry_seconds: 77 })
    await button(wrapper, 'saveAccount').trigger('click'); await flushPromises()
    expect(mocks.savePolicy).toHaveBeenCalledWith(41, expect.objectContaining({ options: expect.objectContaining({ user_prompt: 'new global prompt' }) }), 'v2')
  })

  it('preserves dirty input but blocks stale saves until the user reloads', async () => {
    const wrapper = mount(CodexTicketAccountPanel, { props: { accountId: 41 } }); wrappers.push(wrapper); await flushPromises()
    await wrapper.findAll('select')[0]!.setValue('off')
    mocks.getAccount.mockResolvedValue({ ...account(), revision: 'v2', policy: { enabled: true } })
    await vi.advanceTimersByTimeAsync(5000); await flushPromises()
    expect((wrapper.findAll('select')[0]!.element as HTMLSelectElement).value).toBe('off')
    expect(button(wrapper, 'saveAccount').attributes('disabled')).toBeDefined()
    expect(wrapper.text()).toContain('ticket.conflict')
    await button(wrapper, 'reload').trigger('click'); await flushPromises()
    expect((wrapper.findAll('select')[0]!.element as HTMLSelectElement).value).toBe('on')
    expect(button(wrapper, 'saveAccount').attributes('disabled')).toBeUndefined()
  })

  it('shows a conflict if another writer wins between polling and saving', async () => {
    mocks.savePolicy.mockRejectedValue({ status: 409, message: 'configuration changed' })
    const wrapper = mount(CodexTicketAccountPanel, { props: { accountId: 41 } }); wrappers.push(wrapper); await flushPromises()
    await button(wrapper, 'saveAccount').trigger('click'); await flushPromises()
    expect(wrapper.text()).toContain('ticket.conflict')
    expect(button(wrapper, 'saveAccount').attributes('disabled')).toBeDefined()
  })

  it('disables manual harvesting when the global switch is off', async () => {
    mocks.getAccount.mockResolvedValue({ ...account(), global_enabled: false, enabled: false })
    const wrapper = mount(CodexTicketAccountPanel, { props: { accountId: 41 } }); wrappers.push(wrapper); await flushPromises()
    expect(button(wrapper, 'harvest').attributes('disabled')).toBeDefined()
    expect(wrapper.text()).toContain('masterOff')
  })

  it('shows a cooldown response and submits only the selected saved model', async () => {
    mocks.harvest.mockResolvedValue({ state: 'cooldown' })
    const wrapper = mount(CodexTicketAccountPanel, { props: { accountId: 41 } }); wrappers.push(wrapper); await flushPromises()
    await button(wrapper, 'harvest').trigger('click'); await flushPromises()
    expect(mocks.harvest).toHaveBeenCalledWith(41, 'gpt-6-astra')
    expect(wrapper.get('[role="status"]').text()).toContain('triggers.cooldown')
  })

  it('ignores a late response from the previously selected account', async () => {
    let resolveOld!: (value: TicketAccountView) => void
    mocks.getAccount.mockImplementationOnce(() => new Promise(resolve => { resolveOld = resolve }))
    const wrapper = mount(CodexTicketAccountPanel, { props: { accountId: 41 } }); wrappers.push(wrapper)
    await wrapper.setProps({ accountId: 42 }); await flushPromises()
    resolveOld({ ...account(), global_enabled: false, enabled: false }); await flushPromises()
    expect(wrapper.text()).not.toContain('masterOff')
    await button(wrapper, 'saveAccount').trigger('click'); await flushPromises()
    expect(mocks.savePolicy).toHaveBeenCalledWith(42, expect.anything(), 'v1')
  })
})
