<template>
  <AppLayout>
    <div class="space-y-6">
      <div class="flex flex-col gap-4 lg:flex-row lg:items-center lg:justify-between">
        <div>
          <h1 class="text-2xl font-semibold text-gray-900 dark:text-white">{{ text.title }}</h1>
          <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">{{ text.subtitle }}</p>
        </div>
        <button type="button" class="btn btn-secondary" :disabled="loading" @click="loadAll">
          {{ loading ? text.loading : text.refresh }}
        </button>
      </div>

      <div v-if="loading" class="flex items-center justify-center py-16">
        <div class="h-8 w-8 animate-spin rounded-full border-b-2 border-primary-600"></div>
      </div>

      <template v-else>
        <section class="card">
          <div class="border-b border-gray-100 px-6 py-4 dark:border-dark-700">
            <h2 class="text-lg font-semibold text-gray-900 dark:text-white">{{ text.realAccounts }}</h2>
          </div>

          <div class="grid gap-6 p-6 xl:grid-cols-[minmax(0,420px)_minmax(0,1fr)]">
            <form class="space-y-4" @submit.prevent="saveRealAccount">
              <div class="grid grid-cols-1 gap-3 sm:grid-cols-2">
                <label class="block">
                  <span class="input-label">{{ text.name }}</span>
                  <input v-model.trim="realAccountForm.name" class="input" required />
                </label>
                <label class="block">
                  <span class="input-label">{{ text.platform }}</span>
                  <select v-model="realAccountForm.platform" class="input">
                    <option value="anthropic">Claude</option>
                    <option value="openai">OpenAI</option>
                  </select>
                </label>
              </div>
              <label class="block">
                <span class="input-label">{{ text.identifier }}</span>
                <input v-model.trim="realAccountForm.identifier" class="input" />
              </label>
              <label class="block">
                <span class="input-label">{{ text.notes }}</span>
                <textarea v-model.trim="realAccountForm.notes" class="input min-h-[76px]"></textarea>
              </label>
              <div class="flex flex-wrap gap-2">
                <button type="submit" class="btn btn-primary" :disabled="saving.realAccount">
                  {{ realAccountForm.id ? text.update : text.create }}
                </button>
                <button type="button" class="btn btn-secondary" @click="resetRealAccountForm">{{ text.reset }}</button>
              </div>
            </form>

            <div class="space-y-4">
              <div class="rounded-lg border border-gray-100 dark:border-dark-700">
                <div class="max-h-[340px] overflow-auto">
                  <table class="min-w-full divide-y divide-gray-100 text-sm dark:divide-dark-700">
                    <thead class="bg-gray-50 text-left text-xs font-semibold uppercase text-gray-500 dark:bg-dark-700/50 dark:text-gray-400">
                      <tr>
                        <th class="px-4 py-3">{{ text.name }}</th>
                        <th class="px-4 py-3">{{ text.platform }}</th>
                        <th class="px-4 py-3">{{ text.accounts }}</th>
                        <th class="px-4 py-3">{{ text.snapshot }}</th>
                        <th class="px-4 py-3 text-right">{{ text.actions }}</th>
                      </tr>
                    </thead>
                    <tbody class="divide-y divide-gray-100 dark:divide-dark-700">
                      <tr v-for="item in realAccounts" :key="item.id">
                        <td class="px-4 py-3">
                          <div class="font-medium text-gray-900 dark:text-white">{{ item.name }}</div>
                          <div v-if="item.identifier" class="mt-0.5 max-w-[180px] truncate text-xs text-gray-500 dark:text-gray-400">{{ item.identifier }}</div>
                        </td>
                        <td class="px-4 py-3 text-gray-600 dark:text-gray-300">{{ platformLabel(item.platform) }}</td>
                        <td class="px-4 py-3">
                          <div class="flex max-w-[260px] flex-wrap gap-1">
                            <span
                              v-for="account in item.accounts || []"
                              :key="account.id"
                              class="inline-flex max-w-[150px] items-center gap-1 rounded-full bg-gray-100 px-2 py-1 text-xs text-gray-700 dark:bg-dark-700 dark:text-gray-300"
                            >
                              <span class="truncate">{{ account.name }}</span>
                              <span v-if="account.quota_dimension === 'spark'" class="text-[10px] font-semibold text-amber-600 dark:text-amber-400">Spark</span>
                              <button type="button" class="text-gray-400 hover:text-red-600" @click="detachAccount(item.id, account.id)">x</button>
                            </span>
                            <span v-if="!item.accounts?.length" class="text-xs text-gray-400">{{ text.empty }}</span>
                          </div>
                        </td>
                        <td class="px-4 py-3 text-xs text-gray-600 dark:text-gray-300">
                          <div v-if="snapshotLoaded[item.id]" class="space-y-1">
                            <div v-for="entry in snapshotEntries(item)" :key="`${entry.dimension}-${entry.window}`">
                              {{ quotaDimensionLabel(entry.dimension) }} · {{ entry.window }}: {{ entry.used }}% / {{ entry.remaining }}%
                            </div>
                            <div v-if="snapshotEntries(item).length === 0" class="text-gray-400">{{ text.empty }}</div>
                          </div>
                          <button v-else type="button" class="text-primary-600 hover:text-primary-700" @click="loadSnapshot(item)">
                            {{ text.loadSnapshot }}
                          </button>
                        </td>
                        <td class="px-4 py-3 text-right">
                          <div class="flex justify-end gap-2">
                            <button type="button" class="text-primary-600 hover:text-primary-700" @click="editRealAccount(item)">{{ text.edit }}</button>
                            <button type="button" class="text-red-600 hover:text-red-700" @click="deleteRealAccount(item.id)">{{ text.delete }}</button>
                          </div>
                        </td>
                      </tr>
                      <tr v-if="realAccounts.length === 0">
                        <td colspan="5" class="px-4 py-8 text-center text-gray-500 dark:text-gray-400">{{ text.empty }}</td>
                      </tr>
                    </tbody>
                  </table>
                </div>
              </div>

              <div class="rounded-lg bg-gray-50 p-4 dark:bg-dark-700/40">
                <div class="grid gap-3 lg:grid-cols-[220px_minmax(0,1fr)_auto]">
                  <select v-model.number="attachRealAccountID" class="input">
                    <option :value="0">{{ text.selectRealAccount }}</option>
                    <option v-for="item in realAccounts" :key="item.id" :value="item.id">{{ item.name }}</option>
                  </select>
                  <div class="max-h-[124px] overflow-y-auto rounded-lg border border-gray-200 bg-white p-2 dark:border-dark-600 dark:bg-dark-800">
                    <label
                      v-for="account in attachableAccounts"
                      :key="account.id"
                      class="flex items-center gap-2 rounded px-2 py-1.5 text-sm text-gray-700 hover:bg-gray-50 dark:text-gray-300 dark:hover:bg-dark-700"
                    >
                      <input v-model="attachAccountIDs" type="checkbox" :value="account.id" class="rounded border-gray-300 text-primary-600" />
                      <span class="min-w-0 flex-1 truncate">{{ account.name }}</span>
                      <span class="text-xs text-gray-400">#{{ account.id }}</span>
                    </label>
                    <div v-if="attachableAccounts.length === 0" class="px-2 py-4 text-sm text-gray-500">{{ text.empty }}</div>
                  </div>
                  <button type="button" class="btn btn-primary h-10" :disabled="!canAttach" @click="attachAccounts">
                    {{ text.merge }}
                  </button>
                </div>
              </div>
            </div>
          </div>
        </section>

        <section class="grid gap-6 xl:grid-cols-2">
          <div class="card">
            <div class="border-b border-gray-100 px-6 py-4 dark:border-dark-700">
              <h2 class="text-lg font-semibold text-gray-900 dark:text-white">{{ text.rules }}</h2>
            </div>
            <form class="grid gap-3 p-6 md:grid-cols-2" @submit.prevent="saveRule">
              <label class="block md:col-span-2">
                <span class="input-label">{{ text.name }}</span>
                <input v-model.trim="ruleForm.name" class="input" />
              </label>
              <label class="block">
                <span class="input-label">{{ text.realAccount }}</span>
                <select v-model.number="ruleForm.realAccountID" class="input" required>
                  <option :value="0">{{ text.selectRealAccount }}</option>
                  <option v-for="item in realAccounts" :key="item.id" :value="item.id">{{ item.name }} · {{ platformLabel(item.platform) }}</option>
                </select>
              </label>
              <label class="block">
                <span class="input-label">{{ text.window }}</span>
                <select v-model="ruleForm.window" class="input">
                  <option value="5h">5h</option>
                  <option value="7d">7d</option>
                </select>
              </label>
              <label class="block">
                <span class="input-label">{{ text.quotaDimension }}</span>
                <select v-model="ruleForm.quotaDimension" class="input">
                  <option value="global">{{ text.globalQuota }}</option>
                  <option v-if="selectedRuleRealAccount?.platform === 'openai'" value="spark">{{ text.sparkQuota }}</option>
                </select>
              </label>
              <label class="block">
                <span class="input-label">{{ text.metric }}</span>
                <select v-model="ruleForm.metric" class="input">
                  <option value="used_percent">{{ text.usedPercent }}</option>
                  <option value="remaining_percent">{{ text.remainingPercent }}</option>
                </select>
              </label>
              <label class="block">
                <span class="input-label">{{ text.operator }}</span>
                <select v-model="ruleForm.operator" class="input">
                  <option value=">=">>=</option>
                  <option value="<=">&lt;=</option>
                </select>
              </label>
              <label class="block">
                <span class="input-label">{{ text.threshold }}</span>
                <input v-model.number="ruleForm.threshold" class="input" type="number" min="0" max="100" step="0.1" />
              </label>
              <label class="block">
                <span class="input-label">{{ text.minResetAfter }}</span>
                <input v-model="ruleForm.minResetAfterHours" class="input" type="number" min="0" step="0.1" />
              </label>
              <label class="block">
                <span class="input-label">{{ text.stepPercent }}</span>
                <input v-model="ruleForm.stepPercent" class="input" type="number" min="0" max="100" step="0.1" />
              </label>
              <label class="block">
                <span class="input-label">{{ text.cooldown }}</span>
                <input v-model.number="ruleForm.cooldownMinutes" class="input" type="number" min="0" step="1" />
              </label>
              <label class="flex items-center gap-2 pt-6 text-sm text-gray-700 dark:text-gray-300">
                <input v-model="ruleForm.enabled" type="checkbox" class="rounded border-gray-300 text-primary-600" />
                {{ text.enabled }}
              </label>
              <p class="text-xs leading-5 text-gray-500 dark:text-gray-400 md:col-span-2">
                {{ text.resolveNotificationHint }}
              </p>
              <div class="flex gap-2 md:col-span-2">
                <button type="submit" class="btn btn-primary" :disabled="saving.rule || !ruleForm.realAccountID">{{ ruleForm.id ? text.update : text.create }}</button>
                <button type="button" class="btn btn-secondary" @click="resetRuleForm">{{ text.reset }}</button>
              </div>
            </form>
            <div class="border-t border-gray-100 dark:border-dark-700">
              <div v-for="rule in rules" :key="rule.id" class="flex items-center justify-between gap-3 px-6 py-3 text-sm">
                <div class="min-w-0">
                  <div class="break-words font-medium text-gray-900 dark:text-white">{{ rule.name }}</div>
                  <div class="mt-2 flex flex-wrap gap-1.5 text-xs text-gray-500 dark:text-gray-400">
                    <span
                      v-for="detail in ruleDetailItems(rule)"
                      :key="detail"
                      class="inline-flex max-w-full items-center rounded bg-gray-100 px-2 py-0.5 text-gray-600 dark:bg-dark-700 dark:text-gray-300"
                    >
                      {{ detail }}
                    </span>
                  </div>
                </div>
                <div class="flex flex-shrink-0 gap-2">
                  <button type="button" class="text-primary-600 hover:text-primary-700" @click="editRule(rule)">{{ text.edit }}</button>
                  <button type="button" class="text-red-600 hover:text-red-700" @click="deleteRule(rule.id)">{{ text.delete }}</button>
                </div>
              </div>
              <div v-if="rules.length === 0" class="px-6 py-8 text-center text-sm text-gray-500">{{ text.empty }}</div>
            </div>
          </div>

          <div class="space-y-6">
            <div class="card">
              <div class="border-b border-gray-100 px-6 py-4 dark:border-dark-700">
                <h2 class="text-lg font-semibold text-gray-900 dark:text-white">{{ text.webhooks }}</h2>
              </div>
              <form class="grid gap-3 p-6 md:grid-cols-[1fr_150px_120px]" @submit.prevent="saveWebhook">
                <label class="block">
                  <span class="input-label">{{ text.name }}</span>
                  <input v-model.trim="webhookForm.name" class="input" required />
                </label>
                <label class="block">
                  <span class="input-label">{{ text.channelType }}</span>
                  <select v-model="webhookForm.type" class="input">
                    <option value="json_post">JSON POST</option>
                    <option value="telegram">Telegram</option>
                  </select>
                </label>
                <label class="block">
                  <span class="input-label">{{ text.retry }}</span>
                  <input v-model.number="webhookForm.retryCount" class="input" type="number" min="0" max="10" />
                </label>
                <label v-if="webhookForm.type === 'json_post'" class="block md:col-span-3">
                  <span class="input-label">URL</span>
                  <input v-model.trim="webhookForm.url" class="input" required />
                </label>
                <template v-else>
                  <label class="block md:col-span-3">
                    <span class="input-label">{{ text.telegramBotToken }}</span>
                    <input v-model.trim="webhookForm.telegramBotToken" class="input" type="password" autocomplete="off" required />
                  </label>
                  <label class="block">
                    <span class="input-label">{{ text.telegramChatID }}</span>
                    <input v-model.trim="webhookForm.telegramChatID" class="input" required />
                  </label>
                  <label class="block">
                    <span class="input-label">{{ text.telegramTopicID }}</span>
                    <input v-model.trim="webhookForm.telegramMessageThreadID" class="input" inputmode="numeric" />
                  </label>
                  <label class="block">
                    <span class="input-label">{{ text.notificationLanguage }}</span>
                    <select v-model="webhookForm.telegramLanguage" class="input">
                      <option value="zh">中文</option>
                      <option value="en">English</option>
                    </select>
                  </label>
                  <label class="block">
                    <span class="input-label">{{ text.notificationTimezone }}</span>
                    <input v-model.trim="webhookForm.telegramTimezone" class="input" placeholder="Asia/Shanghai" />
                  </label>
                  <label class="flex items-center gap-2 pt-6 text-sm text-gray-700 dark:text-gray-300">
                    <input v-model="webhookForm.telegramDisableNotification" type="checkbox" class="rounded border-gray-300 text-primary-600" />
                    {{ text.disableNotification }}
                  </label>
                </template>
                <label class="flex items-center gap-2 text-sm text-gray-700 dark:text-gray-300">
                  <input v-model="webhookForm.enabled" type="checkbox" class="rounded border-gray-300 text-primary-600" />
                  {{ text.enabled }}
                </label>
                <div class="flex justify-end gap-2 md:col-span-2">
                  <button type="button" class="btn btn-secondary" :disabled="saving.webhookTest" @click="testWebhook">
                    {{ saving.webhookTest ? text.testing : text.testSend }}
                  </button>
                  <button type="button" class="btn btn-secondary" @click="resetWebhookForm">{{ text.reset }}</button>
                  <button type="submit" class="btn btn-primary" :disabled="saving.webhook">{{ webhookForm.id ? text.update : text.create }}</button>
                </div>
              </form>
              <div class="border-t border-gray-100 dark:border-dark-700">
                <div v-for="webhook in webhooks" :key="webhook.id" class="flex items-center justify-between gap-3 px-6 py-3 text-sm">
                  <div class="min-w-0">
                    <div class="truncate font-medium text-gray-900 dark:text-white">{{ webhook.name }}</div>
                    <div class="mt-0.5 truncate text-xs text-gray-500 dark:text-gray-400">
                      {{ webhookTypeLabel(webhook.type) }} · {{ webhookSummary(webhook) }}
                    </div>
                  </div>
                  <div class="flex flex-shrink-0 gap-2">
                    <button type="button" class="text-primary-600 hover:text-primary-700" @click="editWebhook(webhook)">{{ text.edit }}</button>
                    <button type="button" class="text-red-600 hover:text-red-700" @click="deleteWebhook(webhook.id)">{{ text.delete }}</button>
                  </div>
                </div>
                <div v-if="webhooks.length === 0" class="px-6 py-8 text-center text-sm text-gray-500">{{ text.empty }}</div>
              </div>
            </div>

            <div class="card">
              <div class="border-b border-gray-100 px-6 py-4 dark:border-dark-700">
                <h2 class="text-lg font-semibold text-gray-900 dark:text-white">{{ text.bindings }}</h2>
              </div>
              <form class="grid gap-3 p-6 md:grid-cols-[1fr_1fr_auto]" @submit.prevent="saveBinding">
                <select v-model.number="bindingForm.realAccountID" class="input">
                  <option :value="0">{{ text.selectRealAccount }}</option>
                  <option v-for="item in realAccounts" :key="item.id" :value="item.id">{{ item.name }}</option>
                </select>
                <select v-model.number="bindingForm.webhookID" class="input">
                  <option :value="0">{{ text.selectWebhook }}</option>
                  <option v-for="webhook in webhooks" :key="webhook.id" :value="webhook.id">{{ webhook.name }}</option>
                </select>
                <button type="submit" class="btn btn-primary" :disabled="saving.binding || !bindingForm.realAccountID || !bindingForm.webhookID">
                  {{ bindingForm.id ? text.update : text.create }}
                </button>
                <label class="flex items-center gap-2 text-sm text-gray-700 dark:text-gray-300 md:col-span-3">
                  <input v-model="bindingForm.enabled" type="checkbox" class="rounded border-gray-300 text-primary-600" />
                  {{ text.enabled }}
                </label>
              </form>
              <div class="border-t border-gray-100 dark:border-dark-700">
                <div v-for="binding in bindings" :key="binding.id" class="flex items-center justify-between gap-3 px-6 py-3 text-sm">
                  <div class="min-w-0">
                    <div class="truncate font-medium text-gray-900 dark:text-white">
                      {{ binding.real_account?.name || realAccountName(binding.real_account_id) }} -> {{ binding.webhook?.name || webhookName(binding.webhook_id) }}
                    </div>
                    <div class="mt-0.5 text-xs text-gray-500 dark:text-gray-400">{{ binding.enabled ? text.enabled : text.disabled }}</div>
                  </div>
                  <div class="flex flex-shrink-0 gap-2">
                    <button type="button" class="text-primary-600 hover:text-primary-700" @click="editBinding(binding)">{{ text.edit }}</button>
                    <button type="button" class="text-red-600 hover:text-red-700" @click="deleteBinding(binding.id)">{{ text.delete }}</button>
                  </div>
                </div>
                <div v-if="bindings.length === 0" class="px-6 py-8 text-center text-sm text-gray-500">{{ text.empty }}</div>
              </div>
            </div>
          </div>
        </section>
      </template>
    </div>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import AppLayout from '@/components/layout/AppLayout.vue'
