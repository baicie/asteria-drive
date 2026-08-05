package drive

import (
	"encoding/hex"
	"strings"
)

const MaxIdempotencyKeyBytes = 255

func ValidateIdempotencyKey(value string) error {
	if len(value) == 0 || len(value) > MaxIdempotencyKeyBytes {
		return E(CodeInvalidRequest, "Idempotency-Key must contain between 1 and 255 bytes")
	}
	for index := range value {
		if value[index] < 0x21 || value[index] > 0x7e {
			return E(CodeInvalidRequest, "Idempotency-Key must contain visible ASCII characters only")
		}
	}
	return nil
}

func ValidSHA256Hex(value string) bool {
	if len(value) != 64 || strings.ToLower(value) != value {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 32
}

func ValidateIdempotencyRequest(request IdempotencyRequest) error {
	if !ValidID(request.Identity.TenantID) || !ValidID(request.Identity.PrincipalID) ||
		!ValidIdempotencyScope(request.Scope) || !ValidSHA256Hex(request.KeyHash) ||
		!ValidSHA256Hex(request.RequestDigest) || !ValidID(request.ClaimToken) {
		return E(CodeInvalidRequest, "idempotency claim is invalid")
	}
	if request.Now.IsZero() || !request.LockedUntil.After(request.Now) || !request.ExpiresAt.After(request.LockedUntil) {
		return E(CodeInvalidRequest, "idempotency claim lifetime is invalid")
	}
	return nil
}
