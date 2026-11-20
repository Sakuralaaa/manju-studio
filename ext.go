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
	"net/url"
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
	Takes       int    `json:"takes"`
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
		var turbo *bool
		if a.Turbo {
			turbo = &a.Turbo
		}
		ids, err := queueShot(p, s, queueOpts{Seconds: a.Seconds, Turbo: turbo, Res: a.Res, Takes: a.Takes})
		if err != nil && len(ids) == 0 {
			return nil, err
		}
		return map[string]any{"id": ids[0], "ids": ids, "status": "queued", "estimate": estimateCU(len(ids), a.Turbo)}, nil
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
		"seconds": v.Seconds, "resolution": v.Res, "aspect": v.Aspect, "turbo": v.Turbo, "seed": v.Seed, "shot_id": v.ShotID, "project_id": v.ProjectID, "episode_id": v.EpisodeID,
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

var stepNamesZh = []string{"设定", "剧本", "资产", "分镜", "关键帧", "视频", "成片"}

func missingSteps(st Steps) []string {
	missing := []string{}
	for i, d := range st.Done {
		if !d {
			missing = append(missing, stepNamesZh[i])
		}
	}
	return missing
}

func toolProjects(ctx context.Context) (any, error) {
	out := []map[string]any{}
	for _, p := range listProjects() {
		eps := []map[string]any{}
		for _, e := range listEpisodes(p.ID) {
			st := episodeSteps(p, e)
			eps = append(eps, map[string]any{"id": e.ID, "no": e.No, "title": e.Title, "missing_steps": missingSteps(st), "shots": st.Shots, "videos": st.Videos})
		}
		out = append(out, map[string]any{"id": p.ID, "title": p.Title, "idea": p.Idea, "aspect": p.Aspect, "audio_mode": p.AudioMode, "episodes": eps, "updated_at": p.UpdatedAt})
	}
	return out, nil
}

func shotView(s *Shot, base string) map[string]any {
	m := map[string]any{"id": s.ID, "episode_id": s.EpisodeID, "code": s.Code, "duration": s.Duration, "camera": s.Camera, "action": s.Action, "dialogue": s.Dialogue,
		"assets": s.Assets, "chain": s.Chain, "keyframe_prompt": s.KeyframePrompt, "video_prompt": s.VideoPrompt}
	if s.PickedImageID > 0 {
		m["keyframe_image_id"] = s.PickedImageID
		m["keyframe_url"] = fmt.Sprintf("%s/v1/images/%d/file", base, s.PickedImageID)
	}
	if v := shotVideoFor(s); v != nil {
		m["video_id"] = v.ID
		m["video_url"] = fmt.Sprintf("%s/v1/videos/%d/file", base, v.ID)
	}
	takes := []int64{}
	for _, v := range s.Videos {
		if v.Status == "done" {
			takes = append(takes, v.ID)
		}
	}
	m["takes"] = takes
	return m
}

func toolShots(ctx context.Context, pid int64, base string) (any, error) {
	if _, err := getProject(pid); err != nil {
		return nil, errors.New("作品不存在")
	}
	out := []map[string]any{}
	for _, e := range listEpisodes(pid) {
		for _, s := range listShots(e.ID, true) {
			out = append(out, shotView(s, base))
		}
	}
	return out, nil
}

func toolEpisodes(ctx context.Context, pid int64) (any, error) {
	p, err := getProject(pid)
	if err != nil {
		return nil, errors.New("作品不存在")
	}
	out := []map[string]any{}
	for _, e := range listEpisodes(pid) {
		st := episodeSteps(p, e)
		out = append(out, map[string]any{"id": e.ID, "no": e.No, "title": e.Title, "idea": e.Idea, "summary": e.Summary, "cliffhanger": e.Cliffhanger,
			"missing_steps": missingSteps(st), "shots": st.Shots, "keyframes": st.Keyframes, "videos": st.Videos, "final": e.FinalFile != "", "autopilot": latestPipe(e.ID)})
	}
	return map[string]any{"project": map[string]any{"id": p.ID, "title": p.Title, "world": p.World, "bios": p.Bios, "outline": p.Outline}, "episodes": out}, nil
}

