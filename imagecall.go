package main

// 生图请求：按渠道能力档案拼请求（原生发送 / 写进提示词），调用上游并把图片落盘。
// 规划与解析逻辑沿用影室（实测过三类中转 + 官方接口）。

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type Params struct {
	Size        string `json:"size,omitempty"`
	Aspect      string `json:"aspect,omitempty"`
	Resolution  string `json:"resolution,omitempty"`
	Quality     string `json:"quality,omitempty"`
	N           int    `json:"n,omitempty"`
	Format      string `json:"format,omitempty"`
	Compression int    `json:"compression,omitempty"`
	Background  string `json:"background,omitempty"`
	Moderation  string `json:"moderation,omitempty"`
}

type JobIn struct {
	ChannelID int64  `json:"channel_id"`
	Model     string `json:"model"`
	Prompt    string `json:"prompt"`
	Params    Params `json:"params"`
}

type genResult struct {
	images   [][]byte
	toolPath bool
}

type refFile struct {
	Name string
	Mime string
	Data []byte
}

// ---------- 请求规划：原生发送 / 写进提示词 ----------

type Plan struct {
	Endpoint string         `json:"endpoint"`
	Prompt   string         `json:"prompt"`
	Body     map[string]any `json:"body"`
	Calls    int            `json:"calls"`
	Edit     bool           `json:"edit"`
	RefMode  string         `json:"ref_mode"`
}

func ratioOf(w, h int) string {
	g := gcd(w, h)
	if g == 0 {
		return ""
	}
	return fmt.Sprintf("%d:%d", w/g, h/g)
}

func gcd(a, b int) int {
	for b != 0 {
		a, b = b, a%b
	}
	return a
}

func orientation(ratio string) string {
	parts := strings.Split(ratio, ":")
	if len(parts) != 2 {
		return ""
	}
	a, _ := strconv.ParseFloat(parts[0], 64)
	b, _ := strconv.ParseFloat(parts[1], 64)
	switch {
	case a > b:
		return "横构图"
	case a < b:
		return "竖构图"
	default:
		return "方形构图"
	}
}

