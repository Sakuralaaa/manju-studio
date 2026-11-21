package main

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"mime/multipart"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func setupTestEnv(t *testing.T) func() {
	t.Helper()
	dir := t.TempDir()
	oldDir := dataDir
	oldDB := db
	dataDir = dir
	for _, d := range []string{"images", "videos", "audio", "exports", "tmp"} {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := openDB(dir); err != nil {
		t.Fatal(err)
	}
	return func() {
		if db != nil {
			_ = db.Close()
		}
		dataDir = oldDir
		db = oldDB
	}
}

func makePNGBytes(w, h int, c color.RGBA) []byte {
	im := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			im.SetRGBA(x, y, c)
		}
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, im)
	return buf.Bytes()
}

func TestUploadsAndTaskStatus(t *testing.T) {
	defer setupTestEnv(t)()

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, err := mw.CreateFormFile("file", "sample.png")
	if err != nil {
		t.Fatal(err)
	}
	pngData := makePNGBytes(32, 32, color.RGBA{200, 100, 50, 255})
	_, _ = fw.Write(pngData)
	_ = mw.Close()

	req := httptest.NewRequest("POST", "/v1/uploads", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()
	extUpload(rec, req)
	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var res struct {
		OK       bool   `json:"ok"`
		UploadID string `json:"upload_id"`
		Filename string `json:"filename"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil || !res.OK || !validUploadID(res.UploadID) {
		t.Fatalf("unexpected upload response: %s", rec.Body.String())
	}
	got, meta, err := readUploadBytes(res.UploadID)
	if err != nil || !bytes.Equal(got, pngData) || meta.Filename != "sample.png" {
		t.Fatalf("readUploadBytes mismatch: err=%v meta=%+v", err, meta)
	}

	// upload_image 支持 upload_id
	out, err := callTool(context.Background(), "upload_image", json.RawMessage(`{"upload_id":"`+res.UploadID+`"}`), "http://test")
	if err != nil {
		t.Fatalf("upload_image via upload_id failed: %v", err)
	}
	m := out.(map[string]any)
	if m["ok"] != true || m["image_id"].(int64) <= 0 {
		t.Fatalf("unexpected upload_image out: %+v", m)
	}

	// get_task_status 查任务结果
	r, err := db.Exec(`INSERT INTO tasks(project_id,episode_id,kind,owner_type,owner_id,status,result,created_at,finished_at) VALUES(1,1,'image','shot',9,'done','{"image_ids":[101]}',?,?)`, now(), now())
	if err != nil {
		t.Fatal(err)
	}
	tid, _ := r.LastInsertId()
	tsOut, err := toolTaskStatus(context.Background(), taskStatusArgs{TaskIDs: []int64{tid, 99999}})
	if err != nil {
		t.Fatal(err)
	}
	tm := tsOut.(map[string]any)
	if tm["done"] != 1 || tm["all_done"] != true {
		t.Fatalf("unexpected task status: %+v", tm)
	}
}

func TestConsistencyLookJSONAndClauseExtraction(t *testing.T) {
	defer setupTestEnv(t)()

	res, err := db.Exec(`INSERT INTO projects(title,style,created_at,updated_at) VALUES('测试剧','电影感',?,?)`, now(), now())
	if err != nil {
		t.Fatal(err)
	}
	pid, _ := res.LastInsertId()

	// 1) 结构化 look_json：不拼接重复到 description、不输出否定词、不拼出「戴不戴帽子」
	raw := json.RawMessage(`{
		"project_id": ` + jsonInt(pid) + `,
		"kind": "character",
		"name": "阿策",
		"description": "40出头阳光摇滚型男，爱笑爱张罗，眼神深处藏着防备心，发誓要守住秘密。",
		"look": {
			"age": "视觉年龄32岁",
			"gender": "男",
			"hair": "绝对不戴帽子，蓬松高层次黑褐色碎短发",
			"face": "剑眉星目，高挺鼻梁，清晰下颌线",
			"body": "宽肩窄腰挺拔身材",
			"outfit": "黑色修身机车皮夹克内搭纯白T恤，黑色长裤",
			"accessories": "银色吊坠项链、金属手环（绝对不戴任何帽子）",
			"avoid": ["帽子", "手持物"]
		}
	}`)
	out, err := toolUpsertAsset(context.Background(), raw)
	if err != nil {
		t.Fatalf("toolUpsertAsset failed: %v", err)
	}
	m := out.(map[string]any)
	fixed := m["fixed_look"].(string)
	for _, bad := range []string{"不戴帽子", "戴不戴", "防备心", "发誓", "爱笑"} {
		if strings.Contains(fixed, bad) {
			t.Fatalf("fixed_look should not contain %q, got: %s", bad, fixed)
		}
	}
	for _, want := range []string{"蓬松高层次黑褐色碎短发", "黑色修身机车皮夹克", "银色吊坠项链"} {
		if !strings.Contains(fixed, want) {
			t.Fatalf("fixed_look missing %q, got: %s", want, fixed)
		}
	}
	// description 不应被双写追加
	if strings.Contains(m["description"].(string), "银色吊坠项链") {
		t.Fatalf("description should not duplicate look_json content: %s", m["description"])
	}
	a, _ := getAsset(m["asset_id"].(int64))
	if len(a.Look.Avoid) != 2 || a.Look.Avoid[0] != "帽子" {
		t.Fatalf("avoid not persisted: %+v", a.Look.Avoid)
	}

	// 2) 老资产（无 look_json）回退抽句：按小句切分，过滤性格句与否定句，绝不拦腰截断半个词
	legacy := &Asset{
		Kind: "character",
		Name: "旧角色",
		Description: "绝对不戴任何帽子，一头蓬松利落的黑色碎短发，眼神总是带着倦意，发誓要查清真相，" +
			"穿炭灰色高领毛衣配黑色羊绒大衣，左耳戴一枚极简银色耳钉（双手自然空着，不拿任何手持物品）。",
	}
	gotLegacy := charLook(&Project{}, legacy)
	for _, bad := range []string{"不戴", "倦意", "发誓", "空着", "手持"} {
		if strings.Contains(gotLegacy, bad) {
			t.Fatalf("legacy charLook leaked %q: %s", bad, gotLegacy)
		}
	}
	if !strings.Contains(gotLegacy, "黑色碎短发") || !strings.Contains(gotLegacy, "炭灰色高领毛衣") || !strings.Contains(gotLegacy, "银色耳钉") {
		t.Fatalf("legacy charLook missed visual traits: %s", gotLegacy)
	}
}

func TestPauseAtNormalization(t *testing.T) {
	pa, err := normPauseAt("writer", []string{"assets_img", "keyframes", "assets"})
	if err != nil {
		t.Fatal(err)
	}
	if len(pa) != 2 || pa[0] != "assets" || pa[1] != "keyframes" {
		t.Fatalf("unexpected pause_at: %+v", pa)
	}
	pp := &Pipe{Mode: "writer", PauseAt: pa}
	if pp.confirmed("assets") || pp.confirmed("keyframes") || !pp.confirmed("script") {
		t.Fatalf("pause_at check failed for writer mode")
	}
	pp.Confirmed = append(pp.Confirmed, "assets")
	if !pp.confirmed("assets") || pp.confirmed("keyframes") {
		t.Fatalf("confirmed state transition failed")
	}
	if _, err := normPauseAt("auto", []string{"invalid_stage"}); err == nil {
		t.Fatalf("expected error for invalid pause_at stage")
	}
}

func TestH3OfficialPromptFormat(t *testing.T) {
	defer setupTestEnv(t)()

	res, _ := db.Exec(`INSERT INTO projects(title,style,kind,audio_mode,created_at,updated_at) VALUES('故人','半写实影视','live','native',?,?)`, now(), now())
	pid, _ := res.LastInsertId()
	p, _ := getProject(pid)
	_, _ = insertAsset(pid, "character", assetDraft{Name: "阿策", Description: "碎短发"})
	_, _ = db.Exec(`UPDATE assets SET look_json=?, voice=? WHERE project_id=?`, `{"age":"32岁","gender":"男"}`, `{"desc":"爽朗青年男声"}`, pid)

	s := &Shot{
		Duration:    8,
		Camera:      "中近景 · 缓慢推近",
		VideoPrompt: "阿策笑着摆手往椅背一靠，窗外雪山飞掠。",
		Dialogue:    "阿策：（笑）别把我俩捆热搜好不好？\n旁白：十年未见的旧队友再次同车。",
	}
	i2va := shotVideoPromptFor(p, s, 8, false)
	for _, want := range []string{
		"For the target video, at 0.00 seconds into the target video, <Picture 1> (from [Shot 1]) is fully referenced.",
		"integrated_multimodal_description: [Shot 1] Live-action, cinematic",
		"The camera pushes in at slow speed.",
		"(S1)", "<d>[Chinese] 别把我俩捆热搜好不好？</d>",
		"says in an off-screen voiceover: <d>[Chinese] 十年未见的旧队友再次同车。</d> while on-screen characters' lips remain completely closed.",
		"overall_soundscape:",
		"non_diegetic_music: N/A",
	} {
		if !strings.Contains(i2va, want) {
			t.Fatalf("I2VA prompt missing %q:\n%s", want, i2va)
		}
	}

	// FL2VA 首尾帧对齐头行
	fl2va := shotVideoPromptFor(p, s, 8, true)
	if !strings.Contains(fl2va, "How the reference pictures align with the target video — Picture 1 (from Shot 1) aligns with the 0.00-second mark of the target video; Picture 2 (from Shot 1) aligns with the") {
		t.Fatalf("FL2VA prompt header wrong:\n%s", fl2va)
	}

	// dub 模式不放 <d> 且不含禁用词清单「不要人声」
	p.AudioMode = "dub"
	dubP := shotVideoPromptFor(p, s, 5, false)
	if strings.Contains(dubP, "<d>") || strings.Contains(dubP, "不要任何人声") {
		t.Fatalf("dub prompt should not contain <d> or negative ban list:\n%s", dubP)
	}

	// 已手写官方格式时原样透传
	custom := "integrated_multimodal_description: [Shot 1] Custom shot.\n\noverall_soundscape: Wind.\n\nnon_diegetic_music: N/A"
	sCustom := &Shot{VideoPrompt: custom}
	gotCustom := shotVideoPromptFor(p, sCustom, 5, false)
	if !strings.HasSuffix(gotCustom, custom) || !strings.HasPrefix(gotCustom, "For the target video,") {
		t.Fatalf("custom official prompt pass-through failed:\n%s", gotCustom)
	}
}

func TestShotOpsOrphanCleanupAndWaitingPrev(t *testing.T) {
	defer setupTestEnv(t)()

	res, _ := db.Exec(`INSERT INTO projects(title,created_at,updated_at) VALUES('剧',?,?)`, now(), now())
	pid, _ := res.LastInsertId()
	e, _ := insertEpisode(pid, "梗概")
	_, _ = db.Exec(`UPDATE episodes SET script='# 标题\n## 第 1 场 · 车厢\n【画面】开场\n【卡点】悬念' WHERE id=?`, e.ID)
	p, _ := getProject(pid)

	// 添加两个镜头，第 2 镜设为 first_last
	a1, err := toolAddShot(context.Background(), json.RawMessage(`{"episode_id":`+jsonInt(e.ID)+`,"duration":5,"camera":"中景","picture":"画面1","keyframe_prompt":"画面1详细提示词描述","video_prompt":"动作1"}`))
	if err != nil {
		t.Fatal(err)
	}
	s1ID := a1.(map[string]any)["shot_id"].(int64)
	a2, err := toolAddShot(context.Background(), json.RawMessage(`{"episode_id":`+jsonInt(e.ID)+`,"after_shot_id":`+jsonInt(s1ID)+`,"duration":6,"camera":"近景","picture":"画面2","transition":"first_last","keyframe_prompt":"画面2详细提示词描述","video_prompt":"动作2"}`))
	if err != nil {
		t.Fatal(err)
	}
	s2ID := a2.(map[string]any)["shot_id"].(int64)

	// 给两镜都挂上关键帧
	im1, _ := storeImageX(pid, e.ID, "shot", s1ID, "", makePNGBytes(32, 32, color.RGBA{10, 20, 30, 255}), "gen", "", 0, "")
	im2, _ := storeImageX(pid, e.ID, "shot", s2ID, "", makePNGBytes(32, 32, color.RGBA{40, 50, 60, 255}), "gen", "", 0, "")
	_, _ = db.Exec(`UPDATE shots SET picked_image_id=? WHERE id=?`, im1.ID, s1ID)
	_, _ = db.Exec(`UPDATE shots SET picked_image_id=? WHERE id=?`, im2.ID, s2ID)

	// 整集排队：镜1 queued，镜2 自动排为 waiting_prev（不再漏排！）
	qOut, err := toolQueueEpisodeVideos(context.Background(), json.RawMessage(`{"episode_id":`+jsonInt(e.ID)+`}`))
	if err != nil {
		t.Fatal(err)
	}
	qm := qOut.(map[string]any)
	if qm["queued"] != 2 || qm["waiting_prev"] != 1 {
		t.Fatalf("expected queued=2 waiting_prev=1, got %+v", qm)
	}

	// 模拟镜1视频完成并有缓存尾帧 -> unlockWaitingPrev 自动把镜2推入 queued
	v1s := listVideos(`WHERE shot_id=?`, s1ID)
	_, _ = db.Exec(`UPDATE videos SET status='done', file='v1.mp4' WHERE id=?`, v1s[0].ID)
	_, _ = db.Exec(`UPDATE shots SET picked_video_id=? WHERE id=?`, v1s[0].ID, s1ID)
	_, _ = storeImageX(pid, e.ID, "lastframe", v1s[0].ID, "", makePNGBytes(32, 32, color.RGBA{90, 90, 90, 255}), "extract", "", 0, "")

	if n := unlockWaitingPrev(e.ID); n != 1 {
		t.Fatalf("expected 1 unlocked waiting_prev, got %d", n)
	}
	v2s := listVideos(`WHERE shot_id=?`, s2ID)
	if v2s[0].Status != "queued" || v2s[0].ImageFile == "" {
		t.Fatalf("waiting_prev did not unlock properly: %+v", v2s[0])
	}

	// 重排分镜：保留 ID 和媒体
	if _, err := toolReorderShots(context.Background(), json.RawMessage(`{"episode_id":`+jsonInt(e.ID)+`,"shot_ids":[`+jsonInt(s2ID)+`,`+jsonInt(s1ID)+`]}`)); err != nil {
		t.Fatal(err)
	}
	reordered := listShots(e.ID, false)
	if reordered[0].ID != s2ID || reordered[0].Chain != "off" || reordered[1].ID != s1ID {
		t.Fatalf("reorder failed: %+v", reordered)
	}

	// 孤儿配音文件清理：save_shots 覆盖时删除旧音频文件
	orphan := "line-orphan-test.mp3"
	orphanPath := filepath.Join(dataDir, "audio", orphan)
	_ = os.WriteFile(orphanPath, []byte("dummy"), 0o644)
	_, _ = db.Exec(`INSERT INTO lines(project_id,episode_id,shot_id,sort,speaker,text,audio_file,status,created_at) VALUES(?,?,?,0,'阿策','你好',?,'done',?)`, pid, e.ID, s1ID, orphan, now())
	_, err = toolSaveShots(context.Background(), json.RawMessage(`{"episode_id":`+jsonInt(e.ID)+`,"overwrite":true,"shots":[{"duration":5,"camera":"中景","picture":"新画面","keyframe_prompt":"新画面详细关键帧描述","video_prompt":"新动作"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(orphanPath); !os.IsNotExist(err) {
		t.Fatalf("expected orphan line audio to be removed")
	}
	_ = p
}

func TestSubtitlesContactSheetVCOverrideAndDriveDefer(t *testing.T) {
	// 1) 字幕按字数分配时长
	subs := allocateNativeSubs([]parsedLine{
		{speaker: "A", text: "好"},                 // 1 字 -> 保底 2
		{speaker: "B", text: "今天天气真的非常好啊我们走吧"}, // 14 字
	}, 8.0)
	if len(subs) != 2 || subs[1].Dur <= subs[0].Dur*4 {
		t.Fatalf("expected weighted subtitle durations, got %+v", subs)
	}

	// 2) 竖屏与横屏字号适配
	_, _, fsPort, _ := subtitleStyle(1080, 1920)
	_, _, fsLand, _ := subtitleStyle(1920, 1080)
	if fsPort <= fsLand {
		t.Fatalf("portrait fontSize (%d) should compensate for taller PlayResY vs landscape (%d)", fsPort, fsLand)
	}

	// 3) 抽帧拼图生成合法 JPEG
	jpg, err := buildContactSheet([][]byte{
		makePNGBytes(160, 90, color.RGBA{255, 0, 0, 255}),
		makePNGBytes(160, 90, color.RGBA{0, 255, 0, 255}),
		makePNGBytes(160, 90, color.RGBA{0, 0, 255, 255}),
		makePNGBytes(160, 90, color.RGBA{255, 255, 0, 255}),
	}, 800)
	if err != nil || len(jpg) < 500 {
		t.Fatalf("buildContactSheet failed: err=%v len=%d", err, len(jpg))
	}

	// 4) 变声参数三层覆盖：全局 < 角色 < 单次
	base := defaultVCCfg()
	steps20 := 20
	f0On := true
	semi := -2
	charOv := VCOverride{DiffusionSteps: &steps20, F0: &f0On}
	takeOv := VCOverride{SemiTone: &semi}
	eff := base.applyOverride(charOv).applyOverride(takeOv)
	if eff.DiffusionSteps != 20 || !eff.F0 || eff.SemiTone != -2 || eff.CfgRate != base.CfgRate {
		t.Fatalf("vc override merge unexpected: %+v", eff)
	}

	// 5) 云盘上传空闲延期与上限
	t0 := int64(1_000_000)
	defer1, since1 := shouldDeferIdleStop(0, t0, true, 30*time.Minute)
	if !defer1 || since1 != t0 {
		t.Fatalf("should defer first time when uploading")
	}
	defer2, _ := shouldDeferIdleStop(since1, t0+int64(29*time.Minute/time.Millisecond), true, 30*time.Minute)
	if !defer2 {
		t.Fatalf("should still defer before maxExt")
	}
	defer3, _ := shouldDeferIdleStop(since1, t0+int64(31*time.Minute/time.Millisecond), true, 30*time.Minute)
	if defer3 {
		t.Fatalf("must stop deferring after maxExt")
	}
	defer4, since4 := shouldDeferIdleStop(since1, t0+1000, false, 30*time.Minute)
	if defer4 || since4 != 0 {
		t.Fatalf("must not defer when upload finished")
	}
}

func jsonInt(v int64) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func TestMCPToolsListUniqueAndOps(t *testing.T) {
	defer setupTestEnv(t)()

	res := mcpHandle(context.Background(), rpcReq{JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "tools/list"}, "http://test", "2025-06-18")
	m := res.(map[string]any)["result"].(map[string]any)
	tools := m["tools"].([]mcpTool)
	seen := map[string]bool{}
	for _, tl := range tools {
		if tl.Name == "" {
			t.Fatalf("empty tool name")
		}
		if seen[tl.Name] {
			t.Fatalf("duplicate MCP tool name: %s", tl.Name)
		}
		seen[tl.Name] = true
	}
	for _, want := range []string{
		"get_task_status", "generate_missing_keyframes", "pick_video", "cancel_video",
		"queue_episode_videos", "build_final", "generate_cover", "list_bgm", "upload_bgm",
		"set_episode_audio", "set_shot_sfx", "export_capcut", "add_shot", "delete_shot",
		"reorder_shots", "set_voice_conv_override",
		"list_voice_channels", "preview_shot_prompts", "generate_missing_assets",
		"list_video_queue", "list_lines", "update_line", "generate_line",
	} {
		if !seen[want] {
			t.Fatalf("missing expected MCP tool: %s", want)
		}
	}

	// 验证 set_voice_conv_override 与 delete_shot 预览/确认安全门
	pr, _ := db.Exec(`INSERT INTO projects(title,created_at,updated_at) VALUES('测',?,?)`, now(), now())
	pid, _ := pr.LastInsertId()
	e, _ := insertEpisode(pid, "梗概")
	aid, _ := insertAsset(pid, "character", assetDraft{Name: "小雨", Description: "短发穿大衣"})
	ovOut, err := callVCTool(context.Background(), "set_voice_conv_override", json.RawMessage(`{"asset_id":`+jsonInt(aid)+`,"diffusion_steps":24,"f0":true,"semi_tone":-1}`), "http://test")
	if err != nil {
		t.Fatal(err)
	}
	if ovOut.(map[string]any)["ok"] != true {
		t.Fatalf("set_voice_conv_override failed: %+v", ovOut)
	}
	as, _ := getAsset(aid)
	if as.VcOverride.DiffusionSteps == nil || *as.VcOverride.DiffusionSteps != 24 || as.VcOverride.SemiTone == nil || *as.VcOverride.SemiTone != -1 {
		t.Fatalf("vc_override not saved: %+v", as.VcOverride)
	}

	addOut, err := toolAddShot(context.Background(), json.RawMessage(`{"episode_id":`+jsonInt(e.ID)+`,"duration":5,"camera":"近景","picture":"画面","keyframe_prompt":"详细关键帧提示词内容","video_prompt":"动作"}`))
	if err != nil {
		t.Fatal(err)
	}
	sid := addOut.(map[string]any)["shot_id"].(int64)
	prevDel, err := toolDeleteShot(context.Background(), json.RawMessage(`{"shot_id":`+jsonInt(sid)+`}`))
	if err != nil || prevDel.(map[string]any)["ok"] != false {
		t.Fatalf("delete_shot without confirm should preview: err=%v out=%+v", err, prevDel)
	}
	doDel, err := toolDeleteShot(context.Background(), json.RawMessage(`{"shot_id":`+jsonInt(sid)+`,"confirm":true}`))
	if err != nil || doDel.(map[string]any)["ok"] != true {
		t.Fatalf("delete_shot with confirm failed: err=%v out=%+v", err, doDel)
	}
	if len(listShots(e.ID, false)) != 0 {
		t.Fatalf("shot was not deleted")
	}
}

func TestV0214BugFixes(t *testing.T) {
	defer setupTestEnv(t)()

	// 1) consistency.go: 「无 / 无配饰」不拼出「戴无」，「穿过人群」不混入外观
	l := AssetLook{Hair: "黑色短发", Outfit: "深蓝外套", Accessories: "无配饰"}
	fp := l.formatPositive()
	if strings.Contains(fp, "戴无") || strings.Contains(fp, "无配饰") {
		t.Fatalf("unexpected negative accessory in formatPositive: %s", fp)
	}
	sents := appearanceSentences("穿过人群寻找线索，穿深色风衣", "阿策")
	for _, s := range sents {
		if strings.Contains(s, "穿过人群") {
			t.Fatalf("non-clothing verb leaked into appearance: %v", sents)
		}
	}

	// 2) upsert_asset 局部更新 look 时合并旧字段、不覆盖 description，且已有 look 允许只更新 description
	pr, _ := db.Exec(`INSERT INTO projects(title,created_at,updated_at) VALUES('合并测',?,?)`, now(), now())
	pid, _ := pr.LastInsertId()
	r1, err := toolUpsertAsset(context.Background(), json.RawMessage(`{"project_id":`+jsonInt(pid)+`,"kind":"character","name":"林夏","description":"冷静沉稳的调查记者","look":{"hair":"黑色齐肩短发","outfit":"卡其色风衣"}}`))
	if err != nil {
		t.Fatal(err)
	}
	aid := r1.(map[string]any)["asset_id"].(int64)
	// 仅追加 avoid，不得清空 hair / outfit，也不得覆写 description
	if _, err := toolUpsertAsset(context.Background(), json.RawMessage(`{"asset_id":`+jsonInt(aid)+`,"look":{"avoid":["帽子"]}}`)); err != nil {
		t.Fatal(err)
	}
	a, _ := getAsset(aid)
	if a.Look.Hair != "黑色齐肩短发" || a.Look.Outfit != "卡其色风衣" || len(a.Look.Avoid) != 1 || a.Look.Avoid[0] != "帽子" {
		t.Fatalf("partial look update clobbered fields: %+v", a.Look)
	}
	if a.Description != "冷静沉稳的调查记者" {
		t.Fatalf("partial look update clobbered description: %q", a.Description)
	}
	// 已有 look 时，仅更新性格 description 不应被误拒
	if _, err := toolUpsertAsset(context.Background(), json.RawMessage(`{"asset_id":`+jsonInt(aid)+`,"description":"性格执拗，内心藏着旧案秘密"}`)); err != nil {
		t.Fatalf("updating description when look_json already exists should succeed: %v", err)
	}

	// 3) 删第 1 镜时，原第 2 镜自动重置 chain='off' 且失效 waiting_prev 自动取消；重排后移到已完成镜后立刻解锁
	e, _ := insertEpisode(pid, "梗概")
	a1, _ := toolAddShot(context.Background(), json.RawMessage(`{"episode_id":`+jsonInt(e.ID)+`,"duration":5,"camera":"中景","picture":"镜1","keyframe_prompt":"镜1详细关键帧描述","video_prompt":"动作1"}`))
	a2, _ := toolAddShot(context.Background(), json.RawMessage(`{"episode_id":`+jsonInt(e.ID)+`,"duration":5,"camera":"近景","picture":"镜2","transition":"first_last","keyframe_prompt":"镜2详细关键帧描述","video_prompt":"动作2"}`))
	a3, _ := toolAddShot(context.Background(), json.RawMessage(`{"episode_id":`+jsonInt(e.ID)+`,"duration":5,"camera":"特写","picture":"镜3","transition":"first_last","keyframe_prompt":"镜3详细关键帧描述","video_prompt":"动作3"}`))
	s1 := a1.(map[string]any)["shot_id"].(int64)
	s2 := a2.(map[string]any)["shot_id"].(int64)
	s3 := a3.(map[string]any)["shot_id"].(int64)
	for _, sid := range []int64{s1, s2, s3} {
		im, _ := storeImageX(pid, e.ID, "shot", sid, "", makePNGBytes(32, 32, color.RGBA{20, 40, 60, 255}), "gen", "", 0, "")
		_, _ = db.Exec(`UPDATE shots SET picked_image_id=? WHERE id=?`, im.ID, sid)
	}
	if _, err := toolQueueEpisodeVideos(context.Background(), json.RawMessage(`{"episode_id":`+jsonInt(e.ID)+`}`)); err != nil {
		t.Fatal(err)
	}
	// 删除第 1 镜 -> s2 变成新第 1 镜，chain 归 off，s2 的 waiting_prev 视频自动取消；s3 仍在 s2 后保持 waiting_prev
	if _, err := toolDeleteShot(context.Background(), json.RawMessage(`{"shot_id":`+jsonInt(s1)+`,"confirm":true}`)); err != nil {
		t.Fatal(err)
	}
	s2Row, _ := getShot(s2)
	if s2Row.Chain != "off" {
		t.Fatalf("new first shot should have chain='off', got %q", s2Row.Chain)
	}
	v2s := listVideos(`WHERE shot_id=?`, s2)
	if len(v2s) != 1 || v2s[0].Status != "canceled" {
		t.Fatalf("orphaned waiting_prev on new first shot should be canceled, got %+v", v2s)
	}
	// 新增一个已完成的镜头 s4，把 s3 排到 s4 后面 -> reorder_shots 立刻解锁 s3 的 waiting_prev
	a4, _ := toolAddShot(context.Background(), json.RawMessage(`{"episode_id":`+jsonInt(e.ID)+`,"duration":5,"camera":"全景","picture":"镜4","keyframe_prompt":"镜4详细关键帧描述","video_prompt":"动作4"}`))
	s4 := a4.(map[string]any)["shot_id"].(int64)
	v4id, _ := enqueueVideo(videoIn{ProjectID: pid, EpisodeID: e.ID, ShotID: s4, Source: "project", Prompt: "p4", ImageFile: "x.png", Seconds: 5})
	_, _ = db.Exec(`UPDATE videos SET status='done', file='v4.mp4' WHERE id=?`, v4id)
	_, _ = db.Exec(`UPDATE shots SET picked_video_id=? WHERE id=?`, v4id, s4)
	_, _ = storeImageX(pid, e.ID, "lastframe", v4id, "", makePNGBytes(32, 32, color.RGBA{80, 80, 80, 255}), "extract", "", 0, "")
	ro, err := toolReorderShots(context.Background(), json.RawMessage(`{"episode_id":`+jsonInt(e.ID)+`,"shot_ids":[`+jsonInt(s2)+`,`+jsonInt(s4)+`,`+jsonInt(s3)+`]}`))
	if err != nil {
		t.Fatal(err)
	}
	if ro.(map[string]any)["unlocked_waiting_prev"] != 1 {
		t.Fatalf("expected reorder_shots to unlock s3 immediately, got %+v", ro)
	}
	v3s := listVideos(`WHERE shot_id=?`, s3)
	if len(v3s) != 1 || v3s[0].Status != "queued" || v3s[0].ImageFile == "" {
		t.Fatalf("s3 waiting_prev not unlocked after reorder: %+v", v3s)
	}
}

func TestV0215FrictionFixes(t *testing.T) {
	defer setupTestEnv(t)()

	pr, _ := db.Exec(`INSERT INTO projects(title,style,aspect,takes,created_at,updated_at) VALUES('全流程测','赛博写实','9:16',2,?,?)`, now(), now())
	pid, _ := pr.LastInsertId()
	e, _ := insertEpisode(pid, "梗概")

	// 1) list_voice_channels
	_, _ = db.Exec(`INSERT INTO channels(kind,name,base_url,api_key,api_type,models,voices,caps,sort,created_at) VALUES('voice','MiMo 语音','https://mimo.test','k','mimo','["mimo-tts"]','["v1"]','{}',0,?)`, now())
	vcRes, err := toolVoiceChannels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	vchs := vcRes.(map[string]any)["channels"].([]map[string]any)
	if len(vchs) != 1 || vchs[0]["api_type"] != "mimo" {
		t.Fatalf("unexpected voice channels: %+v", vchs)
	}

	// 2) attach_image(shot_id) + 图片签名免密钥下载 /v1/dl/i...
	a1, _ := toolAddShot(context.Background(), json.RawMessage(`{"episode_id":`+jsonInt(e.ID)+`,"duration":5,"camera":"中景","picture":"画面1","dialogue":"阿策：走吧","keyframe_prompt":"画面1详细关键帧描述","video_prompt":"动作1"}`))
	a2, _ := toolAddShot(context.Background(), json.RawMessage(`{"episode_id":`+jsonInt(e.ID)+`,"duration":6,"camera":"近景","picture":"画面2","transition":"first_last","keyframe_prompt":"画面2详细关键帧描述","video_prompt":"动作2"}`))
	s1 := a1.(map[string]any)["shot_id"].(int64)
	s2 := a2.(map[string]any)["shot_id"].(int64)
	studioImg, _ := storeImage(0, "ext", 0, makePNGBytes(64, 64, color.RGBA{12, 34, 56, 255}), "upload", "", 0, "")
	attOut, err := toolAttachImage(context.Background(), attachArgs{ImageID: studioImg.ID, ShotID: s1})
	if err != nil || attOut.(map[string]any)["shot_id"] != s1 {
		t.Fatalf("attach_image to shot failed: err=%v out=%+v", err, attOut)
	}
	s1Row, _ := getShot(s1)
	if s1Row.PickedImageID == 0 {
		t.Fatalf("shot picked_image_id not set after attach_image")
	}
	// 验证 /v1/dl/ 图片签名链接可免密钥下载
	tok := signDL("i", s1Row.PickedImageID, time.Hour)
	req := httptest.NewRequest("GET", "/v1/dl/"+tok, nil)
	req.SetPathValue("token", tok)
	rec := httptest.NewRecorder()
	handleSignedDL(rec, req)
	if rec.Code != 200 || !strings.HasPrefix(rec.Header().Get("Content-Type"), "image/") {
		t.Fatalf("signed image download failed: code=%d ct=%s", rec.Code, rec.Header().Get("Content-Type"))
	}

	// 3) preview_shot_prompts 0 成本预览
	prevOut, err := toolPreviewShotPrompts(context.Background(), json.RawMessage(`{"shot_id":`+jsonInt(s1)+`}`))
	if err != nil {
		t.Fatal(err)
	}
	pm := prevOut.(map[string]any)
	if !strings.Contains(pm["video_prompt_final"].(string), "integrated_multimodal_description:") || pm["keyframe_prompt_final"].(string) == "" {
		t.Fatalf("preview_shot_prompts missing prompts: %+v", pm)
	}

	// 4) takes >= 2 多抽卡防抢跑：镜1 Take1 完成但 Take2 仍在排队时，自动 unlockWaitingPrev 不抢跑；显式 pick_video 后立即解锁
	_, _ = toolAttachImage(context.Background(), attachArgs{ImageID: studioImg.ID, ShotID: s2})
	if _, err := toolQueueEpisodeVideos(context.Background(), json.RawMessage(`{"episode_id":`+jsonInt(e.ID)+`}`)); err != nil {
		t.Fatal(err)
	}
	v1s := listVideos(`WHERE shot_id=? ORDER BY id`, s1)
	if len(v1s) != 2 {
		t.Fatalf("expected 2 takes for s1, got %d", len(v1s))
	}
	_, _ = db.Exec(`UPDATE videos SET status='done', file='t1.mp4' WHERE id=?`, v1s[0].ID)
	_, _ = db.Exec(`UPDATE shots SET picked_video_id=? WHERE id=?`, v1s[0].ID, s1)
	_, _ = storeImageX(pid, e.ID, "lastframe", v1s[0].ID, "", makePNGBytes(32, 32, color.RGBA{70, 70, 70, 255}), "extract", "", 0, "")
	if n := unlockWaitingPrev(e.ID); n != 0 {
		t.Fatalf("should NOT unlock waiting_prev while take 2 of prev shot is still queued, unlocked=%d", n)
	}
	// 显式选片 pick_video(Take1) -> 立刻解锁镜2
	pkOut, err := toolPickVideo(context.Background(), json.RawMessage(`{"video_id":`+jsonInt(v1s[0].ID)+`}`))
	if err != nil || pkOut.(map[string]any)["unlocked_waiting_prev"] != 2 {
		t.Fatalf("explicit pick_video should unlock both takes of s2, err=%v out=%+v", err, pkOut)
	}

	// 5) list_video_queue & list_shots(episode_id, brief) & list_lines / update_line
	qRes, err := toolListVideoQueue(context.Background(), json.RawMessage(`{"episode_id":`+jsonInt(e.ID)+`}`))
	if err != nil || len(qRes.(map[string]any)["videos"].([]map[string]any)) == 0 {
		t.Fatalf("list_video_queue failed: err=%v out=%+v", err, qRes)
	}
	lsBrief, err := toolShots(context.Background(), 0, e.ID, true, "http://test")
	if err != nil {
		t.Fatal(err)
	}
	firstShot := lsBrief.([]map[string]any)[0]
	if _, hasDetails := firstShot["take_details"]; hasDetails {
		t.Fatalf("brief=true should omit take_details")
	}
	linesOut, err := toolListLines(context.Background(), json.RawMessage(`{"episode_id":`+jsonInt(e.ID)+`}`), "http://test")
	if err != nil {
		t.Fatal(err)
	}
	lArr := linesOut.(map[string]any)["lines"].([]map[string]any)
	if len(lArr) != 1 {
		t.Fatalf("expected 1 line, got %d", len(lArr))
	}
	lid := lArr[0]["line_id"].(int64)
	if _, err := toolUpdateLine(context.Background(), json.RawMessage(`{"line_id":`+jsonInt(lid)+`,"text":"我们走吧","emotion":"低声"}`)); err != nil {
		t.Fatal(err)
	}
	lRow, _ := getLine(lid)
	if lRow.Text != "我们走吧" || lRow.Emotion != "低声" {
		t.Fatalf("update_line did not persist: %+v", lRow)
	}

	// 6) allocateNativeSubs 极端短镜头多句不产生负时长
	many := make([]parsedLine, 20)
	for i := range many {
		many[i] = parsedLine{speaker: "A", text: "你好"}
	}
	for _, sl := range allocateNativeSubs(many, 0.4) {
		if sl.Dur <= 0 || sl.Start < 0 || sl.Start+sl.Dur > 0.4001 {
			t.Fatalf("invalid native subtitle timing: %+v", sl)
		}
	}
}
