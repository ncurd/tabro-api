package service

import (
	"bytes"
	"encoding/json"
	"sort"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
)

type chatCompletionsChoiceAccumulator struct {
	role            string
	content         strings.Builder
	reasoning       strings.Builder
	toolCalls       map[int]*apicompat.ChatToolCall
	toolCallOrder   []int
	finishReason    string
	hasFinishReason bool
}

func extractChatCompletionsResponseFromSSEBody(body string, fallbackModel string) (*apicompat.ChatCompletionsResponse, bool) {
	lines := strings.Split(body, "\n")

	var (
		sawChunk          bool
		id                string
		model             string
		systemFingerprint string
		serviceTier       string
		created           int64
		usage             *apicompat.ChatUsage
	)
	choices := make(map[int]*chatCompletionsChoiceAccumulator)

	for _, line := range lines {
		data, ok := extractOpenAISSEDataLine(line)
		if !ok || data == "" || data == "[DONE]" {
			continue
		}

		var chunk apicompat.ChatCompletionsChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}
		if strings.TrimSpace(chunk.Object) != "chat.completion.chunk" {
			continue
		}

		sawChunk = true
		if chunk.ID != "" {
			id = chunk.ID
		}
		if chunk.Model != "" {
			model = chunk.Model
		}
		if chunk.Created > 0 {
			created = chunk.Created
		}
		if chunk.SystemFingerprint != "" {
			systemFingerprint = chunk.SystemFingerprint
		}
		if chunk.ServiceTier != "" {
			serviceTier = chunk.ServiceTier
		}
		if chunk.Usage != nil {
			cloned := *chunk.Usage
			if cloned.TotalTokens == 0 && (cloned.PromptTokens > 0 || cloned.CompletionTokens > 0) {
				cloned.TotalTokens = cloned.PromptTokens + cloned.CompletionTokens
			}
			usage = &cloned
		}

		for _, choice := range chunk.Choices {
			acc := choices[choice.Index]
			if acc == nil {
				acc = &chatCompletionsChoiceAccumulator{}
				choices[choice.Index] = acc
			}
			if choice.Delta.Role != "" {
				acc.role = choice.Delta.Role
			}
			if choice.Delta.Content != nil {
				acc.content.WriteString(*choice.Delta.Content)
			}
			if choice.Delta.ReasoningContent != nil {
				acc.reasoning.WriteString(*choice.Delta.ReasoningContent)
			}
			mergeChatCompletionToolCalls(acc, choice.Delta.ToolCalls)
			if choice.FinishReason != nil {
				acc.finishReason = *choice.FinishReason
				acc.hasFinishReason = true
			}
		}
	}

	if !sawChunk || len(choices) == 0 {
		return nil, false
	}

	if model == "" {
		model = strings.TrimSpace(fallbackModel)
	}

	indices := make([]int, 0, len(choices))
	for idx := range choices {
		indices = append(indices, idx)
	}
	sort.Ints(indices)

	outChoices := make([]apicompat.ChatChoice, 0, len(indices))
	for _, idx := range indices {
		acc := choices[idx]
		msg := apicompat.ChatMessage{Role: acc.role}
		if msg.Role == "" {
			msg.Role = "assistant"
		}
		if text := acc.content.String(); text != "" {
			raw, _ := json.Marshal(text)
			msg.Content = raw
		}
		if reasoning := acc.reasoning.String(); reasoning != "" {
			msg.ReasoningContent = reasoning
		}
		if len(acc.toolCallOrder) > 0 {
			msg.ToolCalls = make([]apicompat.ChatToolCall, 0, len(acc.toolCallOrder))
			for _, toolIdx := range acc.toolCallOrder {
				tool := acc.toolCalls[toolIdx]
				if tool == nil {
					continue
				}
				cloned := *tool
				cloned.Index = nil
				if cloned.Type == "" {
					cloned.Type = "function"
				}
				msg.ToolCalls = append(msg.ToolCalls, cloned)
			}
		}

		finishReason := "stop"
		if acc.hasFinishReason && acc.finishReason != "" {
			finishReason = acc.finishReason
		} else if len(msg.ToolCalls) > 0 {
			finishReason = "tool_calls"
		}

		outChoices = append(outChoices, apicompat.ChatChoice{
			Index:        idx,
			Message:      msg,
			FinishReason: finishReason,
		})
	}

	return &apicompat.ChatCompletionsResponse{
		ID:                id,
		Object:            "chat.completion",
		Created:           created,
		Model:             model,
		Choices:           outChoices,
		Usage:             usage,
		SystemFingerprint: systemFingerprint,
		ServiceTier:       serviceTier,
	}, true
}

func mergeChatCompletionToolCalls(acc *chatCompletionsChoiceAccumulator, deltas []apicompat.ChatToolCall) {
	if acc == nil || len(deltas) == 0 {
		return
	}
	if acc.toolCalls == nil {
		acc.toolCalls = make(map[int]*apicompat.ChatToolCall, len(deltas))
	}
	for _, delta := range deltas {
		idx := resolveChatCompletionToolCallIndex(acc, delta)
		tool := acc.toolCalls[idx]
		if tool == nil {
			tool = &apicompat.ChatToolCall{Index: chatToolIndexPtr(idx)}
			acc.toolCalls[idx] = tool
			acc.toolCallOrder = append(acc.toolCallOrder, idx)
		}
		if delta.ID != "" {
			tool.ID = delta.ID
		}
		if delta.Type != "" {
			tool.Type = delta.Type
		}
		if delta.Function.Name != "" {
			tool.Function.Name += delta.Function.Name
		}
		if delta.Function.Arguments != "" {
			tool.Function.Arguments += delta.Function.Arguments
		}
		if tool.Type == "" {
			tool.Type = "function"
		}
	}
}

