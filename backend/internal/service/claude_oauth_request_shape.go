package service

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const claudeOAuthToolNamesReverseMapKey = "claude_oauth_tool_names_reverse_map"

var claudeOAuthToolRenameMap = map[string]string{
	"bash":         "Bash",
	"read":         "Read",
	"write":        "Write",
	"edit":         "Edit",
	"glob":         "Glob",
	"grep":         "Grep",
	"task":         "Task",
	"webfetch":     "WebFetch",
	"todowrite":    "TodoWrite",
	"question":     "Question",
	"skill":        "Skill",
	"ls":           "LS",
	"todoread":     "TodoRead",
	"notebookedit": "NotebookEdit",
}

var claudeOAuthToolsToRemove = map[string]bool{}

func extractClaudeOAuthBodyBetas(body []byte) []string {
	betasResult := gjson.GetBytes(body, "betas")
	if !betasResult.Exists() {
		return nil
	}

	betas := make([]string, 0, 4)
	if betasResult.IsArray() {
		betasResult.ForEach(func(_, item gjson.Result) bool {
			if s := strings.TrimSpace(item.String()); s != "" {
				betas = append(betas, s)
			}
			return true
		})
		return betas
	}

	if s := strings.TrimSpace(betasResult.String()); s != "" {
		betas = append(betas, s)
	}
	return betas
}

func extractAndRemoveClaudeOAuthBodyBetas(body []byte) ([]string, []byte) {
	betas := extractClaudeOAuthBodyBetas(body)
	if len(betas) == 0 {
		return nil, body
	}
	next, err := sjson.DeleteBytes(body, "betas")
	if err != nil {
		return betas, body
	}
	return betas, next
}

func mergeClaudeOAuthBodyBetasIntoHeader(bodyBetas []string, incoming string) string {
	if len(bodyBetas) == 0 {
		return incoming
	}
	return mergeAnthropicBeta(bodyBetas, incoming)
}

func disableClaudeThinkingIfToolChoiceForced(body []byte) ([]byte, bool) {
	toolChoiceType := gjson.GetBytes(body, "tool_choice.type").String()
	if toolChoiceType != "any" && toolChoiceType != "tool" {
		return body, false
	}

	out := body
	modified := false
	if gjson.GetBytes(out, "thinking").Exists() {
		if next, ok := deleteJSONPathBytes(out, "thinking"); ok {
			out = next
			modified = true
		}
	}
	if gjson.GetBytes(out, "output_config.effort").Exists() {
		if next, ok := deleteJSONPathBytes(out, "output_config.effort"); ok {
			out = next
			modified = true
		}
	}
	if oc := gjson.GetBytes(out, "output_config"); oc.Exists() && oc.IsObject() && len(oc.Map()) == 0 {
		if next, ok := deleteJSONPathBytes(out, "output_config"); ok {
			out = next
			modified = true
		}
	}
	return out, modified
}

func normalizeClaudeOAuthTemperatureForThinking(body []byte) ([]byte, bool) {
	if !gjson.GetBytes(body, "temperature").Exists() {
		return body, false
	}

	thinkingType := strings.ToLower(strings.TrimSpace(gjson.GetBytes(body, "thinking.type").String()))
	switch thinkingType {
	case "enabled", "adaptive", "auto":
		temp := gjson.GetBytes(body, "temperature")
		if temp.Exists() && temp.Type == gjson.Number && temp.Float() == 1 {
			return body, false
		}
		next, ok := setJSONValueBytes(body, "temperature", 1)
		return next, ok
	default:
		return body, false
	}
}

func normalizeClaudeOAuthThinkingControls(body []byte) ([]byte, bool) {
	out := body
	modified := false
	if next, changed := disableClaudeThinkingIfToolChoiceForced(out); changed {
		out = next
		modified = true
	}
	if next, changed := normalizeClaudeOAuthTemperatureForThinking(out); changed {
		out = next
		modified = true
	}
	return out, modified
}

func prepareClaudeOAuthToolNamesForUpstream(body []byte) ([]byte, map[string]string) {
	return remapClaudeOAuthToolNames(body)
}

