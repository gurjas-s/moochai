// Package central sends node registration and heartbeat payloads.
package central

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"time"

	"peer-ai-client/internal/config"
)

const Version = "v0.1.0-dev"

type Identity struct {
	NodeID      string
	Name        string
	TailscaleIP string
	ListenAddr  string
}

type Payload struct {
	NodeID      string           `json:"node_id"`
	Name        string           `json:"name"`
	TailscaleIP string           `json:"tailscale_ip"`
	ListenAddr  string           `json:"listen_addr"`
	Version     string           `json:"version"`
	Services    []config.Service `json:"services"`
}

type Client struct {
	baseURL    string
	httpClient *http.Client
	retry      RetryPolicy
}

type RetryPolicy struct {
	Initial time.Duration
	Max     time.Duration
}

func New(host string, port int) (*Client, error) {
	if host == "" || port < 1 || port > 65535 {
		return nil, errors.New("invalid central address")
	}
	return &Client{
		baseURL:    "http://" + net.JoinHostPort(host, strconv.Itoa(port)),
		httpClient: &http.Client{Timeout: 10 * time.Second},
		retry:      RetryPolicy{Initial: time.Second, Max: time.Minute},
	}, nil
}

func (c *Client) Register(ctx context.Context, payload Payload) error {
	return c.post(ctx, "/api/nodes/register", payload)
}

func (c *Client) Heartbeat(ctx context.Context, payload Payload) error {
	return c.post(ctx, "/api/nodes/heartbeat", payload)
}

func (c *Client) post(ctx context.Context, path string, payload Payload) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal central payload: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create central request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("send central request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("central returned HTTP %d", resp.StatusCode)
	}
	return nil
}

func (c *Client) Run(ctx context.Context, payload func() Payload, interval time.Duration) error {
	if interval <= 0 {
		return errors.New("heartbeat interval must be positive")
	}
	if err := c.retryCall(ctx, func() error { return c.Register(ctx, payload()) }); err != nil {
		return err
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := c.retryCall(ctx, func() error { return c.Heartbeat(ctx, payload()) }); err != nil {
				return err
			}
		}
	}
}

func (c *Client) retryCall(ctx context.Context, call func() error) error {
	delay := c.retry.Initial
	for {
		if err := call(); err == nil {
			return nil
		} else if ctx.Err() != nil {
			return ctx.Err()
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
		delay *= 2
		if delay > c.retry.Max {
			delay = c.retry.Max
		}
	}
}
