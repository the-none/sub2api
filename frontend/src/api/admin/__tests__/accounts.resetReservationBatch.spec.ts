import { beforeEach, describe, expect, it, vi } from 'vitest'

const { getMock, postMock } = vi.hoisted(() => ({
  getMock: vi.fn(),
  postMock: vi.fn()
}))

vi.mock('../../client', () => ({
  apiClient: {
    get: getMock,
    post: postMock,
    delete: vi.fn()
  }
}))

import { getOpenAIResetReservation } from '../accounts'

describe('getOpenAIResetReservation microtask batching', () => {
  beforeEach(() => {
    getMock.mockReset()
    postMock.mockReset()
  })

  it('uses single GET when only one unique account is queried', async () => {
    getMock.mockResolvedValueOnce({ data: { id: 'r-1', status: 'pending' } })
    const result = await getOpenAIResetReservation(1)
    expect(getMock).toHaveBeenCalledWith('/admin/openai/accounts/1/reset-reservation')
    expect(postMock).not.toHaveBeenCalled()
    expect(result?.id).toBe('r-1')
  })

  it('coalesces concurrent row queries in one microtask into batch POST', async () => {
    postMock.mockResolvedValueOnce({
      data: {
        '10': { id: 'r-10', status: 'pending' },
        '11': null
      }
    })
    const [a, b] = await Promise.all([
      getOpenAIResetReservation(10),
      getOpenAIResetReservation(11)
    ])
    expect(getMock).not.toHaveBeenCalled()
    expect(postMock).toHaveBeenCalledWith('/admin/openai/accounts/reset-reservations/batch', {
      account_ids: [10, 11]
    })
    expect(a?.id).toBe('r-10')
    expect(b).toBeNull()
  })
})
