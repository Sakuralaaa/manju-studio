package main

// 后半段 MCP 工具 + 单镜增删排镜（0.2.13）：
// 选片（pick_video）、取消/删视频（cancel_video）、整集排队（queue_episode_videos）、
// 合成成片（build_final）、封面（generate_cover）、配乐与音效（list_bgm / upload_bgm / set_episode_audio / set_shot_sfx）、
// 剪映草稿（export_capcut）、增删排镜（add_shot / delete_shot / reorder_shots）。

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"
)

var opsTools = []mcpTool{
	{"pick_video", "把一条已完成的视频抽卡候选设为该镜头的选用视频（成片、剪映草稿、首尾帧衔接都会用选中的那条，并自动解锁等待该镜头的首尾帧视频）。", obj(map[string]any{
		"video_id": num("视频编号"),
	}, "video_id")},
	{"cancel_video", "取消排队中 / 运行中 / 等待上一镜头的视频任务；对已完成或失败的视频记录则删除文件与记录（删除已完成/失败视频不可恢复，必须传 confirm=true，不传只预览）。", obj(map[string]any{
		"video_id": num("视频编号"), "confirm": boolp("删除已完成 / 失败视频时确认"),
	}, "video_id")},
	{"queue_episode_videos", "批量排队一集的视频任务（不自动开机，遵守不消耗未授权算力的约束）。mode=missing（默认）只排缺视频的镜头；mode=all 全部重排。首尾帧镜头在上一镜头未完成时自动排为 waiting_prev，上一镜头完成后自动补首帧开跑。", obj(map[string]any{
		"episode_id": num("集编号"), "mode": enumS("missing = 只排缺视频的镜头（默认）；all = 全部镜头重排", "missing", "all"),
	}, "episode_id")},
	{"build_final", "触发一集的成片合成任务（拼接所有镜头视频、混入配音/音效/配乐、烧录字幕），异步返回 task_id；用 get_task_status 查进度，完成后用 get_video_link（episode_id）下载成片。", obj(map[string]any{
		"episode_id": num("集编号"), "subtitles": boolp("是否烧录字幕（可选，不填沿用本集当前设置）"),
	}, "episode_id")},
	{"generate_cover", "为一集生成海报封面（自动带主角和本集首场景作参考）。默认异步返回 task_id（wait=true 时等待出完并返回封面图片编号）。", obj(map[string]any{
		"episode_id": num("集编号"), "title": str("封面主标题（可选，不填用作品标题）"), "wait": boolp("是否等出完再返回，默认 false"),
	}, "episode_id")},
	{"list_bgm", "列出配乐库里已上传的全部配乐（编号、名称、时长、大小）。", obj(map[string]any{})},
	{"upload_bgm", "上传一首配乐到配乐库（支持 POST /v1/uploads 拿到的 upload_id，或 audio_url / audio_base64），统一转 mp3（最长 900 秒），返回 bgm_id 供 set_episode_audio 使用。", obj(map[string]any{
		"upload_id": str("POST /v1/uploads 返回的临时文件编号（推荐）"), "audio_url": str("音频地址"), "audio_base64": str("音频 base64 或 data URL"),
		"name": str("配乐名称（可选）"),
	})},
	{"set_episode_audio", "设置一集的配乐、音量、淡入淡出、对白自动压低配乐（ducking）与字幕烧录开关；bgm_id=0 表示取消配乐。", obj(map[string]any{
		"episode_id": num("集编号"), "bgm_id": num("配乐编号（list_bgm），0 = 不使用配乐"),
		"bgm_volume": numF("配乐音量 0–1"), "bgm_fade_in": numF("淡入秒数 0–15"), "bgm_fade_out": numF("淡出秒数 0–15"),
		"bgm_duck": boolp("对白时自动压低配乐"), "subtitles": boolp("成片是否烧录字幕"),
	}, "episode_id")},
	{"set_shot_sfx", "给单个镜头设置或清除音效（最长 30 秒，支持 upload_id / audio_url / audio_base64）及音量。", obj(map[string]any{
		"shot_id": num("镜头编号"), "upload_id": str("POST /v1/uploads 返回的临时文件编号"), "audio_url": str("音效地址"), "audio_base64": str("音效 base64 或 data URL"),
		"name": str("音效名称（可选）"), "sfx_volume": numF("音效音量 0–2"), "clear": boolp("true = 清除该镜头音效"),
	}, "shot_id")},
	{"export_capcut", "导出本集的剪映草稿压缩包（draft_content.json + draft_meta_info.json + materials/），返回 2 小时有效的免密钥下载地址。", obj(map[string]any{
		"episode_id": num("集编号"), "draft_dir": str("本机剪映草稿目录（可选，留空用剪映占位前缀）"),
	}, "episode_id")},
	{"add_shot", "在分镜表里插入一个新镜头，不改变其他镜头的编号、关键帧和视频。不填 after_shot_id 时追加到末尾；after_shot_id=0 表示插到最前面。", obj(map[string]any{
		"episode_id": num("集编号"), "after_shot_id": num("插在这个镜头之后（不填 = 追加到末尾；0 = 插到第 1 镜之前）"),
		"duration": num("时长 5–10 秒"), "camera": str("景别 + 机位/运镜"), "picture": str("画面：人物动作、表情、环境变化"),
		"dialogue": map[string]any{"description": "台词或旁白：文本或 [{speaker, text, emotion}]", "oneOf": []any{map[string]any{"type": "string"}, arr("", obj(map[string]any{
			"speaker": str("角色名；旁白写「旁白」"), "text": str("台词"), "emotion": str("语气（可选）"),
		}, "speaker", "text"))}},
		"assets":          arr("出现的资产编号或名称", map[string]any{"type": "string"}),
		"transition":      enumS("衔接", "cut", "continuous", "first_last"),
		"keyframe_prompt": str("关键帧中文生图提示词"), "video_prompt": str("视频描述"),
	}, "episode_id", "duration", "camera", "picture", "keyframe_prompt", "video_prompt")},
	{"delete_shot", "删除单个镜头（保留同集其他镜头的关键帧与视频）；会删掉该镜头的关键帧、取消排队视频并清理该镜配音文件。不可恢复：必须传 confirm=true，不传只预览。", obj(map[string]any{
		"shot_id": num("镜头编号"), "confirm": boolp("确认删除"),
	}, "shot_id")},
	{"reorder_shots", "按给定的 shot_ids 顺序重排一集的分镜，所有镜头的关键帧、视频和配音全部保留不脱钩。", obj(map[string]any{
		"episode_id": num("集编号"), "shot_ids": map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "description": "该集全部镜头编号的新顺序（必须恰好包含本集全部镜头）"},
	}, "episode_id", "shot_ids")},
}

