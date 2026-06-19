package kafka

import "testing"

func TestSignWebhookPayload_CanonicalVector(t *testing.T) {
	payload := []byte(`{"id":"evt_123","type":"order.created","source":"billing","data":{"amount":99}}`)

	got := signWebhookPayload(payload, "test-secret", "1700000000")
	want := "v1=11e32a31840c9130f47da0546afd791d0ce053f7dc552a0b3a4fb118bcce6096"
	if got != want {
		t.Fatalf("signature = %q, want %q", got, want)
	}
}

func TestSignWebhookPayload_BindsEveryInput(t *testing.T) {
	base := signWebhookPayload([]byte("payload"), "secret", "1700000000")
	tests := []struct {
		name      string
		payload   []byte
		secret    string
		timestamp string
	}{
		{name: "body", payload: []byte("payload!"), secret: "secret", timestamp: "1700000000"},
		{name: "unicode bytes", payload: []byte("payload-\u00e1"), secret: "secret", timestamp: "1700000000"},
		{name: "secret", payload: []byte("payload"), secret: "other", timestamp: "1700000000"},
		{name: "timestamp", payload: []byte("payload"), secret: "secret", timestamp: "1700000001"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := signWebhookPayload(tt.payload, tt.secret, tt.timestamp); got == base {
				t.Fatalf("changed %s produced unchanged signature %q", tt.name, got)
			}
		})
	}
}
