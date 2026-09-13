package prepaid

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func TestVerifyWebhookSignature(t *testing.T) {
	body := []byte(`{"scope":"user","event":"user.limited","data":{"id":123}}`)
	secret := "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ01"

	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(body)
	sig := hex.EncodeToString(mac.Sum(nil))

	if !VerifyWebhookSignature(body, sig, secret) {
		t.Fatal("expected valid signature")
	}
	if VerifyWebhookSignature(body, sig+"00", secret) {
		t.Fatal("expected invalid signature")
	}
}
