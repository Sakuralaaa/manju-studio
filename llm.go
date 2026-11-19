package main

import (
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

var llmClient = &http.Client{Timeout: 8 * time.Minute}

// callChat 调用文本渠道（OpenAI 兼容 /chat/completions，非流式）
func callChat(ctx context.Context, ch *Channel, model, system, user string, temperature float64) (string, error) {
	if model == "" {
		if len(ch.Models) == 0 {
			return "", errors.New("文本渠道还没有选择模型")
		}
		model = ch.Models[0]
	}
	body := map[string]any{
		"model": model,
		"messages": []any{
			map[string]any{"role": "system", "content": system},
			map[string]any{"role": "user", "content": user},
		},
		"stream":      false,
		"temperature": temperature,
	}
	b, _ := json.Marshal(body)
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		req, _ := http.NewRequestWithContext(ctx, "POST", apiURL(ch.BaseURL, "/chat/completions"), bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
		if ch.APIKey != "" {
			req.Header.Set("Authorization", "Bearer "+ch.APIKey)
		}
		resp, err := llmClient.Do(req)
		if err != nil {
			lastErr = errors.New("请求失败：" + shortErr(err))
			if ctx.Err() != nil {
				break
			}
			time.Sleep(2 * time.Second)
			continue
		}
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
		resp.Body.Close()
		if resp.StatusCode >= 500 {
			lastErr = fmt.Errorf("上游返回 %d：%s", resp.StatusCode, upstreamMsg(raw))
			time.Sleep(2 * time.Second)
			continue
		}
		if resp.StatusCode >= 300 {
			return "", fmt.Errorf("上游返回 %d：%s", resp.StatusCode, upstreamMsg(raw))
		}
		var r struct {
			Choices []struct {
				Message struct {
					Content any `json:"content"`
				} `json:"message"`
			} `json:"choices"`
		}
		if err := json.Unmarshal(raw, &r); err != nil || len(r.Choices) == 0 {
			return "", errors.New("响应格式不对：" + upstreamMsg(raw))
		}
		text := ""
		switch v := r.Choices[0].Message.Content.(type) {
		case string:
			text = v
		case []any:
			for _, p := range v {
				if m, ok := p.(map[string]any); ok {
					if t, ok := m["text"].(string); ok {
						text += t
					}
				}
			}
		}
		text = stripThink(text)
		if strings.TrimSpace(text) == "" {
			return "", errors.New("模型没有返回内容")
		}
		return strings.TrimSpace(text), nil
	}
	return "", lastErr
}

// stripThink 去掉推理模型的 <think> 段
func stripThink(s string) string {
	for {
		i := strings.Index(s, "<think>")
		if i < 0 {
			return s
		}
		j := strings.Index(s[i:], "</think>")
		if j < 0 {
			return s[:i]
		}
		s = s[:i] + s[i+j+len("</think>"):]
	}
}

// extractJSON 从模型输出里取出第一个完整的 JSON 对象
func extractJSON(s string, v any) error {
	s = strings.TrimSpace(s)
	if i := strings.Index(s, "```"); i >= 0 {
		rest := s[i+3:]
		if nl := strings.Index(rest, "\n"); nl >= 0 {
			rest = rest[nl+1:]
		}
		if j := strings.Index(rest, "```"); j >= 0 {
			if json.Unmarshal([]byte(rest[:j]), v) == nil {
				return nil
			}
		}
	}
	start := strings.IndexAny(s, "{[")
	for start >= 0 && start < len(s) {
		depth, inStr, esc := 0, false, false
		for k := start; k < len(s); k++ {
			c := s[k]
			if inStr {
				if esc {
					esc = false
				} else if c == '\\' {
					esc = true
				} else if c == '"' {
					inStr = false
				}
				continue
			}
			switch c {
			case '"':
				inStr = true
			case '{', '[':
				depth++
			case '}', ']':
				depth--
				if depth == 0 {
					if err := json.Unmarshal([]byte(s[start:k+1]), v); err == nil {
						return nil
					}
					k = len(s)
				}
			}
		}
		next := strings.IndexAny(s[start+1:], "{[")
		if next < 0 {
			break
		}
		start += next + 1
	}
	return errors.New("模型输出里没有可用的 JSON")
}

func textChannelFor(p *Project) (*Channel, string, error) {
	var ch *Channel
	if p.TextChannelID > 0 {
		ch, _ = getChannel(p.TextChannelID)
	}
	if ch == nil || ch.Kind != "text" {
		ch = firstChannel("text")
	}
	if ch == nil {
		return nil, "", errors.New("还没有文本渠道，请先在「渠道」里添加")
	}
	model := p.TextModel
	if model == "" || !contains(ch.Models, model) {
		model = ""
		if len(ch.Models) > 0 {
			model = ch.Models[0]
		}
	}
	return ch, model, nil
}

func imageChannelFor(p *Project) (*Channel, string, error) {
	var ch *Channel
	if p.ImageChannelID > 0 {
		ch, _ = getChannel(p.ImageChannelID)
	}
	if ch == nil || ch.Kind != "image" {
		ch = firstChannel("image")
	}
	if ch == nil {
		return nil, "", errors.New("还没有生图渠道，请先在「渠道」里添加")
	}
	model := p.ImageModel
	if model == "" || !contains(ch.Models, model) {
		model = ""
		if len(ch.Models) > 0 {
			model = ch.Models[0]
		}
	}
	return ch, model, nil
}