func remapClaudeOAuthToolNames(body []byte) ([]byte, map[string]string) {
	reverseMap := make(map[string]string, len(claudeOAuthToolRenameMap))
	recordRename := func(original, renamed string) {
		if _, exists := reverseMap[renamed]; !exists {
			reverseMap[renamed] = original
		}
	}

	tools := gjson.GetBytes(body, "tools")
	if tools.Exists() && tools.IsArray() {
		var toolsJSON strings.Builder
		toolsJSON.WriteByte('[')
		toolCount := 0
		tools.ForEach(func(_, tool gjson.Result) bool {
			if tool.Get("type").Exists() && tool.Get("type").String() != "" {
				if toolCount > 0 {
					toolsJSON.WriteByte(',')
				}
				toolsJSON.WriteString(tool.Raw)
				toolCount++
				return true
			}

			name := tool.Get("name").String()
			if claudeOAuthToolsToRemove[name] {
				return true
			}

			toolJSON := tool.Raw
			if newName, ok := claudeOAuthToolRenameMap[name]; ok && newName != name {
				if updatedTool, err := sjson.Set(toolJSON, "name", newName); err == nil {
					toolJSON = updatedTool
					recordRename(name, newName)
				}
			}

			if toolCount > 0 {
				toolsJSON.WriteByte(',')
			}
			toolsJSON.WriteString(toolJSON)
			toolCount++
			return true
		})
		toolsJSON.WriteByte(']')
		body, _ = sjson.SetRawBytes(body, "tools", []byte(toolsJSON.String()))
	}

	if gjson.GetBytes(body, "tool_choice.type").String() == "tool" {
		tcName := gjson.GetBytes(body, "tool_choice.name").String()
		if claudeOAuthToolsToRemove[tcName] {
			body, _ = sjson.DeleteBytes(body, "tool_choice")
		} else if newName, ok := claudeOAuthToolRenameMap[tcName]; ok && newName != tcName {
			body, _ = sjson.SetBytes(body, "tool_choice.name", newName)
			recordRename(tcName, newName)
		}
	}

	messages := gjson.GetBytes(body, "messages")
	if messages.Exists() && messages.IsArray() {
		messages.ForEach(func(msgIndex, msg gjson.Result) bool {
			content := msg.Get("content")
			if !content.Exists() || !content.IsArray() {
				return true
			}
			content.ForEach(func(contentIndex, part gjson.Result) bool {
				switch part.Get("type").String() {
				case "tool_use":
					name := part.Get("name").String()
					if newName, ok := claudeOAuthToolRenameMap[name]; ok && newName != name {
						path := fmt.Sprintf("messages.%d.content.%d.name", msgIndex.Int(), contentIndex.Int())
						body, _ = sjson.SetBytes(body, path, newName)
						recordRename(name, newName)
					}
				case "tool_reference":
					toolName := part.Get("tool_name").String()
					if newName, ok := claudeOAuthToolRenameMap[toolName]; ok && newName != toolName {
						path := fmt.Sprintf("messages.%d.content.%d.tool_name", msgIndex.Int(), contentIndex.Int())
						body, _ = sjson.SetBytes(body, path, newName)
						recordRename(toolName, newName)
					}
				case "tool_result":
					nestedContent := part.Get("content")
					if nestedContent.Exists() && nestedContent.IsArray() {
						nestedContent.ForEach(func(nestedIndex, nestedPart gjson.Result) bool {
							if nestedPart.Get("type").String() != "tool_reference" {
								return true
							}
							nestedToolName := nestedPart.Get("tool_name").String()
							if newName, ok := claudeOAuthToolRenameMap[nestedToolName]; ok && newName != nestedToolName {
								path := fmt.Sprintf("messages.%d.content.%d.content.%d.tool_name", msgIndex.Int(), contentIndex.Int(), nestedIndex.Int())
								body, _ = sjson.SetBytes(body, path, newName)
								recordRename(nestedToolName, newName)
							}
							return true
						})
					}
				}
				return true
			})
			return true
		})
	}

	return body, reverseMap
}

