<template>
  <div class="min-h-screen bg-gray-50 text-gray-900 dark:bg-dark-950 dark:text-gray-100">
    <header class="border-b border-gray-200 bg-white/90 backdrop-blur dark:border-dark-800 dark:bg-dark-950/90">
      <div class="mx-auto flex max-w-7xl items-center justify-between px-6 py-4">
        <router-link to="/home" class="flex items-center gap-3">
          <img src="/logo.png" alt="Logo" class="h-9 w-9 rounded-lg object-contain" />
          <div>
            <div class="text-sm font-semibold text-gray-900 dark:text-white">Tabro Docs</div>
            <div class="text-xs text-gray-500 dark:text-dark-400">Gateway API Reference</div>
          </div>
        </router-link>
        <div class="flex items-center gap-2">
          <router-link to="/home" class="btn btn-ghost btn-sm">首页</router-link>
          <router-link to="/login" class="btn btn-primary btn-sm">登录</router-link>
        </div>
      </div>
    </header>

    <main class="mx-auto grid max-w-7xl gap-8 px-6 py-8 lg:grid-cols-[280px_minmax(0,1fr)]">
      <aside class="lg:sticky lg:top-8 lg:h-[calc(100vh-4rem)]">
        <div class="rounded-lg border border-gray-200 bg-white p-3 shadow-sm dark:border-dark-800 dark:bg-dark-900">
          <div class="px-3 pb-2 text-xs font-semibold uppercase tracking-wide text-gray-400">文档</div>
          <nav class="space-y-1">
            <router-link
              v-for="doc in docs"
              :key="doc.slug"
              :to="`/docs/${doc.slug}`"
              class="block rounded-md px-3 py-2 text-sm transition-colors"
              :class="doc.slug === currentDoc?.slug
                ? 'bg-primary-50 font-semibold text-primary-700 dark:bg-primary-950/40 dark:text-primary-200'
                : 'text-gray-600 hover:bg-gray-100 dark:text-dark-300 dark:hover:bg-dark-800'"
            >
              {{ doc.title }}
              <span class="mt-0.5 block text-xs font-normal text-gray-400">{{ doc.category }}</span>
            </router-link>
          </nav>
        </div>
      </aside>

      <section class="min-w-0 rounded-lg border border-gray-200 bg-white p-6 shadow-sm dark:border-dark-800 dark:bg-dark-900 md:p-8">
        <div v-if="currentDoc" class="mb-6 border-b border-gray-200 pb-5 dark:border-dark-800">
          <div class="mb-2 flex flex-wrap items-center gap-2">
            <span class="rounded-full bg-primary-50 px-2.5 py-1 text-xs font-medium text-primary-700 dark:bg-primary-950/40 dark:text-primary-200">
              {{ currentDoc.category }}
            </span>
            <span v-if="currentDoc.updatedAt" class="text-xs text-gray-400">
              Updated {{ currentDoc.updatedAt }}
            </span>
          </div>
          <h1 class="text-3xl font-bold text-gray-900 dark:text-white">{{ currentDoc.title }}</h1>
          <p v-if="currentDoc.description" class="mt-2 max-w-3xl text-sm text-gray-500 dark:text-dark-300">
            {{ currentDoc.description }}
          </p>
        </div>

        <MdxDocument v-if="currentDoc" :source="currentDoc.source" />
      </section>
    </main>
  </div>
</template>

<script setup lang="ts">
import { computed, watchEffect } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import MdxDocument from '@/components/docs/MdxDocument.vue'
import { docs, getDoc } from '@/docs'

const route = useRoute()
const router = useRouter()

const currentDoc = computed(() => getDoc(route.params.slug as string | undefined))

watchEffect(() => {
  if (route.name === 'DocsIndex' && docs[0]) {
    void router.replace(`/docs/${docs[0].slug}`)
  }
})
</script>
