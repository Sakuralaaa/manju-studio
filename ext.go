package main

// 对外接口：给其他智能体用的 REST（/v1/...）和 MCP（/mcp，streamable HTTP，JSON-RPC 2.0）。
// 两者共用下面这组工具函数。

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func baseURL(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

// ---------- 工具实现 ----------

func toolColabStatus(ctx context.Context) (any, error) {
	b := runner.brief()
	info := cachedColabInfo()
	out := map[string]any{"runtime": b}
	if info != nil {
		out["tier"] = info.TierLabel
	}
	return out, nil
}

func toolColabSpecs(ctx context.Context) (any, error) {
	info, err := refreshColabInfo(ctx)
	if err != nil && (info == nil || info.Tier == "") {
		return nil, err
	}
	specs := []map[string]any{}
	cfg := loadColabCfg()
	for _, s := range info.Specs {
		specs = append(specs, map[string]any{"variant": s.Key.Variant, "accelerator": accLabel(s.Key.Accelerator), "shape": s.Key.Shape, "eligible": s.Eligible, "cu_per_hour": cfg.rate(s.Key.Accelerator)})
	}
	return map[string]any{"tier": info.TierLabel, "specs": specs}, nil
}

type startArgs struct {
	Accelerator string `json:"accelerator"`
	Shape       string `json:"shape"`
	Purpose     string `json:"purpose"`
}

func toolColabStart(ctx context.Context, a startArgs) (any, error) {
	if strings.EqualFold(a.Accelerator, "CPU") {
		a.Accelerator = "NONE"
	}
	if err := startRuntime(strings.ToUpper(a.Accelerator), a.Shape, a.Purpose); err != nil {
		return nil, err
	}
	return runner.brief(), nil
}

func toolColabStop(ctx context.Context) (any, error) {
	runner.Stop("接口关机")
	return map[string]any{"ok": true}, nil
}

type execArgs struct {
	Code       string `json:"code"`
	TimeoutSec int    `json:"timeout_sec"`
}

func toolColabExec(ctx context.Context, a execArgs) (any, error) {
	if strings.TrimSpace(a.Code) == "" {
		return nil, errors.New("code 不能为空")
	}
	to := a.TimeoutSec
	if to <= 0 || to > 1800 {
		to = 300
	}
	c, cancel := context.WithTimeout(ctx, time.Duration(to)*time.Second)
	defer cancel()
	return execOnRuntime(c, a.Code)
}

type videoArgs struct {
	Prompt      string `json:"prompt"`
	ImageURL    string `json:"image_url"`
	ImageBase64 string `json:"image_base64"`
	ImageID     int64  `json:"image_id"`
	ShotID      int64  `json:"shot_id"`
	Seconds     int    `json:"seconds"`
	Res         string `json:"resolution"`
	Aspect      string `json:"aspect"`
	Turbo       bool   `json:"turbo"`
	Seed        int64  `json:"seed"`
}

func loadImageBytes(ctx context.Context, a videoArgs) ([]byte, error) {
	if a.ImageBase64 != "" {
		s := a.ImageBase64
		if i := strings.Index(s, ","); strings.HasPrefix(s, "data:") && i > 0 {
			s = s[i+1:]
		}
		return decodeB64(s)
	}
	if a.ImageURL != "" {
		req, err := http.NewRequestWithContext(ctx, "GET", a.ImageURL, nil)
		if err != nil {
			return nil, errors.New("图片地址无效")
		}
		resp, err := (&http.Client{Timeout: 60 * time.Second}).Do(req)
		if err != nil {
			return nil, errors.New("下载图片失败：" + shortErr(err))
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			return nil, fmt.Errorf("下载图片失败（%d）", resp.StatusCode)
		}
		return io.ReadAll(io.LimitReader(resp.Body, 40<<20))
	}
	return nil, errors.New("需要 image_id、image_url 或 image_base64 之一")
}

func toolVideoSubmit(ctx context.Context, a videoArgs) (any, error) {
	if a.ShotID > 0 {
		s, err := getShot(a.ShotID)
		if err != nil {
			return nil, errors.New("镜头不存在")
		}
		p, _ := getProject(s.ProjectID)
		for _, x := range listShots(p.ID, false) {
			if x.ID == s.ID {
				s.Code = x.Code
			}
		}
		var turbo *bool
		if a.Turbo {
			turbo = &a.Turbo
		}
		id, err := queueShot(p, s, struct {
			Seconds int
			Turbo   *bool
			Res     string
		}{a.Seconds, turbo, a.Res})
		if err != nil {
			return nil, err
		}
		return map[string]any{"id": id, "status": "queued", "estimate": estimateCU(1, a.Turbo)}, nil
	}
	var file string
	if a.ImageID > 0 {
		im, err := getImage(a.ImageID)
		if err != nil {
			return nil, errors.New("图片不存在")
		}
		file = im.File
	} else {
		data, err := loadImageBytes(ctx, a)
		if err != nil {
			return nil, err
		}
		im, err := storeImage(0, "ext", 0, data, "upload", "", 0, "")
		if err != nil {
			return nil, err
		}
		file = im.File
	}
	id, err := enqueueVideo(videoIn{Source: "api", Prompt: a.Prompt, ImageFile: file, Seconds: a.Seconds, Res: a.Res, Aspect: a.Aspect, Turbo: a.Turbo, Seed: a.Seed})
	if err != nil {
		return nil, err
	}
	return map[string]any{"id": id, "status": "queued", "estimate": estimateCU(1, a.Turbo)}, nil
}

func videoView(v *Video, base string) map[string]any {
	out := map[string]any{"id": v.ID, "status": v.Status, "progress": v.Progress, "stage": v.Stage, "error": v.Error,
		"seconds": v.Seconds, "resolution": v.Res, "aspect": v.Aspect, "turbo": v.Turbo, "seed": v.Seed, "shot_id": v.ShotID, "project_id": v.ProjectID,
		"created_at": v.CreatedAt, "finished_at": v.FinishedAt}
	if v.Status == "queued" {
		out["position"] = v.Position
	}
	if v.Status == "done" {
		out["file_url"] = fmt.Sprintf("%s/v1/videos/%d/file", base, v.ID)
		out["bytes"] = v.Bytes
	}
	return out
}

func toolVideoStatus(ctx context.Context, id int64, base string) (any, error) {
	v, err := getVideo(id)
	if err != nil {
		return nil, errors.New("视频任务不存在")
	}
	return videoView(v, base), nil
}

func toolProjects(ctx context.Context) (any, error) {
	rows, err := db.Query(`SELECT ` + projCols + ` FROM projects ORDER BY updated_at DESC`)
	if err != nil {
		return nil, err
	}
	var ps []*Project
	for rows.Next() {
		if p, err := scanProject(rows); err == nil {
			ps = append(ps, p)
		}
	}
	rows.Close()
	out := []map[string]any{}
	names := []string{"设定", "剧本", "资产", "分镜", "关键帧", "视频", "成片"}
	for _, p := range ps {
		st := projectSteps(p)
		missing := []string{}
		for i, d := range st.Done {
			if !d {
				missing = append(missing, names[i])
			}
		}
		out = append(out, map[string]any{"id": p.ID, "title": p.Title, "idea": p.Idea, "aspect": p.Aspect, "shots": st.Shots, "keyframes": st.Keyframes, "videos": st.Videos, "missing_steps": missing, "updated_at": p.UpdatedAt})
	}
	return out, nil
}

func toolShots(ctx context.Context, pid int64, base string) (any, error) {
	if _, err := getProject(pid); err != nil {
		return nil, errors.New("作品不存在")
	}
	out := []map[string]any{}
	for _, s := range listShots(pid, true) {
		m := map[string]any{"id": s.ID, "code": s.Code, "duration": s.Duration, "camera": s.Camera, "action": s.Action, "dialogue": s.Dialogue,
			"assets": s.Assets, "keyframe_prompt": s.KeyframePrompt, "video_prompt": s.VideoPrompt}
		if s.PickedImageID > 0 {
			m["keyframe_image_id"] = s.PickedImageID
			m["keyframe_url"] = fmt.Sprintf("%s/v1/images/%d/file", base, s.PickedImageID)
		}
		if v := shotVideoFor(s); v != nil {
			m["video_id"] = v.ID
			m["video_url"] = fmt.Sprintf("%s/v1/videos/%d/file", base, v.ID)
		}
		out = append(out, m)
	}
	return out, nil
}

type imageArgs struct {
	Prompt    string `json:"prompt"`
	ChannelID int64  `json:"channel_id"`
	Model     string `json:"model"`
	Aspect    string `json:"aspect"`
	N         int    `json:"n"`
}

func toolImageGenerate(ctx context.Context, a imageArgs, base string) (any, error) {
	if strings.TrimSpace(a.Prompt) == "" {
		return nil, errors.New("prompt 不能为空")
	}
	var ch *Channel
	if a.ChannelID > 0 {
		ch, _ = getChannel(a.ChannelID)
	}
	if ch == nil {
		ch = firstChannel("image")
	}
	if ch == nil || ch.Kind != "image" {
		return nil, errors.New("没有可用的生图渠道")
	}
	if a.Aspect == "" {
		a.Aspect = "1:1"
	}
	c, cancel := context.WithTimeout(ctx, 8*time.Minute)
	defer cancel()
	imgs, sent, err := generateImages(c, ch, a.Model, a.Prompt, paramsFor(ch, a.Aspect, clampInt(a.N, 1, 4)), nil)
	if err != nil {
		return nil, err
	}
	out := []map[string]any{}
	for _, data := range imgs {
		im, err := storeImage(0, "ext", 0, data, "gen", sent, ch.ID, a.Model)
		if err != nil {
			continue
		}
		out = append(out, map[string]any{"image_id": im.ID, "width": im.Width, "height": im.Height, "url": fmt.Sprintf("%s/v1/images/%d/file", base, im.ID)})
	}
	return map[string]any{"images": out}, nil
}

// ---------- REST ----------

func extReply(w http.ResponseWriter, v any, err error) {
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	writeJSON(w, v)
}

func extColabStatus(w http.ResponseWriter, r *http.Request) {
	v, err := toolColabStatus(r.Context())
	extReply(w, v, err)
}
func extColabSpecs(w http.ResponseWriter, r *http.Request) {
	v, err := toolColabSpecs(r.Context())
	extReply(w, v, err)
}
func extColabStart(w http.ResponseWriter, r *http.Request) {
	var a startArgs
	_ = readJSON(r, &a)
	v, err := toolColabStart(r.Context(), a)
	extReply(w, v, err)
}
func extColabStop(w http.ResponseWriter, r *http.Request) {
	v, err := toolColabStop(r.Context())
	extReply(w, v, err)
}
func extColabExec(w http.ResponseWriter, r *http.Request) {
	var a execArgs
	if err := readJSON(r, &a); err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	v, err := toolColabExec(r.Context(), a)
	extReply(w, v, err)
}
func extVideoSubmit(w http.ResponseWriter, r *http.Request) {
	var a videoArgs
	if err := readJSON(r, &a); err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	v, err := toolVideoSubmit(r.Context(), a)
	extReply(w, v, err)
}
func extVideoGet(w http.ResponseWriter, r *http.Request) {
	v, err := toolVideoStatus(r.Context(), pathID(r), baseURL(r))
	extReply(w, v, err)
}
func extVideoFile(w http.ResponseWriter, r *http.Request) {
	v, err := getVideo(pathID(r))
	if err != nil || v.File == "" {
		httpErr(w, 404, "视频还没有生成")
		return
	}
	w.Header().Set("Content-Type", "video/mp4")
	http.ServeFile(w, r, filepath.Join(dataDir, "videos", v.File))
}
func extImageFile(w http.ResponseWriter, r *http.Request) {
	im, err := getImage(pathID(r))
	if err != nil {
		httpErr(w, 404, "图片不存在")
		return
	}
	w.Header().Set("Content-Type", im.Mime)
	http.ServeFile(w, r, filepath.Join(dataDir, "images", im.File))
}
func extProjects(w http.ResponseWriter, r *http.Request) {
	v, err := toolProjects(r.Context())
	extReply(w, v, err)
}
func extShots(w http.ResponseWriter, r *http.Request) {
	v, err := toolShots(r.Context(), pathID(r), baseURL(r))
	extReply(w, v, err)
}
func extImageGenerate(w http.ResponseWriter, r *http.Request) {
	var a imageArgs
	if err := readJSON(r, &a); err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	v, err := toolImageGenerate(r.Context(), a, baseURL(r))
	extReply(w, v, err)
}

// ---------- MCP ----------

type mcpTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

func obj(props map[string]any, required ...string) map[string]any {
	m := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		m["required"] = required
	}
	return m
}

