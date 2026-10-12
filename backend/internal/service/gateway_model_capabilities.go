package service

// GetTextModelCapabilities exposes the same exact catalog limits used to bound
// model requests. It never uses fuzzy price matching to infer model capacity.
func (s *GatewayService) GetTextModelCapabilities(model string) (TextModelCapabilities, bool) {
	var pricing *PricingService
	if s != nil && s.billingService != nil {
		pricing = s.billingService.pricingService
	}
	return pricing.GetTextModelCapabilities(model)
}