func buildPlan(ch *Channel, in JobIn, hasRefs bool) Plan {
	c := ch.Caps
	p := in.Params
	body := map[string]any{"model": in.Model}
	var phrases []string

	// 画幅：比例
	if p.Aspect != "" && p.Aspect != "auto" {
		switch c.Aspect.Mode {
		case "native":
			body["aspect_ratio"] = p.Aspect
		case "prompt":
			phrases = append(phrases, fmt.Sprintf("画面比例 %s，%s", p.Aspect, orientation(p.Aspect)))
		}
	}
	// 画幅：尺寸
	if p.Size != "" {
		switch c.Size.Mode {
		case "native":
			body["size"] = p.Size
		case "prompt":
			if p.Size != "auto" {
				var w, h int
				if _, err := fmt.Sscanf(p.Size, "%dx%d", &w, &h); err == nil && w > 0 && h > 0 {
					r := ratioOf(w, h)
					phrases = append(phrases, fmt.Sprintf("画面比例 %s，%s，约 %d×%d 像素", r, orientation(r), w, h))
				}
			}
		}
	}
	// 分辨率
	if p.Resolution != "" {
		switch c.Resolution.Mode {
		case "native":
			body["resolution"] = p.Resolution
		case "prompt":
			switch strings.ToLower(p.Resolution) {
			case "4k":
				phrases = append(phrases, "4K 超高分辨率，细节极其清晰")
			case "2k":
				phrases = append(phrases, "2K 高分辨率，细节清晰")
			case "1k":
				phrases = append(phrases, "1K 分辨率")
			default:
				phrases = append(phrases, p.Resolution+" 分辨率")
			}
		}
	}
	// 质量
	if p.Quality != "" {
		switch c.Quality.Mode {
		case "native":
			body["quality"] = p.Quality
		case "prompt":
			switch p.Quality {
			case "high", "xhigh", "max", "hd":
				phrases = append(phrases, "高画质，细节精致锐利，质感真实")
			case "medium", "standard":
				phrases = append(phrases, "画质精细，细节完整")
			case "low":
				phrases = append(phrases, "简洁画面")
			}
		}
	}
	// 背景
	if p.Background != "" && p.Background != "auto" {
		switch c.Background.Mode {
		case "native":
			body["background"] = p.Background
		case "prompt":
			if p.Background == "transparent" {
				phrases = append(phrases, "透明背景，主体独立无背景")
			} else if p.Background == "opaque" {
				phrases = append(phrases, "完整的不透明背景")
			}
		}
	} else if p.Background == "auto" && c.Background.Mode == "native" {
		body["background"] = "auto"
	}
	// 输出格式 / 压缩
	if p.Format != "" && c.Format.Mode == "native" {
		body["output_format"] = p.Format
		if c.Compression.Mode == "native" && p.Compression > 0 && p.Compression < 100 && (p.Format == "jpeg" || p.Format == "webp") {
			body["output_compression"] = p.Compression
		}
	}
	// 审核
	if p.Moderation != "" && c.Moderation.Mode == "native" {
		body["moderation"] = p.Moderation
	}
	// 返回方式
	if c.Response.Mode == "native" && ch.APIType == "images" {
		rf := "b64_json" // 界面默认「直接返回图片数据」
		if len(c.Response.Values) > 0 && c.Response.Values[0] != "" {
			rf = c.Response.Values[0]
		}
		body["response_format"] = rf
	}
	// 张数
	n := p.N
	if n < 1 {
		n = 1
	}
	if c.N.Max > 0 && n > c.N.Max {
		n = c.N.Max
	}
	calls := 1
	switch c.N.Mode {
	case "native":
		if n > 1 || ch.APIType == "images" {
			body["n"] = n
		}
	case "loop":
		calls = n
	}

	prompt := in.Prompt
	if len(phrases) > 0 {
		sep := "\n\n"
		prompt = strings.TrimRight(prompt, " \n。.，,") + "。" + sep + strings.Join(phrases, "，") + "。"
	}
	body["prompt"] = prompt

	plan := Plan{Prompt: prompt, Body: body, Calls: calls}
	refMode := c.Refs.Mode
	if refMode == "" {
		refMode = "multipart"
	}
	plan.RefMode = refMode
	if ch.APIType == "chat" {
		plan.Endpoint = "/chat/completions"
	} else if hasRefs && refMode != "off" {
		plan.Endpoint = "/images/edits"
		plan.Edit = true
	} else {
		plan.Endpoint = "/images/generations"
	}
	return plan
}

// ---------- 调用上游 ----------

var httpClient = &http.Client{Timeout: 7 * time.Minute}