func str(desc string) map[string]any   { return map[string]any{"type": "string", "description": desc} }
func num(desc string) map[string]any   { return map[string]any{"type": "integer", "description": desc} }
func boolp(desc string) map[string]any { return map[string]any{"type": "boolean", "description": desc} }

var mcpTools = []mcpTool{
	{"colab_status", "查看 Colab 运行时状态：阶段、加速器、已用 CU 估算、排队视频数、订阅档位。", obj(map[string]any{})},
	{"colab_specs", "刷新并列出当前账号可用的运行时规格（变体、加速器、形态、是否可用、每小时 CU 估算）。", obj(map[string]any{})},
	{"colab_start", "申请一台运行时。purpose=video 会安装 ComfyUI 并下载视频模型（需要 GPU）；purpose=manual 只开机用于运行代码。", obj(map[string]any{
		"accelerator": str("加速器，如 G4、A100、L4、T4、CPU；不填用设置里的默认值"),
		"shape":       str("SHAPE_STANDARD 或 SHAPE_HIGHMEM"),
		"purpose":     map[string]any{"type": "string", "enum": []string{"video", "manual"}},
	})},
	{"colab_stop", "立即关机并释放运行时。", obj(map[string]any{})},
	{"colab_run_python", "在当前运行时的 Python 内核里执行代码，返回 stdout、stderr、最后一个表达式的值和错误。", obj(map[string]any{
		"code": str("要执行的 Python 代码"), "timeout_sec": num("超时秒数，默认 300，最多 1800"),
	}, "code")},
	{"video_submit", "提交一个图生视频任务（MiniMax H3，首帧 + 描述 → 5–10 秒带声音的视频）。给 shot_id 时直接用该镜头的关键帧和描述。", obj(map[string]any{
		"prompt": str("视频描述：动作、运镜、声音"), "image_id": num("已有图片编号"), "image_url": str("首帧图片地址"), "image_base64": str("首帧图片 base64"),
		"shot_id": num("作品里的镜头编号"), "seconds": num("时长 5–10 秒，默认 5"), "resolution": map[string]any{"type": "string", "enum": []string{"768p", "480p"}},
		"aspect": str("画幅，如 9:16、16:9、1:1"), "turbo": boolp("使用 Turbo 8 步"), "seed": num("随机种子"),
	})},
	{"video_status", "查询视频任务状态；完成后返回下载地址（同一密钥可下载）。", obj(map[string]any{"id": num("视频任务编号")}, "id")},
	{"list_projects", "列出全部作品及各自还缺哪些步骤。", obj(map[string]any{})},
	{"list_shots", "列出某个作品的分镜：镜头语言、画面、台词、资产编号、关键帧与视频。", obj(map[string]any{"project_id": num("作品编号")}, "project_id")},
	{"image_generate", "用生图渠道生成图片，返回图片编号和下载地址。", obj(map[string]any{
		"prompt": str("提示词"), "channel_id": num("渠道编号，不填用第一个生图渠道"), "model": str("模型名"), "aspect": str("比例，如 9:16"), "n": num("张数 1–4"),
	}, "prompt")},
}

