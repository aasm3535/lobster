package tools

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFetchURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Test") != "hi" {
			t.Errorf("custom header not forwarded")
		}
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("hello from " + r.Method))
	}))
	defer srv.Close()

	reg := newReg()
	urlJSON, _ := json.Marshal(srv.URL)
	out, err := run(t, reg, "fetch_url", `{"url":`+string(urlJSON)+`,"header":"X-Test: hi"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "HTTP 200") || !strings.Contains(out, "hello from GET") {
		t.Fatalf("unexpected body: %q", out)
	}
}
