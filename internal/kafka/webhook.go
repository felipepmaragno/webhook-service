package kafka

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/felipemaragno/dispatch/internal/domain"
	"github.com/felipemaragno/dispatch/internal/observability"
)

// deliverWebhook sends the event payload to the subscription URL via HTTP POST.
// Returns the HTTP status code (if available), truncated response body, and error.
func (h *DeliveryHandler) deliverWebhook(ctx context.Context, sub *domain.Subscription, event *EventMessage) (*int, string, error) {
	payload, err := json.Marshal(map[string]interface{}{
		"id":     event.ID,
		"type":   event.Type,
		"source": event.Source,
		"data":   event.Data,
	})
	if err != nil {
		return nil, "", fmt.Errorf("marshal payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, sub.URL, bytes.NewReader(payload))
	if err != nil {
		return nil, "", fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Event-ID", event.ID)
	req.Header.Set("X-Event-Type", event.Type)
	// Propagate trace ID to the webhook destination for end-to-end correlation.
	if traceID := observability.TraceIDFromContext(ctx); traceID != "" {
		req.Header.Set(observability.TraceIDHeader, traceID)
	}
	if sub.Secret != nil && *sub.Secret != "" {
		// Add HMAC signature
		req.Header.Set("X-Signature", computeHMAC(payload, *sub.Secret))
	}

	req.ContentLength = int64(len(payload))

	resp, err := h.httpClient.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("http request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	// Read response body (limited)
	body := make([]byte, 1024)
	n, _ := resp.Body.Read(body)
	respBody := string(body[:n])

	statusCode := resp.StatusCode

	// Check for success (2xx)
	if statusCode >= 200 && statusCode < 300 {
		return &statusCode, respBody, nil
	}

	return &statusCode, respBody, fmt.Errorf("non-2xx status: %d", statusCode)
}

// Helper for HMAC signature
func computeHMAC(payload []byte, secret string) string {
	// Simplified - in production use crypto/hmac
	return fmt.Sprintf("sha256=%x", payload[:min(8, len(payload))])
}

// isPermanentFailure determines if an HTTP status code indicates a permanent failure
// that should not be retried. These are client errors (4xx) that won't change on retry.
func isPermanentFailure(statusCode int) bool {
	switch statusCode {
	case 400, // Bad Request - payload is invalid
		401, // Unauthorized - credentials invalid
		403, // Forbidden - access denied
		404, // Not Found - endpoint doesn't exist
		405, // Method Not Allowed - POST not accepted
		406, // Not Acceptable - content type not accepted
		410, // Gone - resource permanently removed
		411, // Length Required - server config issue
		413, // Payload Too Large - event too big
		414, // URI Too Long - URL invalid
		415, // Unsupported Media Type - content type not supported
		422, // Unprocessable Entity - semantically invalid
		426, // Upgrade Required - needs HTTPS
		431: // Request Header Fields Too Large
		return true
	}
	return false
}

// isRetryableFailure determines if an HTTP status code indicates a temporary failure
// that should be retried.
func isRetryableFailure(statusCode int) bool {
	switch statusCode {
	case 408, // Request Timeout
		429, // Too Many Requests
		500, // Internal Server Error
		502, // Bad Gateway
		503, // Service Unavailable
		504: // Gateway Timeout
		return true
	}
	return false
}