func doCall(ctx context.Context, ch *Channel, plan Plan, refs []refFile) (*genResult, error) {
	endpoint := apiURL(ch.BaseURL, plan.Endpoint)
	var req *http.Request
	var err error
	switch {
	case ch.APIType == "chat":
		content := []any{map[string]any{"type": "text", "text": plan.Prompt}}
		for _, rf := range refs {
			content = append(content, map[string]any{"type": "image_url", "image_url": map[string]any{"url": dataURL(rf)}})
		}
		body := map[string]any{}
		for k, v := range plan.Body {
			if k != "prompt" {
				body[k] = v
			}
		}
		if len(refs) == 0 {
			body["messages"] = []any{map[string]any{"role": "user", "content": plan.Prompt}}
		} else {
			body["messages"] = []any{map[string]any{"role": "user", "content": content}}
		}
		body["stream"] = false
		b, _ := json.Marshal(body)
		req, err = http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(b))
		if err == nil {
			req.Header.Set("Content-Type", "application/json")
		}
	case plan.Edit && plan.RefMode == "json":
		body := map[string]any{}
		for k, v := range plan.Body {
			body[k] = v
		}
		imgs := []any{}
		for _, rf := range refs {
			imgs = append(imgs, map[string]any{"image_url": dataURL(rf)})
		}
		body["images"] = imgs
		b, _ := json.Marshal(body)
		req, err = http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(b))
		if err == nil {
			req.Header.Set("Content-Type", "application/json")
		}
	case plan.Edit:
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		for k, v := range plan.Body {
			_ = mw.WriteField(k, fmt.Sprint(v))
		}
		field := "image[]"
		if len(refs) == 1 {
			field = "image"
		}
		for _, rf := range refs {
			h := make(textproto.MIMEHeader)
			h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="%s"; filename="%s"`, field, rf.Name))
			h.Set("Content-Type", rf.Mime)
			pw, _ := mw.CreatePart(h)
			_, _ = pw.Write(rf.Data)
		}
		_ = mw.Close()
		req, err = http.NewRequestWithContext(ctx, "POST", endpoint, &buf)
		if err == nil {
			req.Header.Set("Content-Type", mw.FormDataContentType())
		}
	default:
		b, _ := json.Marshal(plan.Body)
		req, err = http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(b))
		if err == nil {
			req.Header.Set("Content-Type", "application/json")
		}
	}
	if err != nil {
		return nil, err
	}
	if ch.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+ch.APIKey)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, errors.New("请求超时")
		}
		return nil, errors.New("请求失败：" + shortErr(err))
	}
	defer resp.Body.Close()
	res := &genResult{toolPath: detectToolPath(resp.Header)}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 512<<20))
	if err != nil {
		return nil, errors.New("读取响应失败：" + shortErr(err))
	}
	if resp.StatusCode >= 300 {
		return res, fmt.Errorf("上游返回 %d：%s", resp.StatusCode, upstreamMsg(raw))
	}
	var refsOut []string
	if ch.APIType == "chat" {
		refsOut = parseChatImages(raw)
	} else {
		refsOut = parseImagesResponse(raw)
	}
	if len(refsOut) == 0 {
		return res, errors.New("响应里没有图片：" + upstreamMsg(raw))
	}
	var dlErr error
	for _, ref := range refsOut {
		data, err := fetchImage(ctx, ch, ref)
		if err != nil {
			log.Printf("下载图片失败: %v (%s)", err, logURL(ref))
			dlErr = err
			continue
		}
		res.images = append(res.images, data)
	}
	if len(res.images) == 0 {
		// 上游已经出图但取不回来：把原因带给界面（401/403 多半是图片地址也要密钥、或密钥不对）
		if dlErr != nil {
			why := "网络错误"
			if m := dlErr.Error(); strings.HasPrefix(m, "下载图片 HTTP") {
				why = strings.TrimPrefix(m, "下载图片 ")
			}
			return res, fmt.Errorf("图片已生成但下载失败（%s）", why)
		}
		return res, errors.New("图片下载失败")
	}
	return res, nil
}

// detectToolPath 识别中转降级到「对话工具链路」的响应头
func detectToolPath(h http.Header) bool {
	for k, vs := range h {
		lk := strings.ToLower(k)
		if strings.HasSuffix(lk, "-image-path") {
			for _, v := range vs {
				if strings.Contains(strings.ToLower(v), "tool") {
					return true
				}
			}
		}
	}
	return false
}

func dataURL(rf refFile) string {
	return "data:" + rf.Mime + ";base64," + base64.StdEncoding.EncodeToString(rf.Data)
}

func parseImagesResponse(raw []byte) []string {
	var r struct {
		Data []struct {
			B64 string `json:"b64_json"`
			URL string `json:"url"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil
	}
	out := []string{}
	for _, d := range r.Data {
		if d.B64 != "" {
			out = append(out, "b64:"+d.B64)
		} else if d.URL != "" {
			out = append(out, d.URL)
		}
	}
	return out
}

var (
	mdImgRe   = regexp.MustCompile(`!\[[^\]]*\]\(([^)\s]+)\)`)
	dataURLRe = regexp.MustCompile(`data:image/[a-zA-Z+.-]+;base64,[A-Za-z0-9+/=]+`)
	urlRe     = regexp.MustCompile(`https?://[^\s)"'<>\]]+\.(?:png|jpe?g|webp|gif)(?:\?[^\s)"'<>\]]*)?`)
)

