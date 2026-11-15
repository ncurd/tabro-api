package handler

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// Filter at response time rather than caching availability with the upstream
// catalogue. A disabled public ID disappears immediately on every replica.
func writeModelPolicyCatalog(c *gin.Context, payload any, google bool) {
	policy := service.ModelAvailabilityPolicyFromContext(c.Request.Context())
	if policy == nil {
		c.JSON(http.StatusOK, payload)
		return
	}
	c.Header("Cache-Control", "no-store")
	raw, err := json.Marshal(payload)
	if err == nil {
		raw, err = filterModelPolicyCatalog(c, raw, policy)
	}
	if err != nil {
		if google {
			googleError(c, http.StatusServiceUnavailable, "Model availability settings are temporarily unavailable")
		} else {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": gin.H{"type": "api_error", "code": "MODEL_POLICY_UNAVAILABLE", "message": "Model availability settings are temporarily unavailable"}})
		}
		return
	}
	c.Data(http.StatusOK, "application/json; charset=utf-8", raw)
}

func writeModelPolicyUpstreamCatalog(c *gin.Context, res *service.UpstreamHTTPResult) {
	policy := service.ModelAvailabilityPolicyFromContext(c.Request.Context())
	if res == nil || res.StatusCode != http.StatusOK || policy == nil {
		writeUpstreamResponse(c, res)
		return
	}
	filtered, err := filterModelPolicyCatalog(c, res.Body, policy)
	if err != nil {
		googleError(c, http.StatusServiceUnavailable, "Model availability settings are temporarily unavailable")
		return
	}
	copyResult := *res
	copyResult.Headers = res.Headers.Clone()
	if copyResult.Headers == nil {
		copyResult.Headers = make(http.Header)
	}
	copyResult.Headers.Del("ETag")
	copyResult.Headers.Set("Cache-Control", "no-store")
	copyResult.Body = filtered
	writeUpstreamResponse(c, &copyResult)
}

func filterModelPolicyCatalog(c *gin.Context, raw []byte, policy service.ModelAvailabilityPolicy) ([]byte, error) {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, err
	}
	key := "data"
	if _, ok := envelope[key]; !ok {
		key = "models"
	}
	var rows []json.RawMessage
	if err := json.Unmarshal(envelope[key], &rows); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		var model struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		}
		if err := json.Unmarshal(row, &model); err != nil {
			return nil, err
		}
		id := model.ID
		if id == "" {
			id = strings.TrimPrefix(model.Name, "models/")
		}
		ids = append(ids, id)
	}
	enabled, err := policy.FilterEnabledModels(c.Request.Context(), ids)
	if err != nil {
		return nil, err
	}
	allowed := make(map[string]bool, len(enabled))
	for _, id := range enabled {
		allowed[strings.ToLower(id)] = true
	}
	filtered := make([]json.RawMessage, 0, len(rows))
	for i, row := range rows {
		if allowed[strings.ToLower(ids[i])] {
			filtered = append(filtered, row)
		}
	}
	envelope[key], err = json.Marshal(filtered)
	if err != nil {
		return nil, err
	}
	return json.Marshal(envelope)
}
