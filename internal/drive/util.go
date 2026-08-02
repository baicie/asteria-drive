package drive

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"mime"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

func NewID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	buf := make([]byte, 36)
	hex.Encode(buf[0:8], b[0:4])
	buf[8] = '-'
	hex.Encode(buf[9:13], b[4:6])
	buf[13] = '-'
	hex.Encode(buf[14:18], b[6:8])
	buf[18] = '-'
	hex.Encode(buf[19:23], b[8:10])
	buf[23] = '-'
	hex.Encode(buf[24:36], b[10:16])
	return string(buf), nil
}

func NormalizeName(input string) (display, normalized string, err error) {
	if !utf8.ValidString(input) {
		return "", "", E(CodeInvalidRequest, "name must be valid UTF-8")
	}
	display = norm.NFC.String(input)
	if display == "" || display == "." || display == ".." {
		return "", "", E(CodeInvalidRequest, "name is invalid")
	}
	if strings.TrimSpace(display) != display || len([]byte(display)) > 255 {
		return "", "", E(CodeInvalidRequest, "name is invalid")
	}
	for _, r := range display {
		if r == '/' || r == '\\' || r == 0 || unicode.IsControl(r) {
			return "", "", E(CodeInvalidRequest, "name is invalid")
		}
	}
	return display, norm.NFC.String(strings.ToLower(display)), nil
}

func ValidChecksum(c Checksum) bool {
	if c.Algorithm == "" && c.Value == "" {
		return true
	}
	if c.Algorithm != "sha256" {
		return false
	}
	digest, err := base64.StdEncoding.DecodeString(c.Value)
	return err == nil && len(digest) == 32 && base64.StdEncoding.EncodeToString(digest) == c.Value
}

func ValidMediaType(value string) bool {
	mediaType, _, err := mime.ParseMediaType(value)
	return err == nil && strings.Contains(mediaType, "/")
}