import accountsAPI from '@/api/admin/accounts'
import usageAlertAPI, {
  type RealAccount,
  type UsageAlertBinding,
  type UsageAlertMetric,
  type UsageAlertQuotaDimension,
  type UsageAlertPlatform,
  type UsageAlertRule,
  type UsageAlertSnapshot,
  type UsageAlertWebhook,
  type UsageAlertWebhookPayload,
  type UsageAlertWebhookType,
  type UsageAlertWindow
} from '@/api/admin/usageAlert'
import type { Account } from '@/types'
import { useAppStore } from '@/stores'

type EditablePlatform = Exclude<UsageAlertPlatform, 'all'>

const { locale } = useI18n()
const appStore = useAppStore()

const zhText = {
  title: '用量告警',
  subtitle: '真实账户、规则和通知渠道绑定',
  refresh: '刷新',
  loading: '加载中',
  realAccounts: '真实账户',
  realAccount: '真实账户',
  rules: '规则',
  webhooks: '通知渠道',
  bindings: '绑定',
  name: '名称',
  platform: '平台',
  identifier: '标识',
  notes: '备注',
  accounts: '账号',
  snapshot: '快照',
  quotaDimension: '额度维度',
  globalQuota: '全局额度',
  sparkQuota: 'Spark 额度',
  actions: '操作',
  create: '创建',
  update: '更新',
  reset: '重置',
  edit: '编辑',
  delete: '删除',
  merge: '合并',
  empty: '暂无数据',
  selectRealAccount: '选择真实账户',
  selectWebhook: '选择通知渠道',
  loadSnapshot: '加载快照',
  allPlatforms: '全部平台',
  window: '窗口',
  metric: '指标',
  operator: '条件',
  threshold: '阈值',
  minResetAfter: '重置至少还有小时',
  minResetAfterShort: '重置剩余',
  stepPercent: '步进百分比',
  stepPercentShort: '步进',
  cooldown: '冷却分钟',
  cooldownShort: '冷却',
  enabled: '启用',
  disabled: '停用',
  resolveNotificationHint: '规则恢复正常时会发送“用量告警已重置”通知。',
  usedPercent: '已用百分比',
  remainingPercent: '剩余百分比',
  retry: '重试',
  channelType: '类型',
  telegramBotToken: 'Bot Token',
  telegramChatID: 'Chat ID',
  telegramTopicID: 'Topic ID',
  notificationLanguage: '通知语言',
  notificationTimezone: '通知时区',
  disableNotification: '静默通知',
  testSend: '测试发送',
  testing: '测试中',
  testSent: '测试已发送',
  saved: '已保存',
  deleted: '已删除',
  merged: '已合并',
  failed: '操作失败',
  confirmDelete: '确认删除？'
}

