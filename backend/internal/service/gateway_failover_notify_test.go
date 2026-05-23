//go:build unit

package service

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type failoverEmailSenderRecorder struct {
	mu       sync.Mutex
	messages []failoverEmailMessage
}

type failoverEmailMessage struct {
	to      string
	subject string
	body    string
}

func (r *failoverEmailSenderRecorder) SendEmail(_ context.Context, to, subject, body string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.messages = append(r.messages, failoverEmailMessage{to: to, subject: subject, body: body})
	return nil
}

func (r *failoverEmailSenderRecorder) snapshot() []failoverEmailMessage {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]failoverEmailMessage, len(r.messages))
	copy(out, r.messages)
	return out
}

func TestGatewayFailoverNotify_SkipsWhenAdminEmailUnset(t *testing.T) {
	repo := newMockSettingRepo()
	sender := &failoverEmailSenderRecorder{}
	notifier := &BalanceNotifyService{
		emailService: sender,
		settingRepo:  repo,
	}

	notifier.NotifyTransientUpstreamFailure(context.Background(), newAnthropicAPIKeyAccountForTest(), 502, "dial tcp timeout", time.Minute)

	require.Empty(t, sender.snapshot())
}

func TestGatewayService_TempUnscheduleTransientUpstreamFailureSendsAdminEmail(t *testing.T) {
	settingsRepo := newMockSettingRepo()
	settingsRepo.data[SettingKeyGatewayFailoverNotifyAdminEmail] = " admin@example.com "
	settingsRepo.data[SettingKeySiteName] = "Tabro"

	sender := &failoverEmailSenderRecorder{}
	notifier := &BalanceNotifyService{
		emailService: sender,
		settingRepo:  settingsRepo,
	}
	accountRepo := &anthropicTempUnschedRepo{}
	svc := &GatewayService{
		rateLimitService:     NewRateLimitService(accountRepo, nil, nil, nil, nil),
		balanceNotifyService: notifier,
	}
	account := newAnthropicAPIKeyAccountForTest()

	ok := svc.tempUnscheduleTransientUpstreamFailure(context.Background(), account, 502, "dial tcp 127.0.0.1:15721: connect: connection refused")

	require.True(t, ok)
	require.Eventually(t, func() bool {
		return len(sender.snapshot()) == 1
	}, time.Second, 10*time.Millisecond)

	messages := sender.snapshot()
	require.Equal(t, "admin@example.com", messages[0].to)
	require.Contains(t, messages[0].subject, "Tabro")
	require.Contains(t, messages[0].subject, "Failover")
	require.Contains(t, messages[0].body, "anthropic-apikey-pass-test")
	require.Contains(t, messages[0].body, "502")
	require.Contains(t, messages[0].body, "127.0.0.1:15721")
}
