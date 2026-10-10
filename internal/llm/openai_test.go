package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

type fakeTokens struct {
	token     string
	refreshes int
}

func (f *fakeTokens) AccessToken(context.Context) (string, error) { return f.token, nil }

func (f *fakeTokens) RefreshRejected(_ context.Context, rejected string) (string, error) {
	f.refreshes++
	f.token = rejected + "-new"
	return f.token, nil
}

func sse(events ...string) string {
	var b strings.Builder
	for _, e := range events {
		fmt.Fprintf(&b, "event: x\ndata: %s\n\n", e)
	}
	return b.String()
}

func newServer(t *testing.T, handler http.HandlerFunc) (*OpenAI, *fakeTokens) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	tokens := &fakeTokens{token: "tok"}
	return &OpenAI{BaseURL: server.URL, HTTP: server.Client(), Tokens: tokens}, tokens
}

func TestRespondStreamsTextAndCollectsToolCalls(t *testing.T) {
	var sent map[string]any
	client, _ := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			t.Errorf("missing bearer token")
		}
		json.NewDecoder(r.Body).Decode(&sent)
		fmt.Fprint(w, sse(
			`{"type":"response.output_text.delta","delta":"Hel"}`,
			`{"type":"response.output_text.delta","delta":"lo"}`,
			`{"type":"response.output_item.done","item":{"type":"message","content":[{"type":"output_text","text":"Hello"}]}}`,
			`{"type":"response.output_item.done","item":{"type":"reasoning"}}`,
			`{"type":"response.output_item.done","item":{"type":"function_call","call_id":"c1","name":"bash","namespace":"local","arguments":"{\"command\":\"ls\"}"}}`,
			`{"type":"response.completed","response":{"usage":{"input_tokens":1200,"output_tokens":30,"input_tokens_details":{"cached_tokens":1024}}}}`,
		))
	})

	var streamed strings.Builder
	resp, err := client.Respond(context.Background(), Request{
		Model: "m",
		Input: []Item{UserMessage("hi")},
		Tools: []ToolSpec{{Namespace: Namespace{Name: "local", Description: "shell"}, Name: "bash", Description: "run", Parameters: json.RawMessage(`{"type":"object"}`)}},
	}, func(d Delta) {
		if d.Kind == OutputText {
			streamed.WriteString(d.Text)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if streamed.String() != "Hello" {
		t.Fatalf("streamed %q", streamed.String())
	}
	if len(resp.Items) != 2 || resp.Items[0].Content != "Hello" {
		t.Fatalf("items %+v", resp.Items)
	}
	if resp.Usage != (Usage{InputTokens: 1200, CachedTokens: 1024, OutputTokens: 30}) {
		t.Fatalf("usage %+v", resp.Usage)
	}
	calls := resp.ToolCalls()
	if len(calls) != 1 || calls[0].CallID != "c1" || calls[0].Namespace != "local" {
		t.Fatalf("tool calls %+v", calls)
	}

	if sent["store"] != false || sent["stream"] != true {
		t.Fatalf("plan usage needs store=false stream=true, sent %v", sent)
	}
	tools := sent["tools"].([]any)
	group := tools[0].(map[string]any)
	if group["type"] != "namespace" || group["name"] != "local" || len(group["tools"].([]any)) != 1 {
		t.Fatalf("tools must be grouped in a namespace, sent %v", tools)
	}
	if _, ok := sent["max_output_tokens"]; ok {
		t.Fatal("sent a parameter plan usage rejects")
	}
}

func TestRespondRetriesOnceWithRefreshedToken(t *testing.T) {
	calls := 0
	client, tokens := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Authorization") != "Bearer tok-new" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		fmt.Fprint(w, sse(`{"type":"response.completed"}`))
	})

	if _, err := client.Respond(context.Background(), Request{Model: "m"}, func(Delta) {}); err != nil {
		t.Fatal(err)
	}
	if calls != 2 || tokens.refreshes != 1 {
		t.Fatalf("calls=%d refreshes=%d, want 2 and 1", calls, tokens.refreshes)
	}
}

func TestRespondReportsUsageLimitMidStream(t *testing.T) {
	client, _ := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, sse(
			`{"type":"response.output_text.delta","delta":"par"}`,
			`{"type":"response.failed","response":{"error":{"code":"subscription_sharing_usage_limit_exceeded","message":"limit"}}}`,
		))
	})

	_, err := client.Respond(context.Background(), Request{Model: "m"}, func(Delta) {})
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Code != "subscription_sharing_usage_limit_exceeded" {
		t.Fatalf("got %v, want the usage limit error", err)
	}
	if !strings.Contains(err.Error(), "usage limit") {
		t.Fatalf("message %q should explain the limit", err.Error())
	}
}

func TestRespondFailsOnTruncatedStream(t *testing.T) {
	client, _ := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, sse(`{"type":"response.output_text.delta","delta":"hal"}`))
	})

	if _, err := client.Respond(context.Background(), Request{Model: "m"}, func(Delta) {}); err == nil {
		t.Fatal("a stream without response.completed must be an error")
	}
}

