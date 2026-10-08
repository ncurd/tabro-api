import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { getOIDCAccountIdentity, getOIDCAccountSummary } from '../oidcAccount'

const { get } = vi.hoisted(() => ({ get: vi.fn() }))
vi.mock('../client', () => ({ apiClient: { get } }))

describe('OIDC account API', () => {
  beforeEach(() => get.mockReset())
  afterEach(() => vi.unstubAllGlobals())

  it('obtains the verified login binding from the API with cancellation', async () => {
    const signal = new AbortController().signal
    get.mockResolvedValue({ data: { status: 'unlinked' } })
    expect(await getOIDCAccountIdentity(signal)).toEqual({ status: 'unlinked' })
    expect(get).toHaveBeenCalledWith('/auth/oidc-account', { signal })
  })

  it('uses only Auth cookies for the cross-origin summary and forbids redirects', async () => {
    const summary = { issuer: 'https://auth.tabro.cn', subject: 'user-one', available_credits: '12.3' }
    const fetchRequest = vi.fn().mockResolvedValue({ ok: true, json: async () => summary })
    vi.stubGlobal('fetch', fetchRequest)
    const signal = new AbortController().signal
    const url = 'https://auth.tabro.cn/api/account/header-summary?client_id=tabro-llm'
    expect(await getOIDCAccountSummary(url, signal)).toEqual(summary)
    expect(fetchRequest).toHaveBeenCalledWith(url, {
      credentials: 'include', cache: 'no-store', redirect: 'error', signal,
      headers: { Accept: 'application/json' }
    })
    expect(get).not.toHaveBeenCalled()
  })

  it('rejects failures without reading or exposing an Auth error response body', async () => {
    const json = vi.fn()
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue({ ok: false, json }))
    await expect(getOIDCAccountSummary('https://auth.tabro.cn/api/account/header-summary', new AbortController().signal))
      .rejects.toThrow('Auth account summary unavailable')
    expect(json).not.toHaveBeenCalled()
  })
})
