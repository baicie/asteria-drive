package drive

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
)

type CursorCodec struct {
	key []byte
}

type cursorPayload struct {
	Version  int    `json:"v"`
	TenantID string `json:"tenant"`
	Scope    string `json:"scope"`
	Name     string `json:"name"`
	ID       string `json:"id"`
}

func NewCursorCodec(key []byte) (*CursorCodec, error) {
	if len(key) < 32 {
		return nil, E(CodeInvalidRequest, "cursor key must be at least 32 bytes")
	}
	return &CursorCodec{key: append([]byte(nil), key...)}, nil
}

func (c *CursorCodec) Encode(tenantID, scope string, position CursorPosition) (string, error) {
	payload, err := json.Marshal(cursorPayload{
		Version: 1, TenantID: tenantID, Scope: scope, Name: position.Name, ID: position.ID,
	})
	if err != nil {
		return "", E(CodeInternal, "could not encode cursor", err)
	}
	sig := c.sign(payload)
	return base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

func (c *CursorCodec) Decode(value, tenantID, scope string) (CursorPosition, error) {
	if value == "" {
		return CursorPosition{}, nil
	}
	var payloadPart, signaturePart string
	for i := 0; i < len(value); i++ {
		if value[i] == '.' {
			payloadPart, signaturePart = value[:i], value[i+1:]
			break
		}
	}
	if payloadPart == "" || signaturePart == "" {
		return CursorPosition{}, E(CodeInvalidCursor, "cursor is invalid")
	}
	payload, err := base64.RawURLEncoding.DecodeString(payloadPart)
	if err != nil {
		return CursorPosition{}, E(CodeInvalidCursor, "cursor is invalid")
	}
	signature, err := base64.RawURLEncoding.DecodeString(signaturePart)
	if err != nil || !hmac.Equal(signature, c.sign(payload)) {
		return CursorPosition{}, E(CodeInvalidCursor, "cursor is invalid")
	}
	var decoded cursorPayload
	if err := json.Unmarshal(payload, &decoded); err != nil || decoded.Version != 1 || decoded.TenantID != tenantID || decoded.Scope != scope || decoded.ID == "" {
		return CursorPosition{}, E(CodeInvalidCursor, "cursor is invalid")
	}
	return CursorPosition{Name: decoded.Name, ID: decoded.ID}, nil
}

func (c *CursorCodec) sign(payload []byte) []byte {
	mac := hmac.New(sha256.New, c.key)
	_, _ = mac.Write(payload)
	return mac.Sum(nil)
}
