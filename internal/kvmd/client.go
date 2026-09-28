// Package kvmd is an HTTP client for the kvmd API exposed by a GL.iNet
// Comet X (PiKVM kvmd fork).
package kvmd

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/txomon/glinet-pikvm-cli/internal/config"
)

// Client talks to one kvmd device over HTTP.
type Client struct {
	http *http.Client
	// uploadHTTP has no Timeout, for MSD uploads that can run well past the
	// normal request timeout. It shares http's Transport. The context passed
	// to each request still bounds it.
	uploadHTTP *http.Client
	baseURL    string
	user       string
	password   string
}

// New builds a Client for d. Requests time out after timeout.
func New(d config.Device, timeout time.Duration) *Client {
	tr := &http.Transport{}
	if d.InsecureTLS {
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	}
	return &Client{
		http:       &http.Client{Timeout: timeout, Transport: tr},
		uploadHTTP: &http.Client{Transport: tr},
		baseURL:    strings.TrimRight(d.URL, "/") + "/api",
		user:       d.User,
		password:   d.Password,
	}
}

// APIError is returned when kvmd answers with a non-2xx status or an
// {"ok":false} envelope.
type APIError struct {
	Status  int
	Kind    string
	Message string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("kvmd %d %s: %s", e.Status, e.Kind, e.Message)
}

// do sends one request with the device's auth headers and returns the raw
// response. The caller must close the response body.
func (c *Client) do(ctx context.Context, method, path string, q url.Values, body io.Reader, contentType string) (*http.Response, error) {
	return c.doWithClient(ctx, c.http, method, path, q, body, contentType, -1)
}

// doWithClient is do but through hc instead of the client's default timeout
// client, and with an explicit contentLength when >= 0 (a streamed body may
// not be able to report its own length via body.Len()). The caller must
// close the response body.
func (c *Client) doWithClient(ctx context.Context, hc *http.Client, method, path string, q url.Values, body io.Reader, contentType string, contentLength int64) (*http.Response, error) {
	u := c.baseURL + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return nil, fmt.Errorf("kvmd: build request for %s: %w", path, err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if contentLength >= 0 {
		req.ContentLength = contentLength
	}
	req.Header.Set("X-KVMD-User", c.user)
	req.Header.Set("X-KVMD-Passwd", c.password)

	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("kvmd: request %s: %w", path, err)
	}
	return resp, nil
}

// envelope is the standard {"ok":bool,"result":...} kvmd response shape.
type envelope struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result"`
}

// envelopeError is the shape of result when ok is false.
type envelopeError struct {
	Error    string `json:"error"`
	ErrorMsg string `json:"error_msg"`
}

// truncate returns the first n bytes of b as a string.
func truncate(b []byte, n int) string {
	if len(b) > n {
		b = b[:n]
	}
	return string(b)
}

// doEnvelope performs one request and unwraps the {"ok","result"} envelope
// into out. A non-2xx status or ok=false becomes an *APIError.
func (c *Client) doEnvelope(ctx context.Context, method, path string, q url.Values, body io.Reader, contentType string, out any) error {
	resp, err := c.do(ctx, method, path, q, body, contentType)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return decodeEnvelope(path, resp, out)
}

// decodeEnvelope reads resp's body, unwraps the {"ok","result"} envelope,
// and decodes result into out. A non-2xx status or ok=false becomes an
// *APIError. The caller owns closing resp.Body.
func decodeEnvelope(path string, resp *http.Response, out any) error {
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("kvmd: read response from %s: %w", path, err)
	}

	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return fmt.Errorf("kvmd: non-JSON response from %s", path)
		}
		return &APIError{Status: resp.StatusCode, Message: truncate(raw, 200)}
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 || !env.OK {
		var ee envelopeError
		if err := json.Unmarshal(env.Result, &ee); err != nil || ee.Error == "" {
			return &APIError{Status: resp.StatusCode, Message: truncate(raw, 200)}
		}
		return &APIError{Status: resp.StatusCode, Kind: ee.Error, Message: ee.ErrorMsg}
	}

	if out == nil {
		return nil
	}
	if err := json.Unmarshal(env.Result, out); err != nil {
		return fmt.Errorf("kvmd: decode result from %s: %w", path, err)
	}
	return nil
}

// getJSON performs a GET and decodes the envelope's result into out.
func (c *Client) getJSON(ctx context.Context, path string, q url.Values, out any) error {
	return c.doEnvelope(ctx, http.MethodGet, path, q, nil, "", out)
}

// post performs a POST and decodes the envelope's result into out.
func (c *Client) post(ctx context.Context, path string, q url.Values, body io.Reader, contentType string, out any) error {
	return c.doEnvelope(ctx, http.MethodPost, path, q, body, contentType, out)
}

// getRaw performs a GET and returns the raw response, for endpoints whose
// body is not the {"ok","result"} envelope (binary snapshots, bare arrays).
// The caller must close the response body.
func (c *Client) getRaw(ctx context.Context, path string, q url.Values) (*http.Response, error) {
	return c.do(ctx, http.MethodGet, path, q, nil, "")
}
