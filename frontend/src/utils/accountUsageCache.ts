import type { Account, AccountUsageInfo } from '@/types'
import { buildOpenAIUsageRefreshKey } from './accountUsageRefresh'
import { enqueueUsageRequest } from './usageLoadQueue'

const CACHE_TTL_MS = 5 * 60 * 1000
const MAX_CACHE_ENTRIES = 1000

interface CacheEntry {
  revision: string
  data: AccountUsageInfo
  expiresAt: number
}

interface PendingEntry {
  revision: string
  source?: 'passive' | 'active'
  promise: Promise<AccountUsageInfo>
}

// Virtual rows are frequently destroyed and recreated. Keep both completed and
// in-flight requests outside component setup so those rows share the same work.
const cache = new Map<number, CacheEntry>()
const pending = new Map<number, PendingEntry>()

export function clearAccountUsageCache(): void {
  cache.clear()
  pending.clear()
}

export function loadAccountUsage(
  account: Account,
  fetchUsage: () => Promise<AccountUsageInfo>,
  options?: { source?: 'passive' | 'active'; bypassCache?: boolean }
): Promise<AccountUsageInfo> {
  const id = account.id
  const revision = JSON.stringify([
    account.platform,
    account.type,
    account.updated_at,
    buildOpenAIUsageRefreshKey(account)
  ])
  if (options?.bypassCache) {
    cache.delete(id)
  } else {
    const cached = cache.get(id)
    if (cached && cached.revision === revision && cached.expiresAt > Date.now()) {
      // Refresh insertion order to evict the least recently used account.
      cache.delete(id)
      cache.set(id, cached)
      return Promise.resolve(cached.data)
    }
    cache.delete(id)

    const existing = pending.get(id)
    if (existing?.revision === revision && existing.source === options?.source) {
      return existing.promise
    }
  }

  const entry: PendingEntry = {
    revision,
    source: options?.source,
    // Explicit probes keep their immediate behavior instead of waiting behind
    // the passive Anthropic rows queued during table scrolling.
    promise: Promise.resolve().then(() => options?.source === 'active'
      ? fetchUsage()
      : enqueueUsageRequest(account, fetchUsage))
  }
  entry.promise = entry.promise.then((data) => {
    // A forced refresh or logout may supersede this request while it is queued.
    if (pending.get(id) === entry) {
      cache.delete(id)
      cache.set(id, { revision, data, expiresAt: Date.now() + CACHE_TTL_MS })
      if (cache.size > MAX_CACHE_ENTRIES) {
        const oldestId = cache.keys().next().value
        if (oldestId !== undefined) cache.delete(oldestId)
      }
    }
    return data
  }).finally(() => {
    if (pending.get(id) === entry) pending.delete(id)
  })
  pending.set(id, entry)
  return entry.promise
}
