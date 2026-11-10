import type { DailyCheckInSummary } from '@/api/oidcAccount'

export function readDailyCheckIn(input: unknown): DailyCheckInSummary | null {
  if (!input || typeof input !== 'object' || Array.isArray(input)) return null
  const value = input as DailyCheckInSummary
  if (!['available', 'claimed', 'disabled'].includes(value.status)
    || typeof value.day !== 'string' || !/^\d{4}-\d{2}-\d{2}$/.test(value.day)
    || !Number.isInteger(value.minimum_points) || value.minimum_points < 1
    || !Number.isInteger(value.maximum_points) || value.maximum_points < value.minimum_points || value.maximum_points > 1000000
    || !Number.isInteger(value.validity_days) || value.validity_days < 1 || value.validity_days > 3660
    || value.status === 'claimed' && (!Number.isInteger(value.claimed_points) || value.claimed_points! < 1 || value.claimed_points! > 1000000)
    || value.expires_at != null && (typeof value.expires_at !== 'string' || !Number.isFinite(Date.parse(value.expires_at)))) return null
  return { status: value.status, day: value.day, minimum_points: value.minimum_points, maximum_points: value.maximum_points,
    validity_days: value.validity_days, claimed_points: value.status === 'claimed' ? value.claimed_points : null,
    expires_at: value.expires_at ?? null }
}