func callOpsTool(ctx context.Context, name string, args json.RawMessage, base string) (any, error) {
	switch name {
	case "pick_video":
		return toolPickVideo(ctx, args)
	case "cancel_video":
		return toolCancelVideo(ctx, args)
	case "queue_episode_videos":
		return toolQueueEpisodeVideos(ctx, args)
	case "build_final":
		return toolBuildFinal(ctx, args)
	case "generate_cover":
		return toolGenerateCover(ctx, args, base)
	case "list_bgm":
		return map[string]any{"items": listBgm(`ORDER BY id DESC`)}, nil
	case "upload_bgm":
		return toolUploadBgm(ctx, args)
	case "set_episode_audio":
		return toolSetEpisodeAudio(ctx, args)
	case "set_shot_sfx":
		return toolSetShotSfx(ctx, args)
	case "export_capcut":
		return toolExportCapcut(ctx, args, base)
	case "add_shot":
		return toolAddShot(ctx, args)
	case "delete_shot":
		return toolDeleteShot(ctx, args)
	case "reorder_shots":
		return toolReorderShots(ctx, args)
	}
	return nil, errUnknownTool
}

func toolPickVideo(ctx context.Context, raw json.RawMessage) (any, error) {
	var a struct {
		VideoID int64 `json:"video_id"`
	}
	if err := strictArgs(raw, &a); err != nil {
		return nil, err
	}
	v, err := getVideo(a.VideoID)
	if err != nil || v.Status != "done" {
		return nil, errors.New("视频不存在或还没有生成完成")
	}
	if v.ShotID <= 0 {
		return nil, errors.New("这条视频不属于任何镜头")
	}
	_, _ = db.Exec(`UPDATE shots SET picked_video_id=? WHERE id=?`, v.ID, v.ShotID)
	unlocked := unlockWaitingPrev(v.EpisodeID)
	touchEpisode(v.EpisodeID)
	return map[string]any{"ok": true, "video_id": v.ID, "shot_id": v.ShotID, "unlocked_waiting_prev": unlocked}, nil
}

