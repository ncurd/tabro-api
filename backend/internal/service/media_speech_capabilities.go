package service

import (
	"context"
	"sort"
	"strings"
	"unicode"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
)

const SpeechTaskTranscriptionStreaming = "transcription.streaming"
const SpeechTaskSynthesis = "speech.synthesis"

// Only advertise protocols and billing meters implemented by this gateway.
func IsNativeRealtimeASRModel(model string) bool {
	id := strings.ToLower(strings.TrimSpace(model))
	if strings.Contains(id, "-8k") {
		return false // The gateway accepts PCM16 at 16 kHz only.
	}
	return strings.HasPrefix(id, "paraformer-realtime-") || id == "paraformer-realtime" ||
		strings.HasPrefix(id, "fun-asr") && strings.Contains(id, "realtime") ||
		strings.HasPrefix(id, "qwen-audio-3.0-asr-flash-streaming")
}

func IsQwenRealtimeTTSModel(model string) bool {
	id := strings.ToLower(strings.TrimSpace(model))
	return id == "qwen3-tts-flash-realtime" || strings.HasPrefix(id, "qwen3-tts-flash-realtime-")
}

// Aliyun counts Han characters twice. Do not apply this rule to legacy Azure or cloned voices.
func AliyunSpeechCharacters(text string) int {
	n := 0
	for _, r := range text {
		n++
		if unicode.Is(unicode.Han, r) {
			n++
		}
	}
	return n
}

func implementedSpeechTasks(upstream string) []string {
	if IsNativeRealtimeASRModel(upstream) {
		return []string{SpeechTaskTranscriptionStreaming}
	}
	if IsQwenRealtimeTTSModel(upstream) {
		return []string{SpeechTaskSynthesis}
	}
	return []string{}
}

// SpeechModelTaskCatalog resolves all advertised aliases with one account lookup per authorized group.
func (s *GatewayService) SpeechModelTaskCatalog(ctx context.Context, models []string) map[string][]string {
	result := make(map[string][]string, len(models))
	for _, model := range models {
		result[model] = []string{}
	}
	if len(models) == 0 || s == nil || s.accountRepo == nil {
		return result
	}
	groups, scoped := GatewayModelGroups(ctx)
	if !scoped {
		if group, ok := ctx.Value(ctxkey.Group).(*Group); ok && group != nil {
			groups = []*Group{group}
		}
	}
	readAccounts := func(group *Group) {
		var accounts []Account
		var err error
		if group != nil {
			accounts, err = s.accountRepo.ListSchedulableByGroupID(ctx, group.ID)
		} else {
			accounts, err = s.accountRepo.ListSchedulable(ctx)
		}
		if err != nil {
			return
		}
		for _, model := range models {
			mapped := model
			if group != nil && s.channelService != nil {
				groupCtx := context.WithValue(ctx, ctxkey.Group, group)
				mapping := s.channelService.ResolveChannelMapping(groupCtx, group.ID, model)
				mapped = firstNonEmpty(mapping.MappedModel, model)
			}
			tasks := map[string]bool{}
			for _, task := range result[model] {
				tasks[task] = true
			}
			for i := range accounts {
				account := &accounts[i]
				if account.Platform != PlatformDashScope || !account.IsModelSupported(mapped) {
					continue
				}
				if group != nil && (group.RequireOAuthOnly && account.Type == AccountTypeAPIKey || group.RequirePrivacySet && !account.IsPrivacySet()) {
					continue
				}
				for _, task := range implementedSpeechTasks(account.GetMappedModel(mapped)) {
					tasks[task] = true
				}
			}
			for task := range tasks {
				if !containsSpeechTask(result[model], task) {
					result[model] = append(result[model], task)
				}
			}
			sort.Strings(result[model])
		}
	}
	if len(groups) == 0 && !scoped {
		readAccounts(nil)
	} else {
		for _, group := range groups {
			if group != nil {
				readAccounts(group)
			}
		}
	}
	return result
}
func containsSpeechTask(tasks []string, task string) bool {
	for _, item := range tasks {
		if item == task {
			return true
		}
	}
	return false
}
