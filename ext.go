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
	Prompt        string `json:"prompt"`
	ImageURL      string `json:"image_url"`
	ImageBase64   string `json:"image_base64"`
	ImageUploadID string `json:"image_upload_id"`
	ImageID       int64  `json:"image_id"`
	ShotID        int64  `json:"shot_id"`
	Seconds       int    `json:"seconds"`
	Res           string `json:"resolution"`
	Aspect        string `json:"aspect"`
	Turbo         bool   `json:"turbo"`
	Seed          int64  `json:"seed"`
	Takes         int    `json:"takes"`
}

func loadImageBytes(ctx context.Context, a videoArgs) ([]byte, error) {
	if strings.TrimSpace(a.ImageUploadID) != "" {
		data, _, err := readUploadBytes(a.ImageUploadID)
		return data, err
	}
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
	return nil, errors.New("需要 image_id、image_upload_id、image_url 或 image_base64 之一")
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
		ids, err := queueShot(p, s, queueOpts{Seconds: a.Seconds, Turbo: turbo, Res: a.Res, Takes: a.Takes, AllowWaitPrev: true})
		if err != nil && len(ids) == 0 {
			return nil, err
		}
		st := "queued"
		if v, err := getVideo(ids[0]); err == nil && v.Status != "" {
			st = v.Status
		}
		effTurbo := p.VideoTurbo
		if turbo != nil {
			effTurbo = *turbo
		}
		return map[string]any{"id": ids[0], "ids": ids, "status": st, "estimate": estimateCU(len(ids), effTurbo)}, nil
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
	if v.VcStatus != "" {
		out["voice_conversion"] = vcView(v, base)
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
	takeDetails := []map[string]any{}
	versions := []map[string]any{}
	pickedVid := int64(0)
	if v := shotVideoFor(s); v != nil {
		pickedVid = v.ID
	}
	for _, v := range s.Videos {
		td := map[string]any{
			"id": v.ID, "status": v.Status, "picked": v.ID == pickedVid,
			"seconds": v.Seconds, "resolution": v.Res, "aspect": v.Aspect, "turbo": v.Turbo, "seed": v.Seed,
			"progress": v.Progress, "stage": v.Stage, "created_at": v.CreatedAt, "finished_at": v.FinishedAt,
		}
		if v.Error != "" {
			td["error"] = v.Error
		}
		if v.VcStatus != "" {
			td["vc_status"] = v.VcStatus
			td["vc_use"] = v.VcUse && v.VcFile != ""
		}
		takeDetails = append(takeDetails, td)
		if v.Status == "done" {
			takes = append(takes, v.ID)
			if v.VcStatus != "" {
				x := vcView(v, base)
				x["take_id"] = v.ID
				versions = append(versions, x)
			}
		}
	}
	m["takes"] = takes
	m["take_details"] = takeDetails
	if p, err := getProject(s.ProjectID); err == nil {
		m["voice_unify"] = vcPreview(p, s)
	}
	if len(versions) > 0 {
		m["take_voice"] = versions
	}
	return m
}

func toolShots(ctx context.Context, pid, eid int64, brief bool, base string) (any, error) {
	var eps []*Episode
	if eid > 0 {
		e, err := getEpisode(eid)
		if err != nil {
			return nil, errors.New("这一集不存在")
		}
		eps = []*Episode{e}
	} else {
		if _, err := getProject(pid); err != nil {
			return nil, errors.New("作品不存在")
		}
		eps = listEpisodes(pid)
	}
	out := []map[string]any{}
	for _, e := range eps {
		for _, s := range listShots(e.ID, true) {
			m := shotView(s, base)
			if brief {
				delete(m, "take_details")
				delete(m, "take_voice")
				delete(m, "voice_unify")
			}
			out = append(out, m)
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
	EpisodeID int64    `json:"episode_id"`
	Mode      string   `json:"mode"`
	PauseAt   []string `json:"pause_at"`
}

func toolAutopilot(ctx context.Context, action string, a pipeArgs) (any, error) {
	if _, _, err := epCtx(a.EpisodeID); err != nil {
		return nil, err
	}
	switch action {
	case "start":
		return startPipe(a.EpisodeID, a.Mode, a.PauseAt...)
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
	studioIn
	Wait *bool `json:"wait"`
}

func toolImageGenerate(ctx context.Context, a imageArgs, base string) (any, error) {
	a.Owner = "ext"
	if a.Wait != nil && !*a.Wait {
		if _, _, _, _, _, err := studioPrepare(a.studioIn); err != nil {
			return nil, err
		}
		tid, err := startTaskR(0, 0, "studio", "ext", studioSeq.Add(1), func(tctx context.Context, tid int64) error {
			ims, err := studioRun(tctx, a.studioIn)
			if err != nil {
				return err
			}
			ids := make([]int64, 0, len(ims))
			for _, im := range ims {
				ids = append(ids, im.ID)
			}
			setTaskResult(tid, map[string]any{"image_ids": ids})
			return nil
		})
		if err != nil {
			return nil, err
		}
		return map[string]any{"ok": true, "task_id": tid, "status": "running", "note": "已异步提交生图；用 get_task_status 轮询完成后拿 image_ids，再用 view_image 看图。"}, nil
	}
	c, cancel := context.WithTimeout(ctx, 8*time.Minute)
	defer cancel()
	ims, err := studioRun(c, a.studioIn)
	if err != nil {
		return nil, err
	}
	out := []map[string]any{}
	for _, im := range ims {
		out = append(out, map[string]any{"image_id": im.ID, "width": im.Width, "height": im.Height, "model": im.Model,
			"channel_id": im.ChannelID, "url": fmt.Sprintf("%s/v1/images/%d/file", base, im.ID)})
	}
	return map[string]any{"images": out, "note": "图片已放进应用的「画室」，用户可以在界面里查看、下载；你要亲眼看效果，用 view_image（image_ids 传上面的编号）拿图片内容块。要放进作品，用 attach_image 存到资产（设定图 / 三视图 / 表情表 / 参考图）。"}, nil
}

// toolImageChannels 生图渠道和各自的模型、参考图上限
func toolImageChannels(ctx context.Context) (any, error) {
	out := []map[string]any{}
	all, _ := listChannels()
	for _, ch := range all {
		if ch.Kind != "image" {
			continue
		}
		out = append(out, map[string]any{"channel_id": ch.ID, "name": ch.Name, "models": ch.Models, "default_model": firstOr(ch.Models), "max_refs": refLimit(ch)})
	}
	return map[string]any{"channels": out, "note": "image_generate / generate_asset_image 传 channel_id 和 model 指定；不传用第一个渠道（作品资产用作品设置里的渠道）。"}, nil
}

// toolVoiceChannels 语音渠道和各自的模型 / 内置音色
func toolVoiceChannels(ctx context.Context) (any, error) {
	out := []map[string]any{}
	all, _ := listChannels()
	for _, ch := range all {
		if ch.Kind != "voice" {
			continue
		}
		out = append(out, map[string]any{
			"channel_id": ch.ID, "name": ch.Name, "api_type": ch.APIType,
			"models": ch.Models, "default_model": firstOr(ch.Models), "voices": ch.Voices,
		})
	}
	return map[string]any{"channels": out, "note": "design_voice / set_voice（含 synth_channel_id）/ clone_voice / update_project（voice_channel_id）可用这里的 channel_id。"}, nil
}

func firstOr(xs []string) string {
	if len(xs) > 0 {
		return xs[0]
	}
	return ""
}

type attachArgs struct {
	ImageID int64  `json:"image_id"`
	AssetID int64  `json:"asset_id"`
	ShotID  int64  `json:"shot_id"`
	Tag     string `json:"tag"`
}

func toolAttachImage(ctx context.Context, a attachArgs) (any, error) {
	if a.AssetID > 0 && a.ShotID > 0 {
		return nil, errors.New("asset_id 和 shot_id 二选一")
	}
	if a.ShotID > 0 {
		im, s, err := copyImageToShot(a.ImageID, a.ShotID)
		if err != nil {
			return nil, err
		}
		return map[string]any{"ok": true, "image_id": im.ID, "shot_id": s.ID, "code": s.Code, "saved_as": "镜头关键帧（已选用）"}, nil
	}
	if a.AssetID <= 0 {
		return nil, errors.New("需要 asset_id 或 shot_id")
	}
	tag := a.Tag
	if tag == "base" || tag == "picked" {
		tag = ""
	}
	im, as, err := copyImageTo(a.ImageID, a.AssetID, tag)
	if err != nil {
		return nil, err
	}
	what := map[string]string{"": "设定图（已选用）", "sheet": "三视图（已选用）", "expr": "表情表（已选用）", "ref": "参考图（以后给它出图时用来锁脸 / 定外形）"}[tag]
	return map[string]any{"ok": true, "image_id": im.ID, "asset_id": as.ID, "code": as.Code, "name": as.Name, "saved_as": what}, nil
}

type assetGenArgs struct {
	AssetID   int64   `json:"asset_id"`
	ProjectID int64   `json:"project_id"`
	Code      string  `json:"code"`
	Tag       string  `json:"tag"`
	N         int     `json:"n"`
	ChannelID int64   `json:"channel_id"`
	Model     string  `json:"model"`
	FaceIDs   []int64 `json:"face_image_ids"`
	Extra     string  `json:"extra_prompt"`
	Wait      *bool   `json:"wait"`
}

// toolAssetGenerate 给资产出图：自动带上参考图（自己的定妆图、手动参考图、本体角色），默认等出完再返回
func toolAssetGenerate(ctx context.Context, a assetGenArgs, base string) (any, error) {
	var as *Asset
	if a.AssetID > 0 {
		as, _ = getAsset(a.AssetID)
	} else if a.ProjectID > 0 && a.Code != "" {
		for _, x := range listAssets(a.ProjectID) {
			if strings.EqualFold(x.Code, strings.TrimSpace(a.Code)) {
				as = x
			}
		}
	}
	if as == nil {
		return nil, errors.New("资产不存在：给 asset_id，或 project_id + code")
	}
	p, err := getProject(as.ProjectID)
	if err != nil {
		return nil, errors.New("作品不存在")
	}
	tag := a.Tag
	if tag == "base" || tag == "picked" {
		tag = ""
	}
	if tag != "" && tag != "sheet" && tag != "expr" {
		return nil, errors.New("tag 只能是 空（设定图）/ sheet（三视图）/ expr（表情表）")
	}
	if tag != "" && as.PickedImageID == 0 {
		return nil, errors.New("先给这个角色生成并选定设定图，再做三视图 / 表情表")
	}
	before := map[int64]bool{}
	for _, im := range listImages(`WHERE owner_type='asset' AND owner_id=?`, as.ID) {
		before[im.ID] = true
	}
	for _, id := range a.FaceIDs {
		if _, err := getImage(id); err != nil {
			return nil, fmt.Errorf("图片 %d 不存在", id)
		}
	}
	tid, err := genAssetTask(p, as, clampInt(a.N, 1, 4), tag, imgOverride{ChannelID: a.ChannelID, Model: a.Model, FaceIDs: a.FaceIDs, Extra: a.Extra})
	if err != nil {
		return nil, err
	}
	refs := []map[string]any{}
	used := assetRefs(as, tag)
	if len(a.FaceIDs) > 0 {
		used = nil
		for _, id := range a.FaceIDs {
			used = append(used, assetRef{ID: id, Kind: "face", Note: "参考照"})
		}
	}
	for _, r := range used {
		role := map[string]string{"own": "自己的" + r.Note, "face": "参考照（锁脸）", "look": "参考图（外形风格）"}[r.Kind]
		if strings.HasPrefix(r.Kind, "base:") {
			role = "本体「" + strings.TrimPrefix(r.Kind, "base:") + "」的" + r.Note + "（只锁脸）"
		}
		refs = append(refs, map[string]any{"image_id": r.ID, "role": role})
	}
	if a.Wait != nil && !*a.Wait {
		return map[string]any{"task_id": tid, "refs": refs, "note": "已开始生成；用 view_asset_images 查看结果"}, nil
	}
	ims, err := waitImages(ctx, tid, "asset", as.ID, before, base)
	if err != nil {
		return nil, err
	}
	if ims == nil {
		return map[string]any{"task_id": tid, "note": "还在生成，稍后用 view_asset_images 查看"}, nil
	}
	return map[string]any{"ok": true, "asset_id": as.ID, "code": as.Code, "images": ims, "refs": refs,
		"note": "新图已放进资产候选；如果这个位置原来是空的会自动选用。用 view_asset_images 看效果。"}, nil
}

// waitImages 等出图任务结束，返回这次新增的图；超时返回 nil, nil
func waitImages(ctx context.Context, tid int64, ownerType string, ownerID int64, before map[int64]bool, base string) ([]map[string]any, error) {
	deadline := time.Now().Add(7 * time.Minute)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return nil, nil
		case <-time.After(2 * time.Second):
		}
		var st, msg string
		_ = db.QueryRow(`SELECT status,error FROM tasks WHERE id=?`, tid).Scan(&st, &msg)
		if st == "failed" {
			return nil, errors.New(msg)
		}
		if st == "done" {
			out := []map[string]any{}
			for _, im := range listImages(`WHERE owner_type=? AND owner_id=? ORDER BY id`, ownerType, ownerID) {
				if !before[im.ID] {
					out = append(out, map[string]any{"image_id": im.ID, "tag": im.Tag, "model": im.Model, "url": fmt.Sprintf("%s/v1/images/%d/file", base, im.ID)})
				}
			}
			return out, nil
		}
	}
	return nil, nil
}

type keyframeArgs struct {
	ShotID    int64    `json:"shot_id"`
	N         int      `json:"n"`
	ChannelID int64    `json:"channel_id"`
	Model     string   `json:"model"`
	RefIDs    []int64  `json:"ref_image_ids"`
	Extra     string   `json:"extra_prompt"`
	RefTags   []string `json:"ref_tags"`
	Wait      *bool    `json:"wait"`
}

func toolKeyframe(ctx context.Context, a keyframeArgs, base string) (any, error) {
	s, err := getShot(a.ShotID)
	if err != nil {
		return nil, errors.New("镜头不存在")
	}
	p, err := getProject(s.ProjectID)
	if err != nil {
		return nil, errors.New("作品不存在")
	}
	if a.RefIDs != nil {
		if err := setShotRefs(s, a.RefIDs); err != nil {
			return nil, err
		}
		s, _ = getShot(s.ID)
	}
	before := map[int64]bool{}
	for _, im := range listImages(`WHERE owner_type='shot' AND owner_id=?`, s.ID) {
		before[im.ID] = true
	}
	for _, t := range a.RefTags {
		if t != "main" && t != "sheet" && t != "expr" {
			return nil, errors.New("ref_tags 只能包含 main（设定图）/ sheet（三视图）/ expr（表情表）")
		}
	}
	tid, err := genKeyframeTask(p, s, clampInt(a.N, 1, 4), imgOverride{ChannelID: a.ChannelID, Model: a.Model, Extra: a.Extra, RefTags: a.RefTags})
	if err != nil {
		return nil, err
	}
	if a.Wait != nil && !*a.Wait {
		return map[string]any{"ok": true, "shot_id": s.ID, "task_id": tid, "status": "running", "note": "已开始生成关键帧；用 get_task_status 查进度，完成后用 view_shot 看效果。"}, nil
	}
	ims, err := waitImages(ctx, tid, "shot", s.ID, before, base)
	if err != nil {
		return nil, err
	}
	if ims == nil {
		return map[string]any{"task_id": tid, "note": "还在生成，稍后用 get_task_status 或 view_shot 查看"}, nil
	}
	return map[string]any{"ok": true, "shot_id": s.ID, "images": ims, "note": "新关键帧已放进候选；原来没有选定时自动选用。用 view_shot 看效果。"}, nil
}

type taskStatusArgs struct {
	TaskID    int64   `json:"task_id"`
	TaskIDs   []int64 `json:"task_ids"`
	ProjectID int64   `json:"project_id"`
	EpisodeID int64   `json:"episode_id"`
}

func taskView(t *Task) map[string]any {
	m := map[string]any{
		"task_id": t.ID, "project_id": t.ProjectID, "episode_id": t.EpisodeID, "kind": t.Kind,
		"owner_type": t.OwnerType, "owner_id": t.OwnerID, "status": t.Status, "error": t.Error,
		"created_at": t.CreatedAt, "finished_at": t.FinishedAt,
	}
	if t.Result != "" {
		var r any
		if json.Unmarshal([]byte(t.Result), &r) == nil {
			m["result"] = r
		} else {
			m["result"] = t.Result
		}
	}
	return m
}

func toolTaskStatus(ctx context.Context, a taskStatusArgs) (any, error) {
	ids := append([]int64{}, a.TaskIDs...)
	if a.TaskID > 0 {
		ids = append([]int64{a.TaskID}, ids...)
	}
	if len(ids) > 0 {
		if len(ids) > 50 {
			return nil, errors.New("一次最多查询 50 个任务")
		}
		out := []map[string]any{}
		seen := map[int64]bool{}
		running, done, failed := 0, 0, 0
		for _, id := range ids {
			if seen[id] {
				continue
			}
			seen[id] = true
			t, err := getTask(id)
			if err != nil {
				out = append(out, map[string]any{"task_id": id, "status": "not_found"})
				continue
			}
			switch t.Status {
			case "running":
				running++
			case "done":
				done++
			case "failed":
				failed++
			}
			out = append(out, taskView(t))
		}
		return map[string]any{"tasks": out, "running": running, "done": done, "failed": failed, "all_done": running == 0}, nil
	}
	if a.EpisodeID > 0 {
		rows, err := db.Query(`SELECT id,project_id,episode_id,kind,owner_type,owner_id,status,error,result,created_at,finished_at FROM tasks WHERE episode_id=? ORDER BY id DESC LIMIT 40`, a.EpisodeID)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		out := []map[string]any{}
		running := 0
		for rows.Next() {
			var t Task
			if rows.Scan(&t.ID, &t.ProjectID, &t.EpisodeID, &t.Kind, &t.OwnerType, &t.OwnerID, &t.Status, &t.Error, &t.Result, &t.CreatedAt, &t.FinishedAt) == nil {
				if t.Status == "running" {
					running++
				}
				out = append(out, taskView(&t))
			}
		}
		return map[string]any{"tasks": out, "running": running, "all_done": running == 0}, nil
	}
	if a.ProjectID > 0 {
		rows, err := db.Query(`SELECT id,project_id,episode_id,kind,owner_type,owner_id,status,error,result,created_at,finished_at FROM tasks WHERE project_id=? ORDER BY id DESC LIMIT 40`, a.ProjectID)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		out := []map[string]any{}
		running := 0
		for rows.Next() {
			var t Task
			if rows.Scan(&t.ID, &t.ProjectID, &t.EpisodeID, &t.Kind, &t.OwnerType, &t.OwnerID, &t.Status, &t.Error, &t.Result, &t.CreatedAt, &t.FinishedAt) == nil {
				if t.Status == "running" {
					running++
				}
				out = append(out, taskView(&t))
			}
		}
		return map[string]any{"tasks": out, "running": running, "all_done": running == 0}, nil
	}
	return nil, errors.New("需要 task_id、task_ids、episode_id 或 project_id")
}

type missingKFArgs struct {
	EpisodeID int64  `json:"episode_id"`
	ChannelID int64  `json:"channel_id"`
	Model     string `json:"model"`
}

func toolGenerateMissingKeyframes(ctx context.Context, a missingKFArgs) (any, error) {
	p, e, err := epCtx(a.EpisodeID)
	if err != nil {
		return nil, err
	}
	tids, skipped, lastErr := startMissingKeyframesDetail(p, e, imgOverride{ChannelID: a.ChannelID, Model: a.Model})
	if len(tids) == 0 && lastErr != nil {
		return nil, lastErr
	}
	if tids == nil {
		tids = []int64{}
	}
	return map[string]any{
		"ok": true, "episode_id": e.ID, "started": len(tids), "skipped": skipped, "task_ids": tids,
		"note": "已批量启动缺失关键帧的异步任务；用 get_task_status（task_ids）轮询进度。",
	}, nil
}

func extKeyframesMissing(w http.ResponseWriter, r *http.Request) {
	var a missingKFArgs
	_ = readJSON(r, &a)
	a.EpisodeID = pathID(r)
	v, err := toolGenerateMissingKeyframes(r.Context(), a)
	extReply(w, v, err)
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
	file := v.useFile()
	switch r.URL.Query().Get("version") {
	case "original":
		file = v.File
	case "converted":
		if v.VcFile == "" {
			httpErr(w, 404, "还没有统一音色后的版本")
			return
		}
		file = v.VcFile
	}
	w.Header().Set("Content-Type", "video/mp4")
	http.ServeFile(w, r, filepath.Join(dataDir, "videos", file))
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
	eid, _ := strconv.ParseInt(r.URL.Query().Get("episode_id"), 10, 64)
	brief := r.URL.Query().Get("brief") == "1" || r.URL.Query().Get("brief") == "true"
	v, err := toolShots(r.Context(), pathID(r), eid, brief, baseURL(r))
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

func extImageCopy(w http.ResponseWriter, r *http.Request) {
	var a attachArgs
	if err := readJSON(r, &a); err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	a.ImageID = pathID(r)
	v, err := toolAttachImage(r.Context(), a)
	extReply(w, v, err)
}
func extImageChannels(w http.ResponseWriter, r *http.Request) {
	v, err := toolImageChannels(r.Context())
	extReply(w, v, err)
}
func extAssetGenerate(w http.ResponseWriter, r *http.Request) {
	var a assetGenArgs
	_ = readJSON(r, &a)
	a.AssetID = pathID(r)
	v, err := toolAssetGenerate(r.Context(), a, baseURL(r))
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
	{"colab_start", "申请一台运行时。purpose=video 会安装 ComfyUI 并下载视频模型（需要 GPU）；purpose=manual 只开机用于运行代码；purpose=cache 用 CPU 运行时把模型文件预缓存到云端硬盘，完成后自动关机（需先开启云端硬盘缓存）。", obj(map[string]any{
		"accelerator": str("加速器，如 G4、A100、L4、T4、CPU；不填用设置里的默认值"),
		"shape":       str("SHAPE_STANDARD 或 SHAPE_HIGHMEM"),
		"purpose":     map[string]any{"type": "string", "enum": []string{"video", "manual", "cache"}},
	})},
	{"colab_stop", "立即关机并释放运行时。", obj(map[string]any{})},
	{"colab_run_python", "在当前运行时的 Python 内核里执行代码，返回 stdout、stderr、最后一个表达式的值和错误。", obj(map[string]any{
		"code": str("要执行的 Python 代码"), "timeout_sec": num("超时秒数，默认 300，最多 1800"),
	}, "code")},
	{"video_submit", "提交一个图生视频任务（MiniMax H3，首帧 + 描述 → 5–10 秒带声音的视频）。给 shot_id 时直接用该镜头的关键帧和描述。", obj(map[string]any{
		"prompt": str("视频描述：动作、运镜、声音"), "image_id": num("已有图片编号"), "image_upload_id": str("POST /v1/uploads 返回的临时上传文件编号"), "image_url": str("首帧图片地址"), "image_base64": str("首帧图片 base64"),
		"shot_id": num("作品里的镜头编号"), "takes": num("抽卡次数 1–4，只对 shot_id 有效"), "seconds": num("时长 5–10 秒，默认 5"), "resolution": map[string]any{"type": "string", "enum": []string{"768p", "480p"}},
		"aspect": str("画幅，如 9:16、16:9、1:1"), "turbo": boolp("使用 Turbo 8 步"), "seed": num("随机种子"),
	})},
	{"video_status", "查询视频任务状态；完成后返回下载地址（同一密钥可下载）。", obj(map[string]any{"id": num("视频任务编号")}, "id")},
	{"list_projects", "列出全部作品（剧集）及每一集还缺哪些步骤。", obj(map[string]any{})},
	{"list_shots", "列出某个作品（或单集）的分镜：镜头语言、画面、台词、资产编号、衔接方式、关键帧、选定视频与抽卡候选。长剧集建议传 episode_id 或 brief=true 省上下文。", obj(map[string]any{
		"project_id": num("作品编号（与 episode_id 二选一）"), "episode_id": num("只列出这一集的分镜（推荐）"),
		"brief": boolp("精简模式：省略详细 take_details / voice_unify，省上下文"),
	})},
	{"list_episodes", "列出某个作品的设定集（世界观、人物小传、总纲）和各集：梗概、摘要、卡点、缺失步骤、一键成集进度。", obj(map[string]any{"project_id": num("作品编号")}, "project_id")},
	{"create_episode", "给作品新建下一集。ai_idea=true 时根据设定集、前情摘要和上一集卡点自动拟定本集梗概。", obj(map[string]any{
		"project_id": num("作品编号"), "idea": str("本集梗概，可留空"), "ai_idea": boolp("自动拟定梗概"),
	}, "project_id")},
	{"get_episode", "查看一集的剧本、摘要、各步骤状态和分镜。", obj(map[string]any{"episode_id": num("集编号")}, "episode_id")},
	{"autopilot_start", "一键成集：梗概→剧本→资产→分镜→关键帧→配音→视频（只排队，按算力设置决定是否开机）→成片→摘要与封面。mode=auto 全自动；mode=ask 在剧本、资产、分镜之后暂停等确认；mode=writer（外部编剧）在梗概、剧本、分镜处暂停（状态 waiting_writer），由你调用 save_synopsis / save_script / upsert_asset / save_shots 写入后自动继续，应用不调用文本模型。支持 pause_at（所有模式通用），例如 [\"assets_img\", \"keyframes\"] 在资产图/三视图后和关键帧后停下审图。", obj(map[string]any{
		"episode_id": num("集编号"), "mode": map[string]any{"type": "string", "enum": []string{"auto", "ask", "writer"}},
		"pause_at": map[string]any{"type": "array", "items": map[string]any{"type": "string", "enum": []string{"script", "assets", "assets_img", "storyboard", "keyframes"}}, "description": "审图/确认暂停点（所有模式通用）：assets / assets_img = 资产图与三视图完成后暂停；keyframes = 关键帧完成后暂停；script / storyboard = 剧本或分镜后暂停"},
	}, "episode_id")},
	{"autopilot_status", "查询一键成集进度：状态（running / confirm / waiting / waiting_writer / done / failed / canceled）、当前阶段、日志。", obj(map[string]any{"episode_id": num("集编号")}, "episode_id")},
	{"autopilot_continue", "确认当前暂停点（或从失败处重试），继续一键成集。", obj(map[string]any{"episode_id": num("集编号")}, "episode_id")},
	{"autopilot_cancel", "停止一键成集。", obj(map[string]any{"episode_id": num("集编号")}, "episode_id")},
	{"generate_dub", "为一集的全部台词生成配音（按各角色音色与旁白音色）；force=true 时全部重做。", obj(map[string]any{"episode_id": num("集编号"), "force": boolp("全部重做")}, "episode_id")},
	{"tts", "用角色音色（asset_id）或指定音色合成一句话，返回音频地址。", obj(map[string]any{
		"text": str("要念的文字"), "emotion": str("语气，如：压低声音、愤怒"), "asset_id": num("角色资产编号"), "voice": map[string]any{"type": "object", "description": "音色：channel_id、mode（fish / builtin / design / clone / openai）、voice、sample、speed、pitch"},
	}, "text")},
	{"design_voice", "按文字描述设计角色音色（或旁白音色）：返回 1–4 个候选（样本名、时长、念的文字、2 小时有效的免密钥下载地址 + MCP 音频内容块），不会改角色的音色。候选按角色存下来，界面音色面板里也能看到、能选。宿主没把音频块交给你时，用下载地址 curl / Invoke-WebRequest 下到工作区再 read_file 听；几个角色一起挑用 export_voice_candidates 打包。之后也可以用 listen_voice / get_audio_link 再取。听完用 set_voice 选定。description 不填就用角色已存的音色描述（upsert_asset 的 voice_description）。有小米 MiMo 渠道用 MiMo，否则用 Fish Audio。", obj(map[string]any{
		"asset_id": num("角色资产编号（给旁白设计时不填，填 project_id）"), "project_id": num("作品编号（旁白）"), "description": str("音色描述：性别、年龄感、音色质感、语速、性格、口音"),
		"sample_text": str("试音台词（150 字以内）"), "n": num("候选个数 1–4，默认 3"), "channel_id": num("语音渠道编号（可选）"),
	})},
	{"set_voice", "把 design_voice 的一个候选（任何一次设计出来的都行，用原始样本名，不是压缩包里的文件名；channel_id 不填用当初设计它的渠道）设为角色（或旁白）的音色；用 Fish 时会在 Fish 建一个私有音色。也可以直接给 voice 对象。之后配音都用它。", obj(map[string]any{
		"asset_id": num("角色资产编号"), "project_id": num("作品编号（设旁白时填，且不填 asset_id）"), "sample": str("design_voice 返回的样本名"), "channel_id": num("design_voice 返回的渠道编号"),
		"description": str("这段音色的描述（记下来以后改用）"), "synth_channel_id": num("配音用哪个渠道（可选）：设计用 MiMo、配音交给 Fish 时填 Fish 渠道编号；只填这个不填 sample = 把已选音色换到这个渠道配音"), "voice": map[string]any{"type": "object", "description": "直接指定音色：channel_id、mode、voice、sample、speed"},
	})},
	{"list_image_channels", "列出生图渠道：渠道编号、可用模型、参考图上限。指定渠道和模型出图前先看这个。", obj(map[string]any{})},
	{"list_voice_channels", "列出语音渠道：渠道编号、名称、类型（mimo / fish / openai）、可用模型与内置音色。调用 design_voice / set_voice / clone_voice 前先看这个。", obj(map[string]any{})},
	{"image_generate", "自由出图（不挂作品）：生成的图放进应用的「画室」，用户能在界面里看和下载；返回图片编号，用 view_image 看图。可带参考图锁脸 / 定风格；要放进作品用 attach_image。给作品角色出图请优先用 generate_asset_image（会自动带上角色的参考图）。传 wait=false 立即返回 task_id，再用 get_task_status 轮询。", obj(map[string]any{
		"prompt": str("提示词。带参考图时写清每张参考图是谁、参考什么，如「参考图1是同一个人，脸型五官保持一致，只改发型和服装」"), "channel_id": num("渠道编号（list_image_channels），不填用第一个生图渠道"), "model": str("模型名，不填用渠道的第一个模型"),
		"aspect": str("比例，如 9:16、3:4、1:1、16:9"), "n": num("张数 1–4"),
		"ref_image_ids": map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "description": "参考图：应用里已有图片的编号（资产图、画室图都行，view_asset_images 能看到编号）"},
		"ref_images":    map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "参考图：base64 或 data URL"},
		"realistic":     boolp("追加真人实拍质感的固定描述"),
		"wait":          boolp("是否等出完再返回，默认 true；false 立即返回 task_id"),
	}, "prompt")},
	{"generate_asset_image", "给作品资产出图（设定图 / 三视图 / 表情表），自动带上参考图：角色自己已选定的图、手动加的参考照、以及「同一个人另一时期」的本体角色（只锁脸，发型服装按这个角色自己的外观）。默认等出完再返回新图编号。", obj(map[string]any{
		"asset_id": num("资产编号"), "project_id": num("作品编号（配合 code）"), "code": str("资产编号，如 C03"),
		"tag":        map[string]any{"type": "string", "enum": []string{"", "sheet", "expr"}, "description": "空 = 设定图；sheet = 三视图；expr = 表情表（后两个需要先有设定图）"},
		"n":          num("张数 1–4，默认 1"),
		"channel_id": num("渠道编号，不填用作品设置里的生图渠道"), "model": str("模型名"),
		"face_image_ids": map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "description": "只用这几张图锁脸（图片编号，任何资产、任何候选、画室图都行），不再自动带角色已选定的图和本体。用户说「拿某张图当参考出一张」时用这个"},
		"extra_prompt":   str("这一次额外的要求，如「年龄 30 岁左右，炭灰高领毛衣配黑大衣」"),
		"wait":           boolp("等出完再返回，默认 true；false 立即返回 task_id，再用 get_task_status 轮询"),
	})},
	{"generate_keyframe", "给一个镜头生成关键帧：自动按镜头里的资产带上角色 / 场景参考图。可指定渠道和模型；extra_prompt / ref_tags 只影响这一次。默认等出完再返回（传 wait=false 立即返回 task_id，避免大批量出图超时），已有的关键帧候选都保留。", obj(map[string]any{
		"shot_id": num("镜头编号"), "n": num("张数 1–4，默认 1"), "channel_id": num("渠道编号，不填用作品设置"), "model": str("模型名"),
		"ref_image_ids": map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "description": "这个镜头额外的参考图编号（构图、姿势、氛围、道具），最多 4 张，会保存在镜头上；传空数组清掉。人物的脸仍按角色资产自动带"},
		"extra_prompt":  str("只对这一次出图追加的要求（不保存到镜头），如「她左手拿着一把黑伞，抬头看雨」。要长期改提示词用 update_shot"),
		"ref_tags":      map[string]any{"type": "array", "items": map[string]any{"type": "string", "enum": []string{"main", "sheet", "expr"}}, "description": "只对这一次：角色带哪几类图（按顺序取第一张有的）。不填 = 默认（三视图优先 + 固定造型）。[\"main\"] = 只用设定图锁脸，不带三视图、不注入固定造型，适合换装 / 特殊姿势的镜头"},
		"wait":          boolp("等出完再返回，默认 true；false 立即返回 task_id，用 get_task_status 查进度"),
	}, "shot_id")},
	{"generate_missing_keyframes", "批量为一集所有还没有关键帧的镜头异步启动关键帧生成任务，立即返回 task_ids，再用 get_task_status 轮询进度。", obj(map[string]any{
		"episode_id": num("集编号"), "channel_id": num("生图渠道编号（可选，不填用作品设置）"), "model": str("模型名（可选）"),
	}, "episode_id")},
	{"get_task_status", "查询异步后台任务状态（生图、关键帧、成片、封面、配音等）：支持单个 task_id、批量 task_ids（最多 50 个），或按 episode_id / project_id 列出最近任务。", obj(map[string]any{
		"task_id": num("单个任务编号"), "task_ids": map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "description": "批量任务编号（最多 50 个）"},
		"episode_id": num("列出该集最近任务"), "project_id": num("列出该作品最近任务"),
	})},
	{"pick_image", "把一张图设为选用（定稿）：资产的设定图 / 三视图 / 表情表、镜头的关键帧、封面。后续出图和关键帧都用选用的那张。候选图编号用 view_asset_images / view_shot 看。", obj(map[string]any{
		"image_id": num("图片编号"),
	}, "image_id")},
	{"upload_image", "上传一张图（优先用 POST /v1/uploads 返回的 upload_id，或直接给 base64 / data URL）：都不填放进画室；填 asset_id 存进资产（tag 同 attach_image）；填 shot_id 设为该镜头的关键帧并选用。", obj(map[string]any{
		"upload_id": str("POST /v1/uploads 返回的临时上传文件编号（推荐，避免大 base64）"), "image": str("base64 或 data URL（与 upload_id 二选一）"),
		"asset_id": num("目标资产编号（与 shot_id 二选一，都不填放进画室）"), "shot_id": num("目标镜头编号（设为该镜头关键帧并选用）"),
		"tag": map[string]any{"type": "string", "enum": []string{"", "sheet", "expr", "ref"}},
	})},
	{"attach_image", "把一张图（画室里的、别的资产或镜头的都行）复制进某个资产或某个镜头：填 asset_id 时 tag 空 = 设定图并选用，sheet / expr = 三视图 / 表情表，ref = 参考照；填 shot_id 时设为该镜头关键帧并选用。", obj(map[string]any{
		"image_id": num("图片编号"), "asset_id": num("目标资产编号（与 shot_id 二选一）"), "shot_id": num("目标镜头编号（设为该镜头关键帧并选用）"),
		"tag": map[string]any{"type": "string", "enum": []string{"", "sheet", "expr", "ref"}},
	}, "image_id")},
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
			"instructions":    "漫剧工坊。外部编剧（如 Antigravity 里的 Gemini）：先调用 get_writing_context，用 view_asset_images 看角色定妆，再用 save_bible / save_synopsis / save_script / upsert_asset / save_shots / save_episode_summary 交回文字，全部用简体中文；一键成集 mode=writer 时写入后自动继续。不要在用户没同意时调用 colab_start（会消耗 Colab 算力单元）。返回音频的工具都会在文字里给出每段的文件名、时长、文字和 2 小时有效的免密钥下载地址：宿主没把音频内容块交给你时，下载到工作区再 read_file 听（批量用 export_voice_candidates）。作品是连载剧集（设定集 + 多集，资产库跨集共享）。可以新建一集并一键成集（autopilot_start，异步，用 autopilot_status 轮询，mode=ask 时用 autopilot_continue 确认）、生成配音、合成语音；也能直接管理 Colab 运行时、执行 Python、提交 MiniMax H3 图生视频（video_submit 后用 video_status 轮询）、调用生图渠道。",
		})
	case "ping":
		return ok(map[string]any{})
	case "tools/list":
		return ok(map[string]any{"tools": append(append(append(append(append(append(append([]mcpTool{}, writerTools...), moreTools...), libTools...), vcTools...), linkTools...), opsTools...), mcpTools...)})
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
			v, err = callMoreTool(ctx, p.Name, p.Arguments, base, pv != "2024-11-05")
		}
		if errors.Is(err, errUnknownTool) {
			v, err = callLibTool(ctx, p.Name, p.Arguments, base, pv != "2024-11-05")
		}
		if errors.Is(err, errUnknownTool) {
			v, err = callVCTool(ctx, p.Name, p.Arguments, base)
		}
		if errors.Is(err, errUnknownTool) {
			v, err = callLinkTool(ctx, p.Name, p.Arguments, base)
		}
		if errors.Is(err, errUnknownTool) {
			v, err = callOpsTool(ctx, p.Name, p.Arguments, base)
		}
		if errors.Is(err, errUnknownTool) {
			v, err = callTool(ctx, p.Name, p.Arguments, base)
		}
		if errors.Is(err, errUnknownTool) {
			return rpcError(q.ID, -32602, "没有这个工具："+p.Name)
		}
		if err != nil {
			return ok(map[string]any{"content": []any{map[string]any{"type": "text", "text": err.Error()}}, "isError": true})
		}
		v = withAudioLinks(v, base)
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
			EpisodeID json.Number `json:"episode_id"`
			Brief     bool        `json:"brief"`
		}
		_ = json.Unmarshal(args, &a)
		pid, _ := strconv.ParseInt(a.ProjectID.String(), 10, 64)
		eid, _ := strconv.ParseInt(a.EpisodeID.String(), 10, 64)
		return toolShots(ctx, pid, eid, a.Brief, base)
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
			PauseAt   []string    `json:"pause_at"`
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
		return toolAutopilot(ctx, strings.TrimPrefix(name, "autopilot_"), pipeArgs{EpisodeID: id, Mode: a.Mode, PauseAt: a.PauseAt})
	case "design_voice", "set_voice":
		var a voiceToolArgs
		_ = json.Unmarshal(args, &a)
		if name == "design_voice" {
			return toolDesignVoice(ctx, a, base)
		}
		return toolSetVoice(ctx, a)
	case "tts":
		var a ttsArgs
		_ = json.Unmarshal(args, &a)
		return toolTTS(ctx, a, base)
	case "list_image_channels":
		return toolImageChannels(ctx)
	case "list_voice_channels":
		return toolVoiceChannels(ctx)
	case "pick_image":
		var a struct {
			ImageID int64 `json:"image_id"`
		}
		_ = json.Unmarshal(args, &a)
		im, err := getImage(a.ImageID)
		if err != nil {
			return nil, errors.New("图片不存在")
		}
		if err := pickImage(im); err != nil {
			return nil, err
		}
		return map[string]any{"ok": true, "image_id": im.ID, "note": "已设为选用"}, nil
	case "upload_image":
		var a struct {
			UploadID string `json:"upload_id"`
			Image    string `json:"image"`
			AssetID  int64  `json:"asset_id"`
			ShotID   int64  `json:"shot_id"`
			Tag      string `json:"tag"`
		}
		_ = json.Unmarshal(args, &a)
		if a.AssetID > 0 && a.ShotID > 0 {
			return nil, errors.New("asset_id 和 shot_id 二选一")
		}
		var data []byte
		var err error
		if strings.TrimSpace(a.UploadID) != "" {
			data, _, err = readUploadBytes(a.UploadID)
		} else if strings.TrimSpace(a.Image) != "" {
			data, err = decodeB64Image(a.Image)
		} else {
			return nil, errors.New("需要 upload_id 或 image（base64 / data URL）")
		}
		if err != nil {
			return nil, err
		}
		if a.ShotID > 0 {
			s, err := getShot(a.ShotID)
			if err != nil {
				return nil, errors.New("镜头不存在")
			}
			im, err := storeImageX(s.ProjectID, s.EpisodeID, "shot", s.ID, "", data, "upload", "", 0, "")
			if err != nil {
				return nil, err
			}
			_, _ = db.Exec(`UPDATE shots SET picked_image_id=? WHERE id=?`, im.ID, s.ID)
			touchEpisode(s.EpisodeID)
			return map[string]any{"ok": true, "image_id": im.ID, "shot_id": s.ID, "code": s.Code, "saved_as": "镜头关键帧（已选用）"}, nil
		}
		im, err := storeImage(0, "ext", 0, data, "upload", "", 0, "")
		if err != nil {
			return nil, err
		}
		if a.AssetID == 0 {
			return map[string]any{"ok": true, "image_id": im.ID, "saved_to": "画室"}, nil
		}
		return toolAttachImage(ctx, attachArgs{ImageID: im.ID, AssetID: a.AssetID, Tag: a.Tag})
	case "attach_image":
		var a attachArgs
		if err := json.Unmarshal(args, &a); err != nil {
			return nil, err
		}
		return toolAttachImage(ctx, a)
	case "generate_asset_image":
		var a assetGenArgs
		if err := json.Unmarshal(args, &a); err != nil {
			return nil, err
		}
		return toolAssetGenerate(ctx, a, base)
	case "generate_keyframe":
		var a keyframeArgs
		if err := json.Unmarshal(args, &a); err != nil {
			return nil, err
		}
		return toolKeyframe(ctx, a, base)
	case "generate_missing_keyframes":
		var a missingKFArgs
		if err := json.Unmarshal(args, &a); err != nil {
			return nil, err
		}
		return toolGenerateMissingKeyframes(ctx, a)
	case "get_task_status":
		var a taskStatusArgs
		if err := json.Unmarshal(args, &a); err != nil {
			return nil, err
		}
		return toolTaskStatus(ctx, a)
	case "image_generate":
		var a imageArgs
		_ = json.Unmarshal(args, &a)
		return toolImageGenerate(ctx, a, base)
	}
	return nil, errUnknownTool
}

