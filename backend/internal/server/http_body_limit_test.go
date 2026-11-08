package server_test

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/server"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/http2"
)

func TestHTTPServerGlobalBodyLimitAppliesToHTTP1AndH2C(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, mode := range []struct {
		name       string
		h2cEnabled bool
		http2      bool
	}{
		{name: "HTTP1"},
		{name: "HTTP1 with h2c enabled", h2cEnabled: true},
		{name: "HTTP2 cleartext", h2cEnabled: true, http2: true},
	} {
		for _, fallback := range []bool{false, true} {
			name := mode.name + "/explicit server limit"
			if fallback {
				name = mode.name + "/gateway fallback limit"
			}
			t.Run(name, func(t *testing.T) {
				const limit int64 = 16
				cfg := &config.Config{
					Server:  config.ServerConfig{MaxRequestBodySize: limit, H2C: config.H2CConfig{Enabled: mode.h2cEnabled}},
					Gateway: config.GatewayConfig{MaxBodySize: limit * 2},
				}
				if fallback {
					cfg.Server.MaxRequestBodySize = 0
					cfg.Gateway.MaxBodySize = limit
				}
				router := gin.New()
				router.POST("/read", func(c *gin.Context) {
					body, err := io.ReadAll(c.Request.Body)
					if err != nil {
						var maxErr *http.MaxBytesError
						if errors.As(err, &maxErr) {
							c.JSON(http.StatusRequestEntityTooLarge, gin.H{"limit": maxErr.Limit})
							return
						}
						c.Status(http.StatusBadRequest)
						return
					}
					c.String(http.StatusOK, string(body))
				})
				upstream := httptest.NewServer(server.ProvideHTTPServer(cfg, router).Handler)
				defer upstream.Close()
				client := upstream.Client()
				if mode.http2 {
					transport := &http2.Transport{
						AllowHTTP: true,
						DialTLSContext: func(ctx context.Context, network, addr string, _ *tls.Config) (net.Conn, error) {
							return (&net.Dialer{}).DialContext(ctx, network, addr)
						},
					}
					defer transport.CloseIdleConnections()
					client = &http.Client{Transport: transport}
				}
				// Repeat boundary cases on one h2c connection: limiting only the
				// HTTP/1 connection preface would leave these streams unbounded.
				for _, size := range []int64{limit, limit + 1, limit - 1, limit + 1} {
					for _, unknownLength := range []bool{false, true} {
						payload := strings.Repeat("x", int(size))
						req, err := http.NewRequest(http.MethodPost, upstream.URL+"/read", strings.NewReader(payload))
						require.NoError(t, err)
						if unknownLength {
							req.ContentLength = -1
						}
						resp, err := client.Do(req)
						require.NoError(t, err)
						body, readErr := io.ReadAll(resp.Body)
						require.NoError(t, resp.Body.Close())
						require.NoError(t, readErr)
						if mode.http2 {
							require.Equal(t, 2, resp.ProtoMajor)
						}
						if size > limit {
							require.Equal(t, http.StatusRequestEntityTooLarge, resp.StatusCode, string(body))
							require.JSONEq(t, `{"limit":16}`, string(body))
						} else {
							require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
							require.Equal(t, payload, string(body))
						}
					}
				}
			})
		}
	}
}

func TestHTTPServerBoundsH2CUpgradeBodyBeforeProtocolHandlerBuffersIt(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const limit int64 = 16
	cfg := &config.Config{Server: config.ServerConfig{
		MaxRequestBodySize: limit,
		H2C:                config.H2CConfig{Enabled: true},
	}}
	router := gin.New()
	router.POST("/read", func(c *gin.Context) {
		t.Error("oversized upgrade must not reach the application")
	})
	reader := &bodyLimitCountingReader{Reader: strings.NewReader(strings.Repeat("x", 1024))}
	req := httptest.NewRequest(http.MethodPost, "/read", reader)
	req.Header.Set("Connection", "Upgrade, HTTP2-Settings")
	req.Header.Set("Upgrade", "h2c")
	req.Header.Set("HTTP2-Settings", "")
	w := httptest.NewRecorder()
	server.ProvideHTTPServer(cfg, router).Handler.ServeHTTP(w, req)
	// MaxBytesReader reads at most one extra byte to distinguish an exact
	// boundary match from an oversized body, including chunked upgrades.
	require.EqualValues(t, limit+1, reader.read)
	require.NotEqual(t, http.StatusOK, w.Code)
}

type bodyLimitCountingReader struct {
	io.Reader
	read int
}

func (r *bodyLimitCountingReader) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	r.read += n
	return n, err
}
