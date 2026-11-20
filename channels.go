package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

// Cap 一项能力：mode = native(原生发送) / prompt(写进提示词) / loop(逐张请求，仅张数) / off(不用)
type Cap struct {
	Mode    string   `json:"mode"`
	Values  []string `json:"values,omitempty"`
	Max     int      `json:"max,omitempty"`
	Default string   `json:"default,omitempty"`
}

type Caps struct {
	Size        Cap `json:"size"`
	Aspect      Cap `json:"aspect"`
	Resolution  Cap `json:"resolution"`
	Quality     Cap `json:"quality"`
	N           Cap `json:"n"`
	Format      Cap `json:"format"`
	Compression Cap `json:"compression"`
	Background  Cap `json:"background"`
	Moderation  Cap `json:"moderation"`
	Response    Cap `json:"response"` // native: 发送 values[0]；off: 不发送
	Refs        Cap `json:"refs"`     // mode: multipart / json / off；max: 上限
}

type Channel struct {
	ID        int64    `json:"id"`
	Name      string   `json:"name"`
	BaseURL   string   `json:"base_url"`
	APIKey    string   `json:"api_key,omitempty"`
	KeyTail   string   `json:"key_tail"`
	APIType   string   `json:"api_type"`
	Models    []string `json:"models"`
	Caps      Caps     `json:"caps"`
	Sort      int      `json:"sort"`
	CreatedAt int64    `json:"created_at"`
	Kind      string   `json:"kind"` // image / text / voice
	Voices    []string `json:"voices"`
}

var allRatios = []string{"1:1", "16:9", "9:16", "4:3", "3:4", "3:2", "2:3", "2:1", "1:2", "9:19.5", "19.5:9", "9:20", "20:9", "21:9", "5:2"}

type Preset struct {
	Key     string   `json:"key"`
	Name    string   `json:"name"`
	BaseURL string   `json:"base_url"`
	APIType string   `json:"api_type"`
	Models  []string `json:"models"`
	Caps    Caps     `json:"caps"`
}

func presets() []Preset {
	off := Cap{Mode: "off"}
	return []Preset{
		{
			Key: "official", Name: "OpenAI 官方", BaseURL: "https://api.openai.com/v1", APIType: "images",
			// 模型名与参数以官方 Images API 文档为准（2026-11 核对）
			Models: []string{"gpt-image-2.6-flare", "gpt-image-2.6-sunburst", "gpt-image-2.5-flare", "gpt-image-2.5-sunburst", "gpt-image-2", "gpt-image-1.5", "gpt-image-1-mini", "chatgpt-image-latest"},
			Caps: Caps{
				Size:        Cap{Mode: "native", Values: []string{"auto", "1024x1024", "1536x1024", "1024x1536"}, Default: "auto"},
				Aspect:      off,
				Resolution:  off,
				Quality:     Cap{Mode: "native", Values: []string{"auto", "low", "medium", "high"}, Default: "auto"},
				N:           Cap{Mode: "native", Max: 10},
				Format:      Cap{Mode: "native", Values: []string{"png", "jpeg", "webp"}, Default: "png"},
				Compression: Cap{Mode: "native"},
				Background:  Cap{Mode: "native", Values: []string{"auto", "transparent", "opaque"}, Default: "auto"},
				Moderation:  Cap{Mode: "native", Values: []string{"auto", "low"}, Default: "auto"},
				Response:    off, // 官方 GPT 图像模型固定返回 b64_json
				Refs:        Cap{Mode: "json", Max: 16},
			},
		},
		{
			Key: "ratio", Name: "比例型", BaseURL: "", APIType: "images",
			Caps: Caps{
				Size:        off,
				Aspect:      Cap{Mode: "native", Values: allRatios, Default: "1:1"},
				Resolution:  Cap{Mode: "off", Values: []string{"1k", "2k"}, Default: "2k"},
				Quality:     Cap{Mode: "off", Values: []string{"low", "medium", "auto"}, Default: "auto"},
				N:           Cap{Mode: "native", Max: 8}, // 实测：上游报「数量必须在 1 到 8 之间」
				Format:      off,
				Compression: off,
				Background:  off,
				Moderation:  off,
				Response:    Cap{Mode: "native", Values: []string{"url"}},
				Refs:        Cap{Mode: "multipart", Max: 5}, // 实测上限 5
			},
		},
		{
			Key: "size", Name: "尺寸型", BaseURL: "", APIType: "images",
			Caps: Caps{
				Size:        Cap{Mode: "native", Values: []string{"1024x1024", "1536x1024", "1024x1536"}, Default: "1024x1024"},
				Aspect:      off,
				Resolution:  off,
				Quality:     Cap{Mode: "native", Values: []string{"low", "medium"}, Default: "medium"},
				N:           Cap{Mode: "loop", Max: 4},
				Format:      Cap{Mode: "native", Values: []string{"png", "jpeg", "webp"}, Default: "png"},
				Compression: off,
				Background:  off,
				Moderation:  off,
				// 实测（2026-11-20）：该中转返回的 /images/... 链接下载要会话凭据（401），改为直接返回图片数据；
				// 若上游不认 b64_json 或仍给链接，会自动退回下载链接
				Response: Cap{Mode: "native", Values: []string{"b64_json"}},
				Refs:     Cap{Mode: "multipart", Max: 20}, // 实测上限 20
			},
		},
		{
			Key: "prompt-ratio", Name: "提示词比例型", BaseURL: "", APIType: "images",
			Caps: Caps{
				Size:        off,
				Aspect:      Cap{Mode: "prompt", Values: []string{"1:1", "16:9", "9:16", "4:3", "3:4", "3:2", "2:3", "21:9"}, Default: "1:1"},
				Resolution:  off,
				Quality:     Cap{Mode: "prompt", Values: []string{"medium", "high"}, Default: "high"},
				N:           Cap{Mode: "native", Max: 4},
				Format:      off,
				Compression: off,
				Background:  off,
				Moderation:  off,
				Response:    Cap{Mode: "native", Values: []string{"b64_json"}},
				Refs:        Cap{Mode: "multipart", Max: 3}, // 声称 4，实测第 4 张上游报错
			},
		},
	}
}

