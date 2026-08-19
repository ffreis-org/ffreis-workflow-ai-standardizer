package llm

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	openai "github.com/sashabaranov/go-openai"
)

// Config configures the LLM client. BaseURL accepts any OpenAI-compatible
// endpoint: Anthropic (via LiteLLM proxy), OpenAI, GitHub Models, Ollama, etc.
type Config struct {
	BaseURL    string
	APIKey     string
	Model      string
	MaxRetries int
	Timeout    time.Duration
}

type Client struct {
	inner  *openai.Client
	model  string
	logger *slog.Logger
	cfg    Config
}

func New(cfg Config, logger *slog.Logger) *Client {
	ocfg := openai.DefaultConfig(cfg.APIKey)
	if cfg.BaseURL != "" {
		ocfg.BaseURL = cfg.BaseURL
	}
	return &Client{
		inner:  openai.NewClientWithConfig(ocfg),
		model:  cfg.Model,
		logger: logger,
		cfg:    cfg,
	}
}

// Complete sends a single-turn chat request and returns the response text.
func (c *Client) Complete(ctx context.Context, system, user string) (string, error) {
	maxRetries := c.cfg.MaxRetries
	if maxRetries <= 0 {
		maxRetries = 3
	}

	msgs := []openai.ChatCompletionMessage{
		{Role: openai.ChatMessageRoleSystem, Content: system},
		{Role: openai.ChatMessageRoleUser, Content: user},
	}

	var lastErr error
	for attempt := 1; attempt <= maxRetries; attempt++ {
		resp, err := c.inner.CreateChatCompletion(ctx, openai.ChatCompletionRequest{
			Model:    c.model,
			Messages: msgs,
		})
		if err != nil {
			lastErr = err
			c.logger.Warn("llm request failed", "attempt", attempt, "error", err)
			if attempt < maxRetries {
				time.Sleep(time.Duration(attempt) * 2 * time.Second)
			}
			continue
		}
		if len(resp.Choices) == 0 {
			return "", fmt.Errorf("llm returned no choices")
		}
		c.logger.Info("llm request complete",
			"model", resp.Model,
			"prompt_tokens", resp.Usage.PromptTokens,
			"completion_tokens", resp.Usage.CompletionTokens,
		)
		return resp.Choices[0].Message.Content, nil
	}
	return "", fmt.Errorf("llm request failed after %d attempts: %w", maxRetries, lastErr)
}

// WithModel returns a new Client using a different model.
func (c *Client) WithModel(model string) *Client {
	clone := *c
	clone.model = model
	return &clone
}
