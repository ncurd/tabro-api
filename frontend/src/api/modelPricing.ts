import { apiClient } from './client'

export interface AvailableModelPricingModel {
  id: string
  pricing_available: boolean
  input_price_per_million?: number
  output_price_per_million?: number
  cache_write_price_per_million?: number
  cache_read_price_per_million?: number
  priority_input_price_per_million?: number
  priority_output_price_per_million?: number
  priority_cache_read_price_per_million?: number
  image_output_price_per_million?: number
  source?: string
}

export interface AvailableModelPricingGroup {
  id: number
  name: string
  platform: string
  rate_multiplier: number
  effective_rate_multiplier: number
  models: AvailableModelPricingModel[]
}

export interface AvailableModelPricingResponse {
  groups: AvailableModelPricingGroup[]
}

export const modelPricingAPI = {
  async getAvailable(): Promise<AvailableModelPricingResponse> {
    const { data } = await apiClient.get<AvailableModelPricingResponse>('/model-pricing/available')
    return data
  }
}

export default modelPricingAPI