func toolCancelVideo(ctx context.Context, raw json.RawMessage) (any, error) {
	var a struct {
		VideoID int64 `json:"video_id"`
		Confirm bool  `json:"confirm"`
	}
	if err := strictArgs(raw, &a); err != nil {
		return nil, err
	}
	v, err := getVideo(a.VideoID)
	if err != nil {
		return nil, errors.New("视频不存在")
	}
	deleting := v.Status != "queued" && v.Status != "running" && v.Status != "waiting_prev"
	if deleting && !a.Confirm {
		return map[string]any{
			"ok": false, "video_id": v.ID, "shot_id": v.ShotID, "status": v.Status,
			"note": fmt.Sprintf("还没删除。视频 #%d 当前状态为 %s，继续将永久删除视频文件与记录；确认请传 confirm=true", v.ID, v.Status),
		}, nil
	}
	if err := cancelVideo(v.ID); err != nil {
		return nil, err
	}
	action := "已取消"
	if deleting {
		action = "已删除"
	}
	return map[string]any{"ok": true, "video_id": v.ID, "action": action}, nil
}

func toolQueueEpisodeVideos(ctx context.Context, raw json.RawMessage) (any, error) {
	var a struct {
		EpisodeID int64  `json:"episode_id"`
		Mode      string `json:"mode"`
	}
	if err := strictArgs(raw, &a); err != nil {
		return nil, err
	}
	p, e, err := epCtx(a.EpisodeID)
	if err != nil {
		return nil, err
	}
	if a.Mode != "" && a.Mode != "missing" && a.Mode != "all" {
		return nil, errors.New("mode 只能是 missing 或 all")
	}
	n, skipped, lastErr := queueEpisode(p, e, a.Mode)
	if n == 0 {
		switch {
		case lastErr != nil:
			return nil, lastErr
		case skipped > 0:
			return nil, fmt.Errorf("有 %d 个镜头还没有关键帧", skipped)
		default:
			return nil, errors.New("没有需要生成的镜头")
		}
	}
	var waiting int
	_ = db.QueryRow(`SELECT COUNT(*) FROM videos WHERE episode_id=? AND status='waiting_prev'`, e.ID).Scan(&waiting)
	touchEpisode(e.ID)
	return map[string]any{
		"ok": true, "episode_id": e.ID, "queued": n, "waiting_prev": waiting, "skipped": skipped,
		"estimate": estimateCU(n, p.VideoTurbo),
		"note":     "视频已排队（未自动开机；开机需征得用户同意后调用 colab_start）。首尾帧镜头在上一镜完成后会自动解锁开跑。",
	}, nil
}

