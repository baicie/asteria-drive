package drive

import "testing"

func TestNormalizeName(t *testing.T) {
	tests := []struct {
		input      string
		display    string
		normalized string
		valid      bool
	}{
		{input: "Report.PDF", display: "Report.PDF", normalized: "report.pdf", valid: true},
		{input: "Cafe\u0301", display: "Caf\u00e9", normalized: "caf\u00e9", valid: true},
		{input: "../bad", valid: false},
		{input: " trailing ", valid: false},
		{input: "", valid: false},
	}
	for _, test := range tests {
		display, normalized, err := NormalizeName(test.input)
		if test.valid && (err != nil || display != test.display || normalized != test.normalized) {
			t.Errorf("NormalizeName(%q)=(%q,%q,%v)", test.input, display, normalized, err)
		}
		if !test.valid && err == nil {
			t.Errorf("NormalizeName(%q) should fail", test.input)
		}
	}
}

func TestChecksumAndMediaTypeValidation(t *testing.T) {
	validSHA256 := "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
	for _, test := range []struct {
		checksum Checksum
		valid    bool
	}{
		{checksum: Checksum{}, valid: true},
		{checksum: Checksum{Algorithm: "sha256", Value: validSHA256}, valid: true},
		{checksum: Checksum{Algorithm: "sha256", Value: "not-base64"}},
		{checksum: Checksum{Algorithm: "sha256", Value: "YQ=="}},
		{checksum: Checksum{Algorithm: "md5", Value: validSHA256}},
		{checksum: Checksum{Value: validSHA256}},
	} {
		if got := ValidChecksum(test.checksum); got != test.valid {
			t.Errorf("ValidChecksum(%+v)=%v, want %v", test.checksum, got, test.valid)
		}
	}
	for value, valid := range map[string]bool{
		"application/octet-stream":  true,
		"text/plain; charset=utf-8": true,
		"text":                      false,
		"text/":                     false,
		"":                          false,
	} {
		if got := ValidMediaType(value); got != valid {
			t.Errorf("ValidMediaType(%q)=%v, want %v", value, got, valid)
		}
	}
}

func TestCursorIsScopedAndTamperProof(t *testing.T) {
	codec, err := NewCursorCodec([]byte("cursor-test-key-that-is-at-least-32-bytes"))
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := codec.Encode("tenant-a", "children:root", CursorPosition{Name: "report", ID: "id-1"})
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := codec.Decode(encoded, "tenant-a", "children:root")
	if err != nil || decoded.ID != "id-1" {
		t.Fatalf("decode cursor: %+v %v", decoded, err)
	}
	for _, attempt := range []struct{ cursor, tenant, scope string }{
		{encoded + "x", "tenant-a", "children:root"},
		{encoded, "tenant-b", "children:root"},
		{encoded, "tenant-a", "children:other"},
	} {
		if _, err := codec.Decode(attempt.cursor, attempt.tenant, attempt.scope); CodeOf(err) != CodeInvalidCursor {
			t.Fatalf("cursor attempt should fail: %+v err=%v", attempt, err)
		}
	}
}