func TestRespondSurfacesHTTPErrors(t *testing.T) {
	client, _ := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error":{"code":"subscription_sharing_unsupported_capability","message":"tool not allowed"}}`)
	})

	_, err := client.Respond(context.Background(), Request{Model: "m"}, func(Delta) {})
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != 400 || !strings.Contains(err.Error(), "tool not allowed") {
		t.Fatalf("got %v", err)
	}
}

func TestListModelsKeepsListedOnesAfterGPT61Sol(t *testing.T) {
	for name, body := range map[string]string{
		"hidden by the server": `{"models":[
			{"slug":"gpt-5.6-luna","display_name":"GPT-5.6-Luna","visibility":"list"},
			{"slug":"internal","display_name":"Hidden","visibility":"hide"}]}`,
		"already listed": `{"models":[
			{"slug":"gpt-5.6-luna","display_name":"GPT-5.6-Luna","visibility":"list"},
			{"slug":"gpt-6.1-sol","display_name":"GPT-6.1-Sol","visibility":"list"}]}`,
	} {
		client, _ := newServer(t, func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/models" {
				t.Errorf("path %s", r.URL.Path)
			}
			fmt.Fprint(w, body)
		})
		models, err := client.ListModels(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		want := []ModelInfo{{Slug: "gpt-6.1-sol", DisplayName: "GPT-6.1-Sol"}, {Slug: "gpt-5.6-luna", DisplayName: "GPT-5.6-Luna"}}
		if !reflect.DeepEqual(models, want) {
			t.Fatalf("%s: models %+v", name, models)
		}
	}
}

func TestItemsKeepRequiredFieldsWhenEmpty(t *testing.T) {
	raw, _ := json.Marshal([]Item{ToolOutput("c1", ""), AssistantMessage(""), {Type: "function_call", CallID: "c2", Name: "bash"}})
	var items []map[string]any
	json.Unmarshal(raw, &items)
	if _, ok := items[0]["output"]; !ok {
		t.Errorf("empty tool output dropped its output field: %s", raw)
	}
	if _, ok := items[1]["content"]; !ok {
		t.Errorf("empty message dropped its content field: %s", raw)
	}
	if _, ok := items[2]["namespace"]; ok || items[2]["arguments"] != "" {
		t.Errorf("function call encoded wrongly: %s", raw)
	}
}

func TestRespondCarriesStreamErrorEventDetails(t *testing.T) {
	client, _ := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, sse(`{"type":"error","code":"server_error","message":"boom"}`))
	})
	_, err := client.Respond(context.Background(), Request{Model: "m"}, func(Delta) {})
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Code != "server_error" || apiErr.Message != "boom" {
		t.Fatalf("got %#v, want the event's code and message", err)
	}
}

func TestRespondFailsOnStreamErrorEvents(t *testing.T) {
	for name, event := range map[string]string{
		"failed without detail": `{"type":"response.failed","response":{}}`,
		"incomplete":            `{"type":"response.incomplete","response":{}}`,
	} {
		client, _ := newServer(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, sse(event)) })
		_, err := client.Respond(context.Background(), Request{Model: "m"}, func(Delta) {})
		if err == nil {
			t.Errorf("%s: should fail", name)
			continue
		}
		if strings.Contains(err.Error(), "200") {
			t.Errorf("%s: message %q shouldn't mention an HTTP status", name, err)
		}
	}
}

func TestRespondRetriesOnlyOnce(t *testing.T) {
	calls := 0
	client, tokens := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusUnauthorized)
	})

	_, err := client.Respond(context.Background(), Request{Model: "m"}, func(Delta) {})
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusUnauthorized {
		t.Fatalf("got %v, want the second 401 surfaced", err)
	}
	if calls != 2 || tokens.refreshes != 1 {
		t.Fatalf("calls=%d refreshes=%d, want 2 and 1", calls, tokens.refreshes)
	}
}

type failingTokens struct{ fakeTokens }

func (f *failingTokens) RefreshRejected(context.Context, string) (string, error) {
	return "", errors.New("session ended")
}

func TestRespondSurfacesRefreshFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()
	client := &OpenAI{BaseURL: server.URL, HTTP: server.Client(), Tokens: &failingTokens{fakeTokens{token: "tok"}}}

	if _, err := client.Respond(context.Background(), Request{Model: "m"}, func(Delta) {}); err == nil || err.Error() != "session ended" {
		t.Fatalf("got %v, want the refresh error", err)
	}
}

func TestDecodeAPIErrorReadsDetail(t *testing.T) {
	var apiErr *APIError
	err := decodeAPIError(http.StatusForbidden, strings.NewReader(`{"detail":"region not permitted"}`))
	if !errors.As(err, &apiErr) || apiErr.Message != "region not permitted" {
		t.Fatalf("got %#v, want the detail text alone", err)
	}
}