type epCreateArgs struct {
	ProjectID int64  `json:"project_id"`
	Idea      string `json:"idea"`
	AIIdea    bool   `json:"ai_idea"`
}

func toolEpisodeCreate(ctx context.Context, a epCreateArgs) (any, error) {
	p, err := getProject(a.ProjectID)
	if err != nil {
		return nil, errors.New("作品不存在")
	}
	e, err := insertEpisode(p.ID, a.Idea)
	if err != nil {
		return nil, err
	}
	out := map[string]any{"episode_id": e.ID, "no": e.No}
	if a.AIIdea && strings.TrimSpace(a.Idea) == "" {
		if tid, err := episodeIdeaTask(p, e); err == nil {
			out["idea_task_id"] = tid
		}
	}
	return out, nil
}

func toolEpisode(ctx context.Context, eid int64, base string) (any, error) {
	p, e, err := epCtx(eid)
	if err != nil {
		return nil, err
	}
	shots := []map[string]any{}
	for _, s := range listShots(e.ID, true) {
		shots = append(shots, shotView(s, base))
	}
	out := map[string]any{"id": e.ID, "project_id": p.ID, "no": e.No, "title": e.Title, "idea": e.Idea, "script": e.Script, "summary": e.Summary,
		"cliffhanger": e.Cliffhanger, "steps": episodeSteps(p, e), "missing_steps": missingSteps(episodeSteps(p, e)), "shots": shots, "autopilot": latestPipe(e.ID)}
	if e.FinalFile != "" {
		out["final_url"] = fmt.Sprintf("%s/v1/episodes/%d/final", base, e.ID)
	}
	return out, nil
}

type pipeArgs struct {
	EpisodeID int64  `json:"episode_id"`
	Mode      string `json:"mode"`
}

func toolAutopilot(ctx context.Context, action string, a pipeArgs) (any, error) {
	if _, _, err := epCtx(a.EpisodeID); err != nil {
		return nil, err
	}
	switch action {
	case "start":
		return startPipe(a.EpisodeID, a.Mode)
	case "continue":
		lp := latestPipe(a.EpisodeID)
		if lp == nil || (lp.Status != "confirm" && lp.Status != "failed" && lp.Status != "canceled" && lp.Status != "waiting_writer") {
			return nil, errors.New("没有需要继续的一键成集")
		}
		if lp.Status == "confirm" {
			lp.Confirmed = append(lp.Confirmed, lp.Stage)
		}
		lp.Status, lp.Message = "running", ""
		lp.say("继续（接口）")
		go runPipe(lp.ID)
		return lp, nil
	case "cancel":
		cancelPipe(a.EpisodeID)
		return map[string]any{"ok": true}, nil
	}
	lp := latestPipe(a.EpisodeID)
	if lp == nil {
		return map[string]any{"status": "none"}, nil
	}
	return lp, nil
}

func toolLines(ctx context.Context, eid int64, force bool) (any, error) {
	p, e, err := epCtx(eid)
	if err != nil {
		return nil, err
	}
	tid, err := linesTask(p, e, force)
	if err != nil {
		return nil, err
	}
	return map[string]any{"task_id": tid}, nil
}

type ttsArgs struct {
	Text    string     `json:"text"`
	Emotion string     `json:"emotion"`
	AssetID int64      `json:"asset_id"`
	Voice   *VoiceSpec `json:"voice"`
}

