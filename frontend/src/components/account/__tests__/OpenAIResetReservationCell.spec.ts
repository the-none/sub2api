import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import OpenAIResetReservationCell from '../OpenAIResetReservationCell.vue'
import type { Account } from '@/types'
import type { OpenAIResetReservation } from '@/api/admin/accounts'

const { get, create, cancel } = vi.hoisted(() => ({
  get: vi.fn(), create: vi.fn(), cancel: vi.fn()
}))
vi.mock('@/api/admin/accounts', () => ({
  getOpenAIResetReservation: get, createOpenAIResetReservation: create, cancelOpenAIResetReservation: cancel
}))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))

const account = (overrides: Partial<Account> = {}) => ({
  id: 1, platform: 'openai', type: 'oauth', status: 'active', ...overrides
} as Account)
const reservation = (status: OpenAIResetReservation['status'] = 'pending'): OpenAIResetReservation => ({
  id: 'reservation-a', status, expires_at: '2099-01-01T12:00:00Z',
  execute_at: '2099-01-01T11:30:00Z', updated_at: '2099-01-01T10:00:00Z', windows_reset: 0
})
const mountCell = (value = account()) => mount(OpenAIResetReservationCell, {
  props: { account: value },
  global: { stubs: { BaseDialog: { template: '<div v-if="show"><slot/><slot name="footer"/></div>', props: ['show'] } } }
})

describe('OpenAI reset reservation', () => {
  beforeEach(() => {
    get.mockReset().mockResolvedValue(null)
    create.mockReset().mockResolvedValue(reservation())
    cancel.mockReset().mockResolvedValue(reservation('cancelled'))
  })

  it('schedules only after explicit action and defaults to 30 minutes', async () => {
    const wrapper = mountCell()
    await flushPromises()
    expect(create).not.toHaveBeenCalled()
    await wrapper.get('[data-testid="reset-reservation-open"]').trigger('click')
    await flushPromises()
    await wrapper.get('[data-testid="reset-reservation-create"]').trigger('click')
    await flushPromises()
    expect(create).toHaveBeenCalledWith(1, 30)
    expect(wrapper.text()).toContain('states.pending')
    expect(wrapper.find('[data-testid="reset-reservation-create"]').exists()).toBe(false)
    wrapper.unmount()
  })

  it('cancels the exact loaded reservation and refuses invalid lead values', async () => {
    const wrapper = mountCell()
    await flushPromises()
    await wrapper.get('[data-testid="reset-reservation-open"]').trigger('click')
    await flushPromises()
    await wrapper.get('[data-testid="reset-reservation-lead"]').setValue('0')
    expect(wrapper.get('[data-testid="reset-reservation-create"]').attributes('disabled')).toBeDefined()
    get.mockResolvedValue(reservation())
    await wrapper.get('[data-testid="reset-reservation-open"]').trigger('click')
    await flushPromises()
    await wrapper.get('[data-testid="reset-reservation-cancel"]').trigger('click')
    await flushPromises()
    expect(cancel).toHaveBeenCalledWith(1, 'reservation-a')
    wrapper.unmount()
  })

  it.each(['resetting', 'unknown', 'consumed'] as const)('does not cancel or replace a %s operation', async status => {
    get.mockResolvedValue(reservation(status))
    const wrapper = mountCell()
    await flushPromises()
    await wrapper.get('[data-testid="reset-reservation-open"]').trigger('click')
    await flushPromises()
    expect(wrapper.find('[data-testid="reset-reservation-create"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="reset-reservation-cancel"]').exists()).toBe(false)
    wrapper.unmount()
  })

  it('ignores a stale response when the row is reused', async () => {
    let finish!: (value: OpenAIResetReservation) => void
    get.mockImplementationOnce(() => new Promise(resolve => { finish = resolve }))
    const wrapper = mountCell()
    await wrapper.setProps({ account: account({ id: 2 }) })
    await flushPromises()
    finish(reservation())
    await flushPromises()
    expect(wrapper.text()).not.toContain('states.pending')
    expect(get).toHaveBeenLastCalledWith(2)
    wrapper.unmount()
  })

  it('does not query or offer reservations on shadow accounts', async () => {
    const wrapper = mountCell(account({ parent_account_id: 10 }))
    await flushPromises()
    expect(get).not.toHaveBeenCalled()
    expect(wrapper.find('button').exists()).toBe(false)
    wrapper.unmount()
  })
})
