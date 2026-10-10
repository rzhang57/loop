// OpenAI client for the Responses API, paid for by a signed-in ChatGPT plan.
// Respond builds the request body plan usage requires (store off, streaming on, tools grouped into
// namespaces), sends it with a token from the TokenSource (auth.Client in practice), retries once
// with a refreshed token on 401, and readStream turns the server-sent events into Deltas and a final
// Response. ListModels backs the tui model picker. APIError maps OpenAI's error codes, including the
// plan-usage ones, to readable messages.

package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	DefaultBaseURL = "https://api.openai.com/v1"
)

// TokenSource supplies the ChatGPT access token and replaces one the API rejected.
type TokenSource interface {
	AccessToken(ctx context.Context) (string, error)
	RefreshRejected(ctx context.Context, rejected string) (string, error)
}

type APIError struct {
	Status  int
	Code    string
	Message string
}

func (e *APIError) Error() string {
	switch e.Code {
	case "subscription_sharing_usage_limit_exceeded":
		return "you've reached your ChatGPT plan's usage limit for this app; check usage settings in ChatGPT"
	case "subscription_sharing_user_not_eligible":
		return "this ChatGPT account isn't eligible to use its plan in other apps"
	case "subscription_sharing_unsupported_capability":
		return "the request used something ChatGPT plan usage doesn't support: " + e.Message
	case "subscription_sharing_usage_unavailable", "subscription_sharing_user_unavailable":
		return "ChatGPT plan usage is temporarily unavailable, try again in a moment"
	case "subscription_sharing_invalid_user":
		return "OpenAI no longer accepts this sign-in, sign in again"
	}
	detail := strings.TrimSpace(fmt.Sprintf("%s %s", statusText(e.Status), e.Code))
	switch {
	case e.Message != "" && detail != "":
		return fmt.Sprintf("openai: %s (%s)", e.Message, detail)
	case e.Message != "":
		return "openai: " + e.Message
	default:
		return fmt.Sprintf("openai: request failed (%s)", detail)
	}
}

func statusText(status int) string {
	if status == 0 {
		return ""
	}
	return fmt.Sprint(status)
}

// OpenAI calls the Responses API with a signed-in ChatGPT account's plan.
type OpenAI struct {
	BaseURL string
	HTTP    *http.Client
	Tokens  TokenSource
	// StreamIdleTimeout gives up on a response that sends nothing for this long; zero waits forever.
	StreamIdleTimeout time.Duration
}

// NewOpenAI gives up on a request whose response doesn't start within a minute, but lets a stream run as long as it needs.
func NewOpenAI(tokens TokenSource) *OpenAI {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = time.Minute
	return &OpenAI{BaseURL: DefaultBaseURL, HTTP: &http.Client{Transport: transport}, Tokens: tokens, StreamIdleTimeout: 90 * time.Second}
}

// gpt61Sol works on every ChatGPT plan, but the model list only shows it to clients claiming a
// recent Codex version, so it is always offered first.
var gpt61Sol = ModelInfo{Slug: "gpt-6.1-sol", DisplayName: "GPT-6.1-Sol"}

