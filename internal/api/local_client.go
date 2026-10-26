package api

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
)

// LocalStatus contains public identity and availability only. Certificates,
// signing proofs and service-private paths are never part of the local API.
type LocalStatus = domain.LocalStatus

// NewLocalClient speaks HTTP over the dedicated identity service's Unix socket.
// It never opens a local TCP listener or reads an application credential file.
func NewLocalClient(c config.Worker) (*Client, error) {
	socket := c.IdentitySocket
	if !filepath.IsAbs(socket) || strings.ContainsAny(socket, "\x00\r\n") {
		return nil, domain.Fail(domain.Invalid, "identity socket must be an absolute path")
	}
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	client := NewMessageClient("unix://identity.local", func(ctx context.Context) (net.Conn, error) {
		return dialer.DialContext(ctx, "unix", socket)
	})
	client.Local = true
	return client, nil
}

func (c *Client) LocalStatus(ctx context.Context) (LocalStatus, error) {
	var status LocalStatus
	if c.IsMessageClient() {
		err := c.Request(ctx, "GET", "/local/status", nil, &status)
		return status, err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/local/status", nil)
	if err != nil {
		return status, err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return status, domain.Fail(domain.Unavailable, "local identity service is unavailable")
	}
	defer resp.Body.Close()
	if resp.Header.Get("X-Fdn-API-Version") != strconv.Itoa(domain.APIVersion) {
		return status, domain.Fail(domain.Prerequisite, "incompatible local identity API version")
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, domain.MaxBody+1))
	if err != nil || len(data) > domain.MaxBody {
		return status, domain.Fail(domain.Unavailable, "invalid local identity status")
	}
	var envelope Envelope
	if json.Unmarshal(data, &envelope) != nil {
		return status, domain.Fail(domain.Unavailable, "invalid local identity response")
	}
	if !envelope.OK {
		if envelope.Error != nil {
			return status, envelope.Error
		}
		return status, domain.Fail(domain.Unavailable, "local identity status failed")
	}
	if resp.StatusCode != http.StatusOK || json.Unmarshal(envelope.Data, &status) != nil {
		return status, domain.Fail(domain.Unavailable, "invalid local identity status")
	}
	return status, nil
}
