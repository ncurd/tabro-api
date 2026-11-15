<template>
  <AppLayout>
    <section v-if="!authStore.isAdmin" class="rounded-xl border border-gray-200 p-6 dark:border-dark-700" role="alert">
      {{ text('adminOnly') }}
    </section>
    <section v-else class="space-y-5">
      <div class="flex flex-wrap items-start justify-between gap-3">
        <div>
          <h1 class="text-2xl font-bold text-gray-900 dark:text-white">{{ text('title') }}</h1>
          <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">{{ text('description') }}</p>
        </div>
        <button class="btn btn-secondary inline-flex items-center gap-2" :disabled="loading || !!pendingModel" @click="loadModels">
          <Icon name="refresh" size="sm" :class="{ 'animate-spin': loading }" />{{ t('common.refresh') }}
        </button>
      </div>

      <div class="rounded-xl border border-blue-200 bg-blue-50 p-4 text-sm text-blue-900 dark:border-blue-900 dark:bg-blue-950/30 dark:text-blue-200">
        <p>{{ text('pricePriority') }} <RouterLink to="/admin/channels" class="font-medium underline">{{ text('manageChannels') }}</RouterLink></p>
        <p class="mt-1">{{ text('currencyHint') }}</p>
        <p class="mt-1">{{ text('newRequestsOnly') }}</p>
      </div>

      <p v-if="error" class="rounded-lg bg-red-50 p-3 text-sm text-red-700 dark:bg-red-950/30 dark:text-red-300" role="alert">{{ error }}</p>
      <p v-if="success" class="text-sm text-green-700 dark:text-green-400" role="status">{{ success }}</p>

      <div class="flex flex-wrap gap-3">
        <input v-model="search" class="input max-w-sm" :placeholder="text('search')" :aria-label="text('search')" type="search" @input="searchChanged" />
        <select v-model="statusFilter" class="input w-auto" :aria-label="text('statusFilter')" @change="filterChanged">
          <option value="">{{ text('allStatus') }}</option>
          <option value="true">{{ text('enabled') }}</option>
          <option value="false">{{ text('disabled') }}</option>
        </select>
      </div>

      <div class="overflow-x-auto rounded-xl border border-gray-200 bg-white dark:border-dark-700 dark:bg-dark-800" :aria-busy="loading">
        <table class="w-full text-left text-sm">
          <thead class="bg-gray-50 text-gray-500 dark:bg-dark-700/50 dark:text-gray-400">
            <tr>
              <th class="px-4 py-3">{{ text('model') }}</th>
              <th class="px-4 py-3">{{ text('state') }}</th>
              <th class="px-4 py-3">{{ text('basePrice') }}</th>
              <th class="px-4 py-3">{{ text('source') }}</th>
              <th class="px-4 py-3">{{ text('actions') }}</th>
            </tr>
          </thead>
          <tbody class="divide-y divide-gray-100 dark:divide-dark-700">
            <tr v-for="model in models" :key="model.model" :data-model="model.model">
              <td class="px-4 py-4">
                <span class="break-all font-medium text-gray-900 dark:text-gray-100">{{ model.model }}</span>
                <div class="mt-1 flex flex-wrap gap-1">
                  <span v-for="platform in model.platforms" :key="platform" class="badge badge-gray">{{ platform }}</span>
                </div>
              </td>
              <td class="px-4 py-4">
                <div class="flex items-center gap-2">
                  <Toggle :model-value="model.enabled" :disabled="!!pendingModel || loading" :aria-label="text('toggleModel', { model: model.model })" @update:model-value="toggleModel(model)" />
                  <span :class="model.enabled ? 'text-green-700 dark:text-green-400' : 'text-gray-500'">{{ text(model.enabled ? 'enabled' : 'disabled') }}</span>
                </div>
              </td>
              <td class="px-4 py-4 text-gray-700 dark:text-gray-300">
                <template v-if="effectiveModelPrice(model)">
                  <div v-for="line in priceLines(effectiveModelPrice(model))" :key="line">{{ line }}</div>
                </template>
                <span v-else class="text-amber-700 dark:text-amber-400">{{ text('unpriced') }}</span>
              </td>
              <td class="px-4 py-4">
                <span class="badge" :class="model.source === 'manual' ? 'badge-primary' : 'badge-gray'">{{ text(model.source) }}</span>
                <div v-if="model.channel_override" class="mt-1 text-xs text-amber-700 dark:text-amber-400">{{ text('channelOverride') }}</div>
              </td>
              <td class="px-4 py-4">
                <div class="flex flex-wrap gap-2">
                  <button class="btn btn-secondary btn-sm" :disabled="!!pendingModel || loading" :aria-label="text('editModel', { model: model.model })" @click="openEditor(model)">{{ text('editPrice') }}</button>
                  <button v-if="model.source === 'manual'" class="btn btn-ghost btn-sm" :disabled="!!pendingModel || loading" :aria-label="text('resetModel', { model: model.model })" @click="resetting = model">{{ text('resetPrice') }}</button>
                </div>
              </td>
            </tr>
            <tr v-if="!models.length"><td colspan="5" class="p-10 text-center text-gray-500">{{ loading ? t('common.loading') : text('empty') }}</td></tr>
          </tbody>
        </table>
        <Pagination v-if="total > 0" :page="page" :total="total" :page-size="pageSize" @update:page="changePage" @update:page-size="changePageSize" />
      </div>
    </section>

    <BaseDialog :show="!!editing && authStore.isAdmin" :title="text('editPrice')" width="normal" :close-on-escape="!pendingModel" @close="closeEditor">
      <form v-if="editing" id="model-price-form" class="space-y-4" @submit.prevent="savePrice">
        <p class="break-all font-medium">{{ editing.model }}</p>
        <p class="text-sm text-gray-500">{{ text(form.billing_mode === 'duration' ? 'durationPriceHint' : 'manualPriceHint') }}</p>
        <p v-if="editing.channel_override" class="text-sm text-amber-700 dark:text-amber-400">{{ text('channelOverrideHint') }}</p>
        <p v-if="formError" class="text-sm text-red-700 dark:text-red-300" role="alert">{{ formError }}</p>
        <label class="block"><span class="input-label">{{ text('billingMode') }}</span>
          <select v-model="form.billing_mode" class="input" :disabled="!!pendingModel" data-field="billing_mode" @change="modeChanged">
            <option v-for="mode in billingModes" :key="mode" :value="mode">{{ text(`modes.${mode}`) }}</option>
          </select>
        </label>
        <label class="block"><span class="input-label">{{ text('priceUnit') }}</span>
          <select v-model="form.price_unit" class="input" :disabled="!!pendingModel" data-field="price_unit">
            <option v-for="unit in unitsForMode(form.billing_mode)" :key="unit" :value="unit">{{ text(`units.${unit}`) }}</option>
          </select>
        </label>
        <template v-if="form.billing_mode === 'token'">
          <label v-for="field in tokenFields" :key="field" class="block"><span class="input-label">{{ text(field) }}</span>
            <input v-model="form[field]" class="input" type="number" min="0" step="any" :data-field="field" :disabled="!!pendingModel" :required="field === 'input_price_per_million' || field === 'output_price_per_million'" />
          </label>
        </template>
        <template v-else-if="form.billing_mode === 'duration'">
          <label v-for="field in durationPriceFields" :key="field" class="block"><span class="input-label">{{ text(field) }}</span>
            <input v-model="form[field]" class="input" type="number" min="0" step="any" :data-field="field" :disabled="!!pendingModel" />
          </label>
        </template>
        <label v-else class="block"><span class="input-label">{{ text('unitPrice') }}</span>
          <input v-model="form.unit_price" class="input" type="number" min="0" step="any" required data-field="unit_price" :disabled="!!pendingModel" />
        </label>
        <p class="text-xs text-gray-500">{{ text('zeroHint') }}</p>
      </form>
      <template #footer>
        <button class="btn btn-secondary" :disabled="!!pendingModel" @click="closeEditor">{{ t('common.cancel') }}</button>
        <button class="btn btn-primary" type="submit" form="model-price-form" :disabled="!!pendingModel">{{ t('common.save') }}</button>
      </template>
    </BaseDialog>

    <BaseDialog :show="!!resetting && authStore.isAdmin" :title="text('resetPrice')" :close-on-escape="!pendingModel" @close="closeReset">
      <div v-if="resetting" class="space-y-3">
        <p class="break-all font-medium">{{ resetting.model }}</p>
        <p class="text-sm text-gray-500">{{ text('resetHint') }}</p>
        <p v-if="effectiveModelPrice({ ...resetting, pricing: null })" class="text-sm">{{ priceLines(resetting.default_pricing).join(' · ') }}</p>
        <p v-else class="text-sm text-amber-700 dark:text-amber-400">{{ text('resetUnpriced') }}</p>
        <p v-if="resetError" role="alert" class="text-sm text-red-700 dark:text-red-300">{{ resetError }}</p>
      </div>
      <template #footer>
        <button class="btn btn-secondary" :disabled="!!pendingModel" @click="closeReset">{{ t('common.cancel') }}</button>
        <button class="btn btn-primary" :disabled="!!pendingModel" @click="resetPrice">{{ text('confirmReset') }}</button>
      </template>
    </BaseDialog>
  </AppLayout>
