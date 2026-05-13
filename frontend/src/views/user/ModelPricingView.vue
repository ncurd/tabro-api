<template>
  <AppLayout>
    <div class="space-y-6">
      <div class="flex flex-col gap-4 lg:flex-row lg:items-end lg:justify-between">
      <div>
        <h1 class="text-2xl font-bold text-gray-900 dark:text-white">模型价格</h1>
        <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">
          当前账户可用分组的模型价格，按分组倍率折算为✦。
        </p>
      </div>
      <button class="btn btn-secondary inline-flex items-center gap-2 self-start lg:self-auto" :disabled="loading" @click="loadPricing">
        <Icon name="refresh" size="sm" :class="{ 'animate-spin': loading }" />
        <span>刷新</span>
      </button>
    </div>

    <div class="grid gap-4 md:grid-cols-3">
      <div class="rounded-lg border border-gray-200 bg-white p-4 dark:border-dark-700 dark:bg-dark-800">
        <p class="text-sm text-gray-500 dark:text-gray-400">可用分组</p>
        <p class="mt-2 text-2xl font-semibold text-gray-900 dark:text-white">{{ groups.length }}</p>
      </div>
      <div class="rounded-lg border border-gray-200 bg-white p-4 dark:border-dark-700 dark:bg-dark-800">
        <p class="text-sm text-gray-500 dark:text-gray-400">可用模型</p>
        <p class="mt-2 text-2xl font-semibold text-gray-900 dark:text-white">{{ totalModels }}</p>
      </div>
      <div class="rounded-lg border border-gray-200 bg-white p-4 dark:border-dark-700 dark:bg-dark-800">
        <p class="text-sm text-gray-500 dark:text-gray-400">价格单位</p>
        <p class="mt-2 text-2xl font-semibold text-gray-900 dark:text-white">✦ / 1M tokens</p>
      </div>
    </div>

      <div class="flex flex-col gap-3 rounded-lg border border-gray-200 bg-white p-4 dark:border-dark-700 dark:bg-dark-800 lg:flex-row lg:items-center lg:justify-between">
      <div class="flex flex-wrap gap-2">
        <button
          class="rounded-md px-3 py-1.5 text-sm font-medium transition"
          :class="selectedGroupId === 'all' ? activeFilterClass : idleFilterClass"
          @click="selectedGroupId = 'all'"
        >
          全部分组
        </button>
        <button
          v-for="group in groups"
          :key="group.id"
          class="rounded-md px-3 py-1.5 text-sm font-medium transition"
          :class="selectedGroupId === group.id ? activeFilterClass : idleFilterClass"
          @click="selectedGroupId = group.id"
        >
          {{ group.name }}
        </button>
      </div>
      <div class="relative w-full lg:w-80">
        <Icon name="search" size="sm" class="absolute left-3 top-1/2 -translate-y-1/2 text-gray-400" />
        <input
          v-model.trim="searchQuery"
          class="input pl-9"
          type="search"
          placeholder="搜索模型"
        />
      </div>
    </div>

      <div v-if="loading" class="flex min-h-64 items-center justify-center rounded-lg border border-gray-200 bg-white dark:border-dark-700 dark:bg-dark-800">
      <div class="h-10 w-10 animate-spin rounded-full border-4 border-primary-500 border-t-transparent"></div>
    </div>

      <div v-else-if="error" class="rounded-lg border border-red-200 bg-red-50 p-4 text-sm text-red-700 dark:border-red-900/50 dark:bg-red-900/20 dark:text-red-200">
      {{ error }}
    </div>

      <div v-else-if="filteredGroups.length === 0" class="rounded-lg border border-gray-200 bg-white p-10 text-center dark:border-dark-700 dark:bg-dark-800">
      <p class="text-base font-medium text-gray-900 dark:text-white">暂无可用价格</p>
      <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">当前筛选条件下没有可展示的模型。</p>
    </div>

      <div v-else class="space-y-6">
      <section
        v-for="group in filteredGroups"
        :key="group.id"
        class="overflow-hidden rounded-lg border border-gray-200 bg-white dark:border-dark-700 dark:bg-dark-800"
      >
        <div class="flex flex-col gap-3 border-b border-gray-200 p-4 dark:border-dark-700 md:flex-row md:items-center md:justify-between">
          <div>
            <div class="flex flex-wrap items-center gap-2">
              <h2 class="text-lg font-semibold text-gray-900 dark:text-white">{{ group.name }}</h2>
              <span class="rounded-md bg-gray-100 px-2 py-0.5 text-xs font-medium uppercase text-gray-600 dark:bg-dark-700 dark:text-gray-300">
                {{ group.platform }}
              </span>
            </div>
            <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">
              有效倍率 {{ formatRate(group.effective_rate_multiplier) }}，{{ group.models.length }} 个模型
            </p>
          </div>
        </div>

        <div class="overflow-x-auto">
          <table class="min-w-full divide-y divide-gray-200 text-sm dark:divide-dark-700">
            <thead class="bg-gray-50 dark:bg-dark-900/60">
              <tr>
                <th class="whitespace-nowrap px-4 py-3 text-left font-medium text-gray-500 dark:text-gray-400">模型</th>
                <th class="whitespace-nowrap px-4 py-3 text-right font-medium text-gray-500 dark:text-gray-400">输入</th>
                <th class="whitespace-nowrap px-4 py-3 text-right font-medium text-gray-500 dark:text-gray-400">输出</th>
                <th class="whitespace-nowrap px-4 py-3 text-right font-medium text-gray-500 dark:text-gray-400">缓存写入</th>
                <th class="whitespace-nowrap px-4 py-3 text-right font-medium text-gray-500 dark:text-gray-400">缓存读取</th>
                <th class="whitespace-nowrap px-4 py-3 text-right font-medium text-gray-500 dark:text-gray-400">优先输入</th>
                <th class="whitespace-nowrap px-4 py-3 text-right font-medium text-gray-500 dark:text-gray-400">优先输出</th>
                <th class="whitespace-nowrap px-4 py-3 text-right font-medium text-gray-500 dark:text-gray-400">图片输出</th>
              </tr>
            </thead>
            <tbody class="divide-y divide-gray-100 dark:divide-dark-700">
              <tr v-for="model in group.models" :key="model.id" class="hover:bg-gray-50 dark:hover:bg-dark-700/40">
                <td class="max-w-xs px-4 py-3">
                  <div class="font-mono text-sm font-medium text-gray-900 dark:text-white">{{ model.id }}</div>
                  <div v-if="model.source" class="mt-0.5 text-xs text-gray-500 dark:text-gray-400">{{ model.source }}</div>
                </td>
                <td class="whitespace-nowrap px-4 py-3 text-right tabular-nums text-gray-700 dark:text-gray-200">{{ formatPrice(model, model.input_price_per_million) }}</td>
                <td class="whitespace-nowrap px-4 py-3 text-right tabular-nums text-gray-700 dark:text-gray-200">{{ formatPrice(model, model.output_price_per_million) }}</td>
                <td class="whitespace-nowrap px-4 py-3 text-right tabular-nums text-gray-700 dark:text-gray-200">{{ formatPrice(model, model.cache_write_price_per_million) }}</td>
                <td class="whitespace-nowrap px-4 py-3 text-right tabular-nums text-gray-700 dark:text-gray-200">{{ formatPrice(model, model.cache_read_price_per_million) }}</td>
                <td class="whitespace-nowrap px-4 py-3 text-right tabular-nums text-gray-700 dark:text-gray-200">{{ formatPrice(model, model.priority_input_price_per_million) }}</td>
                <td class="whitespace-nowrap px-4 py-3 text-right tabular-nums text-gray-700 dark:text-gray-200">{{ formatPrice(model, model.priority_output_price_per_million) }}</td>
                <td class="whitespace-nowrap px-4 py-3 text-right tabular-nums text-gray-700 dark:text-gray-200">{{ formatPrice(model, model.image_output_price_per_million) }}</td>
              </tr>
            </tbody>
          </table>
        </div>
      </section>
      </div>
    </div>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'

