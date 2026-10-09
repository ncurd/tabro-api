<template>
  <BaseDialog :show="show" :title="t('admin.users.userApiKeys')" width="wide" @close="handleClose">
    <div v-if="user" class="space-y-4">
      <div class="flex items-center gap-3 rounded-xl bg-gray-50 p-4 dark:bg-dark-700">
        <div class="flex h-10 w-10 items-center justify-center rounded-full bg-primary-100 dark:bg-primary-900/30">
          <span class="text-lg font-medium text-primary-700 dark:text-primary-300">{{ user.email.charAt(0).toUpperCase() }}</span>
        </div>
        <div><p class="font-medium text-gray-900 dark:text-white">{{ user.email }}</p><p class="text-sm text-gray-500 dark:text-dark-400">{{ user.username }}</p></div>
      </div>
      <form v-if="user.role !== 'admin'" class="space-y-3 rounded-xl border border-gray-200 p-4 dark:border-dark-600" @submit.prevent="provisionOIDCIdentity">
        <div>
          <h3 class="font-medium text-gray-900 dark:text-white">{{ t('admin.users.provisionOIDCIdentity') }}</h3>
          <p class="mt-1 text-sm text-gray-500 dark:text-dark-400">{{ t('admin.users.provisionOIDCIdentityHint') }}</p>
          <p v-if="oidcGatewayKey" class="mt-1 text-sm text-primary-600 dark:text-primary-400">
            {{ t('admin.users.oidcGatewayKeyPresent', { id: oidcGatewayKey.id }) }}
          </p>
        </div>
        <div class="grid gap-3 sm:grid-cols-2">
          <div>
            <label class="input-label" for="oidc-identity-issuer">{{ t('admin.users.oidcIssuer') }}</label>
            <input id="oidc-identity-issuer" v-model.trim="oidcIdentity.issuer" type="url" required class="input" :placeholder="t('admin.users.oidcIssuerPlaceholder')" />
          </div>
          <div>
            <label class="input-label" for="oidc-identity-subject">{{ t('admin.users.oidcSubject') }}</label>
            <input id="oidc-identity-subject" v-model.trim="oidcIdentity.subject" type="text" required class="input" :placeholder="t('admin.users.oidcSubjectPlaceholder')" />
          </div>
        </div>
        <button type="submit" class="btn btn-primary" :disabled="provisioning">
          {{ provisioning ? t('admin.users.provisioningOIDCIdentity') : t('admin.users.provisionOIDCIdentityAction') }}
        </button>
      </form>
      <div v-if="loading" class="flex justify-center py-8"><svg class="h-8 w-8 animate-spin text-primary-500" fill="none" viewBox="0 0 24 24"><circle class="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" stroke-width="4"></circle><path class="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4zm2 5.291A7.962 7.962 0 014 12H0c0 3.042 1.135 5.824 3 7.938l3-2.647z"></path></svg></div>
      <div v-else-if="apiKeys.length === 0" class="py-8 text-center"><p class="text-sm text-gray-500">{{ t('admin.users.noApiKeys') }}</p></div>
      <div v-else ref="scrollContainerRef" class="max-h-96 space-y-3 overflow-y-auto" @scroll="closeGroupSelector">
        <div v-for="key in apiKeys" :key="key.id" class="rounded-xl border border-gray-200 bg-white p-4 dark:border-dark-600 dark:bg-dark-800">
          <div class="flex items-start justify-between">
            <div class="min-w-0 flex-1">
              <div class="mb-1 flex items-center gap-2"><span class="font-medium text-gray-900 dark:text-white">{{ key.name }}</span><span :class="['badge text-xs', key.status === 'active' ? 'badge-success' : 'badge-danger']">{{ key.status }}</span></div>
              <p class="truncate font-mono text-sm text-gray-500">
                <template v-if="key.oidc_managed">OIDC</template>
                <template v-else>{{ key.key.substring(0, 20) }}...{{ key.key.substring(key.key.length - 8) }}</template>
              </p>
            </div>
          </div>
          <div class="mt-3 flex flex-wrap gap-4 text-xs text-gray-500">
            <div class="flex items-center gap-1">
              <span>{{ t('admin.users.group') }}:</span>
              <button
                :ref="(el) => setGroupButtonRef(key.id, el)"
                @click="openGroupSelector(key)"
                class="-mx-1 -my-0.5 flex cursor-pointer items-center gap-1 rounded-md px-1 py-0.5 transition-colors hover:bg-gray-100 dark:hover:bg-dark-700"
                :disabled="updatingKeyIds.has(key.id)"
              >
                <span v-if="key.group_scope === 'public'">{{ t('keys.scopePublic') }}</span>
                <span v-else-if="key.group_scope === 'selected'">{{ t('keys.selectedGroupsLabel', { count: key.group_ids?.length || 0 }) }}</span>
                <GroupBadge
                  v-else-if="key.group_id && key.group"
                  :name="key.group.name"
                  :platform="key.group.platform"
                  :subscription-type="key.group.subscription_type"
                  :rate-multiplier="key.group.rate_multiplier"
                />
                <span v-else class="text-gray-400 italic">{{ t('admin.users.none') }}</span>
                <svg v-if="updatingKeyIds.has(key.id)" class="h-3 w-3 animate-spin text-primary-500" fill="none" viewBox="0 0 24 24"><circle class="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" stroke-width="4"></circle><path class="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4zm2 5.291A7.962 7.962 0 014 12H0c0 3.042 1.135 5.824 3 7.938l3-2.647z"></path></svg>
                <svg v-else class="h-3 w-3 text-gray-400" fill="none" stroke="currentColor" viewBox="0 0 24 24" stroke-width="2"><path stroke-linecap="round" stroke-linejoin="round" d="M8.25 15L12 18.75 15.75 15m-7.5-6L12 5.25 15.75 9" /></svg>
              </button>
            </div>
            <div class="flex items-center gap-1"><span>{{ t('admin.users.columns.created') }}: {{ formatDateTime(key.created_at) }}</span></div>
          </div>
        </div>
      </div>
    </div>
  </BaseDialog>

  <!-- Group Selector Dropdown -->
  <Teleport to="body">
    <div
      v-if="groupSelectorKeyId !== null && dropdownPosition"
      ref="dropdownRef"
      class="animate-in fade-in slide-in-from-top-2 fixed z-[100000020] w-64 overflow-hidden rounded-xl bg-white shadow-lg ring-1 ring-black/5 duration-200 dark:bg-dark-800 dark:ring-white/10"
      :style="{ top: dropdownPosition.top + 'px', left: dropdownPosition.left + 'px' }"
    >
      <div class="max-h-64 overflow-y-auto p-1.5">
        <button class="flex w-full rounded-lg px-3 py-2 text-sm hover:bg-gray-100 dark:hover:bg-dark-700" @click="changeScope(selectedKeyForGroup!, 'public')">{{ t('keys.scopePublic') }}</button>
        <button class="flex w-full rounded-lg px-3 py-2 text-sm hover:bg-gray-100 dark:hover:bg-dark-700" @click="openMultiGroupSelector(selectedKeyForGroup!)">{{ t('keys.scopeSelected') }}</button>
        <!-- Unbind option -->
        <button
          @click="changeGroup(selectedKeyForGroup!, null)"
          :class="[
            'flex w-full items-center rounded-lg px-3 py-2 text-sm transition-colors',
            (selectedKeyForGroup?.group_scope || 'single') === 'single' && !selectedKeyForGroup?.group_id
              ? 'bg-primary-50 dark:bg-primary-900/20'
              : 'hover:bg-gray-100 dark:hover:bg-dark-700'
          ]"
        >
          <span class="text-gray-500 italic">{{ t('admin.users.none') }}</span>
          <svg
            v-if="(selectedKeyForGroup?.group_scope || 'single') === 'single' && !selectedKeyForGroup?.group_id"
            class="ml-auto h-4 w-4 shrink-0 text-primary-600 dark:text-primary-400"
            fill="none" stroke="currentColor" viewBox="0 0 24 24" stroke-width="2"
          ><path stroke-linecap="round" stroke-linejoin="round" d="M5 13l4 4L19 7" /></svg>
        </button>
        <!-- Group options -->
        <button
          v-for="group in allGroups"
          :key="group.id"
          @click="changeGroup(selectedKeyForGroup!, group.id)"
          :class="[
            'flex w-full items-center justify-between rounded-lg px-3 py-2 text-sm transition-colors',
            selectedKeyForGroup?.group_id === group.id
              ? 'bg-primary-50 dark:bg-primary-900/20'
              : 'hover:bg-gray-100 dark:hover:bg-dark-700'
          ]"
        >
          <GroupOptionItem
            :name="group.name"
            :platform="group.platform"
            :subscription-type="group.subscription_type"
            :rate-multiplier="group.rate_multiplier"
            :description="group.description"
            :selected="selectedKeyForGroup?.group_id === group.id"
          />
        </button>
      </div>
    </div>
  </Teleport>
  <BaseDialog :show="!!scopeEditingKey" :title="t('keys.scopeSelected')" width="normal" @close="scopeEditingKey = null">
    <div class="max-h-80 space-y-2 overflow-y-auto">
      <label v-for="id in scopeGroupIds.filter(id => !allGroups.some(group => group.id === id && group.status === 'active'))" :key="`unavailable-${id}`" class="flex items-center gap-3 rounded-lg border border-amber-200 p-3 text-amber-700 dark:border-amber-800 dark:text-amber-300">
        <input v-model="scopeGroupIds" type="checkbox" :value="id" class="checkbox" />
        <span>{{ t('keys.unavailableGroup', { id }) }}</span>
      </label>
      <label v-for="group in allGroups.filter(group => group.status === 'active')" :key="group.id" class="flex items-center gap-3 rounded-lg border border-gray-200 p-3 dark:border-dark-600">
        <input v-model="scopeGroupIds" type="checkbox" :value="group.id" class="checkbox" />
        <GroupBadge :name="group.name" :platform="group.platform" :subscription-type="group.subscription_type" :rate-multiplier="group.rate_multiplier" />
      </label>
    </div>
    <template #footer>
      <button class="btn btn-primary" :disabled="!scopeGroupIds.length || !!scopeEditingKey && updatingKeyIds.has(scopeEditingKey.id)" @click="changeScope(scopeEditingKey!, 'selected', scopeGroupIds)">{{ t('common.save') }}</button>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { ref, computed, watch, onMounted, onUnmounted, type ComponentPublicInstance } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAppStore } from '@/stores/app'