func toolTTS(ctx context.Context, a ttsArgs, base string) (any, error) {
	var v VoiceSpec
	switch {
	case a.Voice != nil:
		v = *a.Voice
	case a.AssetID > 0:
		as, err := getAsset(a.AssetID)
		if err != nil {
			return nil, errors.New("角色不存在")
		}
		v = as.Voice
	default:
		return nil, errors.New("需要 asset_id 或 voice")
	}
	if v.empty() {
		return nil, errors.New("这个角色还没有音色")
	}
	c, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	name, dur, err := speakTo(c, v, a.Text, a.Emotion, "api")
	ledgerAdd(0, 0, "tts", 1, 0, 0, 0, "")
	if err != nil {
		return nil, err
	}
	return map[string]any{"url": base + "/v1/audio/" + name, "duration": dur}, nil
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

func extEpisodes(w http.ResponseWriter, r *http.Request) {
	v, err := toolEpisodes(r.Context(), pathID(r))
	extReply(w, v, err)
}
func extEpisodeCreate(w http.ResponseWriter, r *http.Request) {
	var a epCreateArgs
	_ = readJSON(r, &a)
	a.ProjectID = pathID(r)
	v, err := toolEpisodeCreate(r.Context(), a)
	extReply(w, v, err)
}
func extEpisodeGet(w http.ResponseWriter, r *http.Request) {
	v, err := toolEpisode(r.Context(), pathID(r), baseURL(r))
	extReply(w, v, err)
}
func extAutopilotStart(w http.ResponseWriter, r *http.Request) {
	var a pipeArgs
	_ = readJSON(r, &a)
	a.EpisodeID = pathID(r)
	v, err := toolAutopilot(r.Context(), "start", a)
	extReply(w, v, err)
}
func extAutopilotGet(w http.ResponseWriter, r *http.Request) {
	v, err := toolAutopilot(r.Context(), "status", pipeArgs{EpisodeID: pathID(r)})
	extReply(w, v, err)
}
func extAutopilotContinue(w http.ResponseWriter, r *http.Request) {
	v, err := toolAutopilot(r.Context(), "continue", pipeArgs{EpisodeID: pathID(r)})
	extReply(w, v, err)
}
func extAutopilotCancel(w http.ResponseWriter, r *http.Request) {
	v, err := toolAutopilot(r.Context(), "cancel", pipeArgs{EpisodeID: pathID(r)})
	extReply(w, v, err)
}
func extLinesGenerate(w http.ResponseWriter, r *http.Request) {
	var a struct {
		Force bool `json:"force"`
	}
	_ = readJSON(r, &a)
	v, err := toolLines(r.Context(), pathID(r), a.Force)
	extReply(w, v, err)
}
func extTTS(w http.ResponseWriter, r *http.Request) {
	var a ttsArgs
	if err := readJSON(r, &a); err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	v, err := toolTTS(r.Context(), a, baseURL(r))
	extReply(w, v, err)
}
func extEpisodeFinal(w http.ResponseWriter, r *http.Request) {
	_, e, err := epCtx(pathID(r))
	if err != nil || e.FinalFile == "" {
		httpErr(w, 404, "这一集还没有成片")
		return
	}
	w.Header().Set("Content-Type", "video/mp4")
	http.ServeFile(w, r, filepath.Join(dataDir, "videos", e.FinalFile))
}
func extAudioFile(w http.ResponseWriter, r *http.Request) {
	name := filepath.Base(r.PathValue("name"))
	if name == "." || strings.Contains(name, "..") {
		http.NotFound(w, r)
		return
	}
	http.ServeFile(w, r, filepath.Join(dataDir, "audio", name))
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
		"shot_id": num("作品里的镜头编号"), "takes": num("抽卡次数 1–4，只对 shot_id 有效"), "seconds": num("时长 5–10 秒，默认 5"), "resolution": map[string]any{"type": "string", "enum": []string{"768p", "480p"}},
		"aspect": str("画幅，如 9:16、16:9、1:1"), "turbo": boolp("使用 Turbo 8 步"), "seed": num("随机种子"),
	})},
	{"video_status", "查询视频任务状态；完成后返回下载地址（同一密钥可下载）。", obj(map[string]any{"id": num("视频任务编号")}, "id")},
	{"list_projects", "列出全部作品（剧集）及每一集还缺哪些步骤。", obj(map[string]any{})},
	{"list_shots", "列出某个作品全部集的分镜：镜头语言、画面、台词、资产编号、衔接方式、关键帧、选定视频与抽卡候选。", obj(map[string]any{"project_id": num("作品编号")}, "project_id")},
	{"list_episodes", "列出某个作品的设定集（世界观、人物小传、总纲）和各集：梗概、摘要、卡点、缺失步骤、一键成集进度。", obj(map[string]any{"project_id": num("作品编号")}, "project_id")},
	{"create_episode", "给作品新建下一集。ai_idea=true 时根据设定集、前情摘要和上一集卡点自动拟定本集梗概。", obj(map[string]any{
		"project_id": num("作品编号"), "idea": str("本集梗概，可留空"), "ai_idea": boolp("自动拟定梗概"),
	}, "project_id")},
	{"get_episode", "查看一集的剧本、摘要、各步骤状态和分镜。", obj(map[string]any{"episode_id": num("集编号")}, "episode_id")},
	{"autopilot_start", "一键成集：梗概→剧本→资产→分镜→关键帧→配音→视频（只排队，按算力设置决定是否开机）→成片→摘要与封面。mode=auto 全自动；mode=ask 在剧本、资产、分镜之后暂停等确认；mode=writer（外部编剧）在梗概、剧本、分镜处暂停（状态 waiting_writer），由你调用 save_synopsis / save_script / upsert_asset / save_shots 写入后自动继续，应用不调用文本模型。", obj(map[string]any{
		"episode_id": num("集编号"), "mode": map[string]any{"type": "string", "enum": []string{"auto", "ask", "writer"}},
	}, "episode_id")},
	{"autopilot_status", "查询一键成集进度：状态（running / confirm / waiting / waiting_writer / done / failed / canceled）、当前阶段、日志。", obj(map[string]any{"episode_id": num("集编号")}, "episode_id")},
	{"autopilot_continue", "确认当前暂停点（或从失败处重试），继续一键成集。", obj(map[string]any{"episode_id": num("集编号")}, "episode_id")},
	{"autopilot_cancel", "停止一键成集。", obj(map[string]any{"episode_id": num("集编号")}, "episode_id")},
	{"generate_dub", "为一集的全部台词生成配音（按各角色音色与旁白音色）；force=true 时全部重做。", obj(map[string]any{"episode_id": num("集编号"), "force": boolp("全部重做")}, "episode_id")},
	{"tts", "用角色音色（asset_id）或指定音色合成一句话，返回音频地址。", obj(map[string]any{
		"text": str("要念的文字"), "emotion": str("语气，如：压低声音、愤怒"), "asset_id": num("角色资产编号"), "voice": map[string]any{"type": "object", "description": "音色：channel_id、mode（fish / builtin / design / clone / openai）、voice、sample、speed、pitch"},
	}, "text")},
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

