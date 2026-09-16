import { flushPromises, mount } from '@vue/test-utils'
import { ref } from 'vue'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import UsageAlertsView from '../UsageAlertsView.vue'

const {
  listAccounts,
  listBindings,
  listRealAccounts,
  listRules,
  listWebhooks,
  showError
} = vi.hoisted(() => ({
  listAccounts: vi.fn(),
  listBindings: vi.fn(),
  listRealAccounts: vi.fn(),
  listRules: vi.fn(),
  listWebhooks: vi.fn(),
  showError: vi.fn()
}))

vi.mock('@/api/admin/accounts', () => ({
  default: {
    list: listAccounts
  }
}))

vi.mock('@/api/admin/usageAlert', () => ({
  default: {
    listBindings,
    listRealAccounts,
    listRules,
    listWebhooks
  }
}))

vi.mock('@/stores', () => ({
  useAppStore: () => ({
    showError,
    showSuccess: vi.fn()
  })
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      locale: ref('zh-CN')
    })
  }
})

describe('admin UsageAlertsView', () => {
  beforeEach(() => {
    showError.mockReset()
    listAccounts.mockResolvedValue({ items: [] })
    listBindings.mockResolvedValue([])
    listRealAccounts.mockResolvedValue([])
    listRules.mockResolvedValue([])
    listWebhooks.mockResolvedValue([])
  })

  it('hides deleted sources by default while retaining new unbound sources and a history toggle', async () => {
    listRealAccounts.mockResolvedValue([
      { id: 3, name: 'Retired source', platform: 'openai', has_only_deleted_accounts: true, accounts: [] },
      { id: 9, name: 'Current source', platform: 'openai', has_only_deleted_accounts: false, accounts: [{ id: 13, name: 'Current account' }] },
      { id: 10, name: 'New unbound source', platform: 'openai', has_only_deleted_accounts: false, accounts: [] }
    ])
    const wrapper = mount(UsageAlertsView, {
      global: { stubs: { AppLayout: { template: '<div><slot /></div>' } } }
    })
    await flushPromises()

    expect(wrapper.findAll('[data-testid="real-account-row"]').map((row) => row.text()).join(' ')).not.toContain('Retired source')
    expect(wrapper.findAll('[data-testid="real-account-row"]')).toHaveLength(2)
    expect(wrapper.find('table').text()).toContain('New unbound source')
    expect(wrapper.find('[data-testid="retired-notification-warning"]').exists()).toBe(false)
    expect(wrapper.findAll('option').some((option) => option.text().includes('Retired source'))).toBe(false)

    await wrapper.get('[data-testid="show-retired-real-accounts"]').setValue(true)
    expect(wrapper.findAll('[data-testid="real-account-row"]')).toHaveLength(3)
    const retiredRow = wrapper.findAll('[data-testid="real-account-row"]').find((row) => row.text().includes('Retired source'))!
    expect(retiredRow.text()).toContain('当前关联账号均已删除')
    expect(retiredRow.classes()).toContain('bg-amber-50')
    const attachSelect = wrapper.findAll('select').find((select) => select.find('option[value="3"]').exists())!
    await attachSelect.setValue('3')
    await wrapper.get('[data-testid="show-retired-real-accounts"]').setValue(false)
    expect(attachSelect.element.value).toBe('0')
    wrapper.unmount()
  })

  it('highlights retained rules and notification bindings and preserves their selected source when editing', async () => {
    const retired = { id: 3, name: 'Retired source', platform: 'openai', has_only_deleted_accounts: true, accounts: [] }
    const current = { id: 9, name: 'Current source', platform: 'openai', has_only_deleted_accounts: false, accounts: [] }
    listRealAccounts.mockResolvedValue([retired, current])
    listRules.mockResolvedValue([{
      id: 7, name: 'Retained weekly rule', real_account_id: 3, platform: 'openai', usage_type: 'overall',
      window: '7d', metric: 'used_percent', operator: '>=', threshold: 20, step_percent: 20,
      cooldown_minutes: 240, enabled: true
    }])
    listBindings.mockResolvedValue([{ id: 1, real_account_id: 3, webhook_id: 1, enabled: true }])
    listWebhooks.mockResolvedValue([{ id: 1, name: 'Telegram', type: 'telegram', enabled: true }])
    const wrapper = mount(UsageAlertsView, {
      global: { stubs: { AppLayout: { template: '<div><slot /></div>' } } }
    })
    await flushPromises()

    expect(wrapper.get('[data-testid="retired-notification-warning"]').text()).toContain('Retired source')
    for (const selector of ['usage-alert-rule', 'usage-alert-binding']) {
      const row = wrapper.get(`[data-testid="${selector}"]`)
      expect(row.classes()).toContain('border-amber-500')
      expect(row.get('[role="alert"]').text()).toContain('通知配置仍保留')
      await row.findAll('button').find((button) => button.text() === '编辑')!.trigger('click')
    }
    expect(wrapper.get('[data-testid="usage-alert-rule"]').text()).toContain('Retained weekly rule')
    expect(wrapper.get('[data-testid="usage-alert-binding"]').text()).toContain('Retired source -> Telegram')
    const selectedRetired = wrapper.findAll('select').filter((select) => select.element.value === '3')
    expect(selectedRetired).toHaveLength(2)
    for (const select of selectedRetired) {
      expect(select.get('option[value="3"]').attributes('disabled')).toBeDefined()
      expect(select.get('option[value="9"]').exists()).toBe(true)
    }

    await wrapper.get('[data-testid="show-retired-real-accounts"]').setValue(true)
    const retiredRow = wrapper.findAll('[data-testid="real-account-row"]').find((row) => row.text().includes('Retired source'))!
    const confirmSpy = vi.spyOn(window, 'confirm').mockReturnValue(false)
    await retiredRow.findAll('button').find((button) => button.text() === '删除')!.trigger('click')
    expect(showError).toHaveBeenCalledWith('该真实账户仍有告警规则或通知绑定，请先迁移或删除这些配置，再删除真实账户。')
    expect(confirmSpy).not.toHaveBeenCalled()
    confirmSpy.mockRestore()

    listRealAccounts.mockResolvedValue([{ ...retired, has_only_deleted_accounts: false }, current])
    await wrapper.findAll('button').find((button) => button.text() === '刷新')!.trigger('click')
    await flushPromises()
    expect(wrapper.find('[data-testid="retired-notification-warning"]').exists()).toBe(false)
    expect(wrapper.get('[data-testid="usage-alert-rule"]').find('[role="alert"]').exists()).toBe(false)
    expect(wrapper.get('[data-testid="usage-alert-binding"]').find('[role="alert"]').exists()).toBe(false)
    expect(wrapper.find('table').text()).toContain('Retired source')
    wrapper.unmount()
  })

  it('switches to the standard operator when the metric changes', async () => {
    const wrapper = mount(UsageAlertsView, {
      global: {
        stubs: {
          AppLayout: { template: '<div><slot /></div>' }
        }
      }
    })
    await flushPromises()

    const labels = wrapper.findAll('label')
    const metricSelect = labels.find((label) => label.text().includes('指标'))?.find('select')
    const operatorSelect = labels.find((label) => label.text().includes('条件'))?.find('select')

    expect(metricSelect?.exists()).toBe(true)
    expect(operatorSelect?.exists()).toBe(true)
    expect(operatorSelect?.element.value).toBe('<=')

    await metricSelect?.setValue('used_percent')
    expect(operatorSelect?.element.value).toBe('>=')

    await metricSelect?.setValue('remaining_percent')
    expect(operatorSelect?.element.value).toBe('<=')

    wrapper.unmount()
  })

  it('preserves and warns about an existing non-standard condition', async () => {
    listRealAccounts.mockResolvedValueOnce([{
      id: 2,
      name: 'Claude',
      platform: 'anthropic',
      created_at: '2026-07-13T08:00:00Z',
      updated_at: '2026-07-13T08:00:00Z'
    }])
    listRules.mockResolvedValueOnce([{
      id: 9,
      name: 'Claude fable 7d usage alert',
      platform: 'anthropic',
      real_account_id: 2,
      usage_type: 'fable',
      window: '7d',
      metric: 'used_percent',
      operator: '<=',
      threshold: 50,
      min_reset_after_hours: null,
      step_percent: 10,
      cooldown_minutes: 240,
      enabled: true,
      created_at: '2026-07-13T08:00:00Z',
      updated_at: '2026-07-13T08:00:00Z'
    }])

    const wrapper = mount(UsageAlertsView, {
      global: {
        stubs: {
          AppLayout: { template: '<div><slot /></div>' }
        }
      }
    })
    await flushPromises()

    const editButton = wrapper.findAll('button').find((button) => (
      button.text() === '编辑' &&
      button.element.parentElement?.parentElement?.textContent?.includes('Claude fable 7d usage alert')
    ))
    expect(editButton).toBeTruthy()
    await editButton?.trigger('click')

    const labels = wrapper.findAll('label')
    const metricSelect = labels.find((label) => label.text().includes('指标'))?.find('select')
    const operatorSelect = labels.find((label) => label.text().includes('条件'))?.find('select')

    expect(metricSelect?.element.value).toBe('used_percent')
    expect(operatorSelect?.element.value).toBe('<=')
    expect(wrapper.text()).toContain('当前条件会在已用百分比降至阈值或更低时触发。')

    wrapper.unmount()
  })
})