const enText: typeof zhText = {
  title: 'Usage Alerts',
  subtitle: 'Real accounts, rules, and notification channel bindings',
  refresh: 'Refresh',
  loading: 'Loading',
  realAccounts: 'Real Accounts',
  realAccount: 'Real Account',
  rules: 'Rules',
  webhooks: 'Notification Channels',
  bindings: 'Bindings',
  name: 'Name',
  platform: 'Platform',
  identifier: 'Identifier',
  notes: 'Notes',
  accounts: 'Accounts',
  snapshot: 'Snapshot',
  quotaDimension: 'Quota dimension',
  globalQuota: 'Global quota',
  sparkQuota: 'Spark quota',
  actions: 'Actions',
  create: 'Create',
  update: 'Update',
  reset: 'Reset',
  edit: 'Edit',
  delete: 'Delete',
  merge: 'Merge',
  empty: 'No data',
  selectRealAccount: 'Select real account',
  selectWebhook: 'Select notification channel',
  loadSnapshot: 'Load snapshot',
  allPlatforms: 'All platforms',
  window: 'Window',
  metric: 'Metric',
  operator: 'Operator',
  threshold: 'Threshold',
  minResetAfter: 'Min reset hours',
  minResetAfterShort: 'Reset left',
  stepPercent: 'Step percent',
  stepPercentShort: 'Step',
  cooldown: 'Cooldown minutes',
  cooldownShort: 'Cooldown',
  enabled: 'Enabled',
  disabled: 'Disabled',
  resolveNotificationHint: 'A reset notification is sent when a rule returns to normal.',
  usedPercent: 'Used percent',
  remainingPercent: 'Remaining percent',
  retry: 'Retry',
  channelType: 'Type',
  telegramBotToken: 'Bot Token',
  telegramChatID: 'Chat ID',
  telegramTopicID: 'Topic ID',
  notificationLanguage: 'Language',
  notificationTimezone: 'Timezone',
  disableNotification: 'Silent notification',
  testSend: 'Test',
  testing: 'Testing',
  testSent: 'Test sent',
  saved: 'Saved',
  deleted: 'Deleted',
  merged: 'Merged',
  failed: 'Operation failed',
  confirmDelete: 'Delete this item?'
}

