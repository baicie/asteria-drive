package drive

import "testing"

func TestValidateCompletedParts(t *testing.T) {
	valid := []CompletedPart{{PartNumber: 1, ETag: `"etag-1"`}, {PartNumber: 2, ETag: `"etag-2"`}}
	if err := validateParts(valid); err != nil {
		t.Fatalf("valid parts: %v", err)
	}
	tests := []struct {
		name  string
		parts []CompletedPart
	}{
		{name: "empty"},
		{name: "duplicate", parts: []CompletedPart{{PartNumber: 1, ETag: `"a"`}, {PartNumber: 1, ETag: `"b"`}}},
		{name: "descending", parts: []CompletedPart{{PartNumber: 2, ETag: `"a"`}, {PartNumber: 1, ETag: `"b"`}}},
		{name: "part zero", parts: []CompletedPart{{PartNumber: 0, ETag: `"a"`}}},
		{name: "part over limit", parts: []CompletedPart{{PartNumber: 10001, ETag: `"a"`}}},
		{name: "missing etag", parts: []CompletedPart{{PartNumber: 1}}},
		{name: "invalid checksum", parts: []CompletedPart{{PartNumber: 1, ETag: `"a"`, Checksum: Checksum{Algorithm: "sha256", Value: "invalid"}}}},
	}
	tooMany := make([]CompletedPart, 10001)
	for index := range tooMany {
		tooMany[index] = CompletedPart{PartNumber: index + 1, ETag: `"etag"`}
	}
	tests = append(tests, struct {
		name  string
		parts []CompletedPart
	}{name: "too many", parts: tooMany})
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := validateParts(test.parts); CodeOf(err) != CodeInvalidRequest {
				t.Fatalf("code=%s err=%v, want invalid_request", CodeOf(err), err)
			}
		})
	}
}

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
