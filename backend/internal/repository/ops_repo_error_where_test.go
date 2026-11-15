package repository

import (
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

func TestBuildOpsErrorLogsWhere_QueryUsesQualifiedColumns(t *testing.T) {
	filter := &service.OpsErrorLogFilter{
		Query: "ACCESS_DENIED",
	}

	where, args := buildOpsErrorLogsWhere(filter)
	if where == "" {
		t.Fatalf("where should not be empty")
	}
	if len(args) != 1 {
		t.Fatalf("args len = %d, want 1", len(args))
	}
	if !strings.Contains(where, "e.request_id ILIKE $") {
		t.Fatalf("where should include qualified request_id condition: %s", where)
	}
	if !strings.Contains(where, "e.client_request_id ILIKE $") {
		t.Fatalf("where should include qualified client_request_id condition: %s", where)
	}
	if !strings.Contains(where, "e.error_message ILIKE $") {
		t.Fatalf("where should include qualified error_message condition: %s", where)
	}
}

func TestBuildOpsErrorLogsWhere_UserQueryUsesExistsSubquery(t *testing.T) {
	filter := &service.OpsErrorLogFilter{
		UserQuery: "admin@",
	}

	where, args := buildOpsErrorLogsWhere(filter)
	if where == "" {
		t.Fatalf("where should not be empty")
	}
	if len(args) != 1 {
		t.Fatalf("args len = %d, want 1", len(args))
	}
	if !strings.Contains(where, "EXISTS (SELECT 1 FROM users u WHERE u.id = e.user_id AND u.email ILIKE $") {
		t.Fatalf("where should include EXISTS user email condition: %s", where)
	}
}

func TestBuildOpsErrorLogsWhere_DefaultIncludesTerminalWebSocketFailures(t *testing.T) {
	for _, phase := range []string{"", "billing", "internal"} {
		t.Run(phase, func(t *testing.T) {
			groupID := int64(42)
			where, args := buildOpsErrorLogsWhere(&service.OpsErrorLogFilter{Phase: phase, GroupID: &groupID})
			// Parenthesize the alternative so an otherwise nonmatching tenant or
			// group filter cannot be bypassed by the WebSocket error type.
			if !strings.Contains(where, "(COALESCE(e.status_code, 0) >= 400 OR e.error_type = 'websocket_task_error')") {
				t.Fatalf("terminal WebSocket errors must remain visible without treating all successful HTTP requests as errors: %s", where)
			}
			if !strings.Contains(where, "AND e.group_id = $1") || len(args) < 1 || args[0] != groupID {
				t.Fatalf("visibility must retain the existing group restriction: where=%s args=%v", where, args)
			}
		})
	}
}

func TestBuildOpsErrorLogsWhere_UpstreamStillIncludesRecoveredAttempts(t *testing.T) {
	where, _ := buildOpsErrorLogsWhere(&service.OpsErrorLogFilter{Phase: "upstream"})
	if strings.Contains(where, "status_code, 0) >= 400") || strings.Contains(where, "websocket_task_error") {
		t.Fatalf("explicit upstream phase must continue to include recovered attempts: %s", where)
	}
}
