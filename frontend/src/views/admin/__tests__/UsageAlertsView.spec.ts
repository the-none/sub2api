import { flushPromises, mount } from '@vue/test-utils'
import { ref } from 'vue'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import UsageAlertsView from '../UsageAlertsView.vue'

const {
  listAccounts,
  listBindings,
  listRealAccounts,
  listRules,
  listWebhooks
} = vi.hoisted(() => ({
  listAccounts: vi.fn(),
  listBindings: vi.fn(),
  listRealAccounts: vi.fn(),
  listRules: vi.fn(),
  listWebhooks: vi.fn()
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
    showError: vi.fn(),
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
    listAccounts.mockResolvedValue({ items: [] })
    listBindings.mockResolvedValue([])
    listRealAccounts.mockResolvedValue([])
    listRules.mockResolvedValue([])
    listWebhooks.mockResolvedValue([])
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
