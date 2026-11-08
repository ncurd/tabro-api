package service

// OpenAIImageRequestError describes an image request that cannot be executed
// by the selected image transport before sending anything to the provider.
type OpenAIImageRequestError struct {
	Message string
}

func (e *OpenAIImageRequestError) Error() string { return e.Message }
