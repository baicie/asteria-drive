package drive

import (
	"testing"
	"testing/quick"
	"unicode/utf8"
)

func FuzzNormalizeName(f *testing.F) {
	for _, seed := range []string{"report.pdf", "Cafe\u0301", "../bad", " leading", "\x00", "你好.txt"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, input string) {
		display, normalized, err := NormalizeName(input)
		if err != nil {
			return
		}
		if display == "" || normalized == "" || !utf8.ValidString(display) || !utf8.ValidString(normalized) {
			t.Fatalf("successful normalization returned invalid strings: %q %q", display, normalized)
		}
		displayAgain, normalizedAgain, err := NormalizeName(display)
		if err != nil || displayAgain != display || normalizedAgain != normalized {
			t.Fatalf("normalization is not idempotent: first=(%q,%q) second=(%q,%q,%v)", display, normalized, displayAgain, normalizedAgain, err)
		}
	})
}

func FuzzCursorRoundTripAndTamperRejection(f *testing.F) {
	codec, err := NewCursorCodec([]byte("fuzz-cursor-key-that-is-at-least-32-bytes"))
	if err != nil {
		f.Fatal(err)
	}
	f.Add("report", "11111111-1111-4111-8111-111111111111")
	f.Add("", "id")
	f.Fuzz(func(t *testing.T, name, id string) {
		if id == "" || len(name)+len(id) > 4096 {
			return
		}
		encoded, err := codec.Encode("tenant", "scope", CursorPosition{Name: name, ID: id})
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := codec.Decode(encoded, "tenant", "scope")
		if err != nil || decoded.Name != name || decoded.ID != id {
			t.Fatalf("cursor round trip failed: decoded=%+v err=%v", decoded, err)
		}
		mutated := []byte(encoded)
		mutated[len(mutated)/2] ^= 1
		if _, err := codec.Decode(string(mutated), "tenant", "scope"); CodeOf(err) != CodeInvalidCursor {
			t.Fatalf("tampered cursor was accepted: %v", err)
		}
	})
}

func FuzzCompletedPartsValidation(f *testing.F) {
	f.Add([]byte{1, 2, 3}, "etag")
	f.Add([]byte{0, 0}, "")
	f.Fuzz(func(t *testing.T, raw []byte, etag string) {
		if len(raw) > 128 || len(etag) > 2048 {
			return
		}
		parts := make([]CompletedPart, len(raw))
		for index, value := range raw {
			parts[index] = CompletedPart{PartNumber: int(value), ETag: etag}
		}
		if err := validateParts(parts); err != nil {
			return
		}
		first := partsDigest(parts)
		second := partsDigest(append([]CompletedPart(nil), parts...))
		if first == "" || first != second {
			t.Fatalf("valid parts produced an unstable digest: %q %q", first, second)
		}
	})
}

func TestNormalizeNameProperty(t *testing.T) {
	t.Parallel()
	property := func(input string) bool {
		if len(input) > 1024 {
			return true
		}
		display, normalized, err := NormalizeName(input)
		if err != nil {
			return true
		}
		displayAgain, normalizedAgain, err := NormalizeName(display)
		return err == nil && displayAgain == display && normalizedAgain == normalized
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 2000}); err != nil {
		t.Fatal(err)
	}
}
