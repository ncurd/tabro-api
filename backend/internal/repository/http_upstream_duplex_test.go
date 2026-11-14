package repository

import (
	"bytes"
	"io"
	"testing"

	"github.com/stretchr/testify/require"
)

type testDuplexBody struct {
	bytes.Buffer
	closes int
}

func (b *testDuplexBody) Close() error { b.closes++; return nil }
func TestTrackedUpgradedBodyPreservesWritesAndReleasesOnce(t *testing.T) {
	body := &testDuplexBody{}
	released := 0
	wrapped := wrapTrackedBody(body, func() { released++ })
	duplex, ok := wrapped.(io.ReadWriteCloser)
	require.True(t, ok, "WebSocket dial needs the HTTP 101 body writer")
	_, err := duplex.Write([]byte("audio"))
	require.NoError(t, err)
	require.Equal(t, "audio", body.String())
	require.NoError(t, duplex.Close())
	require.NoError(t, duplex.Close())
	require.Equal(t, 1, released)
}