var _ = base64.StdEncoding
var _ = os.Remove

type voiceToolArgs struct {
	AssetID     int64      `json:"asset_id"`
	ProjectID   int64      `json:"project_id"`
	Description string     `json:"description"`
	SampleText  string     `json:"sample_text"`
	N           int        `json:"n"`
	ChannelID   int64      `json:"channel_id"`
	Sample      string     `json:"sample"`
	Voice       *VoiceSpec `json:"voice"`
	SynthCh     int64      `json:"synth_channel_id"`
}

func toolDesignVoice(ctx context.Context, a voiceToolArgs, base string) (any, error) {
	desc, text := strings.TrimSpace(a.Description), strings.TrimSpace(a.SampleText)
	pid := a.ProjectID
	if a.AssetID > 0 {
		as, err := getAsset(a.AssetID)
		if err != nil || as.Kind != "character" {
			return nil, errors.New("角色不存在")
		}
		pid = as.ProjectID
		if desc == "" {
			desc = strings.TrimSpace(as.Voice.Desc)
		}
		if text == "" {
			text = strings.TrimSpace(as.Voice.SampleText)
		}
	}
	if desc == "" {
		return nil, errors.New("需要 description（或先用 upsert_asset 写 voice_description）")
	}
	text = orDefault(text, "你好，很高兴认识你。接下来的故事，由我来讲给你听。")
	ch, err := designVoiceChannel(a.ChannelID)
	if err != nil {
		return nil, err
	}
	n := a.N
	if n <= 0 {
		n = 3
	}
	c, cancel := context.WithTimeout(ctx, 4*time.Minute)
	defer cancel()
	names, durs, err := designCandidates(c, ch, desc, text, clampInt(n, 1, 4))
	reqs := clampInt(n, 1, 4)
	if ch.APIType == "fish" {
		reqs = 1
	}
	ledgerAdd(pid, 0, "tts", reqs, 0, 0, 0, "")
	if err != nil {
		return nil, err
	}
	saveVoiceCands(pid, a.AssetID, ch, desc, text, names, durs, "mcp")
	out := []map[string]any{}
	for i, nm := range names {
		out = append(out, map[string]any{"sample": nm, "url": base + "/v1/audio/" + nm, "download_url": audioDLURL(base, nm), "duration": durs[i], "text": text})
	}
	res := map[string]any{"channel_id": ch.ID, "channel": ch.APIType, "description": desc, "sample_text": text, "candidates": out}
	if a.AssetID > 0 || pid > 0 {
		res["saved"] = "候选已按角色存下：界面音色面板「设计音色」里能看到、能选；get_audio_link（asset_id, which=candidates）或 export_voice_candidates 可以再取。"
	}
	return res, nil
}