const text = computed(() => (locale.value.startsWith('zh') ? zhText : enText))

const loading = ref(true)
const realAccounts = ref<RealAccount[]>([])
const rules = ref<UsageAlertRule[]>([])
const webhooks = ref<UsageAlertWebhook[]>([])
const bindings = ref<UsageAlertBinding[]>([])
const accounts = ref<Account[]>([])
const snapshots = reactive<Record<string, UsageAlertSnapshot | null>>({})
const snapshotLoaded = reactive<Record<number, boolean>>({})
const attachRealAccountID = ref(0)
const attachAccountIDs = ref<number[]>([])

const saving = reactive({
  realAccount: false,
  rule: false,
  webhook: false,
  webhookTest: false,
  binding: false
})

const realAccountForm = reactive({
  id: null as number | null,
  name: '',
  platform: 'anthropic' as EditablePlatform,
  identifier: '',
  notes: ''
})

const ruleForm = reactive({
  id: null as number | null,
  name: '',
  realAccountID: 0,
  quotaDimension: 'global' as UsageAlertQuotaDimension,
  window: '7d' as UsageAlertWindow,
  metric: 'remaining_percent' as UsageAlertMetric,
  operator: '<=' as '<=' | '>=',
  threshold: 20,
  minResetAfterHours: '',
  stepPercent: '',
  cooldownMinutes: 240,
  enabled: true
})

