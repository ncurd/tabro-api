<template>
  <BaseDialog
    :show="show"
    :title="t('admin.users.createUser')"
    width="normal"
    @close="$emit('close')"
  >
    <form id="create-user-form" @submit.prevent="submit" class="space-y-5">
      <div>
        <label class="input-label">{{ t('admin.users.email') }}</label>
        <input v-model="form.email" type="email" required class="input" :placeholder="t('admin.users.enterEmail')" />
      </div>
      <label class="flex cursor-pointer items-start gap-3 rounded-lg border border-gray-200 p-3 dark:border-dark-600">
        <input v-model="form.api_only" type="checkbox" class="mt-1" />
        <span>
          <span class="block font-medium text-gray-900 dark:text-white">{{ t('admin.users.apiOnly') }}</span>
          <span class="block text-sm text-gray-500 dark:text-dark-400">{{ t('admin.users.apiOnlyHint') }}</span>
        </span>
      </label>
      <div v-if="!form.api_only">
        <label class="input-label">{{ t('admin.users.password') }}</label>
        <div class="flex gap-2">
          <div class="relative flex-1">
            <input v-model="form.password" type="text" required class="input pr-10" :placeholder="t('admin.users.enterPassword')" />
          </div>
          <button type="button" @click="generateRandomPassword" class="btn btn-secondary px-3">
            <Icon name="refresh" size="md" />
          </button>
        </div>
      </div>
      <div>
        <label class="input-label">{{ t('admin.users.username') }}</label>
        <input v-model="form.username" type="text" class="input" :placeholder="t('admin.users.enterUsername')" />
      </div>
      <div class="grid grid-cols-1 sm:grid-cols-2 gap-4">
        <div>
          <label class="input-label">{{ t('admin.users.columns.balance') }}</label>
          <input v-model.number="form.balance" type="number" step="any" class="input" />
        </div>
        <div>
          <label class="input-label">{{ t('admin.users.columns.concurrency') }}</label>
          <input v-model.number="form.concurrency" type="number" class="input" />
        </div>
      </div>
    </form>
    <template #footer>
      <div class="flex justify-end gap-3">
        <button @click="$emit('close')" type="button" class="btn btn-secondary">{{ t('common.cancel') }}</button>
        <button type="submit" form="create-user-form" :disabled="loading" class="btn btn-primary">
          {{ loading ? t('admin.users.creating') : t('common.create') }}
        </button>
      </div>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { reactive, watch } from 'vue'
import { useI18n } from 'vue-i18n'; import { adminAPI } from '@/api/admin'
import { useForm } from '@/composables/useForm'
import BaseDialog from '@/components/common/BaseDialog.vue'
import Icon from '@/components/icons/Icon.vue'

const props = defineProps<{ show: boolean }>()
const emit = defineEmits(['close', 'success']); const { t } = useI18n()

const form = reactive({ email: '', password: '', api_only: false, username: '', notes: '', balance: 0, concurrency: 1 })

const { loading, submit } = useForm({
  form,
  submitFn: async (data) => {
    const { password, ...withoutPassword } = data
    await adminAPI.users.create(data.api_only ? withoutPassword : { ...withoutPassword, password })
    emit('success'); emit('close')
  },
  successMsg: t('admin.users.userCreated')
})

watch(() => props.show, (v) => { if(v) Object.assign(form, { email: '', password: '', api_only: false, username: '', notes: '', balance: 0, concurrency: 1 }) })
watch(() => form.api_only, (apiOnly) => { if (apiOnly) form.password = '' })

const generateRandomPassword = () => {
  const chars = 'ABCDEFGHJKLMNPQRSTUVWXYZabcdefghjkmnpqrstuvwxyz23456789!@#$%^&*'
  let p = ''; for (let i = 0; i < 16; i++) p += chars.charAt(Math.floor(Math.random() * chars.length))
  form.password = p
}
</script>
