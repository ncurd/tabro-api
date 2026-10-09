import { beforeEach, describe, expect, it, vi } from 'vitest'

const client = vi.hoisted(() => ({ post: vi.fn(), put: vi.fn() }))
vi.mock('../client', () => ({ apiClient: client }))
import { create, update } from '../keys'
import { updateApiKeyScope } from '../admin/apiKeys'

describe('key group scope requests', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    client.post.mockResolvedValue({ data: {} })
    client.put.mockResolvedValue({ data: {} })
  })

  it('creates a public key without a legacy binding', async () => {
    await create('public', null, undefined, undefined, undefined, undefined, undefined, undefined, { group_scope: 'public', group_ids: [] })
    expect(client.post).toHaveBeenCalledWith('/keys', { name: 'public', group_id: null, group_scope: 'public', group_ids: [] })
  })

  it('retains the legacy single-group create contract', async () => {
    await create('legacy', 42)
    expect(client.post).toHaveBeenCalledWith('/keys', { name: 'legacy', group_id: 42 })
  })

  it('sends selected IDs and preserves an explicit empty selection', async () => {
    await update(7, { group_scope: 'selected', group_ids: [1, 2] })
    expect(client.put).toHaveBeenLastCalledWith('/keys/7', { group_scope: 'selected', group_ids: [1, 2] })
    await update(7, { group_ids: [] })
    expect(client.put).toHaveBeenLastCalledWith('/keys/7', { group_ids: [] })
  })

  it('updates admin scope without treating null as unbind', async () => {
    await updateApiKeyScope(7, 'public')
    expect(client.put).toHaveBeenCalledWith('/admin/api-keys/7', { group_scope: 'public', group_ids: [] })
  })
})
