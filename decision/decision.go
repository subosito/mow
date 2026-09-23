// Package decision implements optional structured-decision HTTP calls.
package decision

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	maxRequestBytes  = 1 << 20
	maxResponseBytes = 4 << 20
	maxQuestions     = 64
)

// Evaluator is the vendor-neutral seam used by hosts and optional extensions.
type Evaluator interface {
	Evaluate(context.Context, Request) (Result, error)
}

// Question preserves a provider-native typed question. Type is normally
// choice, score, or noul. Instructions/criteria are bounded JSON values.
type Question struct {
	Type         string          `json:"type"`
	Instructions json.RawMessage `json:"instructions"`
	Criteria     json.RawMessage `json:"criteria,omitempty"`
}

type Request struct {
	Model     string              `json:"model"`
	State     json.RawMessage     `json:"state"`
	Questions map[string]Question `json:"questions"`
}

type Usage struct {
	InputTokens  int `json:"input_tokens,omitempty"`
	OutputTokens int `json:"output_tokens,omitempty"`
}

// Result retains native answer objects so new provider fields do not require a
// mow release. Callers interpret them under a versioned question-set policy.
type Result struct {
	Model   string                     `json:"model"`
	Answers map[string]json.RawMessage `json:"answers"`
	Usage   Usage                      `json:"usage,omitempty"`
	Extra   map[string]json.RawMessage `json:"-"`
}

func (r *Result) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if v := raw["model"]; len(v) > 0 {
		if err := json.Unmarshal(v, &r.Model); err != nil {
			return err
		}
		delete(raw, "model")
	}
	if v := raw["answers"]; len(v) > 0 {
		if err := json.Unmarshal(v, &r.Answers); err != nil {
			return err
		}
		delete(raw, "answers")
	}
	if v := raw["usage"]; len(v) > 0 {
		if err := json.Unmarshal(v, &r.Usage); err != nil {
			return err
		}
		delete(raw, "usage")
	}
	r.Extra = raw
	return nil
}

type Options struct {
	BaseURL string
	APIKey  string
	HTTP    *http.Client
	Headers map[string]string
}

type Client struct {
	base    string
	key     string
	http    *http.Client
	headers map[string]string
}

// HTTPError identifies an unsuccessful decision request without retaining or
// exposing a provider response body.
type HTTPError struct{ StatusCode int }

func (e *HTTPError) Error() string { return fmt.Sprintf("decision: HTTP %d", e.StatusCode) }

func New(opt Options) (*Client, error) {
	base, err := validateBaseURL(opt.BaseURL)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(opt.APIKey) == "" {
		return nil, errors.New("decision: API key required")
	}
	hc := opt.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 45 * time.Second}
	}
	copyClient := *hc
	copyClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	headers := make(map[string]string, len(opt.Headers))
	for name, value := range opt.Headers {
		name = http.CanonicalHeaderKey(strings.TrimSpace(name))
		if name == "" || strings.ContainsAny(name+value, "\r\n") || name == "Authorization" || name == "Content-Type" || name == "Content-Length" {
			return nil, errors.New("decision: invalid or reserved header")
		}
		headers[name] = value
	}
	return &Client{base: base, key: opt.APIKey, http: &copyClient, headers: headers}, nil
}

func (c *Client) Evaluate(ctx context.Context, request Request) (Result, error) {
	var out Result
	if c == nil {
		return out, errors.New("decision: nil client")
	}
	if err := validateRequest(request); err != nil {
		return out, err
	}
	raw, err := json.Marshal(request)
	if err != nil || len(raw) > maxRequestBytes {
		return out, errors.New("decision: request is invalid or exceeds 1 MiB")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/decisions", bytes.NewReader(raw))
	if err != nil {
		return out, errors.New("decision: build request")
	}
	req.Header.Set("Authorization", "Bearer "+c.key)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	for name, value := range c.headers {
		req.Header.Set(name, value)
	}
	res, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
		return out, errors.New("decision: transport failed")
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return out, &HTTPError{StatusCode: res.StatusCode}
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, maxResponseBytes+1))
	if err != nil || len(body) > maxResponseBytes {
		return out, errors.New("decision: response unreadable or exceeds 4 MiB")
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	if err := dec.Decode(&out); err != nil {
		return Result{}, errors.New("decision: invalid response")
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return Result{}, errors.New("decision: trailing response data")
	}
	if strings.TrimSpace(out.Model) == "" || len(out.Answers) == 0 || len(out.Answers) > maxQuestions {
		return Result{}, errors.New("decision: incomplete response")
	}
	for id, answer := range out.Answers {
		if !validID(id) || len(answer) == 0 || len(answer) > 256<<10 || !json.Valid(answer) {
			return Result{}, errors.New("decision: invalid answer")
		}
	}
	return out, nil
}

func validateRequest(r Request) error {
	if strings.TrimSpace(r.Model) == "" || len(r.Model) > 256 || strings.ContainsAny(r.Model, " \t\r\n\x00") {
		return errors.New("decision: invalid model")
	}
	if len(r.State) == 0 || len(r.State) > 768<<10 || !json.Valid(r.State) {
		return errors.New("decision: invalid state")
	}
	var state any
	if json.Unmarshal(r.State, &state) != nil {
		return errors.New("decision: invalid state")
	}
	switch state.(type) {
	case string, []any, map[string]any:
	default:
		return errors.New("decision: state must be string, object, or array")
	}
	if len(r.Questions) == 0 || len(r.Questions) > maxQuestions {
		return errors.New("decision: requires 1-64 questions")
	}
	for id, q := range r.Questions {
		if !validID(id) || !validQuestion(q) {
			return errors.New("decision: invalid question")
		}
	}
	return nil
}

func validQuestion(q Question) bool {
	if q.Type != "choice" && q.Type != "score" && q.Type != "noul" {
		return false
	}
	if len(q.Instructions) == 0 || len(q.Instructions) > 64<<10 || !json.Valid(q.Instructions) || string(q.Instructions) == "null" {
		return false
	}
	if len(q.Criteria) > 128<<10 || len(q.Criteria) > 0 && !json.Valid(q.Criteria) {
		return false
	}
	if (q.Type == "choice" || q.Type == "score") && (len(q.Criteria) == 0 || string(q.Criteria) == "null") {
		return false
	}
	return true
}

func validID(s string) bool {
	if len(s) == 0 || len(s) > 128 {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' || r == '.') {
			return false
		}
	}
	return true
}

func validateBaseURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		return "", errors.New("decision: invalid base URL")
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost" || u.Hostname() == "::1")) {
		return "", errors.New("decision: HTTPS or loopback HTTP required")
	}
	return strings.TrimRight(raw, "/"), nil
}
