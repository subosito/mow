package decision_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/subosito/mow/decision"
)

func TestClientNativeDecisionRoundTrip(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/decisions" || r.Method != "POST" {
			t.Fatalf("request %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Fatal("missing auth")
		}
		var body map[string]json.RawMessage
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			t.Fatal("bad body")
		}
		if string(body["state"]) != `{"value":42}` {
			t.Fatalf("state=%s", body["state"])
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"model":"jev-1.13.0","answers":{"route":{"type":"choice","choice":"review","probabilities":{"review":0.9},"confidence":0.8},"ok":{"type":"noul","noul":0.7}},"usage":{"input_tokens":10,"output_tokens":2}}`))
	}))
	defer srv.Close()
	c, err := decision.New(decision.Options{BaseURL: srv.URL + "/v1", APIKey: "test-key", HTTP: srv.Client()})
	if err != nil {
		t.Fatal(err)
	}
	result, err := c.Evaluate(t.Context(), decision.Request{Model: "jev", State: json.RawMessage(`{"value":42}`), Questions: map[string]decision.Question{
		"route": {Type: "choice", Instructions: json.RawMessage(`"Choose"`), Criteria: json.RawMessage(`{"review":"Needs review","stop":null}`)},
		"ok":    {Type: "noul", Instructions: json.RawMessage(`"Is positive?"`)},
	}})
	if err != nil || result.Model != "jev-1.13.0" || len(result.Answers) != 2 || result.Usage.InputTokens != 10 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}
func TestClientSafeFailuresAndValidation(t *testing.T) {
	if _, err := decision.New(decision.Options{BaseURL: "http://example.com/v1", APIKey: "k"}); err == nil {
		t.Fatal("remote HTTP accepted")
	}
	if _, err := decision.New(decision.Options{BaseURL: "https://user:secret@example.com/v1", APIKey: "k"}); err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatalf("userinfo=%v", err)
	}
	redirected := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://example.invalid/private", http.StatusFound)
	}))
	defer redirected.Close()
	c, _ := decision.New(decision.Options{BaseURL: redirected.URL, APIKey: "test-secret", HTTP: redirected.Client()})
	_, err := c.Evaluate(t.Context(), decision.Request{Model: "jev", State: json.RawMessage(`{}`), Questions: map[string]decision.Question{"q": {Type: "noul", Instructions: json.RawMessage(`"Q"`)}}})
	var he *decision.HTTPError
	if !errors.As(err, &he) || he.StatusCode != 302 || strings.Contains(err.Error(), "private") || strings.Contains(err.Error(), "secret") {
		t.Fatalf("redirect=%v", err)
	}
	for _, req := range []decision.Request{
		{}, {Model: "jev", State: json.RawMessage(`42`), Questions: map[string]decision.Question{"q": {Type: "noul", Instructions: json.RawMessage(`"Q"`)}}},
		{Model: "jev", State: json.RawMessage(`{}`), Questions: map[string]decision.Question{"bad id": {Type: "noul", Instructions: json.RawMessage(`"Q"`)}}},
		{Model: "jev", State: json.RawMessage(`{}`), Questions: map[string]decision.Question{"q": {Type: "choice", Instructions: json.RawMessage(`"Q"`)}}},
	} {
		if _, err := c.Evaluate(context.Background(), req); err == nil {
			t.Fatal("invalid request accepted")
		}
	}
}
func TestClientPreservesAdditionalResponseFields(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"model":"jev","answers":{"q":{"type":"noul","noul":0.9}},"future_field":{"preserve":true}}`))
	}))
	defer srv.Close()
	c, _ := decision.New(decision.Options{BaseURL: srv.URL, APIKey: "k", HTTP: srv.Client()})
	result, err := c.Evaluate(t.Context(), decision.Request{Model: "jev", State: json.RawMessage(`{}`), Questions: map[string]decision.Question{"q": {Type: "noul", Instructions: json.RawMessage(`"Q"`)}}})
	if err != nil || len(result.Extra["future_field"]) == 0 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}
func TestClientRejectsMalformedResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"model":"jev","answers":null}`)) }))
	defer srv.Close()
	c, _ := decision.New(decision.Options{BaseURL: srv.URL, APIKey: "k", HTTP: srv.Client()})
	_, err := c.Evaluate(t.Context(), decision.Request{Model: "jev", State: json.RawMessage(`{}`), Questions: map[string]decision.Question{"q": {Type: "noul", Instructions: json.RawMessage(`"Q"`)}}})
	if err == nil {
		t.Fatal("malformed response accepted")
	}
}
