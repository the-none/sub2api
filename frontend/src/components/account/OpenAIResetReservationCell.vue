<template>
  <div v-if="eligible" class="text-[10px]">
    <button
      type="button"
      data-testid="reset-reservation-open"
      class="rounded px-1.5 py-0.5 text-indigo-600 hover:bg-indigo-50 dark:text-indigo-400 dark:hover:bg-indigo-900/30"
      @click="open"
    >
      {{ reservation ? t(`${base}.states.${reservation.status}`) : t(`${base}.title`) }}
    </button>
    <span v-if="reservation?.status === 'pending'" class="text-gray-500 tabular-nums">
      {{ formatTime(reservation.execute_at) }}
    </span>
    <BaseDialog :show="show" :title="t(`${base}.title`)" width="narrow" @close="show = false">
      <div class="space-y-4 text-sm">
        <p class="text-gray-600 dark:text-gray-400">{{ t(`${base}.description`) }}</p>
        <p class="text-gray-600 dark:text-gray-400">{{ t(`${base}.coordination`) }}</p>
        <p v-if="error" role="alert" class="text-red-600">{{ error }}</p>
        <div v-if="reservation" class="space-y-1 rounded bg-gray-50 p-3 dark:bg-dark-800">
          <p class="font-medium">{{ t(`${base}.states.${reservation.status}`) }}</p>
          <p>{{ t(`${base}.expires`, { time: formatTime(reservation.expires_at) }) }}</p>
          <p>{{ t(`${base}.executes`, { time: formatTime(reservation.execute_at) }) }}</p>
          <p v-if="reservation.status === 'unknown'" class="text-amber-600">{{ t(`${base}.unknownHint`) }}</p>
          <p v-if="reservation.status === 'consumed'" class="text-amber-600">{{ t(`${base}.recoveryHint`) }}</p>
          <p v-if="reservation.error_code" class="break-all text-xs text-amber-600">{{ reservation.error_code }}</p>
        </div>
        <label v-if="!active" class="block">
          <span class="input-label">{{ t(`${base}.leadMinutes`) }}</span>
          <input v-model.number="leadMinutes" type="number" min="5" max="1440" step="1" class="input" data-testid="reset-reservation-lead" />
        </label>
      </div>
      <template #footer>
        <div class="flex justify-end gap-2">
          <button type="button" class="btn btn-secondary" :disabled="busy" @click="refresh">{{ t('common.refresh') }}</button>
          <button
            v-if="reservation?.status === 'pending' && !reservation.attempted_at"
            type="button" class="btn btn-secondary" :disabled="busy"
            data-testid="reset-reservation-cancel" @click="cancel"
          >{{ t(`${base}.cancel`) }}</button>
          <button
            v-if="!active" type="button" class="btn btn-primary"
            :disabled="busy || !loaded || !validLead || account.status !== 'active'"
            data-testid="reset-reservation-create" @click="create"
          >{{ t(`${base}.schedule`) }}</button>
        </div>
      </template>
    </BaseDialog>
  </div>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useIntervalFn } from '@vueuse/core'
import { useI18n } from 'vue-i18n'
import type { Account } from '@/types'
import {
  getOpenAIResetReservation, createOpenAIResetReservation, cancelOpenAIResetReservation,
  type OpenAIResetReservation
} from '@/api/admin/accounts'
import BaseDialog from '@/components/common/BaseDialog.vue'

const props = defineProps<{ account: Account }>()
const { t } = useI18n()
const base = 'admin.accounts.resetReservation'
const eligible = computed(() => props.account.platform === 'openai' && props.account.type === 'oauth' && !props.account.parent_account_id && props.account.quota_dimension !== 'spark')
const reservation = ref<OpenAIResetReservation | null>(null)
const show = ref(false)
const busy = ref(false)
const loaded = ref(false)
const error = ref('')
const leadMinutes = ref(30)
const validLead = computed(() => Number.isInteger(leadMinutes.value) && leadMinutes.value >= 5 && leadMinutes.value <= 1440)
const active = computed(() => !!reservation.value && ['pending', 'resetting', 'unknown', 'consumed'].includes(reservation.value.status))
const formatTime = (value: string) => new Date(value).toLocaleString()
let generation = 0

async function run(action: (id: number) => Promise<OpenAIResetReservation | null>) {
  if (busy.value || !eligible.value) return
  const current = generation
  const id = props.account.id
  busy.value = true
  error.value = ''
  try {
    const result = await action(id)
    if (current !== generation) return
    reservation.value = result
    loaded.value = true
  } catch (e) {
    if (current !== generation) return
    error.value = (e as { message?: string })?.message || t('common.error')
  } finally {
    if (current === generation) busy.value = false
  }
}
const refresh = () => run(getOpenAIResetReservation)
const open = () => { show.value = true; void refresh() }
const create = () => {
  if (!validLead.value || active.value || !loaded.value) return
  return run(id => createOpenAIResetReservation(id, leadMinutes.value))
}
const cancel = () => {
  const target = reservation.value
  if (!target || target.status !== 'pending' || target.attempted_at) return
  return run(id => cancelOpenAIResetReservation(id, target.id))
}

watch(() => [props.account.id, eligible.value], () => {
  generation++
  reservation.value = null
  loaded.value = false
  busy.value = false
  show.value = false
  error.value = ''
  if (eligible.value) void refresh()
}, { immediate: true })
useIntervalFn(() => {
  if (eligible.value && active.value && document.visibilityState !== 'hidden') void refresh()
}, 30_000)
</script>
