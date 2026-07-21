package webhooksink

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
)

// signature returns a Stripe-style HMAC-SHA256 header value for the given
// secret, unix timestamp, and exact body bytes (WHK-03).
//
// Format: "t=<unix>,v1=<hex>" where the signed payload is
// strconv.FormatInt(t, 10) + "." + string(body).
//
// An empty secret returns "" (signing disabled — no header should be set).
func signature(secret []byte, t int64, body []byte) string {
	if len(secret) == 0 {
		return ""
	}
	payload := strconv.FormatInt(t, 10) + "." + string(body)
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte(payload))
	return fmt.Sprintf("t=%d,v1=%s", t, hex.EncodeToString(mac.Sum(nil)))
}
