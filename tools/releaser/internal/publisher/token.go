package publisher

import (
	"encoding/base64"
	"encoding/json"
	"time"
)

func tokenExpiry(payload string) time.Time {
	b, e := base64.RawURLEncoding.DecodeString(payload)
	if e != nil {
		return time.Time{}
	}
	var claims struct {
		Exp int64 `json:"exp"`
	}
	if json.Unmarshal(b, &claims) != nil {
		return time.Time{}
	}
	return time.Unix(claims.Exp, 0)
}
