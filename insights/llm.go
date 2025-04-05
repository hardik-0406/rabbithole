package insights

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"
)

const (
	maxRetries        = 3
	retryDelay        = 1 * time.Second
	backoffMultiplier = 2
)

type LLMClient struct {
	apiURL string
	apiKey string
	client *http.Client
}

func NewLLMClient(apiURL, apiKey string) *LLMClient {
	return &LLMClient{
		apiURL: apiURL,
		apiKey: apiKey,
		client: &http.Client{
			Timeout: llmTimeout,
			Transport: &http.Transport{
				MaxIdleConns:        100,
				MaxIdleConnsPerHost: 100,
				IdleConnTimeout:     90 * time.Second,
			},
		},
	}
}

func (c *LLMClient) callLLM(ctx context.Context, prompt string) (string, error) {
	var lastErr error
	currentDelay := retryDelay

	for attempt := 0; attempt < maxRetries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(currentDelay):
				currentDelay *= time.Duration(backoffMultiplier)
			}
		}

		result, err := c.makeRequest(ctx, prompt)
		if err == nil {
			return result, nil
		}

		lastErr = err
		log.Printf("LLM request attempt %d failed: %v", attempt+1, err)
	}

	return "", fmt.Errorf("failed after %d attempts: %w", maxRetries, lastErr)
}

func (c *LLMClient) makeRequest(ctx context.Context, prompt string) (string, error) {
	data := map[string]interface{}{
		"model": chatModel,
		"messages": []map[string]string{
			{"role": "user", "content": prompt},
		},
		"temperature": 0.7,
		"max_tokens":  1000,
	}

	body, err := json.Marshal(data)
	if err != nil {
		return "", fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", c.apiURL, bytes.NewBuffer(body))
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, err := c.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("LLM API returned status %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}

	if len(result.Choices) == 0 {
		return "", fmt.Errorf("no response from LLM")
	}

	return result.Choices[0].Message.Content, nil
}
