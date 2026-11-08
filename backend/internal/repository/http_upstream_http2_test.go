package repository

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestUpstreamTransportProtocolNegotiationAndMultiplexing(t *testing.T) {
	if !localListenerAvailable() {
		t.Skipf("local listeners are not permitted: %v", canListenErr)
	}
	for _, proxyScheme := range []string{"direct", "socks5", "socks5h"} {
		for _, enableHTTP2 := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/http2=%t", proxyScheme, enableHTTP2), func(t *testing.T) {
				held := make(chan struct{})
				release := make(chan struct{})
				releaseHeld := sync.OnceFunc(func() { close(release) })
				var connections atomic.Int64
				upstream := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == "/hold" {
						close(held)
						select {
						case <-release:
						case <-r.Context().Done():
						}
					}
					_, _ = io.WriteString(w, "ok")
				}))
				upstream.EnableHTTP2 = enableHTTP2
				upstream.Config.ConnState = func(_ net.Conn, state http.ConnState) {
					if state == http.StateNew {
						connections.Add(1)
					}
				}
				upstream.StartTLS()
				defer upstream.Close()
				defer releaseHeld()

				var proxyURL *url.URL
				if proxyScheme != "direct" {
					proxyURL = startUpstreamSOCKSProxy(t)
					proxyURL.Scheme = proxyScheme
				}
				transport, err := buildUpstreamTransport(poolSettings{
					maxIdleConns: 1, maxIdleConnsPerHost: 1, maxConnsPerHost: 1,
					idleConnTimeout: time.Minute, responseHeaderTimeout: 5 * time.Second,
				}, proxyURL)
				require.NoError(t, err)
				defer transport.CloseIdleConnections()
				roots := x509.NewCertPool()
				roots.AddCert(upstream.Certificate())
				// Initialize the production protocol defaults before adding the
				// local test CA: a custom TLS config itself suppresses Go's
				// automatic HTTP/2 setup when ForceAttemptHTTP2 is unset.
				_ = transport.Clone()
				if transport.TLSClientConfig == nil {
					transport.TLSClientConfig = &tls.Config{}
				}
				transport.TLSClientConfig.RootCAs = roots
				client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
				response, err := client.Get(upstream.URL + "/warmup")
				require.NoError(t, err)
				_, readErr := io.Copy(io.Discard, response.Body)
				require.NoError(t, response.Body.Close())
				require.NoError(t, readErr)
				if !enableHTTP2 {
					require.Equal(t, 1, response.ProtoMajor, "HTTP/1-only upstream must remain usable")
					return
				}
				require.Equal(t, 2, response.ProtoMajor)
				require.Equal(t, "h2", response.TLS.NegotiatedProtocol)

				// With only one TCP connection allowed, a second request must pass
				// while the first stream is waiting for its upstream response.
				firstDone := make(chan error, 1)
				go func() {
					resp, err := client.Get(upstream.URL + "/hold")
					if err == nil {
						_, err = io.Copy(io.Discard, resp.Body)
						_ = resp.Body.Close()
					}
					firstDone <- err
				}()
				select {
				case <-held:
				case err := <-firstDone:
					t.Fatalf("first stream ended before reaching upstream: %v", err)
				case <-time.After(5 * time.Second):
					t.Fatal("first stream did not reach upstream")
				}
				response, err = client.Get(upstream.URL + "/probe")
				require.NoError(t, err, "second request must multiplex over the occupied connection")
				_, readErr = io.Copy(io.Discard, response.Body)
				require.NoError(t, response.Body.Close())
				require.NoError(t, readErr)
				require.Equal(t, 2, response.ProtoMajor)
				require.EqualValues(t, 1, connections.Load(), "both streams must share one upstream TCP connection")
				releaseHeld()
				require.NoError(t, <-firstDone)
			})
		}
	}
}

// A local SOCKS5 CONNECT relay exercises the production proxy DialContext,
// including its negotiation, rather than replacing it with a direct dialer.
func startUpstreamSOCKSProxy(t *testing.T) *url.URL {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	var active sync.Map
	var workers sync.WaitGroup
	workers.Add(1)
	go func() {
		defer workers.Done()
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			active.Store(conn, struct{}{})
			workers.Add(1)
			go func() {
				defer workers.Done()
				defer conn.Close()
				defer active.Delete(conn)
				_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
				var greeting [2]byte
				if _, err := io.ReadFull(conn, greeting[:]); err != nil || greeting[0] != 5 {
					return
				}
				if _, err := io.CopyN(io.Discard, conn, int64(greeting[1])); err != nil {
					return
				}
				if _, err := conn.Write([]byte{5, 0}); err != nil {
					return
				}
				var request [4]byte
				if _, err := io.ReadFull(conn, request[:]); err != nil || request[0] != 5 || request[1] != 1 {
					return
				}
				var host string
				switch request[3] {
				case 1, 4:
					length := net.IPv4len
					if request[3] == 4 {
						length = net.IPv6len
					}
					address := make([]byte, length)
					if _, err := io.ReadFull(conn, address); err != nil {
						return
					}
					host = net.IP(address).String()
				case 3:
					var length [1]byte
					if _, err := io.ReadFull(conn, length[:]); err != nil {
						return
					}
					address := make([]byte, int(length[0]))
					if _, err := io.ReadFull(conn, address); err != nil {
						return
					}
					host = string(address)
				default:
					return
				}
				var port [2]byte
				if _, err := io.ReadFull(conn, port[:]); err != nil {
					return
				}
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				upstream, err := (&net.Dialer{}).DialContext(ctx, "tcp", net.JoinHostPort(host, strconv.Itoa(int(binary.BigEndian.Uint16(port[:])))))
				if err != nil {
					return
				}
				defer upstream.Close()
				if _, err := conn.Write([]byte{5, 0, 0, 1, 0, 0, 0, 0, 0, 0}); err != nil {
					return
				}
				_ = conn.SetDeadline(time.Time{})
				copied := make(chan struct{})
				go func() {
					defer close(copied)
					_, _ = io.Copy(upstream, conn)
					_ = upstream.Close()
				}()
				_, _ = io.Copy(conn, upstream)
				_ = conn.Close()
				<-copied
			}()
		}
	}()
	t.Cleanup(func() {
		_ = listener.Close()
		active.Range(func(key, _ any) bool {
			_ = key.(net.Conn).Close()
			return true
		})
		workers.Wait()
	})
	return &url.URL{Scheme: "socks5h", Host: listener.Addr().String()}
}
