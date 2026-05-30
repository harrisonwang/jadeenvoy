package e2e

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

func mustReq(t *testing.T, method, url string, body io.Reader) *http.Request {
	t.Helper()
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if body != nil && strings.HasPrefix(method, "P") {
		req.Header.Set("Content-Type", "application/json")
	}
	return req
}