import { adminAPI } from '@/api/admin'
import { formatDateTime } from '@/utils/format'
import type { AdminUser, AdminGroup, ApiKey } from '@/types'
import BaseDialog from '@/components/common/BaseDialog.vue'
import GroupBadge from '@/components/common/GroupBadge.vue'
import GroupOptionItem from '@/components/common/GroupOptionItem.vue'

const props = defineProps<{ show: boolean; user: AdminUser | null }>()
const emit = defineEmits(['close'])
const { t } = useI18n()
const appStore = useAppStore()

const apiKeys = ref<ApiKey[]>([])
const allGroups = ref<AdminGroup[]>([])
const scopeEditingKey = ref<ApiKey | null>(null)
const scopeGroupIds = ref<number[]>([])
const loading = ref(false)
const provisioning = ref(false)
const oidcIdentity = ref({ issuer: '', subject: '' })
const updatingKeyIds = ref(new Set<number>())
const groupSelectorKeyId = ref<number | null>(null)
const dropdownPosition = ref<{ top: number; left: number } | null>(null)
const dropdownRef = ref<HTMLElement | null>(null)
const scrollContainerRef = ref<HTMLElement | null>(null)
const groupButtonRefs = ref<Map<number, HTMLElement>>(new Map())

const selectedKeyForGroup = computed(() => {
  if (groupSelectorKeyId.value === null) return null
  return apiKeys.value.find((k) => k.id === groupSelectorKeyId.value) || null
})
const oidcGatewayKey = computed(() => apiKeys.value.find((key) => key.oidc_managed))

