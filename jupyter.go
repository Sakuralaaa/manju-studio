package main

// Colab 运行时上的 Jupyter Server：REST（会话、文件）+ 内核 WebSocket（执行代码、流式输出）。
// 协议细节见 COLAB_NOTES.md（2026-11-19 在 CPU 运行时上实测）。

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

const proxyHeader = "X-Colab-Runtime-Proxy-Token"

type Jupyter struct {
	BaseURL  string
	tokenFn  func() string
	mu       sync.Mutex // 内核一次只跑一段代码
	kernelID string
	session  string
	http     *http.Client
}

// 直连：WebSocket 不经过 HTTP 代理（实测代理握手会失败）
var directTransport = &http.Transport{Proxy: nil, ForceAttemptHTTP2: false, MaxIdleConns: 8, IdleConnTimeout: 90 * time.Second, TLSHandshakeTimeout: 20 * time.Second}

func newJupyter(base string, tokenFn func() string) *Jupyter {
	return &Jupyter{BaseURL: strings.TrimRight(base, "/"), tokenFn: tokenFn, http: &http.Client{Timeout: 30 * time.Minute}}
}

func (j *Jupyter) do(ctx context.Context, method, path string, body any) (*http.Response, error) {
	var lastErr error
	for attempt := 0; attempt < 6; attempt++ {
		var rd io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rd = bytes.NewReader(b)
		}
		req, _ := http.NewRequestWithContext(ctx, method, j.BaseURL+path, rd)
		req.Header.Set(proxyHeader, j.tokenFn())
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := j.http.Do(req)
		if err != nil {
			lastErr = err
		} else if resp.StatusCode == 502 || resp.StatusCode == 503 || resp.StatusCode == 504 {
			resp.Body.Close()
			lastErr = fmt.Errorf("运行时暂时不可用（%d）", resp.StatusCode)
		} else {
			return resp, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Duration(3+attempt*2) * time.Second):
		}
	}
	return nil, lastErr
}

func (j *Jupyter) Ping(ctx context.Context) error {
	resp, err := j.do(ctx, "GET", "/api", nil)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("运行时返回 %d", resp.StatusCode)
	}
	return nil
}

// ensureKernel 用 sessions 接口建内核（实测比直接建内核稳定）；内核丢了就重建
func (j *Jupyter) ensureKernel(ctx context.Context) error {
	if j.kernelID != "" {
		resp, err := j.do(ctx, "GET", "/api/kernels/"+j.kernelID, nil)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				return nil
			}
		}
		j.kernelID = ""
	}
	resp, err := j.do(ctx, "POST", "/api/sessions", map[string]any{
		"path": "manju.ipynb", "type": "notebook", "name": "manju.ipynb", "kernel": map[string]any{"name": "python3"},
	})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return fmt.Errorf("建内核失败（%d）：%s", resp.StatusCode, upstreamMsg(raw))
	}
	var s struct {
		Kernel struct {
			ID string `json:"id"`
		} `json:"kernel"`
	}
	if json.Unmarshal(raw, &s) != nil || s.Kernel.ID == "" {
		return errors.New("建内核失败：响应里没有内核编号")
	}
	j.kernelID = s.Kernel.ID
	j.session = randHex(16)
	return nil
}

type ExecResult struct {
	Stdout string `json:"stdout"`
	Stderr string `json:"stderr"`
	Result string `json:"result"`
	Error  string `json:"error,omitempty"`
}

