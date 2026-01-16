package kafka

import "testing"

func TestIsPermanentFailure(t *testing.T) {
	tests := []struct {
		statusCode int
		expected   bool
		reason     string
	}{
		{400, true, "Bad Request"},
		{401, true, "Unauthorized"},
		{403, true, "Forbidden"},
		{404, true, "Not Found"},
		{405, true, "Method Not Allowed"},
		{406, true, "Not Acceptable"},
		{410, true, "Gone"},
		{411, true, "Length Required"},
		{413, true, "Payload Too Large"},
		{414, true, "URI Too Long"},
		{415, true, "Unsupported Media Type"},
		{422, true, "Unprocessable Entity"},
		{426, true, "Upgrade Required"},
		{431, true, "Request Header Fields Too Large"},
		// Not permanent failures
		{200, false, "OK"},
		{201, false, "Created"},
		{408, false, "Request Timeout - retryable"},
		{429, false, "Too Many Requests - retryable"},
		{500, false, "Internal Server Error - retryable"},
		{502, false, "Bad Gateway - retryable"},
		{503, false, "Service Unavailable - retryable"},
		{504, false, "Gateway Timeout - retryable"},
	}

	for _, tt := range tests {
		t.Run(tt.reason, func(t *testing.T) {
			result := isPermanentFailure(tt.statusCode)
			if result != tt.expected {
				t.Errorf("isPermanentFailure(%d) = %v, want %v (%s)",
					tt.statusCode, result, tt.expected, tt.reason)
			}
		})
	}
}

func TestIsRetryableFailure(t *testing.T) {
	tests := []struct {
		statusCode int
		expected   bool
		reason     string
	}{
		{408, true, "Request Timeout"},
		{429, true, "Too Many Requests"},
		{500, true, "Internal Server Error"},
		{502, true, "Bad Gateway"},
		{503, true, "Service Unavailable"},
		{504, true, "Gateway Timeout"},
		// Not retryable
		{200, false, "OK - success"},
		{201, false, "Created - success"},
		{400, false, "Bad Request - permanent"},
		{401, false, "Unauthorized - permanent"},
		{403, false, "Forbidden - permanent"},
		{404, false, "Not Found - permanent"},
		{410, false, "Gone - permanent"},
		{422, false, "Unprocessable Entity - permanent"},
	}

	for _, tt := range tests {
		t.Run(tt.reason, func(t *testing.T) {
			result := isRetryableFailure(tt.statusCode)
			if result != tt.expected {
				t.Errorf("isRetryableFailure(%d) = %v, want %v (%s)",
					tt.statusCode, result, tt.expected, tt.reason)
			}
		})
	}
}