type rpcReq struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

func handleMCP(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 64<<20))
	if err != nil {
		httpErr(w, 400, "读取请求失败")
		return
	}
	trim := strings.TrimSpace(string(body))
	base := baseURL(r)
	if strings.HasPrefix(trim, "[") {
		var reqs []rpcReq
		if json.Unmarshal(body, &reqs) != nil {
			writeRPC(w, rpcError(nil, -32700, "解析失败"))
			return
		}
		var outs []any
		for _, q := range reqs {
			if res := mcpHandle(r.Context(), q, base); res != nil {
				outs = append(outs, res)
			}
		}
		if len(outs) == 0 {
			w.WriteHeader(202)
			return
		}
		writeRPC(w, outs)
		return
	}
	var q rpcReq
	if json.Unmarshal(body, &q) != nil {
		writeRPC(w, rpcError(nil, -32700, "解析失败"))
		return
	}
	res := mcpHandle(r.Context(), q, base)
	if res == nil {
		w.WriteHeader(202)
		return
	}
	writeRPC(w, res)
}

func writeRPC(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func rpcError(id json.RawMessage, code int, msg string) map[string]any {
	return map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": code, "message": msg}}
}

func mcpHandle(ctx context.Context, q rpcReq, base string) any {
	if len(q.ID) == 0 || string(q.ID) == "null" {
		return nil // 通知
	}
	ok := func(result any) any { return map[string]any{"jsonrpc": "2.0", "id": q.ID, "result": result} }
	switch q.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(q.Params, &p)
		pv := p.ProtocolVersion
		if pv == "" {
			pv = "2025-06-18"
		}
		return ok(map[string]any{
			"protocolVersion": pv,
			"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
			"serverInfo":      map[string]any{"name": "manju-studio", "title": "漫剧工坊", "version": version},
			"instructions":    "漫剧工坊：管理 Colab 运行时、在运行时上执行 Python、提交 MiniMax H3 图生视频任务、查询作品分镜、调用生图渠道。视频任务是异步的：video_submit 之后用 video_status 轮询。",
		})
	case "ping":
		return ok(map[string]any{})
	case "tools/list":
		return ok(map[string]any{"tools": mcpTools})
	case "tools/call":
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(q.Params, &p); err != nil {
			return rpcError(q.ID, -32602, "参数错误")
		}
		if len(p.Arguments) == 0 || string(p.Arguments) == "null" {
			p.Arguments = []byte("{}")
		}
		v, err := callTool(ctx, p.Name, p.Arguments, base)
		if errors.Is(err, errUnknownTool) {
			return rpcError(q.ID, -32602, "没有这个工具："+p.Name)
		}
		if err != nil {
			return ok(map[string]any{"content": []any{map[string]any{"type": "text", "text": err.Error()}}, "isError": true})
		}
		b, _ := json.MarshalIndent(v, "", "  ")
		return ok(map[string]any{"content": []any{map[string]any{"type": "text", "text": string(b)}}, "structuredContent": wrapStructured(v)})
	}
	return rpcError(q.ID, -32601, "不支持的方法："+q.Method)
}