func toolSetVoice(ctx context.Context, a voiceToolArgs) (any, error) {
	var as *Asset
	var p *Project
	if a.AssetID > 0 {
		x, err := getAsset(a.AssetID)
		if err != nil || x.Kind != "character" {
			return nil, errors.New("角色不存在")
		}
		as = x
	} else if a.ProjectID > 0 {
		x, err := getProject(a.ProjectID)
		if err != nil {
			return nil, errors.New("作品不存在")
		}
		p = x
	} else {
		return nil, errors.New("需要 asset_id 或 project_id")
	}
	var v VoiceSpec
	if a.Voice != nil {
		v = *a.Voice
	} else if strings.TrimSpace(a.Sample) == "" && a.SynthCh > 0 {
		// 只换配音渠道：沿用已选的设计 / 克隆样本
		if as != nil {
			v = as.Voice
		} else {
			v = p.NarratorVoice
		}
		if v.Sample == "" {
			return nil, errors.New("当前音色不是设计或克隆出来的样本，不能换配音渠道")
		}
	} else {
		a.Sample = strings.TrimSpace(a.Sample)
		if a.Sample == "" || strings.ContainsAny(a.Sample, "/\\") {
			return nil, errors.New("需要 design_voice 返回的 sample")
		}
		if !audioExists(a.Sample) {
			return nil, fmt.Errorf("样本 %s 不存在（用 design_voice 返回的 sample 原名，不是压缩包里的文件名）", a.Sample)
		}
		var oPid, oAid int64
		if as != nil {
			oPid, oAid = as.ProjectID, as.ID
		} else {
			oPid = p.ID
		}
		cand := findVoiceCand(oPid, oAid, a.Sample)
		chID := a.ChannelID
		if chID == 0 && cand != nil {
			chID = cand.ChannelID
		}
		ch, err := designVoiceChannel(chID)
		if err != nil && chID != a.ChannelID {
			ch, err = designVoiceChannel(a.ChannelID)
		}
		if err != nil {
			return nil, err
		}
		title := "旁白"
		desc := strings.TrimSpace(a.Description)
		if as != nil {
			title = as.Name + "的声音"
			if desc == "" {
				desc = as.Voice.Desc
			}
		}
		text := sampleText(a.Sample)
		if text == "" && cand != nil {
			text = cand.Text
		}
		if desc == "" && cand != nil {
			desc = cand.Description
		}
		c, cancel := context.WithTimeout(ctx, 4*time.Minute)
		defer cancel()
		v, err = designedVoiceSpec(c, ch, a.Sample, title, desc, text)
		if err != nil {
			return nil, err
		}
		if cand == nil && strings.HasPrefix(a.Sample, "vd-") {
			saveVoiceCands(oPid, oAid, ch, desc, text, []string{a.Sample}, nil, "mcp")
		}
	}
	if a.SynthCh > 0 && v.Sample != "" {
		sc, err := voiceChannel(a.SynthCh)
		if err != nil || (sc.APIType != "mimo" && sc.APIType != "fish") {
			return nil, errors.New("配音渠道需要是小米 MiMo 或 Fish Audio")
		}
		v.ChannelID = sc.ID
		if v.Mode == "fish" {
			v.Mode = "design"
		}
		if sc.APIType == "fish" {
			c, cancel := context.WithTimeout(ctx, 4*time.Minute)
			defer cancel()
			if _, err := fishModelFromSample(c, sc, v.Sample, v.Title); err != nil {
				return nil, err
			}
		}
		v.Voice = ""
	}
	if v.empty() {
		return nil, errors.New("音色不完整")
	}
	if as != nil {
		_, _ = db.Exec(`UPDATE assets SET voice=? WHERE id=?`, mustJSON(v), as.ID)
		touchProject(as.ProjectID)
		return map[string]any{"ok": true, "asset_id": as.ID, "voice": v}, nil
	}
	_, _ = db.Exec(`UPDATE projects SET narrator_voice=? WHERE id=?`, mustJSON(v), p.ID)
	touchProject(p.ID)
	return map[string]any{"ok": true, "project_id": p.ID, "narrator_voice": v}, nil
}