func toolBuildFinal(ctx context.Context, raw json.RawMessage) (any, error) {
	var a struct {
		EpisodeID int64 `json:"episode_id"`
		Subtitles *bool `json:"subtitles"`
	}
	if err := strictArgs(raw, &a); err != nil {
		return nil, err
	}
	p, e, err := epCtx(a.EpisodeID)
	if err != nil {
		return nil, err
	}
	if a.Subtitles != nil {
		_, _ = db.Exec(`UPDATE episodes SET subtitles=? WHERE id=?`, b2i(*a.Subtitles), e.ID)
		e.Subtitles = *a.Subtitles
	}
	tid, err := finalTask(p, e)
	if err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "episode_id": e.ID, "task_id": tid, "note": "成片合成已启动；用 get_task_status 查进度，完成后用 get_video_link（episode_id）取下载链接。"}, nil
}

func toolGenerateCover(ctx context.Context, raw json.RawMessage, base string) (any, error) {
	var a struct {
		EpisodeID int64  `json:"episode_id"`
		Title     string `json:"title"`
		Wait      bool   `json:"wait"`
	}
	if err := strictArgs(raw, &a); err != nil {
		return nil, err
	}
	p, e, err := epCtx(a.EpisodeID)
	if err != nil {
		return nil, err
	}
	before := map[int64]bool{}
	for _, im := range listImages(`WHERE owner_type='cover' AND owner_id=?`, e.ID) {
		before[im.ID] = true
	}
	tid, err := coverTask(p, e, a.Title)
	if err != nil {
		return nil, err
	}
	if !a.Wait {
		return map[string]any{"ok": true, "episode_id": e.ID, "task_id": tid, "status": "running", "note": "封面生成已启动；用 get_task_status 查进度，完成后用 view_image 查看。"}, nil
	}
	ims, err := waitImages(ctx, tid, "cover", e.ID, before, base)
	if err != nil {
		return nil, err
	}
	if ims == nil {
		return map[string]any{"ok": true, "episode_id": e.ID, "task_id": tid, "note": "封面还在生成，请用 get_task_status 查询"}, nil
	}
	return map[string]any{"ok": true, "episode_id": e.ID, "images": ims}, nil
}

// resolveAudioInput 支持 upload_id / audio_url / audio_base64 三选一，返回本地临时文件路径、默认名称及清理函数
func resolveAudioInput(ctx context.Context, uploadID, audioURL, audioB64 string) (path, defName string, cleanup func(), err error) {
	cleanup = func() {}
	n := 0
	for _, ok := range []bool{strings.TrimSpace(uploadID) != "", strings.TrimSpace(audioURL) != "", strings.TrimSpace(audioB64) != ""} {
		if ok {
			n++
		}
	}
	if n != 1 {
		return "", "", cleanup, errors.New("音频来源三选一：upload_id、audio_url 或 audio_base64")
	}
	if strings.TrimSpace(uploadID) != "" {
		p, meta, err := resolveUpload(uploadID)
		if err != nil {
			return "", "", cleanup, err
		}
		return p, strings.TrimSuffix(meta.Filename, filepath.Ext(meta.Filename)), cleanup, nil
	}
	var data []byte
	if strings.TrimSpace(audioB64) != "" {
		s := strings.TrimSpace(audioB64)
		if i := strings.Index(s, ","); strings.HasPrefix(s, "data:") && i > 0 {
			s = s[i+1:]
		}
		data, err = base64.StdEncoding.DecodeString(strings.Join(strings.Fields(s), ""))
		if err != nil {
			return "", "", cleanup, errors.New("audio_base64 不是有效的 base64")
		}
		defName = "上传音频"
	} else {
		data, err = fetchAudioURL(ctx, strings.TrimSpace(audioURL))
		if err != nil {
			return "", "", cleanup, err
		}
		defName = "网络音频"
	}
	tmp, err := os.MkdirTemp(filepath.Join(dataDir, "tmp"), "audin-")
	if err != nil {
		return "", "", cleanup, err
	}
	in := filepath.Join(tmp, "in")
	if err := os.WriteFile(in, data, 0o644); err != nil {
		_ = os.RemoveAll(tmp)
		return "", "", cleanup, err
	}
	return in, defName, func() { _ = os.RemoveAll(tmp) }, nil
}