// 支持的协议版本（新的在前）
var mcpVersions = []string{"2025-11-25", "2025-06-18", "2025-03-26", "2024-11-05"}

func handleMCP(w http.ResponseWriter, r *http.Request) {
	// Streamable HTTP：校验 Origin，防 DNS 重绑定（非浏览器客户端不带 Origin）
	if o := r.Header.Get("Origin"); o != "" && o != "null" {
		if u, err := url.Parse(o); err != nil || u.Host != r.Host {
			httpErr(w, 403, "不允许的来源")
			return
		}
	}
	pv := r.Header.Get("Mcp-Protocol-Version")
	if pv != "" && !contains(mcpVersions, pv) {
		httpErr(w, 400, "不支持的 MCP 协议版本："+pv)
		return
	}
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
			if res := mcpHandle(r.Context(), q, base, pv); res != nil {
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
	if q.JSONRPC != "2.0" || q.Method == "" {
		if q.Method == "" && (len(q.ID) > 0) {
			// 客户端发来的响应（我们不发请求），按规范接受即可
			w.WriteHeader(202)
			return
		}
		writeRPC(w, rpcError(q.ID, -32600, "无效请求"))
		return
	}
	res := mcpHandle(r.Context(), q, base, pv)
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

func mcpHandle(ctx context.Context, q rpcReq, base, pv string) any {
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
		if !contains(mcpVersions, pv) {
			pv = "2025-06-18"
		}
		return ok(map[string]any{
			"protocolVersion": pv,
			"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
			"serverInfo":      map[string]any{"name": "manju-studio", "title": "漫剧工坊", "version": version},
			"instructions":    "漫剧工坊。外部编剧（如 Antigravity 里的 Gemini）：先调用 get_writing_context，用 view_asset_images 看角色定妆，再用 save_bible / save_synopsis / save_script / upsert_asset / save_shots / save_episode_summary 交回文字，全部用简体中文；一键成集 mode=writer 时写入后自动继续。不要在用户没同意时调用 colab_start（会消耗 Colab 算力单元）。作品是连载剧集（设定集 + 多集，资产库跨集共享）。可以新建一集并一键成集（autopilot_start，异步，用 autopilot_status 轮询，mode=ask 时用 autopilot_continue 确认）、生成配音、合成语音；也能直接管理 Colab 运行时、执行 Python、提交 MiniMax H3 图生视频（video_submit 后用 video_status 轮询）、调用生图渠道。",
		})
	case "ping":
		return ok(map[string]any{})
	case "tools/list":
		return ok(map[string]any{"tools": append(append([]mcpTool{}, writerTools...), mcpTools...)})
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
		v, err := callWriterTool(ctx, p.Name, p.Arguments, base, pv != "2024-11-05")
		if errors.Is(err, errUnknownTool) {
			v, err = callTool(ctx, p.Name, p.Arguments, base)
		}
		if errors.Is(err, errUnknownTool) {
			return rpcError(q.ID, -32602, "没有这个工具："+p.Name)
		}
		if err != nil {
			return ok(map[string]any{"content": []any{map[string]any{"type": "text", "text": err.Error()}}, "isError": true})
		}
		if rich, isRich := v.(*mcpRich); isRich {
			res := map[string]any{"content": rich.Blocks}
			if rich.Structured != nil {
				res["structuredContent"] = wrapStructured(rich.Structured)
			}
			return ok(res)
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
	case "list_episodes":
		var a struct {
			ProjectID json.Number `json:"project_id"`
		}
		_ = json.Unmarshal(args, &a)
		id, _ := strconv.ParseInt(a.ProjectID.String(), 10, 64)
		return toolEpisodes(ctx, id)
	case "create_episode":
		var a epCreateArgs
		_ = json.Unmarshal(args, &a)
		return toolEpisodeCreate(ctx, a)
	case "get_episode", "autopilot_status", "autopilot_continue", "autopilot_cancel", "autopilot_start", "generate_dub":
		var a struct {
			EpisodeID json.Number `json:"episode_id"`
			Mode      string      `json:"mode"`
			Force     bool        `json:"force"`
		}
		_ = json.Unmarshal(args, &a)
		id, _ := strconv.ParseInt(a.EpisodeID.String(), 10, 64)
		switch name {
		case "get_episode":
			return toolEpisode(ctx, id, base)
		case "generate_dub":
			return toolLines(ctx, id, a.Force)
		}
		return toolAutopilot(ctx, strings.TrimPrefix(name, "autopilot_"), pipeArgs{EpisodeID: id, Mode: a.Mode})
	case "tts":
		var a ttsArgs
		_ = json.Unmarshal(args, &a)
		return toolTTS(ctx, a, base)
	case "image_generate":
		var a imageArgs
		_ = json.Unmarshal(args, &a)
		return toolImageGenerate(ctx, a, base)
	}
	return nil, errUnknownTool
}

var _ = base64.StdEncoding
var _ = os.Remove
