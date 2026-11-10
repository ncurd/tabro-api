import { apiClient } from './client'

export interface OIDCAccountLinks {
  daily_check_in?: string
  credit_details?: string
  profile?: string
  security_settings?: string
}

export interface OIDCAccountIdentity {
  status: 'linked' | 'unlinked' | 'disabled'
  issuer: string | null
  subject: string | null
  client_id: string | null
  summary_url: string | null
  links: OIDCAccountLinks
}

export interface OIDCAccountSummary {
  daily_check_in?: DailyCheckInSummary | null
  issuer: string
  subject: string
  tenant_id: string | null
  status: 'available' | 'unavailable'
  available_credits: string | null
}

export interface DailyCheckInSummary {
  status: 'available' | 'claimed' | 'disabled'
  day: string
  minimum_points: number
  maximum_points: number
  validity_days: number
  claimed_points: number | null
  expires_at: string | null
}

/** The API supplies the verified OIDC login identity and trusted Auth URLs. */
export async function getOIDCAccountIdentity(signal?: AbortSignal): Promise<OIDCAccountIdentity> {
  const { data } = await apiClient.get<OIDCAccountIdentity>('/auth/oidc-account', { signal })
  return data
}

/** Auth authenticates its own cookie. Never forward the API access token to Auth. */
export async function getOIDCAccountSummary(url: string, signal: AbortSignal): Promise<OIDCAccountSummary> {
  const response = await fetch(url, {
    credentials: 'include',
    cache: 'no-store',
    redirect: 'error',
    signal,
    headers: { Accept: 'application/json' }
  })
  if (!response.ok) throw new Error('Auth account summary unavailable')
  return response.json() as Promise<OIDCAccountSummary>
}