const setGroupButtonRef = (keyId: number, el: Element | ComponentPublicInstance | null) => {
  if (el instanceof HTMLElement) {
    groupButtonRefs.value.set(keyId, el)
  } else {
    groupButtonRefs.value.delete(keyId)
  }
}

watch(() => props.show, (v) => {
  if (v && props.user) {
    oidcIdentity.value = { issuer: '', subject: '' }
    load()
    loadGroups()
  } else {
    closeGroupSelector()
  }
})

const load = async () => {
  if (!props.user) return
  loading.value = true
  groupButtonRefs.value.clear()
  try {
    const res = await adminAPI.users.getUserApiKeys(props.user.id)
    apiKeys.value = res.items || []
  } catch (error) {
    console.error('Failed to load API keys:', error)
  } finally {
    loading.value = false
  }
}

const provisionOIDCIdentity = async () => {
  if (!props.user || provisioning.value) return
  provisioning.value = true
  try {
    await adminAPI.apiKeys.provisionOIDCGatewayIdentity(props.user.id, {
      issuer: oidcIdentity.value.issuer,
      subject: oidcIdentity.value.subject
    })
    await load()
    appStore.showSuccess(t('admin.users.oidcIdentityProvisioned'))
  } catch (error: any) {
    appStore.showError(error?.response?.data?.detail || error?.response?.data?.message || t('admin.users.oidcIdentityProvisionFailed'))
  } finally {
    provisioning.value = false
  }
}