func handlePresets(w http.ResponseWriter, r *http.Request) { writeJSON(w, presets()) }

// firstChannel 某类渠道里排第一的那个
func firstChannel(kind string) *Channel {
	cs, _ := listChannels()
	for _, c := range cs {
		if c.Kind == kind {
			return c
		}
	}
	return nil
}

func keyTail(k string) string {
	if len(k) <= 4 {
		if k == "" {
			return ""
		}
		return "••••"
	}
	return "••••" + k[len(k)-4:]
}

func scanChannel(sc interface{ Scan(...any) error }) (*Channel, error) {
	var c Channel
	var models, caps string
	var voices string
	if err := sc.Scan(&c.ID, &c.Name, &c.BaseURL, &c.APIKey, &c.APIType, &models, &caps, &c.Sort, &c.CreatedAt, &c.Kind, &voices); err != nil {
		return nil, err
	}
	_ = json.Unmarshal([]byte(voices), &c.Voices)
	if c.Voices == nil {
		c.Voices = []string{}
	}
	_ = json.Unmarshal([]byte(models), &c.Models)
	_ = json.Unmarshal([]byte(caps), &c.Caps)
	if c.Models == nil {
		c.Models = []string{}
	}
	c.KeyTail = keyTail(c.APIKey)
	return &c, nil
}

const chanCols = `id,name,base_url,api_key,api_type,models,caps,sort,created_at,kind,voices`

func getChannel(id int64) (*Channel, error) {
	return scanChannel(db.QueryRow(`SELECT `+chanCols+` FROM channels WHERE id=?`, id))
}

