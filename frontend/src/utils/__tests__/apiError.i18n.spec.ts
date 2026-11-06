import { describe, expect, it } from 'vitest'
import { createI18n } from 'vue-i18n'
import en from '@/i18n/locales/en'
import zh from '@/i18n/locales/zh'
import { extractI18nErrorMessage } from '@/utils/apiError'

describe('API errors with the production i18n runtime', () => {
  it.each([
    ['en', 'Payment provider misconfigured. Please contact an administrator.', 'Certificate Serial'],
    ['zh', '支付通道配置错误，请联系管理员', '证书序列号']
  ])('translates known errors and preserves missing-key fallbacks in %s', (locale, expected, field) => {
    const { global } = createI18n({
      legacy: false,
      locale,
      fallbackLocale: 'en',
      messages: { en, zh },
      missingWarn: false,
      fallbackWarn: false
    })
    const translate = Object.assign(
      (key: string, params?: Record<string, unknown>) => global.t(key, params ?? {}),
      { te: (key: string) => global.te(key) }
    )

    expect(translate.te('payment.errors.PAYMENT_PROVIDER_MISCONFIGURED')).toBe(true)
    expect(translate.te('payment.errors')).toBe(false)
    expect(translate.te('payment.errors.UNKNOWN_TEST_REASON')).toBe(false)
    expect(extractI18nErrorMessage(
      { reason: 'PAYMENT_PROVIDER_MISCONFIGURED' }, translate, 'payment.errors', 'fallback'
    )).toBe(expected)
    expect(extractI18nErrorMessage(
      { reason: 'UNKNOWN_TEST_REASON', message: 'Server detail' }, translate, 'payment.errors', 'fallback'
    )).toBe('Server detail')
    expect(extractI18nErrorMessage(
      { reason: 'UNKNOWN_TEST_REASON' }, translate, 'payment.errors', 'fallback'
    )).toBe('fallback')
    expect(extractI18nErrorMessage(
      { reason: 'WXPAY_CONFIG_MISSING_KEY', metadata: { key: 'certSerial' } },
      translate, 'payment.errors', 'fallback'
    )).toContain(field)
  })
})