func toolUploadBgm(ctx context.Context, raw json.RawMessage) (any, error) {
	var a struct {
		UploadID string `json:"upload_id"`
		AudioURL string `json:"audio_url"`
		AudioB64 string `json:"audio_base64"`
		Name     string `json:"name"`
	}
	if err := strictArgs(raw, &a); err != nil {
		return nil, err
	}
	in, defName, cleanup, err := resolveAudioInput(ctx, a.UploadID, a.AudioURL, a.AudioB64)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	file, dur, err := storeAudioFromPath(in, "bgm", 900)
	if err != nil {
		return nil, err
	}
	name := orDefault(strings.TrimSpace(a.Name), orDefault(defName, "配乐"))
	var size int64
	if fi, err := os.Stat(filepath.Join(dataDir, "audio", file)); err == nil {
		size = fi.Size()
	}
	res, err := db.Exec(`INSERT INTO bgm(name,file,duration,bytes,created_at) VALUES(?,?,?,?,?)`, name, file, dur, size, now())
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	b, _ := getBgm(id)
	return map[string]any{"ok": true, "bgm_id": id, "bgm": b}, nil
}

func toolSetEpisodeAudio(ctx context.Context, raw json.RawMessage) (any, error) {
	var a struct {
		EpisodeID  int64    `json:"episode_id"`
		BgmID      *int64   `json:"bgm_id"`
		BgmVolume  *float64 `json:"bgm_volume"`
		BgmFadeIn  *float64 `json:"bgm_fade_in"`
		BgmFadeOut *float64 `json:"bgm_fade_out"`
		BgmDuck    *bool    `json:"bgm_duck"`
		Subtitles  *bool    `json:"subtitles"`
	}
	if err := strictArgs(raw, &a); err != nil {
		return nil, err
	}
	_, e, err := epCtx(a.EpisodeID)
	if err != nil {
		return nil, err
	}
	if a.BgmID != nil {
		if *a.BgmID > 0 {
			if _, err := getBgm(*a.BgmID); err != nil {
				return nil, err
			}
		}
		_, _ = db.Exec(`UPDATE episodes SET bgm_id=? WHERE id=?`, *a.BgmID, e.ID)
	}
	if a.BgmVolume != nil {
		_, _ = db.Exec(`UPDATE episodes SET bgm_volume=? WHERE id=?`, clampF(*a.BgmVolume, 0, 1), e.ID)
	}
	if a.BgmFadeIn != nil {
		_, _ = db.Exec(`UPDATE episodes SET bgm_fade_in=? WHERE id=?`, clampF(*a.BgmFadeIn, 0, 15), e.ID)
	}
	if a.BgmFadeOut != nil {
		_, _ = db.Exec(`UPDATE episodes SET bgm_fade_out=? WHERE id=?`, clampF(*a.BgmFadeOut, 0, 15), e.ID)
	}
	if a.BgmDuck != nil {
		_, _ = db.Exec(`UPDATE episodes SET bgm_duck=? WHERE id=?`, b2i(*a.BgmDuck), e.ID)
	}
	if a.Subtitles != nil {
		_, _ = db.Exec(`UPDATE episodes SET subtitles=? WHERE id=?`, b2i(*a.Subtitles), e.ID)
	}
	touchEpisode(e.ID)
	e, _ = getEpisode(e.ID)
	return map[string]any{
		"ok": true, "episode_id": e.ID, "bgm_id": e.BgmID, "bgm_volume": e.BgmVolume,
		"bgm_fade_in": e.BgmFadeIn, "bgm_fade_out": e.BgmFadeOut, "bgm_duck": e.BgmDuck, "subtitles": e.Subtitles,
	}, nil
}