func TestDecodeAPIErrorShapes(t *testing.T) {
	cases := map[string]string{
		`{"detail":"region not permitted"}`:                  "region not permitted",
		`upstream exploded`:                                  "upstream exploded",
		`{"error":{"type":"invalid_request","message":"x"}}`: "invalid_request",
	}
	for body, want := range cases {
		err := decodeAPIError(http.StatusForbidden, strings.NewReader(body))
		if !strings.Contains(err.Error(), want) || !strings.Contains(err.Error(), "403") {
			t.Errorf("%s: got %q, want it to mention %q and 403", body, err, want)
		}
	}
}

func TestToolsAreGroupedByNamespace(t *testing.T) {
	local := Namespace{Name: "local", Description: "shell"}
	web := Namespace{Name: "web", Description: "internet"}
	body := responsesRequest(Request{Model: "m", Tools: []ToolSpec{
		{Namespace: local, Name: "bash"},
		{Namespace: web, Name: "fetch"},
		{Namespace: local, Name: "read"},
	}})
	groups := body["tools"].([]map[string]any)
	if len(groups) != 2 || groups[0]["name"] != "local" || groups[1]["name"] != "web" {
		t.Fatalf("groups %v", groups)
	}
	if n := len(groups[0]["tools"].([]map[string]any)); n != 2 {
		t.Fatalf("local has %d tools, want 2", n)
	}
}

func TestRespondStreamsReasoningSummaries(t *testing.T) {
	var sent map[string]any
	client, _ := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&sent)
		fmt.Fprint(w, sse(
			`{"type":"response.created"}`,
			`{"type":"response.reasoning_summary_part.added"}`,
			`{"type":"response.reasoning_summary_text.delta","delta":"**Planning**"}`,
			`{"type":"response.reasoning_summary_part.added"}`,
			`{"type":"response.reasoning_summary_text.delta","delta":"**Checking**"}`,
			`{"type":"response.output_text.delta","delta":"ok"}`,
			`{"type":"response.completed"}`,
		))
	})

	var got []Delta
	if _, err := client.Respond(context.Background(), Request{Model: "m"}, func(d Delta) { got = append(got, d) }); err != nil {
		t.Fatal(err)
	}
	want := []Delta{
		{Kind: ResponseStarted}, {Kind: ReasoningPart}, {Kind: ReasoningText, Text: "**Planning**"},
		{Kind: ReasoningPart}, {Kind: ReasoningText, Text: "**Checking**"}, {Kind: OutputText, Text: "ok"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("deltas %+v", got)
	}
	if reasoning, _ := sent["reasoning"].(map[string]any); reasoning["summary"] != "auto" {
		t.Fatalf("request should ask for reasoning summaries, sent %v", sent["reasoning"])
	}
}

func TestRespondGivesUpOnAStalledStream(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	client, _ := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, sse(`{"type":"response.created"}`, `{"type":"response.output_text.delta","delta":"par"}`))
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-r.Context().Done():
		}
	})
	client.StreamIdleTimeout = 150 * time.Millisecond

	start := time.Now()
	var text strings.Builder
	_, err := client.Respond(context.Background(), Request{Model: "m"}, func(d Delta) { text.WriteString(d.Text) })
	if !errors.Is(err, ErrStreamStalled) || !strings.Contains(err.Error(), "nothing arrived for 150ms") {
		t.Fatalf("got %v, want ErrStreamStalled saying how long it waited", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("took %s to give up", elapsed)
	}
	if text.String() != "par" {
		t.Fatalf("text before the stall should still arrive, got %q", text.String())
	}
}

func TestRespondWaitsForASlowButSteadyStream(t *testing.T) {
	client, _ := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		for _, word := range []string{"one ", "two ", "three ", "four ", "five"} {
			fmt.Fprint(w, sse(fmt.Sprintf(`{"type":"response.output_text.delta","delta":%q}`, word)))
			w.(http.Flusher).Flush()
			time.Sleep(80 * time.Millisecond)
		}
		fmt.Fprint(w, sse(`{"type":"response.completed"}`))
	})
	client.StreamIdleTimeout = 150 * time.Millisecond

	var text strings.Builder
	if _, err := client.Respond(context.Background(), Request{Model: "m"}, func(d Delta) { text.WriteString(d.Text) }); err != nil {
		t.Fatalf("a stream that keeps sending must not be cut off: %v", err)
	}
	if text.String() != "one two three four five" {
		t.Fatalf("got %q", text.String())
	}
}

func TestCancellingIsNotReportedAsAStall(t *testing.T) {
	client, _ := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, sse(`{"type":"response.created"}`))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	})
	client.StreamIdleTimeout = time.Minute
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(100*time.Millisecond, cancel)

	_, err := client.Respond(ctx, Request{Model: "m"}, func(Delta) {})
	if err == nil || errors.Is(err, ErrStreamStalled) {
		t.Fatalf("a user cancel should surface as a cancel, got %v", err)
	}
}

func TestNewOpenAIGuardsAgainstStalls(t *testing.T) {
	if NewOpenAI(nil).StreamIdleTimeout <= 0 {
		t.Fatal("the real client should give up on stalled streams")
	}
}