function defaultWebhookLanguage() {
  return locale.value.startsWith('zh') ? 'zh' : 'en'
}

const webhookForm = reactive({
  id: null as number | null,
  name: '',
  type: 'json_post' as UsageAlertWebhookType,
  url: '',
  telegramBotToken: '',
  telegramChatID: '',
  telegramMessageThreadID: '',
  telegramLanguage: defaultWebhookLanguage(),
  telegramTimezone: 'Asia/Shanghai',
  telegramDisableNotification: false,
  retryCount: 2,
  enabled: true
})

const bindingForm = reactive({
  id: null as number | null,
  realAccountID: 0,
  webhookID: 0,
  enabled: true
})

const selectedAttachRealAccount = computed(() => realAccounts.value.find((item) => item.id === attachRealAccountID.value) || null)
const selectedRuleRealAccount = computed(() => realAccounts.value.find((item) => item.id === ruleForm.realAccountID) || null)
watch(() => selectedRuleRealAccount.value?.platform, (platform) => {
  if (platform !== 'openai') ruleForm.quotaDimension = 'global'
})
const attachableAccounts = computed(() => {
  const realAccount = selectedAttachRealAccount.value
  return accounts.value.filter((account) => {
    if (!['openai', 'anthropic'].includes(account.platform)) return false
    if (!['oauth', 'setup-token'].includes(account.type)) return false
    return !realAccount || account.platform === realAccount.platform
  })
})
const canAttach = computed(() => attachRealAccountID.value > 0 && attachAccountIDs.value.length > 0)

