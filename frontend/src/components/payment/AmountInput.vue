<template>
  <div class="space-y-4">
    <div v-if="currencies.length > 1">
      <label class="mb-2 block text-sm font-medium text-gray-700 dark:text-gray-300">
        {{ t('payment.currency') }}
      </label>
      <div class="grid grid-cols-2 gap-2 sm:grid-cols-4">
        <button
          v-for="item in currencies"
          :key="item.code"
          type="button"
          :class="[
            'rounded-lg border px-3 py-2 text-center text-sm font-medium transition-colors',
            currency === item.code
              ? 'border-primary-500 bg-primary-50 text-primary-700 dark:border-primary-400 dark:bg-primary-900/40 dark:text-primary-300'
              : 'border-gray-200 bg-white text-gray-700 hover:border-gray-300 dark:border-dark-600 dark:bg-dark-800 dark:text-gray-200 dark:hover:border-dark-500',
          ]"
          @click="selectCurrency(item.code)"
        >
          {{ item.code }}
        </button>
      </div>
    </div>

    <div>
      <label class="mb-2 block text-sm font-medium text-gray-700 dark:text-gray-300">
        {{ t('payment.customAmount') }}
      </label>
      <div class="relative">
        <span class="absolute left-3 top-1/2 -translate-y-1/2 text-gray-400 dark:text-dark-500">
          {{ selectedCurrencySymbol }}
        </span>
        <input
          type="text"
          inputmode="decimal"
          :value="customText"
          :placeholder="placeholderText"
          class="input w-full py-3 pl-12 pr-4"
          @input="handleInput"
        />
      </div>
    </div>

    <div>
      <label class="mb-2 block text-sm font-medium text-gray-700 dark:text-gray-300">
        {{ t('payment.quickAmounts') }}
      </label>
      <div class="grid grid-cols-4 gap-2">
        <button
          v-for="amt in filteredAmounts"
          :key="amt"
          type="button"
          :class="[
            'rounded-lg border px-2 py-2 text-center text-sm font-medium transition-colors',
            modelValue === amt
              ? 'border-primary-500 bg-primary-50 text-primary-700 dark:border-primary-400 dark:bg-primary-900/40 dark:text-primary-300'
              : 'border-gray-200 bg-white text-gray-700 hover:border-gray-300 dark:border-dark-600 dark:bg-dark-800 dark:text-gray-200 dark:hover:border-dark-500',
          ]"
          @click="selectAmount(amt)"
        >
          {{ selectedCurrencySymbol }}{{ amt }}
        </button>
      </div>
    </div>

    <div
      v-if="convertedCredits != null && convertedCredits > 0"
      class="rounded-lg border border-primary-100 bg-primary-50 px-3 py-2 text-sm dark:border-primary-800/50 dark:bg-primary-900/20"
    >
      <div class="flex items-center justify-between gap-3">
        <span class="text-gray-600 dark:text-gray-300">{{ t('payment.convertedCredits') }}</span>
        <span class="font-semibold text-primary-700 dark:text-primary-300">
          {{ formatCredits(convertedCredits) }}
        </span>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { formatCredits } from '@/utils/credits'
import type { PaymentCurrency, PaymentCurrencyOption } from '@/utils/paymentCurrency'

const props = withDefaults(defineProps<{
  amounts?: number[]
  modelValue: number | null
  min?: number
  max?: number
  currency?: PaymentCurrency
  currencies?: PaymentCurrencyOption[]
  convertedCredits?: number
}>(), {
  amounts: () => [50, 100, 500, 1000],
  min: 0,
  max: 0,
  currency: 'CNY',
  currencies: () => [{ code: 'CNY', symbol: '¥', creditRate: 1 }],
  convertedCredits: 0,
})

const emit = defineEmits<{
  'update:modelValue': [value: number | null]
  'update:currency': [value: PaymentCurrency]
}>()

const { t } = useI18n()

const customText = ref('')

// 0 = no limit
const filteredAmounts = computed(() =>
  props.amounts.filter((a) => (props.min <= 0 || a >= props.min) && (props.max <= 0 || a <= props.max))
)

const currencies = computed<PaymentCurrencyOption[]>(() =>
  props.currencies.length > 0 ? props.currencies : [{ code: 'CNY', symbol: '¥', creditRate: 1 }]
)
const selectedCurrencySymbol = computed(() =>
  currencies.value.find((item) => item.code === props.currency)?.symbol || props.currency || ''
)

const placeholderText = computed(() => {
  if (props.min > 0 && props.max > 0) return `${props.min} - ${props.max}`
  if (props.min > 0) return `≥ ${props.min}`
  if (props.max > 0) return `≤ ${props.max}`
  return t('payment.enterAmount')
})

const AMOUNT_PATTERN = /^\d*(\.\d{0,2})?$/

function selectAmount(amt: number) {
  customText.value = String(amt)
  emit('update:modelValue', amt)
}

function selectCurrency(code: PaymentCurrency) {
  emit('update:currency', code)
}

function handleInput(e: Event) {
  const val = (e.target as HTMLInputElement).value
  if (!AMOUNT_PATTERN.test(val)) return
  customText.value = val
  if (val === '') {
    emit('update:modelValue', null)
    return
  }
  const num = parseFloat(val)
  if (!isNaN(num) && num > 0) {
    emit('update:modelValue', num)
  } else {
    emit('update:modelValue', null)
  }
}

watch(() => props.modelValue, (v) => {
  if (v !== null && String(v) !== customText.value) {
    customText.value = String(v)
  }
}, { immediate: true })
</script>
