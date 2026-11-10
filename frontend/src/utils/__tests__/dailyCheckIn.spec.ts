import { describe, expect, it } from 'vitest'
import { readDailyCheckIn } from '../dailyCheckIn'

const available = { status: 'available', day: '2026-11-10', minimum_points: 5, maximum_points: 50,
  validity_days: 30, claimed_points: null, expires_at: null }

describe('daily check-in summaries', () => {
  it('keeps the advertised range, configured validity and claimed expiry', () => {
    expect(readDailyCheckIn(available)).toEqual(available)
    const claimed = { ...available, status: 'claimed', claimed_points: 17, expires_at: '2026-12-10T06:00:00Z' }
    expect(readDailyCheckIn(claimed)).toEqual(claimed)
  })
  it.each([null, {}, { ...available, validity_days: 0 }, { ...available, maximum_points: 1 },
    { ...available, status: 'claimed' }, { ...available, expires_at: 'invalid' }])('rejects a malformed activity without advertising a reward', value => {
    expect(readDailyCheckIn(value)).toBeNull()
  })
})