</template>

<script setup lang="ts">
import { onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import AppLayout from '@/components/layout/AppLayout.vue'
import BaseDialog from '@/components/common/BaseDialog.vue'
import Pagination from '@/components/common/Pagination.vue'
import Toggle from '@/components/common/Toggle.vue'
import Icon from '@/components/icons/Icon.vue'
import { useAuthStore } from '@/stores/auth'
import { adminModelsAPI, type AdminModelPolicy, type AdminModelPrice, type ModelBillingMode } from '@/api/admin/models'
import { buildModelPrice, createModelPriceForm, durationPriceFields, effectiveModelPrice, unitsForMode } from '@/utils/adminModelPricing'

const { t } = useI18n()
const text = (key: string, params: Record<string, string> = {}) => t(`modelManagement.${key}`, params)
const authStore = useAuthStore()
const models = ref<AdminModelPolicy[]>([])
const loading = ref(false)
const error = ref('')
const success = ref('')
const search = ref('')
const statusFilter = ref('')
const page = ref(1)
const pageSize = ref(20)
const total = ref(0)
const pendingModel = ref('')
const editing = ref<AdminModelPolicy | null>(null)
const resetting = ref<AdminModelPolicy | null>(null)
const formError = ref('')
const resetError = ref('')
const form = reactive(createModelPriceForm({ model: '', platforms: [], enabled: true, pricing: null, default_pricing: null, source: 'unpriced', channel_override: false }))
const billingModes: ModelBillingMode[] = ['token', 'duration', 'audio', 'video', 'image', 'per_request']
const tokenFields = ['input_price_per_million', 'output_price_per_million', 'cache_write_price_per_million', 'cache_read_price_per_million'] as const
let requestSequence = 0
let controller: AbortController | null = null
let searchTimer: ReturnType<typeof setTimeout> | undefined

function failureMessage(failure: unknown, fallback: string): string {
  const candidate = failure as { response?: { data?: { message?: string; error?: string } } }
  const message = candidate?.response?.data?.message || candidate?.response?.data?.error
  return typeof message === 'string' && message.trim() ? message : text(fallback)
}

async function loadModels() {
  if (!authStore.isAdmin) return
  const sequence = ++requestSequence
  controller?.abort()
  controller = new AbortController()
  loading.value = true
  error.value = ''
  try {
    const data = await adminModelsAPI.list({ page: page.value, page_size: pageSize.value, search: search.value.trim() || undefined,
      enabled: statusFilter.value === '' ? undefined : statusFilter.value === 'true' }, controller.signal)
    if (sequence !== requestSequence || !authStore.isAdmin) return
    models.value = data.items
    total.value = data.total
  } catch (failure) {
    if (sequence === requestSequence && !controller?.signal.aborted) error.value = failureMessage(failure, 'loadFailed')
  } finally { if (sequence === requestSequence) loading.value = false }
}

function searchChanged() {
  clearTimeout(searchTimer)
  searchTimer = setTimeout(() => { page.value = 1; void loadModels() }, 250)
}
function filterChanged() { clearTimeout(searchTimer); page.value = 1; void loadModels() }
function changePage(value: number) { page.value = value; void loadModels() }
function changePageSize(value: number) { pageSize.value = value; page.value = 1; void loadModels() }

function replaceModel(updated: AdminModelPolicy) {
  models.value = models.value.map(model => model.model === updated.model ? updated : model)
}
async function toggleModel(model: AdminModelPolicy) {
  if (pendingModel.value || !authStore.isAdmin) return
  pendingModel.value = model.model
  error.value = ''; success.value = ''
  try {
    const updated = await adminModelsAPI.update({ model: model.model, enabled: !model.enabled, pricing: model.pricing })
    replaceModel(updated)
    success.value = text('stateSaved')
    await loadModels()
  } catch (failure) { error.value = failureMessage(failure, 'saveFailed') }
  finally { pendingModel.value = '' }
}

function openEditor(model: AdminModelPolicy) { editing.value = model; formError.value = ''; Object.assign(form, createModelPriceForm(model)) }
function closeEditor() { if (!pendingModel.value) editing.value = null }
function closeReset() { if (!pendingModel.value) { resetting.value = null; resetError.value = '' } }
function modeChanged() { form.price_unit = unitsForMode(form.billing_mode)[0] }
async function savePrice() {
  if (!editing.value || pendingModel.value || !authStore.isAdmin) return
  const result = buildModelPrice(form)
  if (result.error) { formError.value = text(result.error); return }
  const current = editing.value
  pendingModel.value = current.model; formError.value = ''; success.value = ''
  try {
    const updated = await adminModelsAPI.update({ model: current.model, enabled: current.enabled, pricing: result.price })
    replaceModel(updated); editing.value = null
    success.value = text('priceSaved')
    await loadModels()
  } catch (failure) { formError.value = failureMessage(failure, 'saveFailed') }
  finally { pendingModel.value = '' }
}
async function resetPrice() {
  if (!resetting.value || pendingModel.value || !authStore.isAdmin) return
  pendingModel.value = resetting.value.model; resetError.value = ''; success.value = ''
  try {
    const updated = await adminModelsAPI.resetPrice(resetting.value.model)
    replaceModel(updated); resetting.value = null
    success.value = text('priceReset')
    await loadModels()
  } catch (failure) { resetError.value = failureMessage(failure, 'saveFailed') }
  finally { pendingModel.value = '' }
}

function priceLines(price: AdminModelPrice | null): string[] {
  if (!price) return []
  const amount = (value: number) => value.toLocaleString(undefined, { maximumSignificantDigits: 12 })
  if (price.billing_mode === 'duration') return durationPriceFields.map(field => `${text(field)}: ${price[field] == null ? text('unpriced') : `${amount(price[field]!)} USD`}`)
  if (price.billing_mode !== 'token') return price.unit_price == null ? [text('unpriced')]
    : [`${amount(price.unit_price)} USD / ${text(`units.${price.price_unit}`)}`]
  return tokenFields.filter(field => price[field] != null).map(field => `${text(field)}: ${amount(price[field]!)} USD`)
}

watch(() => authStore.isAdmin, admin => {
  if (!admin) { requestSequence++; controller?.abort(); editing.value = null; resetting.value = null; models.value = [] }
})
onMounted(() => { void loadModels() })
onBeforeUnmount(() => { requestSequence++; controller?.abort(); clearTimeout(searchTimer) })
</script>
