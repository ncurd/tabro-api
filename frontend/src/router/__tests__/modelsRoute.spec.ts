import { describe, expect, it } from 'vitest'
import router from '../index'

describe('model management route', () => {
  it('uses the shared administrator authorization guard', () => {
    const route = router.resolve('/admin/models')
    expect(route.name).toBe('AdminModels')
    expect(route.meta.requiresAuth).toBe(true)
    expect(route.meta.requiresAdmin).toBe(true)
  })
})
