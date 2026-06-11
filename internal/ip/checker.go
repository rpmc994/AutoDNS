// Package ip resolves the host's current public WAN IP address.
package ip

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const ipifyURL = "https://api.ipify.org"

// httpClient is a package-level client with a conservative timeout so the
// caller's context cancellation is the primary deadline, but the client
// itself also has a hard cap.
var httpClient = &http.Client{Timeout: 15 * time.Second}

// GetPublicIP fetches the current public WAN IP via api.ipify.org.
// The context is honoured for cancellation and timeout.
func GetPublicIP(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ipifyURL, nil)
	if err != nil {
		return "", fmt.Errorf("building ipify request: %w", err)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("calling ipify: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("ipify returned status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 64))
	if err != nil {
		return "", fmt.Errorf("reading ipify response: %w", err)
	}

	addr := strings.TrimSpace(string(body))
	if addr == "" {
		return "", fmt.Errorf("ipify returned empty body")
	}

	return addr, nil
}
