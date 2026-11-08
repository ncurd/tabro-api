package middleware

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"
)

// readGatewayRequestBody reads through the gateway's configured RequestBodyLimit
// reader. Restore only complete bodies: replacing an errored MaxBytesReader with
// its partial output would hide the limit from billing and downstream handlers.
func readGatewayRequestBody(c *gin.Context) ([]byte, error) {
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		return nil, err
	}
	c.Request.Body = io.NopCloser(bytes.NewReader(body))
	return body, nil
}

func gatewayRequestBodyLimitError(err error) (string, bool) {
	var maxErr *http.MaxBytesError
	if !errors.As(err, &maxErr) {
		return "", false
	}
	return fmt.Sprintf("Request body too large: maximum %d bytes", maxErr.Limit), true
}
