package service

import (
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"go.uber.org/zap"
)

func reconstructBufferedResponsesResponse(
	acc *apicompat.BufferedResponseAccumulator,
	finalResponse *apicompat.ResponsesResponse,
	terminalEventType string,
	requestID string,
	scope string,
) *apicompat.ResponsesResponse {
	if finalResponse != nil {
		acc.SupplementResponseOutput(finalResponse)
		return finalResponse
	}
	if acc == nil || !acc.HasContent() {
		return nil
	}

	terminalEventType = strings.TrimSpace(terminalEventType)
	if terminalEventType == "response.failed" {
		return nil
	}

	status := "completed"
	if terminalEventType == "response.incomplete" {
		status = "incomplete"
	}

	synthesized := &apicompat.ResponsesResponse{Status: status}
	acc.SupplementResponseOutput(synthesized)
	if len(synthesized.Output) == 0 {
		return nil
	}

	logger.L().Warn("openai buffered: synthesized final response from streamed deltas",
		zap.String("scope", scope),
		zap.String("request_id", requestID),
		zap.String("terminal_event_type", terminalEventType),
	)
	return synthesized
}