// Exec 在内核里执行一段代码，onOut 实时收到 stdout/stderr
func (j *Jupyter) Exec(ctx context.Context, code string, onOut func(stream, text string)) (*ExecResult, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if err := j.ensureKernel(ctx); err != nil {
		return nil, err
	}
	u, _ := url.Parse(j.BaseURL)
	scheme := "wss"
	if u.Scheme == "http" {
		scheme = "ws"
	}
	wsURL := fmt.Sprintf("%s://%s/api/kernels/%s/channels?session_id=%s", scheme, u.Host, j.kernelID, j.session)
	h := http.Header{}
	h.Set(proxyHeader, j.tokenFn())
	dctx, dcancel := context.WithTimeout(ctx, 30*time.Second)
	conn, _, err := websocket.Dial(dctx, wsURL, &websocket.DialOptions{HTTPHeader: h, HTTPClient: &http.Client{Transport: directTransport}})
	dcancel()
	if err != nil {
		j.kernelID = "" // 下次重建
		return nil, errors.New("连接内核失败：" + shortErr(err))
	}
	defer conn.CloseNow()
	conn.SetReadLimit(64 << 20)

	msgID := randHex(16)
	msg := map[string]any{
		"header":        map[string]any{"msg_id": msgID, "username": "manju", "session": j.session, "msg_type": "execute_request", "version": "5.3", "date": time.Now().UTC().Format(time.RFC3339)},
		"parent_header": map[string]any{}, "metadata": map[string]any{}, "channel": "shell", "buffers": []any{},
		"content": map[string]any{"code": code, "silent": false, "store_history": false, "user_expressions": map[string]any{}, "allow_stdin": false, "stop_on_error": true},
	}
	b, _ := json.Marshal(msg)
	if err := conn.Write(ctx, websocket.MessageText, b); err != nil {
		return nil, errors.New("发送代码失败：" + shortErr(err))
	}
	res := &ExecResult{}
	var out, errb strings.Builder
	gotReply, gotIdle := false, false
	interrupted := false
	for !(gotReply && gotIdle) {
		_, data, err := conn.Read(ctx)
		if err != nil {
			if ctx.Err() != nil && !interrupted {
				interrupted = true
				ictx, c := context.WithTimeout(context.Background(), 15*time.Second)
				if resp, e := j.do(ictx, "POST", "/api/kernels/"+j.kernelID+"/interrupt", nil); e == nil {
					resp.Body.Close()
				}
				c()
				return nil, errors.New("已中断")
			}
			return nil, errors.New("内核连接断开：" + shortErr(err))
		}
		var m struct {
			MsgType      string `json:"msg_type"`
			ParentHeader struct {
				MsgID string `json:"msg_id"`
			} `json:"parent_header"`
			Header struct {
				MsgType string `json:"msg_type"`
			} `json:"header"`
			Content json.RawMessage `json:"content"`
		}
		if json.Unmarshal(data, &m) != nil || m.ParentHeader.MsgID != msgID {
			continue
		}
		mt := m.MsgType
		if mt == "" {
			mt = m.Header.MsgType
		}
		switch mt {
		case "stream":
			var c struct {
				Name string `json:"name"`
				Text string `json:"text"`
			}
			_ = json.Unmarshal(m.Content, &c)
			if c.Name == "stderr" {
				errb.WriteString(c.Text)
			} else {
				out.WriteString(c.Text)
			}
			if onOut != nil {
				onOut(c.Name, c.Text)
			}
		case "execute_result", "display_data":
			var c struct {
				Data map[string]any `json:"data"`
			}
			_ = json.Unmarshal(m.Content, &c)
			if t, ok := c.Data["text/plain"].(string); ok && mt == "execute_result" {
				res.Result = t
			}
		case "error":
			var c struct {
				Ename  string `json:"ename"`
				Evalue string `json:"evalue"`
			}
			_ = json.Unmarshal(m.Content, &c)
			res.Error = c.Ename + ": " + c.Evalue
		case "execute_reply":
			gotReply = true
			var c struct {
				Status string `json:"status"`
				Ename  string `json:"ename"`
				Evalue string `json:"evalue"`
			}
			_ = json.Unmarshal(m.Content, &c)
			if c.Status == "error" && res.Error == "" {
				res.Error = c.Ename + ": " + c.Evalue
			}
		case "status":
			var c struct {
				State string `json:"execution_state"`
			}
			_ = json.Unmarshal(m.Content, &c)
			if c.State == "idle" {
				gotIdle = true
			}
		}
	}
	_ = conn.Close(websocket.StatusNormalClosure, "")
	res.Stdout = tailString(out.String(), 64<<10)
	res.Stderr = tailString(errb.String(), 64<<10)
	return res, nil
}

func tailString(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

// contentsPath /content/x → content/x（contents 根目录是 /）
func contentsPath(abs string) string {
	return strings.TrimPrefix(abs, "/")
}

func (j *Jupyter) Upload(ctx context.Context, absPath string, data []byte) error {
	resp, err := j.do(ctx, "PUT", "/api/contents/"+escapePath(contentsPath(absPath)), map[string]any{
		"type": "file", "format": "base64", "content": base64.StdEncoding.EncodeToString(data),
	})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("上传失败（%d）：%s", resp.StatusCode, upstreamMsg(raw))
	}
	return nil
}

func (j *Jupyter) Download(ctx context.Context, absPath string, w io.Writer) (int64, error) {
	resp, err := j.do(ctx, "GET", "/files/"+escapePath(contentsPath(absPath)), nil)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return 0, fmt.Errorf("下载失败（%d）", resp.StatusCode)
	}
	return io.Copy(w, resp.Body)
}

func escapePath(p string) string {
	parts := strings.Split(p, "/")
	for i, s := range parts {
		parts[i] = url.PathEscape(s)
	}
	return strings.Join(parts, "/")
}