onMounted(loadAll)

async function loadAll() {
  loading.value = true
  try {
    const [realAccountRows, ruleRows, webhookRows, bindingRows, openaiAccounts, claudeAccounts] = await Promise.all([
      usageAlertAPI.listRealAccounts(),
      usageAlertAPI.listRules(),
      usageAlertAPI.listWebhooks(),
      usageAlertAPI.listBindings(),
      accountsAPI.list(1, 1000, { platform: 'openai' }),
      accountsAPI.list(1, 1000, { platform: 'anthropic' })
    ])
    realAccounts.value = realAccountRows
    rules.value = ruleRows
    webhooks.value = webhookRows
    bindings.value = bindingRows
    accounts.value = [...openaiAccounts.items, ...claudeAccounts.items]
  } catch (error) {
    appStore.showError(errorMessage(error))
  } finally {
    loading.value = false
  }
}

async function loadSnapshot(item: RealAccount) {
  try {
    const dimensions = realAccountQuotaDimensions(item)
    const rows = await Promise.all(dimensions.map((dimension) => usageAlertAPI.getSnapshot(item.id, dimension)))
    dimensions.forEach((dimension, index) => {
      snapshots[snapshotKey(item.id, dimension)] = rows[index]
    })
    snapshotLoaded[item.id] = true
  } catch (error) {
    appStore.showError(errorMessage(error))
  }
}

async function saveRealAccount() {
  saving.realAccount = true
  try {
    const payload = {
      name: realAccountForm.name,
      platform: realAccountForm.platform,
      identifier: nullable(realAccountForm.identifier),
      notes: nullable(realAccountForm.notes)
    }
    if (realAccountForm.id) {
      await usageAlertAPI.updateRealAccount(realAccountForm.id, payload)
    } else {
      await usageAlertAPI.createRealAccount(payload)
    }
    appStore.showSuccess(text.value.saved)
    resetRealAccountForm()
    await loadAll()
  } catch (error) {
    appStore.showError(errorMessage(error))
  } finally {
    saving.realAccount = false
  }
}

function editRealAccount(item: RealAccount) {
  realAccountForm.id = item.id
  realAccountForm.name = item.name
  realAccountForm.platform = item.platform
  realAccountForm.identifier = item.identifier || ''
  realAccountForm.notes = item.notes || ''
}

function resetRealAccountForm() {
  realAccountForm.id = null
  realAccountForm.name = ''
  realAccountForm.platform = 'anthropic'
  realAccountForm.identifier = ''
  realAccountForm.notes = ''
}

async function deleteRealAccount(id: number) {
  if (!window.confirm(text.value.confirmDelete)) return
  try {
    await usageAlertAPI.deleteRealAccount(id)
    appStore.showSuccess(text.value.deleted)
    await loadAll()
  } catch (error) {
    appStore.showError(errorMessage(error))
  }
}

async function attachAccounts() {
  if (!canAttach.value) return
  try {
    await usageAlertAPI.attachAccounts(attachRealAccountID.value, attachAccountIDs.value.map(Number))
    appStore.showSuccess(text.value.merged)
    attachAccountIDs.value = []
    await loadAll()
  } catch (error) {
    appStore.showError(errorMessage(error))
  }
}

async function detachAccount(realAccountID: number, accountID: number) {
  try {
    await usageAlertAPI.detachAccount(realAccountID, accountID)
    appStore.showSuccess(text.value.saved)
    await loadAll()
  } catch (error) {
    appStore.showError(errorMessage(error))
  }
}

async function saveRule() {
  saving.rule = true
  try {
    const minReset = ruleForm.minResetAfterHours === '' ? null : Number(ruleForm.minResetAfterHours)
    const stepPercent = ruleForm.stepPercent === '' ? null : Number(ruleForm.stepPercent)
    const payload = {
      name: ruleForm.name,
      real_account_id: ruleForm.realAccountID,
      platform: (selectedRuleRealAccount.value?.platform || 'all') as UsageAlertPlatform,
      quota_dimension: ruleForm.quotaDimension,
      window: ruleForm.window,
      metric: ruleForm.metric,
      operator: ruleForm.operator,
      threshold: Number(ruleForm.threshold),
      min_reset_after_hours: minReset,
      step_percent: stepPercent,
      cooldown_minutes: Number(ruleForm.cooldownMinutes),
      enabled: ruleForm.enabled
    }
    if (ruleForm.id) {
      await usageAlertAPI.updateRule(ruleForm.id, payload)
    } else {
      await usageAlertAPI.createRule(payload)
    }
    appStore.showSuccess(text.value.saved)
    resetRuleForm()
    await loadAll()
  } catch (error) {
    appStore.showError(errorMessage(error))
  } finally {
    saving.rule = false
  }
}