func toolSetShotSfx(ctx context.Context, raw json.RawMessage) (any, error) {
	var a struct {
		ShotID    int64    `json:"shot_id"`
		UploadID  string   `json:"upload_id"`
		AudioURL  string   `json:"audio_url"`
		AudioB64  string   `json:"audio_base64"`
		Name      string   `json:"name"`
		SfxVolume *float64 `json:"sfx_volume"`
		Clear     bool     `json:"clear"`
	}
	if err := strictArgs(raw, &a); err != nil {
		return nil, err
	}
	s, err := getShot(a.ShotID)
	if err != nil {
		return nil, errors.New("镜头不存在")
	}
	if a.Clear {
		if s.SfxFile != "" {
			_ = os.Remove(filepath.Join(dataDir, "audio", s.SfxFile))
		}
		_, _ = db.Exec(`UPDATE shots SET sfx_file='', sfx_name='' WHERE id=?`, s.ID)
		touchEpisode(s.EpisodeID)
		return map[string]any{"ok": true, "shot_id": s.ID, "cleared": true}, nil
	}
	hasAudio := strings.TrimSpace(a.UploadID) != "" || strings.TrimSpace(a.AudioURL) != "" || strings.TrimSpace(a.AudioB64) != ""
	if hasAudio {
		in, defName, cleanup, err := resolveAudioInput(ctx, a.UploadID, a.AudioURL, a.AudioB64)
		if err != nil {
			return nil, err
		}
		defer cleanup()
		file, _, err := storeAudioFromPath(in, "sfx", 30)
		if err != nil {
			return nil, err
		}
		if s.SfxFile != "" {
			_ = os.Remove(filepath.Join(dataDir, "audio", s.SfxFile))
		}
		name := orDefault(strings.TrimSpace(a.Name), orDefault(defName, "音效"))
		_, _ = db.Exec(`UPDATE shots SET sfx_file=?, sfx_name=? WHERE id=?`, file, name, s.ID)
	}
	if a.SfxVolume != nil {
		_, _ = db.Exec(`UPDATE shots SET sfx_volume=? WHERE id=?`, clampF(*a.SfxVolume, 0, 2), s.ID)
	}
	if !hasAudio && a.SfxVolume == nil {
		return nil, errors.New("请提供音效（upload_id / audio_url / audio_base64）、sfx_volume 或 clear=true")
	}
	touchEpisode(s.EpisodeID)
	s, _ = getShot(s.ID)
	return map[string]any{"ok": true, "shot_id": s.ID, "sfx_name": s.SfxName, "sfx_volume": s.SfxVolume}, nil
}

