package service

import "strings"

func isOpenAIResponseTerminalEventType(eventType string) bool {
	switch strings.TrimSpace(eventType) {
	case "response.completed", "response.done", "response.failed", "response.incomplete", "response.cancelled", "response.canceled":
		return true
	default:
		return false
	}
}

func isOpenAIImageTerminalEventType(eventType string) bool {
	switch strings.TrimSpace(eventType) {
	case "image_generation.completed", "image_edit.completed":
		return true
	default:
		return false
	}
}
