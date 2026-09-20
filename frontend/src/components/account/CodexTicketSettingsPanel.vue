<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { configToOptions, getTicketConfig, optionsToConfig, saveTicketConfig, ticketDefaults, type TicketConfig } from '@/api/admin/codexTickets'
import CodexTicketOptionsForm from './CodexTicketOptionsForm.vue'

const { t } = useI18n()
const prefix = 'admin.accounts.ticket.'
const config = ref<TicketConfig | null>(null)
const options = ref(ticketDefaults())
const saving = ref(false)
const message = ref('')
const error = ref('')
const clearProxy = ref(false)
async function load() {
  try { config.value = await getTicketConfig(); options.value = configToOptions(config.value); error.value = '' }
  catch { error.value = t(prefix + 'loadFailed') }
}
async function save() {
  if (!config.value || saving.value) return
  saving.value = true; error.value = ''; message.value = ''
  try {
    config.value = await saveTicketConfig(optionsToConfig(config.value, options.value), clearProxy.value)
    options.value = configToOptions(config.value); clearProxy.value = false; message.value = t(prefix + 'saved')
  } catch (e: any) { error.value = e.response?.data?.message || e.message || t(prefix + 'saveFailed') }
  finally { saving.value = false }
}
onMounted(load)
</script>

<template>
  <section class="space-y-4 rounded-lg border border-gray-200 p-4 dark:border-dark-600">
    <h3 class="font-semibold">{{ t(prefix + 'title') }}</h3>
    <p class="input-hint">{{ t(prefix + 'globalHint') }}</p>
    <p v-if="error" role="alert" class="text-sm text-red-600">{{ error }} <button v-if="!config" type="button" class="underline" @click="load">{{ t(prefix + 'refresh') }}</button></p>
    <template v-if="config">
      <fieldset :disabled="saving" class="space-y-4">
        <label class="flex items-center gap-2"><input v-model="config.enabled" type="checkbox" />{{ t(prefix + 'master') }}</label>
        <label class="flex items-center gap-2"><input type="checkbox" :checked="config.default_account_enabled !== false" @change="config.default_account_enabled = ($event.target as HTMLInputElement).checked" />{{ t(prefix + 'defaultEnabled') }}</label>
        <label class="block"><span class="input-label">{{ t(prefix + 'proxy') }}</span><input v-model="config.harvest_proxy_url" class="input font-mono" autocomplete="off" :disabled="clearProxy" /></label>
        <label class="flex items-center gap-2 text-sm"><input v-model="clearProxy" type="checkbox" />{{ t(prefix + 'clearProxy') }}</label>
        <label class="block"><span class="input-label">{{ t(prefix + 'concurrency') }}</span><input v-model.number="config.max_concurrency" class="input" type="number" min="1" max="32" /></label>
        <CodexTicketOptionsForm v-model="options" />
        <details><summary class="cursor-pointer text-sm">{{ t(prefix + 'advanced') }}</summary>
          <label class="mt-3 block"><span class="input-label">{{ t(prefix + 'targetLength') }}</span><input v-model.number="config.target_length" class="input" type="number" min="16" max="8192" /></label>
        </details>
      </fieldset>
      <button type="button" class="btn btn-primary" :disabled="saving" @click="save">{{ t(prefix + 'saveGlobal') }}</button>
      <p v-if="message" role="status" class="text-sm text-emerald-600">{{ message }}</p>
    </template>
  </section>
</template>
