//go:build billing_integration

package service

import (
	"database/sql"
	"encoding/json"
	"net/url"
	"os"
	"strings"
	"testing"

	_ "github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

func TestGatewayPricingReplayAfterPostgreSQLJSONBRoundTrip(t *testing.T) {
	rawDSN := os.Getenv("BILLING_CENTER_TEST_DSN")
	if rawDSN == "" {
		t.Skip("BILLING_CENTER_TEST_DSN is not configured")
	}
	dsn, err := url.Parse(rawDSN)
	require.NoError(t, err)
	require.Equal(t, "billing_center_test", strings.TrimPrefix(dsn.Path, "/"), "use a dedicated test database")
	require.Contains(t, []string{"127.0.0.1", "localhost", "::1"}, dsn.Hostname())
	db, err := sql.Open("postgres", rawDSN)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	testGatewayPricingReplay(t, func(raw json.RawMessage) json.RawMessage {
		var stored []byte
		require.NoError(t, db.QueryRow(`SELECT $1::jsonb`, string(raw)).Scan(&stored))
		return stored
	})
}