const loadGroups = async () => {
  try {
    const groups = await adminAPI.groups.getAll()
    allGroups.value = groups
  } catch (error) {
    console.error('Failed to load groups:', error)
  }
}

const DROPDOWN_HEIGHT = 272 // max-h-64 = 16rem = 256px + padding
const DROPDOWN_GAP = 4

const openGroupSelector = (key: ApiKey) => {
  if (groupSelectorKeyId.value === key.id) {
    closeGroupSelector()
  } else {
    const buttonEl = groupButtonRefs.value.get(key.id)
    if (buttonEl) {
      const rect = buttonEl.getBoundingClientRect()
      const spaceBelow = window.innerHeight - rect.bottom
      const openUpward = spaceBelow < DROPDOWN_HEIGHT && rect.top > spaceBelow
      dropdownPosition.value = {
        top: openUpward ? rect.top - DROPDOWN_HEIGHT - DROPDOWN_GAP : rect.bottom + DROPDOWN_GAP,
        left: rect.left
      }
    }
    groupSelectorKeyId.value = key.id
  }
}

const closeGroupSelector = () => {
  groupSelectorKeyId.value = null
  dropdownPosition.value = null
}

const changeGroup = async (key: ApiKey, newGroupId: number | null) => {
  closeGroupSelector()
  if ((key.group_scope || 'single') === 'single' && (key.group_id === newGroupId || (!key.group_id && newGroupId === null))) return

  updatingKeyIds.value.add(key.id)
  try {
    const result = await adminAPI.apiKeys.updateApiKeyGroup(key.id, newGroupId)
    // Update local data
    const idx = apiKeys.value.findIndex((k) => k.id === key.id)
    if (idx !== -1) {
      apiKeys.value[idx] = result.api_key
    }
    if (result.auto_granted_group_access && result.granted_group_name) {
      appStore.showSuccess(t('admin.users.groupChangedWithGrant', { group: result.granted_group_name }))
    } else {
      appStore.showSuccess(t('admin.users.groupChangedSuccess'))
    }
  } catch (error: any) {
    appStore.showError(error?.message || t('admin.users.groupChangeFailed'))
  } finally {
    updatingKeyIds.value.delete(key.id)
  }
}

const openMultiGroupSelector = (key: ApiKey) => {
  closeGroupSelector()
  scopeEditingKey.value = key
  scopeGroupIds.value = [...(key.group_ids || (key.group_id ? [key.group_id] : []))]
}

const changeScope = async (key: ApiKey, scope: 'public' | 'selected', ids: number[] = []) => {
  closeGroupSelector()
  if (scope === 'selected' && !ids.length) {
    appStore.showError(t('keys.selectedGroupsRequired'))
    return
  }
  updatingKeyIds.value.add(key.id)
  try {
    const result = await adminAPI.apiKeys.updateApiKeyScope(key.id, scope, ids)
    const idx = apiKeys.value.findIndex(item => item.id === key.id)
    if (idx !== -1) apiKeys.value[idx] = result.api_key
    scopeEditingKey.value = null
    appStore.showSuccess(t('admin.users.groupChangedSuccess'))
  } catch (error: any) {
    appStore.showError(error?.message || t('admin.users.groupChangeFailed'))
  } finally {
    updatingKeyIds.value.delete(key.id)
  }
}

const handleKeyDown = (event: KeyboardEvent) => {
  if (event.key === 'Escape' && groupSelectorKeyId.value !== null) {
    event.stopPropagation()
    closeGroupSelector()
  }
}

const handleClickOutside = (event: MouseEvent) => {
  const target = event.target as HTMLElement
  if (dropdownRef.value && !dropdownRef.value.contains(target)) {
    // Check if the click is on one of the group trigger buttons
    for (const el of groupButtonRefs.value.values()) {
      if (el.contains(target)) return
    }
    closeGroupSelector()
  }
}

const handleClose = () => {
  scopeEditingKey.value = null
  closeGroupSelector()
  emit('close')
}

onMounted(() => {
  document.addEventListener('click', handleClickOutside)
  document.addEventListener('keydown', handleKeyDown, true)
})

onUnmounted(() => {
  document.removeEventListener('click', handleClickOutside)
  document.removeEventListener('keydown', handleKeyDown, true)
})
</script>