func resolveChatCompletionToolCallIndex(acc *chatCompletionsChoiceAccumulator, delta apicompat.ChatToolCall) int {
	if delta.Index != nil {
		return *delta.Index
	}
	if delta.ID != "" {
		for idx, tool := range acc.toolCalls {
			if tool != nil && tool.ID == delta.ID {
				return idx
			}
		}
	}
	next := len(acc.toolCallOrder)
	for {
		if _, exists := acc.toolCalls[next]; !exists {
			return next
		}
		next++
	}
}

func chatCompletionsResponseToResponsesResponse(chatResp *apicompat.ChatCompletionsResponse) *apicompat.ResponsesResponse {
	if chatResp == nil {
		return nil
	}

	resp := &apicompat.ResponsesResponse{
		ID:     chatResp.ID,
		Object: "response",
		Model:  chatResp.Model,
		Status: "completed",
	}

	if chatResp.Usage != nil {
		resp.Usage = &apicompat.ResponsesUsage{
			InputTokens:  chatResp.Usage.PromptTokens,
			OutputTokens: chatResp.Usage.CompletionTokens,
			TotalTokens:  chatResp.Usage.TotalTokens,
		}
		if resp.Usage.TotalTokens == 0 && (resp.Usage.InputTokens > 0 || resp.Usage.OutputTokens > 0) {
			resp.Usage.TotalTokens = resp.Usage.InputTokens + resp.Usage.OutputTokens
		}
		if chatResp.Usage.PromptTokensDetails != nil {
			resp.Usage.InputTokensDetails = &apicompat.ResponsesInputTokensDetails{
				CachedTokens: chatResp.Usage.PromptTokensDetails.CachedTokens,
			}
		}
	}

	if len(chatResp.Choices) == 0 {
		return resp
	}

	choice := chatResp.Choices[0]
	switch choice.FinishReason {
	case "length":
		resp.Status = "incomplete"
		resp.IncompleteDetails = &apicompat.ResponsesIncompleteDetails{Reason: "max_output_tokens"}
	case "content_filter":
		resp.Status = "incomplete"
		resp.IncompleteDetails = &apicompat.ResponsesIncompleteDetails{Reason: "content_filter"}
	default:
		resp.Status = "completed"
	}

	var output []apicompat.ResponsesOutput
	if reasoning := strings.TrimSpace(choice.Message.ReasoningContent); reasoning != "" {
		output = append(output, apicompat.ResponsesOutput{
			Type: "reasoning",
			Summary: []apicompat.ResponsesSummary{{
				Type: "summary_text",
				Text: reasoning,
			}},
		})
	}

	if text := extractChatMessageTextContent(choice.Message.Content); text != "" {
		output = append(output, apicompat.ResponsesOutput{
			Type:   "message",
			Role:   normalizeChatAssistantRole(choice.Message.Role),
			Status: resp.Status,
			Content: []apicompat.ResponsesContentPart{{
				Type: "output_text",
				Text: text,
			}},
		})
	} else if len(choice.Message.ToolCalls) == 0 && len(output) == 0 {
		output = append(output, apicompat.ResponsesOutput{
			Type:   "message",
			Role:   normalizeChatAssistantRole(choice.Message.Role),
			Status: resp.Status,
		})
	}

	for _, toolCall := range choice.Message.ToolCalls {
		output = append(output, apicompat.ResponsesOutput{
			Type:      "function_call",
			CallID:    toolCall.ID,
			Name:      toolCall.Function.Name,
			Arguments: toolCall.Function.Arguments,
		})
	}

	resp.Output = output
	return resp
}

func extractChatMessageTextContent(raw json.RawMessage) string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return ""
	}

	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text
	}

	var parts []apicompat.ChatContentPart
	if err := json.Unmarshal(raw, &parts); err != nil {
		return ""
	}
	var builder strings.Builder
	for _, part := range parts {
		if part.Type == "text" && part.Text != "" {
			builder.WriteString(part.Text)
		}
	}
	return builder.String()
}

func normalizeChatAssistantRole(role string) string {
	role = strings.TrimSpace(role)
	if role == "" {
		return "assistant"
	}
	return role
}

func openAIUsageFromChatCompletionsResponse(chatResp *apicompat.ChatCompletionsResponse) OpenAIUsage {
	if chatResp == nil || chatResp.Usage == nil {
		return OpenAIUsage{}
	}
	usage := OpenAIUsage{
		InputTokens:  chatResp.Usage.PromptTokens,
		OutputTokens: chatResp.Usage.CompletionTokens,
	}
	if chatResp.Usage.PromptTokensDetails != nil {
		usage.CacheReadInputTokens = chatResp.Usage.PromptTokensDetails.CachedTokens
	}
	return usage
}

func chatToolIndexPtr(v int) *int {
	return &v
}