// structuredContent 必须是对象
func wrapStructured(v any) any {
	b, _ := json.Marshal(v)
	if len(b) > 0 && b[0] == '{' {
		var m map[string]any
		_ = json.Unmarshal(b, &m)
		return m
	}
	var x any
	_ = json.Unmarshal(b, &x)
	return map[string]any{"items": x}
}

var errUnknownTool = errors.New("unknown tool")

func callTool(ctx context.Context, name string, args json.RawMessage, base string) (any, error) {
	switch name {
	case "colab_status":
		return toolColabStatus(ctx)
	case "colab_specs":
		return toolColabSpecs(ctx)
	case "colab_start":
		var a startArgs
		_ = json.Unmarshal(args, &a)
		return toolColabStart(ctx, a)
	case "colab_stop":
		return toolColabStop(ctx)
	case "colab_run_python":
		var a execArgs
		_ = json.Unmarshal(args, &a)
		return toolColabExec(ctx, a)
	case "video_submit":
		var a videoArgs
		_ = json.Unmarshal(args, &a)
		return toolVideoSubmit(ctx, a)
	case "video_status":
		var a struct {
			ID json.Number `json:"id"`
		}
		_ = json.Unmarshal(args, &a)
		id, _ := strconv.ParseInt(a.ID.String(), 10, 64)
		return toolVideoStatus(ctx, id, base)
	case "list_projects":
		return toolProjects(ctx)
	case "list_shots":
		var a struct {
			ProjectID json.Number `json:"project_id"`
		}
		_ = json.Unmarshal(args, &a)
		id, _ := strconv.ParseInt(a.ProjectID.String(), 10, 64)
		return toolShots(ctx, id, base)
	case "image_generate":
		var a imageArgs
		_ = json.Unmarshal(args, &a)
		return toolImageGenerate(ctx, a, base)
	}
	return nil, errUnknownTool
}

var _ = base64.StdEncoding
var _ = os.Remove
