// Package cloudflare provides a minimal Cloudflare DNS API client.
package cloudflare

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

const cfBaseURL = "https://api.cloudflare.com/client/v4"

// Client interacts with the Cloudflare DNS API.
type Client struct {
	token string
	http  *http.Client
}

// NewClient creates a new Cloudflare API client using the given bearer token.
func NewClient(token string) *Client {
	return &Client{
		token: token,
		http: &http.Client{
			Timeout: 20 * time.Second,
			Transport: &http.Transport{
				Proxy:                 http.ProxyFromEnvironment,
				MaxIdleConns:          10,
				IdleConnTimeout:       30 * time.Second,
				TLSHandshakeTimeout:   5 * time.Second,
				ResponseHeaderTimeout: 10 * time.Second,
				ExpectContinueTimeout: 1 * time.Second,
			},
		},
	}
}

// dnsRecord is the subset of the Cloudflare DNS record payload we need.
type dnsRecord struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Type    string `json:"type"`
	Content string `json:"content"`
	TTL     int    `json:"ttl"`
	Proxied bool   `json:"proxied"`
}

type listResponse struct {
	Result  []dnsRecord `json:"result"`
	Success bool        `json:"success"`
	Errors  []cfError   `json:"errors"`
}

type updateResponse struct {
	Result  dnsRecord `json:"result"`
	Success bool      `json:"success"`
	Errors  []cfError `json:"errors"`
}

type cfError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// FindRecord looks up the first A record matching recordName in the given zone.
func (c *Client) FindRecord(ctx context.Context, zoneID, recordName string) (dnsRecord, error) {
	url := fmt.Sprintf("%s/zones/%s/dns_records?type=A&name=%s", cfBaseURL, zoneID, url.QueryEscape(recordName))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return dnsRecord{}, fmt.Errorf("building find-record request: %w", err)
	}
	c.setHeaders(req)

	var result listResponse
	if err := c.doJSON(req, &result); err != nil {
		return dnsRecord{}, fmt.Errorf("finding DNS record: %w", err)
	}
	if !result.Success {
		return dnsRecord{}, fmt.Errorf("cloudflare error: %v", result.Errors)
	}
	if len(result.Result) == 0 {
		return dnsRecord{}, fmt.Errorf("no A record found for %q in zone %q", recordName, zoneID)
	}

	return result.Result[0], nil
}

// UpdateRecord sets the content (IP) of an existing A record.
// It returns a summary message suitable for logging/UI display.
func (c *Client) UpdateRecord(ctx context.Context, zoneID, recordID, recordName, ip string, proxied bool) (string, error) {
	url := fmt.Sprintf("%s/zones/%s/dns_records/%s", cfBaseURL, zoneID, recordID)

	payload := dnsRecord{
		Type:    "A",
		Name:    recordName,
		Content: ip,
		TTL:     1, // 1 = automatic TTL
		Proxied: proxied,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("marshalling update payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, url, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("building update-record request: %w", err)
	}
	c.setHeaders(req)

	var result updateResponse
	if err := c.doJSON(req, &result); err != nil {
		return "", fmt.Errorf("updating DNS record: %w", err)
	}
	if !result.Success {
		return "", fmt.Errorf("cloudflare update error: %v", result.Errors)
	}

	msg := fmt.Sprintf(
		"Cloudflare update OK: id=%s name=%s ip=%s proxied=%t ttl=%d",
		result.Result.ID,
		result.Result.Name,
		result.Result.Content,
		result.Result.Proxied,
		result.Result.TTL,
	)

	return msg, nil
}

// setHeaders attaches the auth token and content-type to the request.
func (c *Client) setHeaders(req *http.Request) {
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "AutoDNS/1.0")
}

// doJSON executes req and JSON-decodes the response body into dest.
func (c *Client) doJSON(req *http.Request, dest interface{}) error {
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // 1 MB cap
	if err != nil {
		return fmt.Errorf("reading response body: %w", err)
	}

	if resp.StatusCode >= 400 {
		return fmt.Errorf("cloudflare HTTP %d: %s", resp.StatusCode, string(raw))
	}

	if err := json.Unmarshal(raw, dest); err != nil {
		return fmt.Errorf("decoding cloudflare response: %w", err)
	}

	return nil
}
