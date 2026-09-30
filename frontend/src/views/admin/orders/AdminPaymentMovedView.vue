<template>
  <AppLayout>
    <section class="card mx-auto max-w-xl p-8 text-center" role="status">
      <h1 class="text-xl font-semibold text-gray-900 dark:text-white">
        {{ t('purchase.notConfiguredTitle') }}
      </h1>
      <p class="mt-3 text-gray-600 dark:text-gray-300">
        {{ t('purchase.notConfiguredDesc') }}
      </p>
      <RouterLink to="/admin/orders" class="btn mt-6 inline-flex">
        {{ t('nav.orderManagement') }}
      </RouterLink>
    </section>
  </AppLayout>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import AppLayout from '@/components/layout/AppLayout.vue'

const { t } = useI18n()
</script>
