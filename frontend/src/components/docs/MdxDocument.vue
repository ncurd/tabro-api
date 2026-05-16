<template>
  <article class="doc-content">
    <template v-for="(segment, index) in segments" :key="index">
      <DocChart v-if="segment.type === 'chart'" :config="segment.chart" />
      <div v-else v-html="segment.html"></div>
    </template>
  </article>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { marked } from 'marked'
import type { Token } from 'marked'
import DOMPurify from 'dompurify'
import DocChart from './DocChart.vue'

interface HtmlSegment {
  type: 'html'
  html: string
}

interface ChartSegment {
  type: 'chart'
  chart: Record<string, unknown> | null
}

type Segment = HtmlSegment | ChartSegment

const props = defineProps<{
  source: string
}>()

marked.setOptions({
  breaks: true,
  gfm: true,
})

function renderMarkdownTokens(tokens: Token[]): string {
  if (tokens.length === 0) {
    return ''
  }
  const html = marked.parser(tokens) as string
  return DOMPurify.sanitize(html, {
    ADD_ATTR: ['target', 'rel'],
  })
}

function parseChart(raw: string): Record<string, unknown> | null {
  try {
    const parsed = JSON.parse(raw)
    return parsed && typeof parsed === 'object' ? parsed : null
  } catch {
    return null
  }
}

const segments = computed<Segment[]>(() => {
  const tokens = marked.lexer(props.source)
  const result: Segment[] = []
  let markdownBuffer: Token[] = []

  const flushMarkdown = () => {
    const html = renderMarkdownTokens(markdownBuffer)
    if (html) {
      result.push({ type: 'html', html })
    }
    markdownBuffer = []
  }

  for (const token of tokens) {
    if (token.type === 'code' && token.lang?.trim().toLowerCase() === 'chart') {
      flushMarkdown()
      result.push({ type: 'chart', chart: parseChart(token.text) })
      continue
    }
    markdownBuffer.push(token)
  }

  flushMarkdown()
  return result
})
</script>

<style scoped>
.doc-content :deep(h1) {
  margin-bottom: 0.75rem;
  font-size: 2.25rem;
  line-height: 1.15;
  font-weight: 750;
  color: rgb(17 24 39);
}

.dark .doc-content :deep(h1) {
  color: white;
}

.doc-content :deep(h2) {
  margin-top: 2.25rem;
  margin-bottom: 0.85rem;
  border-top: 1px solid rgb(229 231 235);
  padding-top: 1.5rem;
  font-size: 1.4rem;
  line-height: 1.25;
  font-weight: 700;
  color: rgb(31 41 55);
}

.dark .doc-content :deep(h2) {
  border-top-color: rgb(55 65 81);
  color: rgb(243 244 246);
}

.doc-content :deep(h3) {
  margin-top: 1.5rem;
  margin-bottom: 0.5rem;
  font-size: 1.05rem;
  font-weight: 700;
  color: rgb(31 41 55);
}

.dark .doc-content :deep(h3) {
  color: rgb(243 244 246);
}

.doc-content :deep(p),
.doc-content :deep(li) {
  color: rgb(75 85 99);
  line-height: 1.75;
}

.dark .doc-content :deep(p),
.dark .doc-content :deep(li) {
  color: rgb(209 213 219);
}

.doc-content :deep(a) {
  color: rgb(13 148 136);
  text-decoration: none;
}

.doc-content :deep(a:hover) {
  text-decoration: underline;
}

.doc-content :deep(ul),
.doc-content :deep(ol) {
  margin: 0.75rem 0 1rem 1.25rem;
}

.doc-content :deep(ul) {
  list-style: disc;
}

.doc-content :deep(ol) {
  list-style: decimal;
}

.doc-content :deep(table) {
  margin: 1rem 0 1.25rem;
  width: 100%;
  border-collapse: collapse;
  overflow: hidden;
  border-radius: 0.5rem;
  font-size: 0.9rem;
}

.doc-content :deep(th),
.doc-content :deep(td) {
  border: 1px solid rgb(229 231 235);
  padding: 0.65rem 0.75rem;
  text-align: left;
  vertical-align: top;
}

.dark .doc-content :deep(th),
.dark .doc-content :deep(td) {
  border-color: rgb(55 65 81);
}

.doc-content :deep(th) {
  background: rgb(249 250 251);
  color: rgb(31 41 55);
  font-weight: 700;
}

.dark .doc-content :deep(th) {
  background: rgb(17 24 39);
  color: rgb(243 244 246);
}

.doc-content :deep(code) {
  border-radius: 0.35rem;
  background: rgb(243 244 246);
  padding: 0.12rem 0.3rem;
  color: rgb(15 118 110);
  font-size: 0.88em;
}

.dark .doc-content :deep(code) {
  background: rgb(31 41 55);
  color: rgb(94 234 212);
}

.doc-content :deep(pre) {
  margin: 1rem 0;
  overflow-x: auto;
  border-radius: 0.5rem;
  background: rgb(17 24 39);
  padding: 1rem;
}

.doc-content :deep(pre code) {
  background: transparent;
  padding: 0;
  color: rgb(229 231 235);
}

.doc-content :deep(blockquote) {
  margin: 1rem 0;
  border-left: 4px solid rgb(20 184 166);
  background: rgb(240 253 250);
  padding: 0.8rem 1rem;
}

.dark .doc-content :deep(blockquote) {
  background: rgba(20, 184, 166, 0.12);
}
</style>
