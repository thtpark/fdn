package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/thtpark/fdn/internal/config"
	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/pki"
)

type Client struct {
	BaseURL    string
	Credential config.Credential
	HTTP       *http.Client
	Local      bool
}

func NewClient(base, caFile, serverName string, credential config.Credential) (*Client, error) {
	if err := config.URL(base); err != nil {
		return nil, err
	}
	tlsConfig, err := pki.Client(caFile, serverName)
	if err != nil {
		return nil, err
	}
	transport := &http.Transport{TLSClientConfig: tlsConfig, Proxy: nil,
		DialContext:         (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 30 * time.Second,
		MaxIdleConns: 16, MaxIdleConnsPerHost: 8, IdleConnTimeout: 60 * time.Second}
	return &Client{BaseURL: strings.TrimRight(base, "/"), Credential: credential,
		HTTP: &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

func (c *Client) Close() { c.HTTP.CloseIdleConnections() }

func Path(parts ...string) string {
	for i, part := range parts {
		parts[i] = url.PathEscape(part)
	}
	return "/v1/" + strings.Join(parts, "/")
}

func (c *Client) Request(ctx context.Context, method, path string, input, output any) error {
	if !strings.HasPrefix(path, "/v1/") && path != "/healthz" && !(c.Local && strings.HasPrefix(path, "/local/")) {
		return domain.Fail(domain.Invalid, "API path must be relative")
	}
	if strings.Contains(path, "://") || strings.HasPrefix(path, "//") {
		return domain.Fail(domain.Invalid, "invalid API path")
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
	}
	var data []byte
	var err error
	if input != nil {
		data, err = json.Marshal(input)
		if err != nil {
			return err
		}
		if len(data) > domain.MaxBody {
			return domain.Fail(domain.Invalid, "request exceeds size limit")
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if input != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.Credential.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Credential.Token)
	}
	response, err := c.HTTP.Do(req)
	if err != nil {
		if ctx.Err() != nil && ctx.Err() == context.Canceled {
			return domain.Fail(domain.Cancelled, "request cancelled")
		}
		return domain.Fail(domain.Unavailable, "Foundation connection or TLS verification failed; reconcile operation_id before retrying writes")
	}
	defer response.Body.Close()
	if response.Header.Get("X-Fdn-API-Version") != strconv.Itoa(domain.APIVersion) {
		return domain.Fail(domain.Prerequisite, "incompatible Foundation API version")
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 32<<20+1))
	if err != nil || len(body) > 32<<20 {
		return domain.Fail(domain.Unavailable, "invalid or oversized API response")
	}
	var envelope Envelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		return domain.Fail(domain.Unavailable, "invalid API response")
	}
	if !envelope.OK {
		if envelope.Error == nil {
			return domain.Fail(domain.Internal, "failed API response omitted its error")
		}
		return envelope.Error
	}
	if response.StatusCode != http.StatusOK {
		return domain.Fail(domain.Unavailable, "unexpected API status")
	}
	if output != nil {
		if err := json.Unmarshal(envelope.Data, output); err != nil {
			return domain.Fail(domain.Internal, "API response did not match its contract")
		}
	}
	return nil
}

func Call[T any](ctx context.Context, c *Client, method, path string, input any) (T, error) {
	var output T
	err := c.Request(ctx, method, path, input, &output)
	return output, err
}
