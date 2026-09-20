<script setup lang="ts">
import { computed, onUnmounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { getTicketAccount, harvestTicket, saveTicketPolicy, ticketDefaults, type TicketAccountView, type TicketPolicy } from '@/api/admin/codexTickets'
import { getAll as getProxies } from '@/api/admin/proxies'
import type { Proxy } from '@/types'
import CodexTicketOptionsForm from './CodexTicketOptionsForm.vue'

const props = defineProps<{ accountId: number }>()
const emit = defineEmits<{ saved: [value: { accountId: number; policy: TicketPolicy }] }>()
const { t } = useI18n()
const prefix = 'admin.accounts.ticket.'
const view = ref<TicketAccountView | null>(null)
const mode = ref('inherit')
const override = ref(false)
const options = ref(ticketDefaults())
const proxyId = ref<number | null>(null)
const proxies = ref<Proxy[]>([])
const saving = ref(false)
const harvesting = ref('')
const error = ref('')
const loadError = ref('')
const message = ref('')
const baseRevision = ref('')
const cleanForm = ref('')
const conflict = ref(false)
const formSnapshot = () => JSON.stringify({ mode: mode.value, override: override.value, options: options.value, proxyId: proxyId.value })
const dirty = computed(() => formSnapshot() !== cleanForm.value)
function syncForm(result: TicketAccountView) {
  mode.value = result.policy.enabled == null ? 'inherit' : result.policy.enabled ? 'on' : 'off'
  override.value = !!result.policy.options
  options.value = JSON.parse(JSON.stringify(result.policy.options || result.options))
  proxyId.value = result.policy.proxy_id ?? null
  baseRevision.value = result.revision
  cleanForm.value = formSnapshot()
  conflict.value = false
}

let generation = 0
let readSequence = 0
let poll: ReturnType<typeof setTimeout> | undefined
const stateLabel = (state: string) => t(prefix + 'states.' + state)
const formatTime = (value?: string) => value ? new Date(value).toLocaleString() : '—'
const enabledLabel = computed(() => view.value?.enabled ? t(prefix + 'on') : t(prefix + 'off'))

async function load(reset = false) {
  const current = generation
  const read = ++readSequence
  if (saving.value && !reset) return
  try {
    const result = await getTicketAccount(props.accountId)
    const availableProxies = reset ? await getProxies() : proxies.value
    if (current !== generation || read !== readSequence) return
    proxies.value = availableProxies
    view.value = result
    if (reset || !dirty.value) syncForm(result)
    else if (result.revision !== baseRevision.value) conflict.value = true
    loadError.value = ''
  } catch { if (current === generation && read === readSequence) loadError.value = t(prefix + 'loadFailed') }
  finally {
    if (current === generation) {
      clearTimeout(poll)
      poll = setTimeout(() => { void load() }, 5000)
    }
  }
}

async function save() {
  if (saving.value || conflict.value) return
  const current = generation
  const id = props.accountId
  readSequence++
  saving.value = true; error.value = ''; message.value = ''
  const policy: TicketPolicy = { enabled: mode.value === 'inherit' ? null : mode.value === 'on',
    options: override.value ? { ...options.value, models: [...options.value.models] } : null,
    proxy_id: proxyId.value || null }
  try {
    const result = await saveTicketPolicy(id, policy, baseRevision.value)
    if (current !== generation) return
    view.value = result; syncForm(result); emit('saved', { accountId: id, policy: result.policy }); message.value = t(prefix + 'saved')
  } catch (e: any) { if (current === generation) { error.value = e.response?.data?.message || e.message || t(prefix + 'saveFailed'); if (e.status === 409 || e.response?.status === 409) conflict.value = true } }
  finally { if (current === generation) { saving.value = false; clearTimeout(poll); poll = setTimeout(() => { void load() }, 5000) } }
}

async function harvest(model: string) {
  if (harvesting.value) return
  const current = generation
  harvesting.value = model; error.value = ''; message.value = ''
  try {
    const result = await harvestTicket(props.accountId, model)
    if (current !== generation) return
    message.value = t(prefix + 'triggers.' + result.state)
    await load()
  } catch (e: any) { if (current === generation) error.value = e.response?.data?.message || e.message }
  finally { if (current === generation) harvesting.value = '' }
}

watch(() => props.accountId, () => {
  generation++; clearTimeout(poll); view.value = null; conflict.value = false; cleanForm.value = ''; baseRevision.value = ''; saving.value = false; harvesting.value = ''; message.value = ''; error.value = ''
  void load(true)
}, { immediate: true })
onUnmounted(() => { generation++; clearTimeout(poll) })
</script>

<template>
  <section class="space-y-3 border-t border-gray-200 pt-4 dark:border-dark-600">
    <h3 class="font-semibold">{{ t(prefix + 'title') }}</h3>
    <p v-if="error || loadError" role="alert" class="text-sm text-red-600">{{ error || loadError }}</p>
    <button v-if="!view" type="button" class="text-sm underline" @click="load(true)">{{ t(prefix + 'refresh') }}</button>
    <template v-if="view">
      <p v-if="conflict" role="alert" class="text-sm text-amber-600">{{ t(prefix + 'conflict') }} <button type="button" class="underline" :disabled="saving" @click="load(true)">{{ t(prefix + 'reload') }}</button></p>
      <p class="input-hint">{{ t(prefix + 'effective', { state: enabledLabel }) }} <span v-if="!view.global_enabled">{{ t(prefix + 'masterOff') }}</span></p>
      <fieldset :disabled="saving || !view.eligible" class="space-y-3">
        <label class="block"><span class="input-label">{{ t(prefix + 'participation') }}</span>
          <select v-model="mode" class="input"><option value="inherit">{{ t(prefix + 'inherit') }}</option><option value="on">{{ t(prefix + 'on') }}</option><option value="off">{{ t(prefix + 'off') }}</option></select>
        </label>
        <label class="block"><span class="input-label">{{ t(prefix + 'proxyId') }}</span><select v-model="proxyId" class="input"><option :value="null">{{ t(prefix + 'inherit') }}</option><option v-for="proxy in proxies.filter(p => p.status === 'active' || p.id === proxyId)" :key="proxy.id" :value="proxy.id">{{ proxy.name }} (#{{ proxy.id }})</option></select></label>
        <label class="flex items-center gap-2"><input v-model="override" type="checkbox" />{{ t(prefix + 'override') }}</label>
        <CodexTicketOptionsForm v-if="override" v-model="options" />
        <details v-else><summary class="cursor-pointer text-sm">{{ t(prefix + 'inheritedOptions') }}</summary><CodexTicketOptionsForm :model-value="view.options" disabled /></details>
      </fieldset>
      <button type="button" class="btn btn-secondary" :disabled="saving || conflict || !view.eligible" @click="save">{{ t(prefix + 'saveAccount') }}</button>
      <p class="input-hint">{{ t(prefix + 'accountSaveHint') }}</p>
      <div v-for="item in view.progress" :key="item.model" class="space-y-1 rounded-lg bg-gray-50 p-3 text-sm dark:bg-dark-800">
        <div class="flex items-center justify-between gap-2"><strong>{{ item.model }}</strong><span>{{ stateLabel(item.state) }}</span></div>
        <p v-if="item.reason">{{ t(prefix + 'reason') }}{{ t(prefix + 'reasons.' + item.reason) }}<span v-if="item.http_status"> · HTTP {{ item.http_status }}</span><span v-if="item.ticket_length"> · {{ item.ticket_length }}</span></p>
        <p>{{ t(prefix + 'lastAttempt') }}{{ formatTime(item.last_attempt) }}</p>
        <p>{{ t(prefix + 'lastSuccess') }}{{ formatTime(item.last_success) }}</p>
        <p>{{ t(prefix + 'nextAttempt') }}{{ formatTime(item.next_attempt) }}</p>
        <p>{{ t(prefix + 'attempts', { count: item.attempts, failures: item.consecutive_failures }) }}</p>
        <p v-for="ticket in view.tickets.filter(ticket => ticket.model === item.model)" :key="ticket.model">{{ ticket.ready ? t(prefix + 'expires', { time: formatTime(ticket.expires_at) }) : ticket.blocked ? t(prefix + 'block') : t(prefix + 'allow') }}</p>
        <button type="button" class="btn btn-secondary" :disabled="!!harvesting || !view.enabled || !view.proxy_configured || item.state === 'harvesting' || item.state === 'inactive'" @click="harvest(item.model)">{{ t(prefix + 'harvest') }}</button>
      </div>
      <p class="input-hint">{{ t(prefix + 'runtimeHint') }}</p>
      <button type="button" class="text-sm underline" @click="load()">{{ t(prefix + 'refresh') }}</button>
    </template>
    <p v-if="message" role="status" class="text-sm text-emerald-600">{{ message }}</p>
  </section>
</template>
