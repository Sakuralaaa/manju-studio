// manju-mcp-bridge：把 stdio 上的 MCP（每行一条 JSON-RPC）转发到漫剧工坊的 Streamable HTTP 端点。
// 给只能配本地命令、或不方便把密钥写进 headers 的 MCP 客户端用（比如 Antigravity 的 command 方式）。
//
// 环境变量：
//
//	MANJU_MCP_URL  漫剧工坊的 MCP 地址，如 https://manju.example.com/mcp
//	MANJU_API_KEY  在「接口」页新建的密钥（只从环境变量读，不会打印）
//
// 构建：go build -o manju-mcp-bridge ./cmd/manju-mcp-bridge
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

var (
	outMu     sync.Mutex
	stateMu   sync.Mutex
	sessionID string
	protoVer  string
)

func emit(b []byte) {
	var buf bytes.Buffer
	if json.Compact(&buf, b) != nil {
		return
	}
	outMu.Lock()
	defer outMu.Unlock()
	buf.WriteByte('\n')
	_, _ = os.Stdout.Write(buf.Bytes())
}

func logf(format string, a ...any) { fmt.Fprintf(os.Stderr, "[manju-bridge] "+format+"\n", a...) }

func rpcErr(id json.RawMessage, code int, msg string) {
	if len(id) == 0 || string(id) == "null" {
		return
	}
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": code, "message": msg}})
	emit(b)
}

func main() {
	url := strings.TrimSpace(os.Getenv("MANJU_MCP_URL"))
	key := strings.TrimSpace(os.Getenv("MANJU_API_KEY"))
	if url == "" || key == "" {
		logf("需要环境变量 MANJU_MCP_URL 和 MANJU_API_KEY")
		os.Exit(2)
	}
	client := &http.Client{Timeout: 15 * time.Minute}
	in := bufio.NewReader(os.Stdin)
	var wg sync.WaitGroup
	for {
		line, err := in.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			msg := append([]byte(nil), bytes.TrimSpace(line)...)
			wg.Add(1)
			go func() { defer wg.Done(); forward(client, url, key, msg) }()
		}
		if err != nil {
			if err != io.EOF {
				logf("读取 stdin 失败：%v", err)
			}
			break
		}
	}
	wg.Wait()
}

func forward(client *http.Client, url, key string, msg []byte) {
	var head struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
	}
	_ = json.Unmarshal(msg, &head)
	req, err := http.NewRequest("POST", url, bytes.NewReader(msg))
	if err != nil {
		rpcErr(head.ID, -32603, "桥接地址无效")
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Authorization", "Bearer "+key)
	stateMu.Lock()
	if sessionID != "" {
		req.Header.Set("Mcp-Session-Id", sessionID)
	}
	if protoVer != "" && head.Method != "initialize" {
		req.Header.Set("Mcp-Protocol-Version", protoVer)
	}
	stateMu.Unlock()
	resp, err := client.Do(req)
	if err != nil {
		logf("请求失败：%v", err)
		rpcErr(head.ID, -32603, "连不上漫剧工坊："+err.Error())
		return
	}
	defer resp.Body.Close()
	if sid := resp.Header.Get("Mcp-Session-Id"); sid != "" {
		stateMu.Lock()
		sessionID = sid
		stateMu.Unlock()
	}
	if resp.StatusCode == 202 || resp.StatusCode == 204 {
		return
	}
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		msg := strings.TrimSpace(string(b))
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(b, &e) == nil && e.Error != "" {
			msg = e.Error
		}
		rpcErr(head.ID, -32603, fmt.Sprintf("漫剧工坊返回 %d：%s", resp.StatusCode, msg))
		return
	}
	ct := resp.Header.Get("Content-Type")
	if strings.HasPrefix(ct, "text/event-stream") {
		readSSE(resp.Body, head.Method)
		return
	}
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		rpcErr(head.ID, -32603, "读取响应失败")
		return
	}
	noteInit(head.Method, b)
	emit(b)
}

// noteInit 记下协商好的协议版本，之后每个请求都带 Mcp-Protocol-Version
func noteInit(method string, b []byte) {
	if method != "initialize" {
		return
	}
	var r struct {
		Result struct {
			ProtocolVersion string `json:"protocolVersion"`
		} `json:"result"`
	}
	if json.Unmarshal(b, &r) == nil && r.Result.ProtocolVersion != "" {
		stateMu.Lock()
		protoVer = r.Result.ProtocolVersion
		stateMu.Unlock()
	}
}

func readSSE(r io.Reader, method string) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 1<<20), 64<<20)
	var data []string
	flush := func() {
		if len(data) > 0 {
			b := []byte(strings.Join(data, "\n"))
			noteInit(method, b)
			emit(b)
			data = nil
		}
	}
	for sc.Scan() {
		ln := sc.Text()
		switch {
		case ln == "":
			flush()
		case strings.HasPrefix(ln, "data:"):
			data = append(data, strings.TrimPrefix(strings.TrimPrefix(ln, "data:"), " "))
		}
	}
	flush()
}
