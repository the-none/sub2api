<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { ticketDefaults, type TicketOptions } from '@/api/admin/codexTickets'

const props = defineProps<{ modelValue: TicketOptions; disabled?: boolean }>()
const emit = defineEmits<{ 'update:modelValue': [value: TicketOptions] }>()
const { t } = useI18n()
const prefix = 'admin.accounts.ticket.'
function update(key: keyof TicketOptions, value: unknown) { emit('update:modelValue', { ...props.modelValue, [key]: value }) }
const fields = [
  { key: 'retry_seconds', min: 1, max: 3600 }, { key: 'timeout_seconds', min: 1, max: 120 },
  { key: 'ttl_seconds', min: 60, max: 86400 }, { key: 'refresh_before_seconds', min: 0, max: 86399 },
  { key: 'max_backoff_seconds', min: 1, max: 86400 }
] as const
const preview = computed(() => JSON.stringify({ model: props.modelValue.models[0] || '', store: false, stream: true,
  instructions: props.modelValue.instructions, input: [{ role: 'user', content: [{ type: 'input_text', text: props.modelValue.user_prompt }] }] }, null, 2))
</script>

<template>
  <fieldset :disabled="disabled" class="space-y-3">
    <label class="block"><span class="input-label">{{ t(prefix + 'models') }}</span>
      <textarea class="input" rows="2" :value="modelValue.models.join('\n')"
        @change="update('models', ($event.target as HTMLTextAreaElement).value.split(/[\n,]+/).map(s => s.trim()).filter(Boolean))" />
    </label>
    <p class="input-hint">{{ t(prefix + 'modelsHint') }}</p>
    <div class="grid gap-3 sm:grid-cols-2">
      <label v-for="field in fields" :key="field.key" class="block">
        <span class="input-label">{{ t(prefix + field.key) }}</span>
        <input class="input" type="number" :min="field.min" :max="field.max" step="1" :value="modelValue[field.key]"
          @input="update(field.key, ($event.target as HTMLInputElement).valueAsNumber)" />
      </label>
    </div>
    <p class="input-hint">{{ t(prefix + 'timingHint') }}</p>
    <label class="block"><span class="input-label">{{ t(prefix + 'failClosed') }}</span>
      <select class="input" :value="String(modelValue.fail_closed)" @change="update('fail_closed', ($event.target as HTMLSelectElement).value === 'true')">
        <option value="true">{{ t(prefix + 'block') }}</option><option value="false">{{ t(prefix + 'allow') }}</option>
      </select>
    </label>
    <label class="block"><span class="input-label">{{ t(prefix + 'instructions') }}</span>
      <textarea class="input" rows="2" maxlength="8000" :value="modelValue.instructions" @input="update('instructions', ($event.target as HTMLTextAreaElement).value)" />
    </label>
    <label class="block"><span class="input-label">{{ t(prefix + 'userPrompt') }}</span>
      <textarea class="input" rows="2" maxlength="8000" :value="modelValue.user_prompt" @input="update('user_prompt', ($event.target as HTMLTextAreaElement).value)" />
    </label>
    <button type="button" class="btn btn-secondary" @click="emit('update:modelValue', { ...modelValue, instructions: ticketDefaults().instructions, user_prompt: ticketDefaults().user_prompt })">{{ t(prefix + 'resetPrompts') }}</button>
    <details><summary class="cursor-pointer text-sm">{{ t(prefix + 'preview') }}</summary><pre class="mt-2 overflow-auto whitespace-pre-wrap rounded bg-gray-50 p-3 text-xs dark:bg-dark-800">{{ preview }}</pre></details>
  </fieldset>
</template>
