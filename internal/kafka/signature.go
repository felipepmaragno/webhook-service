package kafka

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
)

const (
	webhookTimestampHeader  = "X-Dispatch-Timestamp"
	webhookSignatureHeader  = "X-Dispatch-Signature"
	webhookSignatureVersion = "v1="
)

func signWebhookPayload(payload []byte, secret, timestamp string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(timestamp))
	_, _ = mac.Write([]byte{'.'})
	_, _ = mac.Write(payload)
	return webhookSignatureVersion + hex.EncodeToString(mac.Sum(nil))
}