func toolExportCapcut(ctx context.Context, raw json.RawMessage, base string) (any, error) {
	var a struct {
		EpisodeID int64  `json:"episode_id"`
		DraftDir  string `json:"draft_dir"`
	}
	if err := strictArgs(raw, &a); err != nil {
		return nil, err
	}
	p, e, err := epCtx(a.EpisodeID)
	if err != nil {
		return nil, err
	}
	tl, err := buildTimeline(p, e)
	if err != nil {
		return nil, err
	}
	cleanExports()
	dir := filepath.Join(dataDir, "exports")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	draftName := safeName(fmt.Sprintf("%s_第%d集", p.Title, e.No))
	zipName := fmt.Sprintf("manju_capcut_p%d_e%d_%s.zip", p.ID, e.No, time.Now().Format("20060102_150405"))
	dst := filepath.Join(dir, zipName)
	f, err := os.Create(dst)
	if err != nil {
		return nil, err
	}
	root := strings.TrimRight(strings.ReplaceAll(strings.TrimSpace(a.DraftDir), `\`, "/"), "/")
	err = writeCapcutZip(f, tl, draftName, root)
	f.Close()
	if err != nil {
		_ = os.Remove(dst)
		return nil, err
	}
	var size int64
	if fi, err := os.Stat(dst); err == nil {
		size = fi.Size()
	}
	u := base + "/v1/dl/" + signDLName("z", zipName, audioLinkTTL)
	return map[string]any{
		"ok": true, "episode_id": e.ID, "filename": zipName, "bytes": size, "url": u, "expires_in_sec": int(audioLinkTTL.Seconds()),
		"how_to_download": fmt.Sprintf("curl -fL -o %s \"%s\"", zipName, u),
	}, nil
}

func toolAddShot(ctx context.Context, raw json.RawMessage) (any, error) {
	var a struct {
		EpisodeID   int64  `json:"episode_id"`
		AfterShotID *int64 `json:"after_shot_id"`
		shotIn
	}
	if err := strictArgs(raw, &a); err != nil {
		return nil, err
	}
	p, e, err := epCtx(a.EpisodeID)
	if err != nil {
		return nil, err
	}
	act := strings.TrimSpace(orDefault(a.Picture, a.Action))
	if act == "" {
		return nil, errors.New("缺 picture（画面里发生什么）")
	}
	if strings.TrimSpace(a.Camera) == "" {
		return nil, errors.New("缺 camera")
	}
	kp := strings.TrimSpace(a.KeyframePrompt)
	if runeLen(kp) < 8 {
		return nil, errors.New("keyframe_prompt 太短")
	}
	vp := strings.TrimSpace(a.VideoPrompt)
	if vp == "" {
		return nil, errors.New("缺 video_prompt")
	}
	d := math.Round(a.Duration)
	if d == 0 {
		d = 5
	}
	if d < 5 || d > 10 {
		return nil, fmt.Errorf("duration=%v 不行：视频模型每段只能 5–10 秒", a.Duration)
	}
	dia, err := renderDialogue(a.Dialogue, 1)
	if err != nil {
		return nil, err
	}
	assets := listAssets(p.ID)
	codes, err := resolveShotAssets(assets, a.Assets)
	if err != nil {
		return nil, err
	}
	chain, ok := transitionChain[strings.TrimSpace(a.Transition)]
	if !ok {
		return nil, errors.New("transition 只能是 cut / continuous / first_last")
	}
	shots := listShots(e.ID, false)
	sortPos := len(shots) + 1
	if a.AfterShotID != nil {
		if *a.AfterShotID == 0 {
			sortPos = 1
		} else {
			found := false
			for _, s := range shots {
				if s.ID == *a.AfterShotID {
					sortPos = s.Sort + 1
					found = true
					break
				}
			}
			if !found {
				return nil, errors.New("after_shot_id 不属于这一集")
			}
		}
		_, _ = db.Exec(`UPDATE shots SET sort=sort+1 WHERE episode_id=? AND sort>=?`, e.ID, sortPos)
	}
	if sortPos == 1 && chain != "off" {
		chain = "off"
	}
	res, err := db.Exec(`INSERT INTO shots(project_id,episode_id,sort,duration,camera,action,dialogue,asset_codes,keyframe_prompt,video_prompt,chain,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
		p.ID, e.ID, sortPos, d, strings.TrimSpace(a.Camera), act, dia, mustJSON(codes), kp, vp, chain, now())
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	renumberShots(e.ID)
	syncLines(p, e)
	s, _ := getShot(id)
	return map[string]any{"ok": true, "shot_id": s.ID, "code": s.Code, "episode_id": e.ID, "assets": s.Assets, "chain": s.Chain}, nil
}

func toolDeleteShot(ctx context.Context, raw json.RawMessage) (any, error) {
	var a struct {
		ShotID  int64 `json:"shot_id"`
		Confirm bool  `json:"confirm"`
	}
	if err := strictArgs(raw, &a); err != nil {
		return nil, err
	}
	s, err := getShot(a.ShotID)
	if err != nil {
		return nil, errors.New("镜头不存在")
	}
	ims := listImages(`WHERE owner_type='shot' AND owner_id=?`, s.ID)
	vids := listVideos(`WHERE shot_id=?`, s.ID)
	var lineN int
	_ = db.QueryRow(`SELECT COUNT(*) FROM lines WHERE shot_id=?`, s.ID).Scan(&lineN)
	plan := map[string]any{"shot_id": s.ID, "episode_id": s.EpisodeID, "code": s.Code, "keyframes": len(ims), "videos": len(vids), "lines": lineN}
	if !a.Confirm {
		plan["ok"] = false
		plan["note"] = fmt.Sprintf("还没删除。会删掉镜头 %s（含 %d 张关键帧、%d 条视频记录、%d 条台词配音），同集其他镜头不受影响；确认请传 confirm=true", s.Code, len(ims), len(vids), lineN)
		return plan, nil
	}
	for _, im := range ims {
		deleteImageRow(im)
	}
	for _, v := range vids {
		if v.Status == "queued" || v.Status == "waiting_prev" || v.Status == "running" {
			_ = cancelVideo(v.ID)
		} else {
			if v.File != "" {
				_ = os.Remove(filepath.Join(dataDir, "videos", v.File))
			}
			if v.VcFile != "" {
				_ = os.Remove(filepath.Join(dataDir, "videos", v.VcFile))
			}
			_, _ = db.Exec(`DELETE FROM videos WHERE id=?`, v.ID)
		}
	}
	if s.SfxFile != "" {
		_ = os.Remove(filepath.Join(dataDir, "audio", s.SfxFile))
	}
	cleanLineAudioFiles(`WHERE shot_id=?`, s.ID)
	_, _ = db.Exec(`DELETE FROM lines WHERE shot_id=?`, s.ID)
	_, _ = db.Exec(`DELETE FROM shots WHERE id=?`, s.ID)
	renumberShots(s.EpisodeID)
	plan["ok"] = true
	plan["note"] = "已删除"
	return plan, nil
}

func toolReorderShots(ctx context.Context, raw json.RawMessage) (any, error) {
	var a struct {
		EpisodeID int64   `json:"episode_id"`
		ShotIDs   []int64 `json:"shot_ids"`
	}
	if err := strictArgs(raw, &a); err != nil {
		return nil, err
	}
	_, e, err := epCtx(a.EpisodeID)
	if err != nil {
		return nil, err
	}
	cur := listShots(e.ID, false)
	if len(cur) == 0 {
		return nil, errors.New("这一集还没有分镜")
	}
	if len(a.ShotIDs) != len(cur) {
		return nil, fmt.Errorf("shot_ids 数量不对：本集共 %d 个镜头，传了 %d 个", len(cur), len(a.ShotIDs))
	}
	valid := map[int64]bool{}
	for _, s := range cur {
		valid[s.ID] = true
	}
	seen := map[int64]bool{}
	for _, id := range a.ShotIDs {
		if !valid[id] || seen[id] {
			return nil, fmt.Errorf("shot_ids 必须恰好包含本集全部镜头且不能重复（异常编号 %d）", id)
		}
		seen[id] = true
	}
	for i, id := range a.ShotIDs {
		_, _ = db.Exec(`UPDATE shots SET sort=? WHERE id=? AND episode_id=?`, i+1, id, e.ID)
	}
	// 新第 1 镜没有上一镜，若原本设了衔接则改回 cut
	_, _ = db.Exec(`UPDATE shots SET chain='off' WHERE id=? AND chain<>'off'`, a.ShotIDs[0])
	touchEpisode(e.ID)
	order := []map[string]any{}
	for _, s := range listShots(e.ID, false) {
		order = append(order, map[string]any{"shot_id": s.ID, "code": s.Code, "chain": s.Chain})
	}
	return map[string]any{"ok": true, "episode_id": e.ID, "shots": order}, nil
}