// ListModels returns the models the signed-in account can pick from, GPT-6.1 Sol first.
func (o *OpenAI) ListModels(ctx context.Context) ([]ModelInfo, error) {
	resp, err := o.do(ctx, http.MethodGet, "/models", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var body struct {
		Models []struct {
			Slug        string `json:"slug"`
			DisplayName string `json:"display_name"`
			Visibility  string `json:"visibility"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("decode model list: %w", err)
	}
	models := []ModelInfo{gpt61Sol}
	for _, m := range body.Models {
		if m.Visibility == "list" && m.Slug != gpt61Sol.Slug {
			models = append(models, ModelInfo{Slug: m.Slug, DisplayName: m.DisplayName})
		}
	}
	return models, nil
}

func (o *OpenAI) Respond(ctx context.Context, req Request, onDelta func(Delta)) (Response, error) {
	body, err := json.Marshal(responsesRequest(req))
	if err != nil {
		return Response{}, err
	}
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	resp, err := o.do(ctx, http.MethodPost, "/responses", body)
	if err != nil {
		return Response{}, err
	}
	defer resp.Body.Close()
	if o.StreamIdleTimeout <= 0 {
		return readStream(resp.Body, onDelta)
	}
	stream := newIdleReader(resp.Body, o.StreamIdleTimeout, func() { cancel(ErrStreamStalled) })
	defer stream.stop()
	out, err := readStream(stream, onDelta)
	if errors.Is(context.Cause(ctx), ErrStreamStalled) {
		return out, fmt.Errorf("%w: nothing arrived for %s, so the request was abandoned", ErrStreamStalled, o.StreamIdleTimeout)
	}
	return out, err
}

// responsesRequest builds the body plan usage requires: store off, stream on, tools grouped in namespaces.
func responsesRequest(req Request) map[string]any {
	body := map[string]any{
		"model":  req.Model,
		"input":  req.Input,
		"store":  false,
		"stream": true,
		// Reasoning summaries are only streamed when requested; every model on the plan accepts this.
		"reasoning": map[string]string{"summary": "auto"},
	}
	if req.Instructions != "" {
		body["instructions"] = req.Instructions
	}
	var groups []map[string]any
	index := map[string]int{}
	for _, t := range req.Tools {
		i, ok := index[t.Namespace.Name]
		if !ok {
			i = len(groups)
			index[t.Namespace.Name] = i
			groups = append(groups, map[string]any{
				"type":        "namespace",
				"name":        t.Namespace.Name,
				"description": t.Namespace.Description,
				"tools":       []map[string]any{},
			})
		}
		groups[i]["tools"] = append(groups[i]["tools"].([]map[string]any), map[string]any{
			"type":        "function",
			"name":        t.Name,
			"description": t.Description,
			"parameters":  t.Parameters,
		})
	}
	if len(groups) > 0 {
		body["tools"] = groups
	}
	return body
}

// do sends an authenticated request, replacing the access token and retrying once if it was rejected.
func (o *OpenAI) do(ctx context.Context, method, path string, body []byte) (*http.Response, error) {
	token, err := o.Tokens.AccessToken(ctx)
	if err != nil {
		return nil, err
	}
	resp, err := o.send(ctx, method, path, body, token)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		resp.Body.Close()
		if token, err = o.Tokens.RefreshRejected(ctx, token); err != nil {
			return nil, err
		}
		if resp, err = o.send(ctx, method, path, body, token); err != nil {
			return nil, err
		}
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		return nil, decodeAPIError(resp.StatusCode, resp.Body)
	}
	return resp, nil
}

func (o *OpenAI) send(ctx context.Context, method, path string, body []byte, token string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, o.BaseURL+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return o.HTTP.Do(req)
}

// decodeAPIError reads an error body in OpenAI's usual {"error":{...}} shape, the {"detail":...} shape
// some admission errors use, or anything else as plain text.
func decodeAPIError(status int, body io.Reader) error {
	var payload struct {
		Error *struct {
			Code    string `json:"code"`
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
		Detail string `json:"detail"`
	}
	raw, _ := io.ReadAll(io.LimitReader(body, 64*1024))
	text := strings.TrimSpace(string(raw))
	switch {
	case json.Unmarshal(raw, &payload) != nil:
		return &APIError{Status: status, Message: text}
	case payload.Error != nil:
		code := payload.Error.Code
		if code == "" {
			code = payload.Error.Type
		}
		return &APIError{Status: status, Code: code, Message: payload.Error.Message}
	case payload.Detail != "":
		return &APIError{Status: status, Message: payload.Detail}
	default:
		return &APIError{Status: status, Message: text}
	}
}

type streamEvent struct {
	Type     string          `json:"type"`
	Delta    string          `json:"delta"`
	Item     json.RawMessage `json:"item"`
	Code     string          `json:"code"`
	Message  string          `json:"message"`
	Response struct {
		Error *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
		Usage struct {
			InputTokens        int `json:"input_tokens"`
			OutputTokens       int `json:"output_tokens"`
			InputTokensDetails struct {
				CachedTokens int `json:"cached_tokens"`
			} `json:"input_tokens_details"`
		} `json:"usage"`
	} `json:"response"`
}

type outputItem struct {
	Type      string `json:"type"`
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	Arguments string `json:"arguments"`
	Content   []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
}

// readStream consumes server-sent events until the response completes or fails.
func readStream(r io.Reader, onDelta func(Delta)) (Response, error) {
	var resp Response
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		data, ok := strings.CutPrefix(scanner.Text(), "data:")
		if !ok {
			continue
		}
		data = strings.TrimSpace(data)
		if data == "" || data == "[DONE]" {
			continue
		}
		var event streamEvent
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			return resp, fmt.Errorf("decode stream event: %w", err)
		}
		switch event.Type {
		case "response.created":
			onDelta(Delta{Kind: ResponseStarted})
		case "response.reasoning_summary_part.added":
			onDelta(Delta{Kind: ReasoningPart})
		case "response.reasoning_summary_text.delta":
			onDelta(Delta{Kind: ReasoningText, Text: event.Delta})
		case "response.output_text.delta":
			onDelta(Delta{Kind: OutputText, Text: event.Delta})
		case "response.output_item.done":
			if item, ok := decodeOutputItem(event.Item); ok {
				resp.Items = append(resp.Items, item)
			}
		case "response.completed":
			u := event.Response.Usage
			resp.Usage = Usage{InputTokens: u.InputTokens, CachedTokens: u.InputTokensDetails.CachedTokens, OutputTokens: u.OutputTokens}
			return resp, nil
		case "response.failed", "response.incomplete":
			if e := event.Response.Error; e != nil {
				return resp, &APIError{Code: e.Code, Message: e.Message}
			}
			return resp, fmt.Errorf("openai: response ended early (%s)", event.Type)
		case "error":
			return resp, &APIError{Code: event.Code, Message: event.Message}
		}
	}
	if err := scanner.Err(); err != nil {
		return resp, err
	}
	return resp, errors.New("openai: stream ended before the response completed")
}

func decodeOutputItem(raw json.RawMessage) (Item, bool) {
	var out outputItem
	if json.Unmarshal(raw, &out) != nil {
		return Item{}, false
	}
	switch out.Type {
	case "function_call":
		return Item{Type: "function_call", CallID: out.CallID, Name: out.Name, Namespace: out.Namespace, Arguments: out.Arguments}, true
	case "message":
		var text strings.Builder
		for _, part := range out.Content {
			if part.Type == "output_text" {
				text.WriteString(part.Text)
			}
		}
		return AssistantMessage(text.String()), true
	}
	return Item{}, false
}