func parseChatImages(raw []byte) []string {
	var r struct {
		Data    []json.RawMessage `json:"data"`
		Choices []struct {
			Message struct {
				Content any `json:"content"`
				Images  []struct {
					ImageURL struct {
						URL string `json:"url"`
					} `json:"image_url"`
				} `json:"images"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil
	}
	out := []string{}
	seen := map[string]bool{}
	add := func(s string) {
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	for _, c := range r.Choices {
		for _, im := range c.Message.Images {
			add(im.ImageURL.URL)
		}
		text := ""
		switch v := c.Message.Content.(type) {
		case string:
			text = v
		case []any:
			for _, part := range v {
				if m, ok := part.(map[string]any); ok {
					if t, ok := m["text"].(string); ok {
						text += "\n" + t
					}
					if iu, ok := m["image_url"].(map[string]any); ok {
						if u, ok := iu["url"].(string); ok {
							add(u)
						}
					}
				}
			}
		}
		for _, m := range mdImgRe.FindAllStringSubmatch(text, -1) {
			add(m[1])
		}
		for _, m := range dataURLRe.FindAllString(text, -1) {
			add(m)
		}
		if len(out) == 0 {
			for _, m := range urlRe.FindAllString(text, -1) {
				add(m)
			}
		}
	}
	if len(out) == 0 && len(r.Data) > 0 {
		out = parseImagesResponse(raw)
	}
	return out
}

func fetchImage(ctx context.Context, ch *Channel, ref string) ([]byte, error) {
	if strings.HasPrefix(ref, "b64:") {
		return decodeB64(strings.TrimPrefix(ref, "b64:"))
	}
	if strings.HasPrefix(ref, "data:") {
		if i := strings.Index(ref, ","); i > 0 {
			return decodeB64(ref[i+1:])
		}
		return nil, errors.New("无效的 data URL")
	}
	u := ref
	if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
		base, err := url.Parse(normBase(ch.BaseURL))
		if err != nil {
			return nil, err
		}
		rel, err := url.Parse(u)
		if err != nil {
			return nil, err
		}
		u = base.ResolveReference(rel).String()
	}
	var lastErr error
	withAuth := false
	for attempt := 0; attempt < 4; attempt++ {
		req, _ := http.NewRequestWithContext(ctx, "GET", u, nil)
		if withAuth && ch.APIKey != "" {
			req.Header.Set("Authorization", "Bearer "+ch.APIKey)
		}
		resp, err := httpClient.Do(req)
		if err != nil {
			lastErr = err
			time.Sleep(time.Second)
			continue
		}
		data, err := io.ReadAll(io.LimitReader(resp.Body, 200<<20))
		resp.Body.Close()
		if err != nil {
			lastErr = err
			continue
		}
		if resp.StatusCode == 401 || resp.StatusCode == 403 {
			lastErr = fmt.Errorf("下载图片 HTTP %d", resp.StatusCode)
			if !withAuth && sameHost(u, ch.BaseURL) {
				withAuth = true
				continue
			}
			break
		}
		if resp.StatusCode >= 300 {
			lastErr = fmt.Errorf("下载图片 HTTP %d", resp.StatusCode)
			time.Sleep(time.Second)
			continue
		}
		return data, nil
	}
	return nil, lastErr
}

func sameHost(a, b string) bool {
	ua, e1 := url.Parse(a)
	ub, e2 := url.Parse(b)
	return e1 == nil && e2 == nil && ua.Host == ub.Host
}

func decodeB64(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	if d, err := base64.StdEncoding.DecodeString(s); err == nil {
		return d, nil
	}
	return base64.RawStdEncoding.DecodeString(strings.TrimRight(s, "="))
}

func logURL(ref string) string {
	if len(ref) > 120 {
		return ref[:120]
	}
	return ref
}

// isResponseFormatRejected 请求了 b64_json 而上游以 4xx 拒绝、且报错点名返回格式
func isResponseFormatRejected(plan Plan, err error) bool {
	if plan.Body["response_format"] != "b64_json" {
		return false
	}
	m := strings.ToLower(err.Error())
	if !strings.HasPrefix(m, "上游返回 4") {
		return false
	}
	return strings.Contains(m, "response_format") || strings.Contains(m, "b64") || strings.Contains(m, "response format")
}

// planWithURL 复制一份请求，返回方式改为链接
func planWithURL(plan Plan) Plan {
	body := make(map[string]any, len(plan.Body))
	for k, v := range plan.Body {
		body[k] = v
	}
	body["response_format"] = "url"
	plan.Body = body
	return plan
}

// isRetryable 上游 5xx 偶发错误自动重试一次
func isRetryable(err error) bool {
	m := err.Error()
	return strings.HasPrefix(m, "上游返回 5") || strings.HasPrefix(m, "请求失败")
}