function editRule(rule: UsageAlertRule) {
  ruleForm.id = rule.id
  ruleForm.name = rule.name
  ruleForm.realAccountID = rule.real_account_id || 0
  ruleForm.quotaDimension = rule.quota_dimension || 'global'
  ruleForm.window = rule.window
  ruleForm.metric = rule.metric
  ruleForm.operator = rule.operator
  ruleForm.threshold = rule.threshold
  ruleForm.minResetAfterHours = rule.min_reset_after_hours == null ? '' : String(rule.min_reset_after_hours)
  ruleForm.stepPercent = rule.step_percent == null ? '' : String(rule.step_percent)
  ruleForm.cooldownMinutes = rule.cooldown_minutes
  ruleForm.enabled = rule.enabled
}

function resetRuleForm() {
  ruleForm.id = null
  ruleForm.name = ''
  ruleForm.realAccountID = 0
  ruleForm.quotaDimension = 'global'
  ruleForm.window = '7d'
  ruleForm.metric = 'remaining_percent'
  ruleForm.operator = '<='
  ruleForm.threshold = 20
  ruleForm.minResetAfterHours = ''
  ruleForm.stepPercent = ''
  ruleForm.cooldownMinutes = 240
  ruleForm.enabled = true
}

async function deleteRule(id: number) {
  if (!window.confirm(text.value.confirmDelete)) return
  try {
    await usageAlertAPI.deleteRule(id)
    appStore.showSuccess(text.value.deleted)
    await loadAll()
  } catch (error) {
    appStore.showError(errorMessage(error))
  }
}

async function saveWebhook() {
  saving.webhook = true
  try {
    const payload = buildWebhookPayload()
    if (webhookForm.id) {
      await usageAlertAPI.updateWebhook(webhookForm.id, payload)
    } else {
      await usageAlertAPI.createWebhook(payload)
    }
    appStore.showSuccess(text.value.saved)
    resetWebhookForm()
    await loadAll()
  } catch (error) {
    appStore.showError(errorMessage(error))
  } finally {
    saving.webhook = false
  }
}

async function testWebhook() {
  saving.webhookTest = true
  try {
    await usageAlertAPI.testWebhook(buildWebhookPayload(true))
    appStore.showSuccess(text.value.testSent)
  } catch (error) {
    appStore.showError(errorMessage(error))
  } finally {
    saving.webhookTest = false
  }
}

function buildWebhookPayload(forceEnabled = false): UsageAlertWebhookPayload {
  const payload: UsageAlertWebhookPayload = {
    name: webhookForm.name,
    type: webhookForm.type,
    url: webhookForm.type === 'json_post' ? webhookForm.url : '',
    config: {},
    retry_count: Number(webhookForm.retryCount),
    enabled: forceEnabled ? true : webhookForm.enabled
  }
  if (webhookForm.type === 'telegram') {
    const config: Record<string, unknown> = {
      bot_token: webhookForm.telegramBotToken,
      chat_id: webhookForm.telegramChatID,
      language: webhookForm.telegramLanguage,
      timezone: nullable(webhookForm.telegramTimezone) || 'Asia/Shanghai',
      disable_notification: webhookForm.telegramDisableNotification
    }
    const threadID = nullable(webhookForm.telegramMessageThreadID)
    if (threadID) {
      config.message_thread_id = Number(threadID)
    }
    payload.config = config
  }
  return payload
}

function editWebhook(webhook: UsageAlertWebhook) {
  const config = webhook.config || {}
  webhookForm.id = webhook.id
  webhookForm.name = webhook.name
  webhookForm.type = webhook.type || 'json_post'
  webhookForm.url = webhook.url || ''
  webhookForm.telegramBotToken = configString(config.bot_token)
  webhookForm.telegramChatID = configString(config.chat_id)
  webhookForm.telegramMessageThreadID = configString(config.message_thread_id)
  webhookForm.telegramLanguage = normalizeWebhookLanguage(configString(config.language))
  webhookForm.telegramTimezone = configString(config.timezone) || 'Asia/Shanghai'
  webhookForm.telegramDisableNotification = Boolean(config.disable_notification)
  webhookForm.retryCount = webhook.retry_count
  webhookForm.enabled = webhook.enabled
}

function resetWebhookForm() {
  webhookForm.id = null
  webhookForm.name = ''
  webhookForm.type = 'json_post'
  webhookForm.url = ''
  webhookForm.telegramBotToken = ''
  webhookForm.telegramChatID = ''
  webhookForm.telegramMessageThreadID = ''
  webhookForm.telegramLanguage = defaultWebhookLanguage()
  webhookForm.telegramTimezone = 'Asia/Shanghai'
  webhookForm.telegramDisableNotification = false
  webhookForm.retryCount = 2
  webhookForm.enabled = true
}

async function deleteWebhook(id: number) {
  if (!window.confirm(text.value.confirmDelete)) return
  try {
    await usageAlertAPI.deleteWebhook(id)
    appStore.showSuccess(text.value.deleted)
    await loadAll()
  } catch (error) {
    appStore.showError(errorMessage(error))
  }
}

