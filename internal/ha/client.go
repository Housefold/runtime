// Package ha contains the Runtime's bounded Home Assistant connectivity probe.
package ha

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/coder/websocket"
)

const (
	defaultRESTURL      = "http://supervisor/core/api/"
	defaultWebSocketURL = "ws://supervisor/core/websocket"
	probeTimeout        = 5 * time.Second
)

// Status is the result of a one-shot Home Assistant connectivity check.
type Status string

const (
	StatusConnected   Status = "connected"
	StatusDenied      Status = "denied"
	StatusUnavailable Status = "unavailable"
)

// Result contains only a coarse status and a safe error category.
type Result struct {
	Status        Status `json:"status"`
	ErrorCategory string `json:"error_category,omitempty"`
}

// Probe verifies REST and WebSocket authentication once using the documented
// Supervisor Core proxy URLs. It never returns transport details, response
// bodies, endpoint data, or the supplied token.
func Probe(ctx context.Context, token string) Result {
	return probeAt(ctx, token, defaultRESTURL, defaultWebSocketURL)
}

func probeAt(ctx context.Context, token, restURL, websocketURL string) Result {
	if strings.TrimSpace(token) == "" {
		return Result{Status: StatusUnavailable, ErrorCategory: "token_unavailable"}
	}
	return probeAtWithTimeout(ctx, token, restURL, websocketURL, probeTimeout)
}

func probeAtWithTimeout(parent context.Context, token, restURL, websocketURL string, timeout time.Duration) Result {
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, restURL, nil)
	if err != nil {
		return Result{Status: StatusUnavailable, ErrorCategory: "rest_configuration_error"}
	}
	request.Header.Set("Authorization", "Bearer "+token)
	client := http.Client{CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	response, err := client.Do(request)
	if err != nil {
		return Result{Status: StatusUnavailable, ErrorCategory: "rest_transport_error"}
	}
	_ = response.Body.Close()
	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
		return Result{Status: StatusDenied, ErrorCategory: "rest_authentication_rejected"}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return Result{Status: StatusUnavailable, ErrorCategory: "rest_http_error"}
	}

	if result := probeWebSocket(ctx, token, websocketURL); result.Status != StatusConnected {
		return result
	}
	return Result{Status: StatusConnected}
}

func probeWebSocket(ctx context.Context, token, websocketURL string) Result {
	dialOptions := &websocket.DialOptions{HTTPClient: &http.Client{
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}}
	conn, response, err := websocket.Dial(ctx, websocketURL, dialOptions)
	if err != nil {
		if response != nil && (response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden) {
			return Result{Status: StatusDenied, ErrorCategory: "websocket_authentication_rejected"}
		}
		return Result{Status: StatusUnavailable, ErrorCategory: "websocket_transport_error"}
	}
	defer func() { _ = conn.CloseNow() }()

	_, message, err := conn.Read(ctx)
	if err != nil {
		return Result{Status: StatusUnavailable, ErrorCategory: "websocket_protocol_error"}
	}
	var initial struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(message, &initial); err != nil || initial.Type != "auth_required" {
		return Result{Status: StatusUnavailable, ErrorCategory: "websocket_protocol_error"}
	}
	request, err := json.Marshal(struct {
		Type        string `json:"type"`
		AccessToken string `json:"access_token"`
	}{Type: "auth", AccessToken: token})
	if err != nil {
		return Result{Status: StatusUnavailable, ErrorCategory: "websocket_protocol_error"}
	}
	if err := conn.Write(ctx, websocket.MessageText, request); err != nil {
		return Result{Status: StatusUnavailable, ErrorCategory: "websocket_transport_error"}
	}
	_, message, err = conn.Read(ctx)
	if err != nil {
		return Result{Status: StatusUnavailable, ErrorCategory: "websocket_transport_error"}
	}
	var auth struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(message, &auth); err != nil {
		return Result{Status: StatusUnavailable, ErrorCategory: "websocket_protocol_error"}
	}
	switch strings.TrimSpace(auth.Type) {
	case "auth_ok":
		return Result{Status: StatusConnected}
	case "auth_invalid":
		return Result{Status: StatusDenied, ErrorCategory: "websocket_authentication_rejected"}
	default:
		return Result{Status: StatusUnavailable, ErrorCategory: "websocket_protocol_error"}
	}
}
