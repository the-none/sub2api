import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import AccountDeletionDialog from '../AccountDeletionDialog.vue'
import ConfirmDialog from '@/components/common/ConfirmDialog.vue'

const { listRealAccounts, listRules, listBindings } = vi.hoisted(() => ({
  listRealAccounts: vi.fn(), listRules: vi.fn(), listBindings: vi.fn()
}))
vi.mock('@/api/admin/usageAlert', () => ({ default: { listRealAccounts, listRules, listBindings } }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string, values?: unknown) => key + (values ? JSON.stringify(values) : '') }) }))

const source = (id: number) => ({
  id: 3, name: 'Shared subscription', accounts: [{ id, quota_dimension: 'global' }]
})
const mountDialog = () => mount(AccountDeletionDialog, {
  props: { show: true, accountIds: [4], message: 'Delete account?' },
  global: { stubs: { BaseDialog: { template: '<div><slot /><slot name="footer" /></div>' } } }
})

describe('AccountDeletionDialog', () => {
  beforeEach(() => {
    vi.resetAllMocks()
    listRealAccounts.mockResolvedValue([source(4)])
    listRules.mockResolvedValue([{ id: 7, real_account_id: 3 }])
    listBindings.mockResolvedValue([{ id: 1, real_account_id: 3 }])
  })

  it('requires a successful check and shows the final-account warning before confirming', async () => {
    const wrapper = mountDialog()
    const confirm = wrapper.getComponent(ConfirmDialog)
    expect(confirm.props('confirmDisabled')).toBe(true)
    confirm.vm.$emit('confirm')
    expect(wrapper.emitted('confirm')).toBeUndefined()
    await flushPromises()
    expect(wrapper.get('[role="alert"]').text()).toContain('deleteUsageLastAccount')
    expect(wrapper.text()).toContain('"rules":1,"bindings":1')
    expect(confirm.props('confirmDisabled')).toBe(false)
    confirm.vm.$emit('confirm')
    expect(wrapper.emitted('confirm')).toHaveLength(1)
    wrapper.unmount()
  })

  it('blocks deletion after a failed check and allows retry or cancellation', async () => {
    listRules.mockRejectedValueOnce(new Error('offline'))
    const wrapper = mountDialog()
    await flushPromises()
    const confirm = wrapper.getComponent(ConfirmDialog)
    expect(wrapper.get('[role="alert"]').text()).toContain('deleteUsageCheckFailed')
    expect(confirm.props('confirmDisabled')).toBe(true)
    confirm.vm.$emit('confirm')
    expect(wrapper.emitted('confirm')).toBeUndefined()
    await wrapper.get('[role="alert"] button').trigger('click')
    await flushPromises()
    expect(confirm.props('confirmDisabled')).toBe(false)
    confirm.vm.$emit('cancel')
    expect(wrapper.emitted('cancel')).toHaveLength(1)
    expect(wrapper.emitted('confirm')).toBeUndefined()
    wrapper.unmount()
  })

  it('ignores a stale request after switching the deletion target', async () => {
    let finishOld!: (value: unknown[]) => void
    listRealAccounts.mockReturnValueOnce(new Promise((resolve) => { finishOld = resolve }))
    const wrapper = mountDialog()
    listRealAccounts.mockResolvedValue([source(4)])
    await wrapper.setProps({ accountIds: [13] })
    await flushPromises()
    expect(wrapper.find('[role="alert"]').exists()).toBe(false)
    finishOld([source(4)])
    await flushPromises()
    expect(wrapper.find('[role="alert"]').exists()).toBe(false)
    await wrapper.setProps({ show: false })
    wrapper.getComponent(ConfirmDialog).vm.$emit('confirm')
    expect(wrapper.emitted('confirm')).toBeUndefined()
    wrapper.unmount()
  })

  it('cannot close or confirm again while deletion is executing', async () => {
    const wrapper = mountDialog()
    await flushPromises()
    await wrapper.setProps({ deleting: true })
    expect(wrapper.findAll('button').every((button) => button.element.disabled)).toBe(true)
    const dialog = wrapper.getComponent(ConfirmDialog)
    dialog.vm.$emit('cancel')
    dialog.vm.$emit('confirm')
    expect(wrapper.emitted('cancel')).toBeUndefined()
    expect(wrapper.emitted('confirm')).toBeUndefined()
    await wrapper.setProps({ deleting: false })
    await wrapper.findAll('button').find((button) => button.text() === 'common.cancel')!.trigger('click')
    expect(wrapper.emitted('cancel')).toHaveLength(1)
    wrapper.unmount()
  })
})
