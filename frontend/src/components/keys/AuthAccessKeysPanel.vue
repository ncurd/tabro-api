<template>
  <section class="card space-y-4 p-6" data-testid="auth-access-keys">
    <h2 class="text-lg font-semibold text-gray-900 dark:text-white">{{ t('keys.authKeysTitle') }}</h2>
    <p class="text-sm text-gray-600 dark:text-gray-300">{{ t('keys.authKeysDescription') }}</p>
    <p class="text-sm text-gray-600 dark:text-gray-300">{{ t('keys.authKeysDirectDisabled') }}</p>
    <a v-if="manageUrl" :href="manageUrl" class="btn btn-primary">{{ t('keys.authKeysManage') }}</a>
    <p v-else class="text-sm text-amber-700 dark:text-amber-400">{{ t('keys.authKeysMissingCenter') }}</p>
  </section>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { authAccessKeysUrl } from '@/utils/authAccessKeys'

const props = defineProps<{ accountCenterUrl?: string }>()
const { t } = useI18n()
const manageUrl = computed(() => authAccessKeysUrl(props.accountCenterUrl))
</script>
