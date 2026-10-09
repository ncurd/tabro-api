/**
 * Admin API Keys API endpoints
 * Handles API key management for administrators
 */

import { apiClient } from '../client'
import type { ApiKey, ApiKeyGroupScope } from '@/types'

export interface UpdateApiKeyGroupResult {
  api_key: ApiKey
  auto_granted_group_access: boolean
  granted_group_id?: number
  granted_group_name?: string
}

export interface ProvisionOIDCGatewayIdentityResult {
  api_key: ApiKey
  issuer: string
  subject: string
}

/** Provision an internal billing key and bind one verified IdP identity to a user. */
export async function provisionOIDCGatewayIdentity(
  userId: number,
  identity: { issuer: string; subject: string }
): Promise<ProvisionOIDCGatewayIdentityResult> {
  const { data } = await apiClient.put<ProvisionOIDCGatewayIdentityResult>(
    `/admin/users/${userId}/oidc-gateway-identity`,
    identity
  )
  return data
}

/**
 * Update an API key's group binding
 * @param id - API Key ID
 * @param groupId - Group ID (0 to unbind, positive to bind, null/undefined to skip)
 * @returns Updated API key with auto-grant info
 */
export async function updateApiKeyGroup(id: number, groupId: number | null): Promise<UpdateApiKeyGroupResult> {
  const { data } = await apiClient.put<UpdateApiKeyGroupResult>(`/admin/api-keys/${id}`, {
    group_id: groupId === null ? 0 : groupId
  })
  return data
}

export async function updateApiKeyScope(id: number, groupScope: ApiKeyGroupScope, groupIds: number[] = []): Promise<UpdateApiKeyGroupResult> {
  const { data } = await apiClient.put<UpdateApiKeyGroupResult>(`/admin/api-keys/${id}`, {
    group_scope: groupScope,
    group_ids: groupIds
  })
  return data
}

export const apiKeysAPI = {
  updateApiKeyGroup,
  updateApiKeyScope,
  provisionOIDCGatewayIdentity
}

export default apiKeysAPI
