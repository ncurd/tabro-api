import { apiClient } from '../client'

export type ModelBillingMode = 'token' | 'duration' | 'per_request' | 'image' | 'audio' | 'video'
export type ModelPriceUnit = 'million_tokens' | 'second' | 'character' | 'image' | 'request'

export interface AdminModelPrice {
  billing_mode: ModelBillingMode
  price_unit: ModelPriceUnit
  input_price_per_million?: number
  output_price_per_million?: number
  cache_write_price_per_million?: number
  cache_read_price_per_million?: number
  input_audio_price_per_second?: number
  output_audio_price_per_second?: number
  output_video_price_per_second?: number
  unit_price?: number
}

export interface AdminModelPolicy {
  model: string
  platforms: string[]
  enabled: boolean
  pricing: AdminModelPrice | null
  default_pricing: AdminModelPrice | null
  source: 'manual' | 'automatic' | 'unpriced'
  updated_at?: string
  channel_override: boolean
}

export interface AdminModelList {
  items: AdminModelPolicy[]
  total: number
  page: number
  page_size: number
}

export interface UpdateAdminModelRequest {
  model: string
  enabled: boolean
  pricing: AdminModelPrice | null
}

export const adminModelsAPI = {
  async list(params: { search?: string; page?: number; page_size?: number; enabled?: boolean }, signal?: AbortSignal): Promise<AdminModelList> {
    const { data } = await apiClient.get<AdminModelList>('/admin/models', { params, signal })
    return data
  },
  async update(body: UpdateAdminModelRequest): Promise<AdminModelPolicy> {
    const { data } = await apiClient.put<AdminModelPolicy>('/admin/models', body)
    return data
  },
  async resetPrice(model: string): Promise<AdminModelPolicy> {
    const { data } = await apiClient.post<AdminModelPolicy>('/admin/models/reset-price', { model })
    return data
  }
}