func restoreClaudeOAuthToolNamesFromResponse(body []byte, reverseMap map[string]string) []byte {
	if len(reverseMap) == 0 {
		return body
	}
	content := gjson.GetBytes(body, "content")
	if !content.Exists() || !content.IsArray() {
		return body
	}
	content.ForEach(func(index, part gjson.Result) bool {
		switch part.Get("type").String() {
		case "tool_use":
			name := part.Get("name").String()
			if origName, ok := reverseMap[name]; ok {
				path := fmt.Sprintf("content.%d.name", index.Int())
				body, _ = sjson.SetBytes(body, path, origName)
			}
		case "tool_reference":
			toolName := part.Get("tool_name").String()
			if origName, ok := reverseMap[toolName]; ok {
				path := fmt.Sprintf("content.%d.tool_name", index.Int())
				body, _ = sjson.SetBytes(body, path, origName)
			}
		}
		return true
	})
	return body
}

func restoreClaudeOAuthToolNamesFromStreamLine(line []byte, reverseMap map[string]string) []byte {
	if len(reverseMap) == 0 {
		return line
	}
	trimmed := bytes.TrimSpace(line)
	if bytes.HasPrefix(trimmed, []byte("data:")) {
		payload := bytes.TrimSpace(trimmed[len("data:"):])
		if len(payload) == 0 || bytes.Equal(payload, []byte("[DONE]")) {
			return line
		}
		updated, changed := restoreClaudeOAuthToolNamesInStreamPayload(payload, reverseMap)
		if !changed {
			return line
		}
		return append([]byte("data: "), updated...)
	}

	updated, changed := restoreClaudeOAuthToolNamesInStreamPayload(trimmed, reverseMap)
	if !changed {
		return line
	}
	return updated
}

func restoreClaudeOAuthToolNamesInStreamPayload(payload []byte, reverseMap map[string]string) ([]byte, bool) {
	if len(reverseMap) == 0 || len(payload) == 0 || !gjson.ValidBytes(payload) {
		return payload, false
	}

	contentBlock := gjson.GetBytes(payload, "content_block")
	if !contentBlock.Exists() {
		return payload, false
	}

	switch contentBlock.Get("type").String() {
	case "tool_use":
		name := contentBlock.Get("name").String()
		if origName, ok := reverseMap[name]; ok {
			updated, err := sjson.SetBytes(payload, "content_block.name", origName)
			return updated, err == nil
		}
	case "tool_reference":
		toolName := contentBlock.Get("tool_name").String()
		if origName, ok := reverseMap[toolName]; ok {
			updated, err := sjson.SetBytes(payload, "content_block.tool_name", origName)
			return updated, err == nil
		}
	}
	return payload, false
}

func restoreClaudeOAuthToolNamesInStreamEvent(event map[string]any, reverseMap map[string]string) bool {
	if len(reverseMap) == 0 || len(event) == 0 {
		return false
	}
	contentBlock, ok := event["content_block"].(map[string]any)
	if !ok || len(contentBlock) == 0 {
		return false
	}

	switch contentBlock["type"] {
	case "tool_use":
		name, _ := contentBlock["name"].(string)
		if origName, ok := reverseMap[name]; ok {
			contentBlock["name"] = origName
			return true
		}
	case "tool_reference":
		toolName, _ := contentBlock["tool_name"].(string)
		if origName, ok := reverseMap[toolName]; ok {
			contentBlock["tool_name"] = origName
			return true
		}
	}
	return false
}

func restoreClaudeOAuthToolNamesInAnthropicStreamEvent(event *apicompat.AnthropicStreamEvent, reverseMap map[string]string) {
	if event == nil || len(reverseMap) == 0 {
		return
	}
	restoreBlock := func(block *apicompat.AnthropicContentBlock) {
		if block == nil || block.Type != "tool_use" {
			return
		}
		if origName, ok := reverseMap[block.Name]; ok {
			block.Name = origName
		}
	}

	restoreBlock(event.ContentBlock)
	if event.Message != nil {
		for i := range event.Message.Content {
			restoreBlock(&event.Message.Content[i])
		}
	}
}

func setClaudeOAuthToolNamesReverseMap(c *gin.Context, reverseMap map[string]string) {
	if c == nil {
		return
	}
	c.Set(claudeOAuthToolNamesReverseMapKey, reverseMap)
}

func getClaudeOAuthToolNamesReverseMap(c *gin.Context) map[string]string {
	if c == nil {
		return nil
	}
	v, ok := c.Get(claudeOAuthToolNamesReverseMapKey)
	if !ok {
		return nil
	}
	reverseMap, _ := v.(map[string]string)
	return reverseMap
}
