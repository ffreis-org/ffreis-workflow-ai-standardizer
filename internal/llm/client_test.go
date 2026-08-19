package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	openai "github.com/sashabaranov/go-openai"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func chatCompletionResponse(content string) openai.ChatCompletionResponse {
	return openai.ChatCompletionResponse{
		ID:      "test-id",
		Object:  "chat.completion",
		Model:   "test-model",
		Choices: []openai.ChatCompletionChoice{{Message: openai.ChatCompletionMessage{Role: "assistant", Content: content}}},
	}
}

func TestComplete_Success_ReturnsResponseContent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/chat/completions") {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(chatCompletionResponse("hello world"))
	}))
	defer server.Close()

	c := New(Config{BaseURL: server.URL, APIKey: "test-key", Model: "test-model"}, testLogger())

	got, err := c.Complete(context.Background(), "system prompt", "user prompt")
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if got != "hello world" {
		t.Errorf("Complete() = %q, want %q", got, "hello world")
	}
}

func TestComplete_NoChoicesReturned_ReturnsError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(openai.ChatCompletionResponse{Choices: nil})
	}))
	defer server.Close()

	c := New(Config{BaseURL: server.URL, APIKey: "test-key", Model: "test-model", MaxRetries: 1}, testLogger())

	_, err := c.Complete(context.Background(), "system", "user")
	if err == nil || !strings.Contains(err.Error(), "no choices") {
		t.Fatalf("err = %v, want it to mention 'no choices'", err)
	}
}

func TestComplete_AllAttemptsFail_ReturnsWrappedErrorAfterAttempts(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = fmt.Fprint(w, `{"error":{"message":"boom","type":"server_error"}}`)
	}))
	defer server.Close()

	// MaxRetries=1: exactly one attempt, no inter-attempt sleep — keeps the
	// test fast while still covering the "exhausted retries" path.
	c := New(Config{BaseURL: server.URL, APIKey: "test-key", Model: "test-model", MaxRetries: 1}, testLogger())

	_, err := c.Complete(context.Background(), "system", "user")
	if err == nil || !strings.Contains(err.Error(), "llm request failed after 1 attempts") {
		t.Fatalf("err = %v, want it to mention exhausted attempts", err)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("server received %d calls, want 1", got)
	}
}

func TestComplete_RetriesOnceThenSucceeds(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		if n == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = fmt.Fprint(w, `{"error":{"message":"transient","type":"server_error"}}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(chatCompletionResponse("recovered"))
	}))
	defer server.Close()

	// MaxRetries=2 exercises the sleep-then-retry branch (attempt < maxRetries).
	// The retry backoff (2s * attempt) is not configurable, so this test
	// takes a couple of seconds — that's the cost of covering the real
	// branch instead of faking time.
	c := New(Config{BaseURL: server.URL, APIKey: "test-key", Model: "test-model", MaxRetries: 2}, testLogger())

	start := time.Now()
	got, err := c.Complete(context.Background(), "system", "user")
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if got != "recovered" {
		t.Errorf("Complete() = %q, want %q", got, "recovered")
	}
	if elapsed := time.Since(start); elapsed < time.Second {
		t.Errorf("elapsed = %v, want >= 1s (retry backoff should have slept)", elapsed)
	}
	if callCount := atomic.LoadInt32(&calls); callCount != 2 {
		t.Errorf("server received %d calls, want 2", callCount)
	}
}

func TestComplete_DefaultMaxRetries_UsedWhenUnset(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(chatCompletionResponse("ok"))
	}))
	defer server.Close()

	// MaxRetries left at zero — Complete must fall back to its default (3)
	// rather than looping zero times.
	c := New(Config{BaseURL: server.URL, APIKey: "test-key", Model: "test-model"}, testLogger())

	if _, err := c.Complete(context.Background(), "system", "user"); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("server received %d calls, want 1 (should succeed on first try)", got)
	}
}

func TestWithModel_ReturnsClonedClientWithNewModel(t *testing.T) {
	orig := New(Config{APIKey: "test-key", Model: "model-a"}, testLogger())

	clone := orig.WithModel("model-b")

	if orig.model != "model-a" {
		t.Errorf("original client model mutated: got %q, want %q", orig.model, "model-a")
	}
	if clone.model != "model-b" {
		t.Errorf("clone.model = %q, want %q", clone.model, "model-b")
	}
	if clone == orig {
		t.Error("WithModel returned the same pointer as the original client")
	}
}