func listChannels() ([]*Channel, error) {
	rows, err := db.Query(`SELECT ` + chanCols + ` FROM channels ORDER BY sort, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Channel{}
	for rows.Next() {
		c, err := scanChannel(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, nil
}

func handleChannelList(w http.ResponseWriter, r *http.Request) {
	cs, err := listChannels()
	if err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	for _, c := range cs {
		c.APIKey = ""
	}
	writeJSON(w, cs)
}

func normBase(u string) string {
	u = strings.TrimSpace(u)
	u = strings.TrimRight(u, "/")
	return u
}

// apiURL 拼接接口地址：Base URL 已含 /v1 则不再追加
func apiURL(base, p string) string {
	base = normBase(base)
	if strings.HasSuffix(base, "/v1") || strings.Contains(base, "/v1/") {
		return base + p
	}
	return base + "/v1" + p
}

func handleChannelSave(w http.ResponseWriter, r *http.Request) {
	var in Channel
	if err := readJSON(r, &in); err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	in.BaseURL = normBase(in.BaseURL)
	if in.Name == "" || in.BaseURL == "" {
		httpErr(w, 400, "名称和接口地址不能为空")
		return
	}
	if in.Kind != "text" && in.Kind != "voice" {
		in.Kind = "image"
	}
	switch in.Kind {
	case "text":
		in.APIType = "chat"
	case "voice":
		if in.APIType != "fish" && in.APIType != "mimo" {
			in.APIType = "openai"
		}
	default:
		if in.APIType != "chat" {
			in.APIType = "images"
		}
	}
	if in.Models == nil {
		in.Models = []string{}
	}
	if in.Voices == nil {
		in.Voices = []string{}
	}
	id := pathID(r)
	if id == 0 {
		var maxSort int
		_ = db.QueryRow(`SELECT COALESCE(MAX(sort),0) FROM channels`).Scan(&maxSort)
		res, err := db.Exec(`INSERT INTO channels(kind,name,base_url,api_key,api_type,models,caps,sort,created_at,voices) VALUES(?,?,?,?,?,?,?,?,?,?)`,
			in.Kind, in.Name, in.BaseURL, strings.TrimSpace(in.APIKey), in.APIType, mustJSON(in.Models), mustJSON(in.Caps), maxSort+1, now(), mustJSON(in.Voices))
		if err != nil {
			httpErr(w, 500, err.Error())
			return
		}
		id, _ = res.LastInsertId()
	} else {
		old, err := getChannel(id)
		if err != nil {
			httpErr(w, 404, "渠道不存在")
			return
		}
		key := strings.TrimSpace(in.APIKey)
		if key == "" {
			key = old.APIKey
		}
		_, err = db.Exec(`UPDATE channels SET name=?,base_url=?,api_key=?,api_type=?,models=?,caps=?,voices=? WHERE id=?`,
			in.Name, in.BaseURL, key, in.APIType, mustJSON(in.Models), mustJSON(in.Caps), mustJSON(in.Voices), id)
		if err != nil {
			httpErr(w, 500, err.Error())
			return
		}
	}
	c, _ := getChannel(id)
	c.APIKey = ""
	writeJSON(w, c)
}

func handleChannelDelete(w http.ResponseWriter, r *http.Request) {
	_, err := db.Exec(`DELETE FROM channels WHERE id=?`, pathID(r))
	if err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

// handleChannelProbe 测试连接：调用 /v1/models
func handleChannelProbe(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ID      int64  `json:"id"`
		BaseURL string `json:"base_url"`
		APIKey  string `json:"api_key"`
		APIType string `json:"api_type"`
	}
	if err := readJSON(r, &in); err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	key := strings.TrimSpace(in.APIKey)
	base := in.BaseURL
	if in.ID > 0 {
		if c, err := getChannel(in.ID); err == nil {
			if key == "" {
				key = c.APIKey
			}
			if base == "" {
				base = c.BaseURL
			}
		}
	}
	if base == "" {
		httpErr(w, 400, "请填写 Base URL")
		return
	}
	start := time.Now()
	u := apiURL(base, "/models")
	if in.APIType == "fish" {
		u = strings.TrimSuffix(normBase(base), "/v1") + "/model?self=true&page_size=1"
	}
	req, _ := http.NewRequestWithContext(r.Context(), "GET", u, nil)
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
		if in.APIType == "mimo" {
			req.Header.Set("api-key", key)
		}
	}
	cl := &http.Client{Timeout: 25 * time.Second}
	resp, err := cl.Do(req)
	if err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": "连接失败：" + shortErr(err)})
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode >= 300 {
		writeJSON(w, map[string]any{"ok": false, "error": fmt.Sprintf("HTTP %d：%s", resp.StatusCode, upstreamMsg(body))})
		return
	}
	if in.APIType == "fish" {
		writeJSON(w, map[string]any{"ok": true, "models": []string{}, "latency_ms": time.Since(start).Milliseconds()})
		return
	}
	var parsed struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	_ = json.Unmarshal(body, &parsed)
	models := []string{}
	for _, m := range parsed.Data {
		if m.ID != "" {
			models = append(models, m.ID)
		}
	}
	sort.Strings(models)
	writeJSON(w, map[string]any{"ok": true, "models": models, "latency_ms": time.Since(start).Milliseconds()})
}

func shortErr(err error) string {
	s := err.Error()
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}

func upstreamMsg(body []byte) string {
	var e struct {
		Error any    `json:"error"`
		Msg   string `json:"message"`
	}
	if json.Unmarshal(body, &e) == nil {
		switch v := e.Error.(type) {
		case string:
			return v
		case map[string]any:
			if m, ok := v["message"].(string); ok {
				return m
			}
		}
		if e.Msg != "" {
			return e.Msg
		}
	}
	s := strings.TrimSpace(string(body))
	if len([]rune(s)) > 300 {
		s = string([]rune(s)[:300])
	}
	return s
}

var _ = sql.ErrNoRows
