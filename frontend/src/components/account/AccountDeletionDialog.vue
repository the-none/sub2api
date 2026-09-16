<template>
  <ConfirmDialog
    :show="show"
    :title="t('admin.accounts.deleteAccount')"
    :message="message"
    :confirm-text="t('common.delete')"
    :cancel-text="t('common.cancel')"
    :danger="true"
    :confirm-disabled="loading || failed || deleting || !accountIds.length"
    :cancel-disabled="deleting"
    @confirm="confirm"
    @cancel="cancel"
  >
    <p v-if="loading" role="status" class="text-sm text-gray-500">{{ t('admin.accounts.deleteUsageChecking') }}</p>
    <div v-else-if="failed" role="alert" class="rounded-lg border border-red-300 bg-red-50 p-3 text-sm text-red-800 dark:border-red-700 dark:bg-red-950/40 dark:text-red-200">
      <p>{{ t('admin.accounts.deleteUsageCheckFailed') }}</p>
      <button type="button" class="mt-2 font-semibold underline" @click="loadImpact">{{ t('admin.accounts.deleteUsageRetry') }}</button>
    </div>
    <div v-for="impact in impacts" :key="impact.realAccountID" role="alert" class="rounded-lg border border-amber-300 bg-amber-50 p-3 text-sm text-amber-900 dark:border-amber-700 dark:bg-amber-950/40 dark:text-amber-200">
      <p class="font-semibold">{{ impact.name }}</p>
      <p class="mt-1">{{ t('admin.accounts.deleteUsageRetained', { rules: impact.ruleCount, bindings: impact.bindingCount }) }}</p>
      <p class="mt-1 font-semibold">
        {{ impact.remainingAccounts === 0 ? t('admin.accounts.deleteUsageLastAccount') : t('admin.accounts.deleteUsageSharedAccount', { count: impact.remainingAccounts }) }}
      </p>
    </div>
  </ConfirmDialog>
</template>

<script setup lang="ts">
import { onBeforeUnmount, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import usageAlertAPI from '@/api/admin/usageAlert'
import ConfirmDialog from '@/components/common/ConfirmDialog.vue'
import { accountDeletionImpacts, type AccountDeletionImpact } from '@/utils/accountDeletionImpact'

const props = defineProps<{ show: boolean; accountIds: number[]; message: string; deleting?: boolean }>()
const emit = defineEmits<{ (e: 'confirm'): void; (e: 'cancel'): void }>()
const { t } = useI18n()
const loading = ref(false)
const failed = ref(false)
const impacts = ref<AccountDeletionImpact[]>([])
let request = 0

async function loadImpact() {
  const current = ++request
  impacts.value = []
  failed.value = false
  loading.value = props.show
  if (!props.show) return
  const ids = [...props.accountIds]
  try {
    const [realAccounts, rules, bindings] = await Promise.all([
      usageAlertAPI.listRealAccounts(), usageAlertAPI.listRules(), usageAlertAPI.listBindings()
    ])
    if (current !== request) return
    impacts.value = accountDeletionImpacts(ids, realAccounts, rules, bindings)
  } catch {
    if (current === request) failed.value = true
  } finally {
    if (current === request) loading.value = false
  }
}

function confirm() {
  if (!props.show || loading.value || failed.value || props.deleting || !props.accountIds.length) return
  emit('confirm')
}

function cancel() {
  if (!props.deleting) emit('cancel')
}

watch(() => [props.show, props.accountIds] as const, loadImpact, { immediate: true })
onBeforeUnmount(() => { request++ })
</script>