async function saveBinding() {
  saving.binding = true
  try {
    const payload = {
      real_account_id: bindingForm.realAccountID,
      webhook_id: bindingForm.webhookID,
      enabled: bindingForm.enabled
    }
    if (bindingForm.id) {
      await usageAlertAPI.updateBinding(bindingForm.id, payload)
    } else {
      await usageAlertAPI.createBinding(payload)
    }
    appStore.showSuccess(text.value.saved)
    resetBindingForm()
    await loadAll()
  } catch (error) {
    appStore.showError(errorMessage(error))
  } finally {
    saving.binding = false
  }
}

function editBinding(binding: UsageAlertBinding) {
  bindingForm.id = binding.id
  bindingForm.realAccountID = binding.real_account_id
  bindingForm.webhookID = binding.webhook_id
  bindingForm.enabled = binding.enabled
}

function resetBindingForm() {
  bindingForm.id = null
  bindingForm.realAccountID = 0
  bindingForm.webhookID = 0
  bindingForm.enabled = true
}

async function deleteBinding(id: number) {
  if (!window.confirm(text.value.confirmDelete)) return
  try {
    await usageAlertAPI.deleteBinding(id)
    appStore.showSuccess(text.value.deleted)
    await loadAll()
  } catch (error) {
    appStore.showError(errorMessage(error))
  }
}

function snapshotEntries(realAccount: RealAccount) {
  return realAccountQuotaDimensions(realAccount).flatMap((dimension) => {
    const snapshot = snapshots[snapshotKey(realAccount.id, dimension)]
    if (!snapshot) return []
    return Object.entries(snapshot.windows || {}).map(([window, value]) => ({
      dimension,
      window,
      used: formatPercent(value?.used_percent),
      remaining: formatPercent(value?.remaining_percent)
    }))
  })
}

function snapshotKey(realAccountID: number, quotaDimension: UsageAlertQuotaDimension) {
  return `${realAccountID}:${quotaDimension}`
}

function realAccountQuotaDimensions(realAccount: RealAccount): UsageAlertQuotaDimension[] {
  const dimensions = new Set<UsageAlertQuotaDimension>(['global'])
  for (const account of realAccount.accounts || []) {
    dimensions.add(account.quota_dimension || 'global')
  }
  return [...dimensions]
}

function quotaDimensionLabel(quotaDimension: UsageAlertQuotaDimension | string) {
  return quotaDimension === 'spark' ? text.value.sparkQuota : text.value.globalQuota
}

function platformLabel(platform: string) {
  if (platform === 'anthropic') return 'Claude'
  if (platform === 'openai') return 'OpenAI'
  return text.value.allPlatforms
}

function metricLabel(metric: string) {
  return metric === 'remaining_percent' ? text.value.remainingPercent : text.value.usedPercent
}

function ruleRealAccountLabel(rule: UsageAlertRule) {
  return rule.real_account?.name || (rule.real_account_id ? realAccountName(rule.real_account_id) : '-')
}

function ruleDetailItems(rule: UsageAlertRule) {
  return [
    ruleRealAccountLabel(rule),
    platformLabel(rule.real_account?.platform || rule.platform),
    quotaDimensionLabel(rule.quota_dimension),
    rule.window,
    `${metricLabel(rule.metric)} ${rule.operator} ${formatRuleNumber(rule.threshold)}%`,
    `${text.value.stepPercentShort} ${rule.step_percent == null ? '-' : `${formatRuleNumber(rule.step_percent)}%`}`,
    `${text.value.minResetAfterShort} ${rule.min_reset_after_hours == null ? '-' : `${formatRuleNumber(rule.min_reset_after_hours)}h`}`,
    `${text.value.cooldownShort} ${rule.cooldown_minutes}m`,
    rule.enabled ? text.value.enabled : text.value.disabled
  ]
}

function realAccountName(id: number) {
  return realAccounts.value.find((item) => item.id === id)?.name || `#${id}`
}

function webhookName(id: number) {
  return webhooks.value.find((item) => item.id === id)?.name || `#${id}`
}

function webhookTypeLabel(type?: string) {
  if (type === 'telegram') return 'Telegram'
  return 'JSON POST'
}

function webhookSummary(webhook: UsageAlertWebhook) {
  if (webhook.type === 'telegram') {
    const config = webhook.config || {}
    const chatID = configString(config.chat_id)
    const threadID = configString(config.message_thread_id)
    const language = normalizeWebhookLanguage(configString(config.language)).toUpperCase()
    const timezone = configString(config.timezone) || 'Asia/Shanghai'
    const target = threadID ? `${chatID} / topic ${threadID}` : chatID
    return `${target} · ${language} · ${timezone}`
  }
  return webhook.url || '-'
}

function normalizeWebhookLanguage(language: string) {
  return language === 'zh' ? 'zh' : 'en'
}

function configString(value: unknown) {
  if (value == null) return ''
  return String(value)
}

function formatPercent(value?: number | null) {
  if (value == null || Number.isNaN(value)) return '-'
  return Number(value).toFixed(1)
}

function formatRuleNumber(value: number) {
  return Number.isInteger(value) ? String(value) : Number(value).toFixed(1)
}

function nullable(value: string) {
  const trimmed = value.trim()
  return trimmed === '' ? null : trimmed
}

function errorMessage(error: unknown) {
  const maybe = error as { response?: { data?: { detail?: string; message?: string } }; message?: string }
  return maybe.response?.data?.detail || maybe.response?.data?.message || maybe.message || text.value.failed
}
</script>