import { modelPricingAPI, type AvailableModelPricingGroup, type AvailableModelPricingModel } from '@/api/modelPricing'
import AppLayout from '@/components/layout/AppLayout.vue'
import Icon from '@/components/icons/Icon.vue'
import { extractApiErrorMessage } from '@/utils/apiError'
import { formatCredits } from '@/utils/credits'

const groups = ref<AvailableModelPricingGroup[]>([])
const loading = ref(false)
const error = ref('')
const selectedGroupId = ref<number | 'all'>('all')
const searchQuery = ref('')

const activeFilterClass = 'bg-primary-600 text-white shadow-sm'
const idleFilterClass = 'bg-gray-100 text-gray-700 hover:bg-gray-200 dark:bg-dark-700 dark:text-gray-200 dark:hover:bg-dark-600'

const totalModels = computed(() => groups.value.reduce((total, group) => total + group.models.length, 0))

const filteredGroups = computed(() => {
  const query = searchQuery.value.toLowerCase()
  return groups.value
    .filter((group) => selectedGroupId.value === 'all' || group.id === selectedGroupId.value)
    .map((group) => ({
      ...group,
      models: query
        ? group.models.filter((model) => model.id.toLowerCase().includes(query))
        : group.models
    }))
    .filter((group) => group.models.length > 0)
})

function formatRate(value: number): string {
  return `${value.toFixed(2)}x`
}

function formatPrice(model: AvailableModelPricingModel, value: number | undefined): string {
  if (!model.pricing_available || value == null) {
    return '暂无'
  }
  return formatCredits(value, { fractionDigits: 4 })
}

async function loadPricing() {
  loading.value = true
  error.value = ''
  try {
    const data = await modelPricingAPI.getAvailable()
    groups.value = data.groups ?? []
  } catch (err) {
    error.value = extractApiErrorMessage(err, '价格加载失败，请稍后重试')
  } finally {
    loading.value = false
  }
}

onMounted(loadPricing)
</script>
