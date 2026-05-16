<template>
  <section class="my-6 rounded-lg border border-gray-200 bg-white p-4 shadow-sm dark:border-dark-700 dark:bg-dark-900">
    <div v-if="title || description" class="mb-4">
      <h3 v-if="title" class="text-sm font-semibold text-gray-900 dark:text-white">{{ title }}</h3>
      <p v-if="description" class="mt-1 text-sm text-gray-500 dark:text-dark-300">{{ description }}</p>
    </div>

    <div v-if="chartData" class="h-72">
      <component :is="chartComponent" :data="chartData" :options="mergedOptions" />
    </div>
    <div v-else class="rounded-md bg-red-50 p-3 text-sm text-red-700 dark:bg-red-950/40 dark:text-red-200">
      图表配置解析失败，请检查 chart 代码块中的 JSON。
    </div>
  </section>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import {
  ArcElement,
  BarElement,
  CategoryScale,
  Chart as ChartJS,
  Filler,
  Legend,
  LinearScale,
  LineElement,
  PointElement,
  Tooltip,
} from 'chart.js'
import type { ChartType } from 'chart.js'
import { Bar, Doughnut, Line, Pie } from 'vue-chartjs'

ChartJS.register(
  ArcElement,
  BarElement,
  CategoryScale,
  Filler,
  Legend,
  LinearScale,
  LineElement,
  PointElement,
  Tooltip
)

interface ChartBlockConfig {
  type?: ChartType
  title?: string
  description?: string
  data?: Record<string, unknown>
  options?: Record<string, any>
}

const props = defineProps<{
  config: ChartBlockConfig | null
}>()

const type = computed(() => props.config?.type || 'bar')
const title = computed(() => props.config?.title || '')
const description = computed(() => props.config?.description || '')
const chartData = computed<any>(() => props.config?.data || null)

const chartComponent = computed(() => {
  switch (type.value) {
    case 'line':
      return Line
    case 'doughnut':
      return Doughnut
    case 'pie':
      return Pie
    default:
      return Bar
  }
})

const mergedOptions = computed<any>(() => ({
  responsive: true,
  maintainAspectRatio: false,
  plugins: {
    legend: {
      position: 'bottom',
    },
    tooltip: {
      mode: 'index',
      intersect: false,
    },
    ...(props.config?.options?.plugins || {}),
  },
  scales: type.value === 'pie' || type.value === 'doughnut'
    ? undefined
    : {
        x: {
          grid: { display: false },
        },
        y: {
          beginAtZero: true,
        },
        ...(props.config?.options?.scales || {}),
      },
  ...props.config?.options,
}))
</script>
