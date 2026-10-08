<template>
  <div v-if="appStore.oidcBillingEnabled" class="rounded-xl border border-primary-200 bg-primary-50 p-4 text-sm text-primary-800 dark:border-primary-800 dark:bg-primary-900/20 dark:text-primary-200" role="status">
    {{ t('admin.settings.oidc.billingUserNotice', {
      multiplier: appStore.cachedPublicSettings?.oidc_billing_rate_multiplier ?? 1,
      time: appStore.cachedPublicSettings?.oidc_billing_settlement_time || '00:00',
      timezone: appStore.cachedPublicSettings?.oidc_billing_settlement_timezone || 'Asia/Shanghai'
    }) }}
  </div>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { useAppStore } from '@/stores/app'

const { t } = useI18n()
const appStore = useAppStore()
</script>
