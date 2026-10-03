//go:build !windows

package web

import (
	"fmt"
	"net/http"
)

func requireOperatorHTTPClient(*http.Request) error {
	return fmt.Errorf("Run as you client identity verification is supported only on Windows")
}
