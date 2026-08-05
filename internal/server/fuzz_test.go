package server

import (
	"bytes"
	"net/http/httptest"
	"testing"

	"github.com/baicie/asteria-drive/internal/drive"
)

func FuzzParseETag(f *testing.F) {
	for _, seed := range []string{`"1"`, `"9223372036854775807"`, "1", `"0"`, `"-1"`, ""} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, value string) {
		revision, err := parseETag(value)
		if err == nil && revision <= 0 {
			t.Fatalf("successful ETag parse returned revision %d", revision)
		}
	})
}

func FuzzDecodeJSONBoundary(f *testing.F) {
	f.Add([]byte(`{"name":"report"}`))
	f.Add([]byte(`{"unknown":true}`))
	f.Add([]byte("{"))
	f.Fuzz(func(t *testing.T, body []byte) {
		if len(body) > 2<<20 {
			return
		}
		request := httptest.NewRequest("POST", "/", bytes.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		var destination struct {
			Name string `json:"name"`
		}
		err := decodeJSON(response, request, &destination, maxJSONBody, false)
		if err != nil && drive.CodeOf(err) == drive.CodeInternal {
			t.Fatalf("JSON boundary returned internal error: %v", err)
		}
	})
}
