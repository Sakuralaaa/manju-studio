package main

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type VideoPost struct {
	Upscale bool `json:"upscale,omitempty"`
	Interp  bool `json:"interp,omitempty"`
	Width   int  `json:"width,omitempty"`
	Height  int  `json:"height,omitempty"`
}

type Video struct {
	ID            int64     `json:"id"`
	ProjectID     int64     `json:"project_id"`
	EpisodeID     int64     `json:"episode_id"`
	ShotID        int64     `json:"shot_id"`
	Source        string    `json:"source"`
	Status        string    `json:"status"`
	Progress      float64   `json:"progress"`
	Stage         string    `json:"stage"`
	Prompt        string    `json:"prompt"`
	ImageFile     string    `json:"-"`
	ImageURL      string    `json:"image_url"`
	LastImageFile string    `json:"-"`
	LastImageURL  string    `json:"last_image_url"`
	Seconds       int       `json:"seconds"`
	Res           string    `json:"res"`
	Aspect        string    `json:"aspect"`
	Turbo         bool      `json:"turbo"`
	Seed          int64     `json:"seed"`
	File          string    `json:"-"`
	URL           string    `json:"url"`
	Bytes         int64     `json:"bytes"`
	Error         string    `json:"error"`
	Post          VideoPost `json:"post"`
	EstCU         float64   `json:"est_cu"`
	CreatedAt     int64     `json:"created_at"`
	StartedAt     int64     `json:"started_at"`
	FinishedAt    int64     `json:"finished_at"`
	Position      int       `json:"position,omitempty"`
	// 生成后统一音色：原版（File）一直保留，转换版存 VcFile；VcUse 决定成片 / 下载用哪一版
	VcStatus  string         `json:"vc_status"` // '' 没做 / auto 等视频出来后自动做 / queued / running / done / failed / skipped
	VcFile    string         `json:"-"`
	VcURL     string         `json:"vc_url"`
	VcError   string         `json:"vc_error"` // 失败原因或跳过原因
	VcAssetID  int64          `json:"vc_asset_id"`
	VcUse      bool           `json:"vc_use"`
	VcInfo     map[string]any `json:"vc_info"`
	VcAt       int64          `json:"vc_at"`
	VcOverride VCOverride     `json:"vc_override"`
}

// useFile 成片 / 下载用的那一版：选了转换版且转换版存在时用转换版，否则原版
func (v *Video) useFile() string {
	if v.VcUse && v.VcFile != "" {
		return v.VcFile
	}
	return v.File
}

const videoCols = `id,project_id,episode_id,shot_id,source,status,progress,stage,prompt,image_file,last_image_file,seconds,res,aspect,turbo,seed,file,bytes,error,post,est_cu,created_at,started_at,finished_at,vc_status,vc_file,vc_error,vc_asset_id,vc_use,vc_info,vc_at,vc_override`

func scanVideoNoPos(sc interface{ Scan(...any) error }) (*Video, error) {
	var v Video
	var turbo, vcUse int
	var post, vcInfo, vcOv string
	if err := sc.Scan(&v.ID, &v.ProjectID, &v.EpisodeID, &v.ShotID, &v.Source, &v.Status, &v.Progress, &v.Stage, &v.Prompt, &v.ImageFile, &v.LastImageFile, &v.Seconds, &v.Res, &v.Aspect, &turbo, &v.Seed, &v.File, &v.Bytes, &v.Error, &post, &v.EstCU, &v.CreatedAt, &v.StartedAt, &v.FinishedAt,
		&v.VcStatus, &v.VcFile, &v.VcError, &v.VcAssetID, &vcUse, &vcInfo, &v.VcAt, &vcOv); err != nil {
		return nil, err
	}
	v.Turbo, v.VcUse = turbo == 1, vcUse == 1
	_ = json.Unmarshal([]byte(vcInfo), &v.VcInfo)
	_ = json.Unmarshal([]byte(vcOv), &v.VcOverride)
	if v.VcFile != "" {
		v.VcURL = "/files/videos/" + v.VcFile
	}
	_ = json.Unmarshal([]byte(post), &v.Post)
	if v.File != "" {
		v.URL = "/files/videos/" + v.File
	}
	if v.ImageFile != "" {
		v.ImageURL = "/files/images/" + v.ImageFile
	}
	if v.LastImageFile != "" {
		v.LastImageURL = "/files/images/" + v.LastImageFile
	}
	return &v, nil
}

func scanVideo(sc interface{ Scan(...any) error }) (*Video, error) {
	v, err := scanVideoNoPos(sc)
	if err != nil {
		return nil, err
	}
	if v.Status == "queued" {
		_ = db.QueryRow(`SELECT COUNT(*) FROM videos WHERE status='queued' AND id<?`, v.ID).Scan(&v.Position)
		v.Position++
	}
	return v, nil
}

func getVideo(id int64) (*Video, error) {
	return scanVideo(db.QueryRow(`SELECT `+videoCols+` FROM videos WHERE id=?`, id))
}

func listVideos(where string, args ...any) []*Video {
	rows, err := db.Query(`SELECT `+videoCols+` FROM videos `+where, args...)
	if err != nil {
		return []*Video{}
	}
	var raw []*Video
	for rows.Next() {
		if v, err := scanVideoNoPos(rows); err == nil {
			raw = append(raw, v)
		}
	}
	rows.Close()
	for _, v := range raw {
		if v.Status == "queued" {
			_ = db.QueryRow(`SELECT COUNT(*) FROM videos WHERE status='queued' AND id<?`, v.ID).Scan(&v.Position)
			v.Position++
		}
	}
	if raw == nil {
		raw = []*Video{}
	}
	return raw
}

type videoIn struct {
	ProjectID     int64
	EpisodeID     int64
	ShotID        int64
	Source        string
	Prompt        string
	ImageFile     string
	LastImageFile string
	Seconds       int
	Res           string
	Aspect        string
	Turbo         bool
	Seed          int64
	Post          VideoPost
	WaitingPrev   bool
}

func enqueueVideo(in videoIn) (int64, error) {
	if !in.WaitingPrev && strings.TrimSpace(in.ImageFile) == "" {
		return 0, errors.New("缺少首帧图片")
	}
	if strings.TrimSpace(in.Prompt) == "" {
		return 0, errors.New("缺少视频描述")
	}
	in.Seconds = clampInt(in.Seconds, 5, 10)
	if in.Res != "480p" {
		in.Res = "768p"
	}
	if in.Aspect == "" {
		in.Aspect = "9:16"
	}
	cfg := loadColabCfg()
	per := cfg.ClipMin
	if in.Turbo {
		per = cfg.ClipMinTurbo
	}
	if in.Post.Upscale {
		per += cfg.UpscaleMin
	}
	if in.Post.Interp {
		per += cfg.InterpMin
	}
	est := per / 60 * cfg.rate(cfg.Accelerator)
	vcSt := ""
	if in.ShotID > 0 && shotWantsVC(in.ShotID) {
		vcSt = "auto" // 视频出来后自动转成说话角色的音色
	}
	status, stage := "queued", ""
	if in.WaitingPrev {
		status, stage = "waiting_prev", "等待上一镜头完成"
	}
	res, err := db.Exec(`INSERT INTO videos(project_id,episode_id,shot_id,source,status,stage,prompt,image_file,last_image_file,seconds,res,aspect,turbo,seed,post,est_cu,created_at,vc_status) VALUES(?,?,?,?, ?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		in.ProjectID, in.EpisodeID, in.ShotID, in.Source, status, stage, in.Prompt, in.ImageFile, in.LastImageFile, in.Seconds, in.Res, in.Aspect, b2i(in.Turbo), in.Seed, mustJSON(in.Post), est, now(), vcSt)
	if err != nil {
		return 0, err
	}
	if !in.WaitingPrev {
		runner.poke()
	}
	return res.LastInsertId()
}

// shotVideoPrompt 视频描述（MiniMax H3 官方多模态格式，默认按镜头时长与首尾帧模式组装）
func shotVideoPrompt(p *Project, s *Shot) string {
	sec := int(s.Duration + 0.5)
	if sec < 5 {
		sec = 5
	}
	return shotVideoPromptFor(p, s, sec, s.Chain == "first")
}

type queueOpts struct {
	Seconds       int
	Turbo         *bool
	Res           string
	Takes         int
	AllowWaitPrev bool
}

func projectPost(p *Project) VideoPost {
	return VideoPost{Upscale: p.PostUpscale, Interp: p.PostInterp}
}

// queueShot 给一个镜头排队 N 次抽卡；首尾帧衔接时首帧用上一镜头的最后一帧、尾帧用本镜头关键帧。
// AllowWaitPrev=true 且上一镜还没出片时，排成 waiting_prev，待上一镜完成后自动补首帧开跑。
func queueShot(p *Project, s *Shot, o queueOpts) ([]int64, error) {
	if s.PickedImageID == 0 {
		return nil, fmt.Errorf("镜头 %s 还没有关键帧", s.Code)
	}
	im, err := getImage(s.PickedImageID)
	if err != nil {
		return nil, errors.New("关键帧不存在")
	}
	var busy int
	_ = db.QueryRow(`SELECT COUNT(*) FROM videos WHERE shot_id=? AND status IN ('queued','running','waiting_prev')`, s.ID).Scan(&busy)
	if busy > 0 {
		return nil, fmt.Errorf("镜头 %s 已在队列中", s.Code)
	}
	first, last := im.File, ""
	waitPrev := false
	if s.Chain == "first" {
		lf, err := ensurePrevLastFrame(s)
		if err != nil {
			if o.AllowWaitPrev && prevShot(s) != nil {
				waitPrev = true
				first, last = "", im.File
			} else {
				return nil, fmt.Errorf("镜头 %s：%s", s.Code, err.Error())
			}
		} else {
			first, last = lf.File, im.File
		}
	}
	sec := o.Seconds
	if sec == 0 {
		sec = int(s.Duration + 0.5)
	}
	turbo := p.VideoTurbo
	if o.Turbo != nil {
		turbo = *o.Turbo
	}
	res := p.VideoRes
	if o.Res != "" {
		res = o.Res
	}
	n := o.Takes
	if n <= 0 {
		n = p.Takes
	}
	n = clampInt(n, 1, 4)
	var ids []int64
	for i := 0; i < n; i++ {
		id, err := enqueueVideo(videoIn{ProjectID: p.ID, EpisodeID: s.EpisodeID, ShotID: s.ID, Source: "project", Prompt: shotVideoPromptFor(p, s, sec, last != ""), ImageFile: first, LastImageFile: last,
			Seconds: sec, Res: res, Aspect: p.Aspect, Turbo: turbo, Post: projectPost(p), WaitingPrev: waitPrev})
		if err != nil {
			return ids, err
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// unlockWaitingPrev 上一镜完成或改选后，检查本集 waiting_prev 的首尾帧视频，补上首帧并推入 queued
func unlockWaitingPrev(episodeID int64) int {
	if episodeID <= 0 {
		return 0
	}
	vs := listVideos(`WHERE episode_id=? AND status='waiting_prev' ORDER BY id`, episodeID)
	unlocked := 0
	for _, v := range vs {
		s, err := getShot(v.ShotID)
		if err != nil {
			continue
		}
		lf, err := ensurePrevLastFrame(s)
		if err != nil {
			continue
		}
		_, _ = db.Exec(`UPDATE videos SET image_file=?, status='queued', stage='' WHERE id=? AND status='waiting_prev'`, lf.File, v.ID)
		unlocked++
	}
	if unlocked > 0 {
		runner.poke()
		touchEpisode(episodeID)
	}
	return unlocked
}

func handleShotVideo(w http.ResponseWriter, r *http.Request) {
	s, err := getShot(pathID(r))
	if err != nil {
		httpErr(w, 404, "镜头不存在")
		return
	}
	p, _ := getProject(s.ProjectID)
	var in struct {
		Seconds int    `json:"seconds"`
		Turbo   *bool  `json:"turbo"`
		Res     string `json:"res"`
		Takes   int    `json:"takes"`
	}
	_ = readJSON(r, &in)
	ids, err := queueShot(p, s, queueOpts{Seconds: in.Seconds, Turbo: in.Turbo, Res: in.Res, Takes: in.Takes, AllowWaitPrev: true})
	if err != nil && len(ids) == 0 {
		httpErr(w, 400, err.Error())
		return
	}
	touchEpisode(s.EpisodeID)
	writeJSON(w, map[string]any{"ids": ids})
}

// queueEpisode 排队本集还缺视频的镜头（mode=all 时全部重来）；首尾帧镜头在上一镜未完成时自动排为 waiting_prev
func queueEpisode(p *Project, e *Episode, mode string) (int, int, error) {
	n, skipped := 0, 0
	var lastErr error
	shots := listShots(e.ID, true)
	for _, s := range shots {
		hasDone, busy := false, false
		for _, v := range s.Videos {
			if v.Status == "done" {
				hasDone = true
			}
			if v.Status == "queued" || v.Status == "running" || v.Status == "waiting_prev" {
				busy = true
			}
		}
		if busy || (mode != "all" && hasDone) {
			continue
		}
		if s.PickedImageID == 0 {
			skipped++
			continue
		}
		ids, err := queueShot(p, s, queueOpts{AllowWaitPrev: true})
		n += len(ids)
		if err != nil {
			lastErr = err
		}
	}
	return n, skipped, lastErr
}

func handleEpisodeVideosQueue(w http.ResponseWriter, r *http.Request) {
	p, e, err := epCtx(pathID(r))
	if err != nil {
		httpErr(w, 404, err.Error())
		return
	}
	var in struct {
		Mode  string `json:"mode"`
		Start bool   `json:"start"`
	}
	_ = readJSON(r, &in)
	n, skipped, lastErr := queueEpisode(p, e, in.Mode)
	if n == 0 {
		switch {
		case lastErr != nil:
			httpErr(w, 400, lastErr.Error())
		case skipped > 0:
			httpErr(w, 400, fmt.Sprintf("有 %d 个镜头还没有关键帧或在等上一镜头", skipped))
		default:
			httpErr(w, 400, "没有需要生成的镜头")
		}
		return
	}
	if in.Start && runner.brief().Phase == "off" {
		if err := startRuntime("", "", "video"); err != nil {
			writeJSON(w, map[string]any{"queued": n, "start_error": err.Error()})
			return
		}
	}
	touchEpisode(e.ID)
	writeJSON(w, map[string]any{"queued": n, "skipped": skipped})
}

func handleVideoPick(w http.ResponseWriter, r *http.Request) {
	v, err := getVideo(pathID(r))
	if err != nil || v.Status != "done" {
		httpErr(w, 404, "视频不存在")
		return
	}
	_, _ = db.Exec(`UPDATE shots SET picked_video_id=? WHERE id=?`, v.ID, v.ShotID)
	unlockWaitingPrev(v.EpisodeID)
	touchEpisode(v.EpisodeID)
	writeJSON(w, map[string]any{"ok": true})
}

func cancelVideo(id int64) error {
	v, err := getVideo(id)
	if err != nil {
		return errors.New("视频不存在")
	}
	switch v.Status {
	case "queued", "waiting_prev":
		_, _ = db.Exec(`UPDATE videos SET status='canceled', finished_at=? WHERE id=?`, now(), id)
	case "running":
		_, _ = db.Exec(`UPDATE videos SET status='canceled', finished_at=? WHERE id=?`, now(), id)
		runner.mu.Lock()
		if runner.CurrentJob == id && runner.cancelJob != nil {
			runner.cancelJob()
		}
		runner.mu.Unlock()
	default:
		if v.File != "" {
			_ = os.Remove(filepath.Join(dataDir, "videos", v.File))
		}
		if v.VcFile != "" {
			_ = os.Remove(filepath.Join(dataDir, "videos", v.VcFile))
		}
		_, _ = db.Exec(`UPDATE shots SET picked_video_id=0 WHERE picked_video_id=?`, id)
		_, _ = db.Exec(`DELETE FROM videos WHERE id=?`, id)
	}
	touchEpisode(v.EpisodeID)
	return nil
}

func handleVideoCancel(w http.ResponseWriter, r *http.Request) {
	if err := cancelVideo(pathID(r)); err != nil {
		httpErr(w, 404, err.Error())
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

func handleVideoList(w http.ResponseWriter, r *http.Request) {
	vs := listVideos(`WHERE status IN ('queued','running','waiting_prev') OR finished_at>? ORDER BY CASE status WHEN 'running' THEN 0 WHEN 'queued' THEN 1 WHEN 'waiting_prev' THEN 2 ELSE 3 END, id DESC LIMIT 60`, now()-24*3600_000)
	titles := map[int64]string{}
	for _, v := range vs {
		if v.EpisodeID > 0 && titles[v.EpisodeID] == "" {
			var t string
			var no int
			_ = db.QueryRow(`SELECT p.title, e.no FROM episodes e JOIN projects p ON p.id=e.project_id WHERE e.id=?`, v.EpisodeID).Scan(&t, &no)
			if t != "" {
				titles[v.EpisodeID] = fmt.Sprintf("%s · 第 %d 集", t, no)
			}
		}
	}
	cfg := loadColabCfg()
	var mins float64
	for _, v := range listVideos(`WHERE status='queued'`) {
		per := cfg.ClipMin
		if v.Turbo {
			per = cfg.ClipMinTurbo
		}
		if v.Post.Upscale {
			per += cfg.UpscaleMin
		}
		if v.Post.Interp {
			per += cfg.InterpMin
		}
		mins += per
	}
	writeJSON(w, map[string]any{"videos": vs, "titles": titles, "queue_minutes": mins, "vc_pending": vcPendingCount()})
}

// ---------- 上一镜头最后一帧 ----------

func shotVideoFor(s *Shot) *Video {
	var pick *Video
	for _, v := range s.Videos {
		if v.Status != "done" {
			continue
		}
		if v.ID == s.PickedVideoID {
			return v
		}
		pick = v
	}
	return pick
}

func prevLastFrame(s *Shot) *Image {
	pv := prevShot(s)
	if pv == nil {
		return nil
	}
	v := shotVideoFor(pv)
	if v == nil {
		return nil
	}
	ims := listImages(`WHERE owner_type='lastframe' AND owner_id=? ORDER BY id DESC LIMIT 1`, v.ID)
	if len(ims) > 0 {
		return ims[0]
	}
	return nil
}

// ensurePrevLastFrame 用 ffmpeg 抽出上一镜头选定视频的最后一帧（缓存为图片）
func ensurePrevLastFrame(s *Shot) (*Image, error) {
	if im := prevLastFrame(s); im != nil {
		return im, nil
	}
	pv := prevShot(s)
	if pv == nil {
		return nil, errors.New("这是第一个镜头，没有上一镜头可以衔接")
	}
	v := shotVideoFor(pv)
	if v == nil {
		return nil, errors.New("上一个镜头还没有视频")
	}
	data, err := lastFrame(filepath.Join(dataDir, "videos", v.File))
	if err != nil {
		return nil, err
	}
	return storeImageX(s.ProjectID, s.EpisodeID, "lastframe", v.ID, "", data, "extract", "", 0, "")
}

func lastFrame(path string) ([]byte, error) {
	out, err := exec.Command("ffmpeg", "-v", "error", "-sseof", "-0.25", "-i", path, "-update", "1", "-frames:v", "1", "-f", "image2", "-c:v", "png", "pipe:1").Output()
	if err != nil || len(out) < 100 {
		out, err = exec.Command("ffmpeg", "-v", "error", "-i", path, "-vf", "reverse", "-frames:v", "1", "-f", "image2", "-c:v", "png", "pipe:1").Output()
	}
	if err != nil || len(out) < 100 {
		return nil, errors.New("抽取最后一帧失败")
	}
	return out, nil
}

// ---------- ⑦ 成片 ----------

type clipInfo struct {
	path     string
	dur      float64
	hasAudio bool
	w, h     int
}

func probe(path string) (clipInfo, error) {
	out, err := exec.Command("ffprobe", "-v", "error", "-show_entries", "format=duration:stream=codec_type,width,height", "-of", "json", path).Output()
	if err != nil {
		return clipInfo{}, errors.New("读取媒体信息失败")
	}
	var p struct {
		Format struct {
			Duration string `json:"duration"`
		} `json:"format"`
		Streams []struct {
			CodecType string `json:"codec_type"`
			Width     int    `json:"width"`
			Height    int    `json:"height"`
		} `json:"streams"`
	}
	_ = json.Unmarshal(out, &p)
	ci := clipInfo{path: path}
	ci.dur, _ = strconv.ParseFloat(p.Format.Duration, 64)
	for _, s := range p.Streams {
		if s.CodecType == "audio" {
			ci.hasAudio = true
		}
		if s.CodecType == "video" && ci.w == 0 {
			ci.w, ci.h = s.Width, s.Height
		}
	}
	return ci, nil
}

var speakerRe = regexp.MustCompile(`^\s*[（(]?[^：:\n（(]{1,8}[)）]?\s*[：:]\s*`)

func subtitleLines(dialogue string) []string {
	var out []string
	for _, pl := range parseDialogue(dialogue) {
		out = append(out, pl.text)
	}
	return out
}

func srtTime(t float64) string {
	ms := int64(t*1000 + 0.5)
	return fmt.Sprintf("%02d:%02d:%02d,%03d", ms/3600000, ms/60000%60, ms/1000%60, ms%1000)
}

// ---------- 时间线（成片与剪映草稿共用） ----------

type tlLine struct {
	L     *Line
	Start float64 // 相对镜头开头
	Dur   float64
	Tempo float64 // >1 表示加速
}

type tlClip struct {
	Shot  *Shot
	Video *Video
	Path  string
	Info  clipInfo
	Start float64
	Lines []tlLine
	Subs  []tlLine // 字幕时段（无配音时按台词均分）
}

type timeline struct {
	P     *Project
	E     *Episode
	Clips []*tlClip
	Total float64
	Dub   bool
}

func buildTimeline(p *Project, e *Episode) (*timeline, error) {
	shots := listShots(e.ID, true)
	if len(shots) == 0 {
		return nil, errors.New("还没有分镜")
	}
	lines := listLines(`WHERE episode_id=? ORDER BY sort, id`, e.ID)
	byShot := map[int64][]*Line{}
	for _, l := range lines {
		byShot[l.ShotID] = append(byShot[l.ShotID], l)
	}
	tl := &timeline{P: p, E: e, Dub: p.AudioMode == "dub"}
	missing := 0
	for _, s := range shots {
		v := shotVideoFor(s)
		if v == nil {
			missing++
			continue
		}
		path := filepath.Join(dataDir, "videos", v.useFile())
		ci, err := probe(path)
		if err != nil {
			return nil, fmt.Errorf("镜头 %s：%s", s.Code, err.Error())
		}
		c := &tlClip{Shot: s, Video: v, Path: path, Info: ci, Start: tl.Total}
		ls := byShot[s.ID]
		if tl.Dub && len(ls) > 0 {
			// 逐句排开：开头留 0.3 秒，句间 0.25 秒；超出镜头时先压缩间隔，再整体加速（最多 1.5 倍）
			var speech float64
			for _, l := range ls {
				speech += l.Duration
			}
			gap := 0.25
			avail := ci.dur - 0.5
			if speech+gap*float64(len(ls)-1) > avail {
				gap = 0.08
			}
			tempo := 1.0
			if need := speech + gap*float64(len(ls)-1); need > avail && speech > 0 {
				tempo = need / avail
				if tempo > 1.5 {
					tempo = 1.5
				}
			}
			t := 0.3
			for _, l := range ls {
				d := l.Duration / tempo
				if l.AudioFile != "" {
					c.Lines = append(c.Lines, tlLine{L: l, Start: t, Dur: d, Tempo: tempo})
				}
				c.Subs = append(c.Subs, tlLine{L: l, Start: t, Dur: d})
				t += d + gap
			}
		} else {
			c.Subs = allocateNativeSubs(parseDialogue(s.Dialogue), ci.dur)
		}
		tl.Clips = append(tl.Clips, c)
		tl.Total += ci.dur
	}
	if missing > 0 {
		return nil, fmt.Errorf("还有 %d 个镜头没有视频", missing)
	}
	return tl, nil
}

func deliverDims(p *Project) (int, int) {
	if p.Deliver == "1080p" {
		switch {
		case ratioValue(p.Aspect) < 1:
			return 1080, 1920
		case ratioValue(p.Aspect) > 1:
			return 1920, 1080
		}
		return 1080, 1080
	}
	return videoDims(p.Aspect, "768p")
}

func handleFinalBuild(w http.ResponseWriter, r *http.Request) {
	p, e, err := epCtx(pathID(r))
	if err != nil {
		httpErr(w, 404, err.Error())
		return
	}
	var in struct {
		Subtitles *bool `json:"subtitles"`
	}
	_ = readJSON(r, &in)
	if in.Subtitles != nil {
		_, _ = db.Exec(`UPDATE episodes SET subtitles=? WHERE id=?`, b2i(*in.Subtitles), e.ID)
		e.Subtitles = *in.Subtitles
	}
	tid, err := finalTask(p, e)
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	writeJSON(w, map[string]any{"task_id": tid})
}

var subPunctRe = regexp.MustCompile(`[\s，。！？；：、,.!?;:“”"'‘’（）()\[\]【】—…-]+`)

// allocateNativeSubs 原声模式按每句台词有效字数比例分配字幕时段（避免长短句平均切分导致飘移）
func allocateNativeSubs(texts []parsedLine, dur float64) []tlLine {
	if len(texts) == 0 || dur <= 0 {
		return nil
	}
	weights := make([]float64, len(texts))
	totalW := 0.0
	for i, pl := range texts {
		clean := subPunctRe.ReplaceAllString(strings.TrimSpace(pl.text), "")
		w := float64(len([]rune(clean)))
		if w < 2 {
			w = 2
		}
		weights[i] = w
		totalW += w
	}
	gap := 0.06
	if len(texts) <= 1 || dur <= float64(len(texts))*0.2 {
		gap = 0
	}
	avail := dur - gap*float64(len(texts)-1)
	if avail <= 0 {
		avail = dur
		gap = 0
	}
	out := make([]tlLine, 0, len(texts))
	t := 0.0
	for i, pl := range texts {
		d := avail * (weights[i] / totalW)
		if d < 0.05 {
			d = 0.05
		}
		if t+d > dur {
			d = dur - t
		}
		out = append(out, tlLine{L: &Line{Text: pl.text, Speaker: pl.speaker}, Start: t, Dur: d})
		t += d + gap
	}
	return out
}

// subtitleStyle 按画幅与分辨率自适应计算 ASS 字幕字号与底部留白（修复竖屏在 PlayResY=H/2 下字体偏小约 44% 的问题）
func subtitleStyle(w, h int) (playW, playH, fontSize, marginV int) {
	playW, playH = w/2, h/2
	if playW < 100 {
		playW = 540
	}
	if playH < 100 {
		playH = 540
	}
	// 以画布高度为基准：横屏约 2.6% 高度（1080p -> PlayResY 540 -> FontSize 14），
	// 竖屏 PlayResY 更长、一行宽度更窄，按短边与纵横比折中，保证竖屏与横屏观感一致且不轻易折三行。
	if h > w {
		// 竖屏（如 1080x1920 -> 540x960；768x1344 -> 384x672）：字号约占短边宽度 4.4%（960 高下约 24px，768 竖屏约 17px）
		fontSize = int(float64(playW)*0.044 + 0.5)
		marginV = int(float64(playH)*0.08 + 0.5)
	} else {
		// 横屏 / 方形（如 1920x1080 -> 960x540）：字号约占高度 2.8%（540 高下约 15px）
		fontSize = int(float64(playH)*0.028 + 0.5)
		marginV = int(float64(playH)*0.075 + 0.5)
	}
	if fontSize < 12 {
		fontSize = 12
	}
	if marginV < 24 {
		marginV = 24
	}
	return
}

func finalTask(p *Project, e *Episode) (int64, error) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		return 0, errors.New("服务器上没有 ffmpeg")
	}
	tl, err := buildTimeline(p, e)
	if err != nil {
		return 0, err
	}
	return startTaskR(p.ID, e.ID, "final", "episode", e.ID, func(ctx context.Context, tid int64) error {
		name, err := buildFinal(ctx, tl)
		if err != nil {
			return err
		}
		old := e.FinalFile
		_, _ = db.Exec(`UPDATE episodes SET final_file=?, final_at=? WHERE id=?`, name, now(), e.ID)
		if old != "" && old != name {
			_ = os.Remove(filepath.Join(dataDir, "videos", old))
			_ = os.Remove(filepath.Join(dataDir, "videos", strings.TrimSuffix(old, ".mp4")+".srt"))
		}
		setTaskResult(tid, map[string]any{"episode_id": e.ID, "final_file": name})
		return nil
	})
}

func runFF(ctx context.Context, what string, args ...string) error {
	b, err := exec.CommandContext(ctx, "ffmpeg", append([]string{"-y", "-v", "error"}, args...)...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s失败：%s", what, lastLines(string(b), 3))
	}
	return nil
}

// buildFinal：逐镜头统一画面（可选 1080p lanczos 放大、烧录字幕）并混好镜头声（原声 × 音量 + 音效）
// → 拼接 → 叠加配音轨 → 配乐（循环、淡入淡出、对白时自动压低）
func buildFinal(ctx context.Context, tl *timeline) (string, error) {
	p, e := tl.P, tl.E
	tmp, err := os.MkdirTemp(filepath.Join(dataDir, "tmp"), "final-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)
	W, H := deliverDims(p)
	origVol := 1.0
	if tl.Dub {
		origVol = p.OrigVolume
	}
	var list strings.Builder
	var srt strings.Builder
	cue := 1
	for i, c := range tl.Clips {
		var clipSrt string
		if len(c.Subs) > 0 {
			var cs strings.Builder
			for k, sl := range c.Subs {
				a, b := sl.Start, sl.Start+sl.Dur
				if b > c.Info.dur {
					b = c.Info.dur
				}
				fmt.Fprintf(&cs, "%d\n%s --> %s\n%s\n\n", k+1, srtTime(a), srtTime(b), sl.L.Text)
				fmt.Fprintf(&srt, "%d\n%s --> %s\n%s\n\n", cue, srtTime(c.Start+a), srtTime(c.Start+b), sl.L.Text)
				cue++
			}
			clipSrt = filepath.Join(tmp, fmt.Sprintf("c%03d.srt", i))
			_ = os.WriteFile(clipSrt, []byte(cs.String()), 0o644)
		}
		vf := fmt.Sprintf("scale=%d:%d:force_original_aspect_ratio=decrease:flags=lanczos,pad=%d:%d:(ow-iw)/2:(oh-ih)/2:color=black,fps=24,format=yuv420p", W, H, W, H)
		if e.Subtitles && clipSrt != "" {
			pw, ph, fs, mv := subtitleStyle(W, H)
			style := fmt.Sprintf("FontName=Noto Sans CJK SC,FontSize=%d,PrimaryColour=&H00FFFFFF,OutlineColour=&H00000000,BorderStyle=1,Outline=1.2,Shadow=0,MarginV=%d", fs, mv)
			vf += ",subtitles=" + ffEscape(clipSrt) + ":original_size=" + fmt.Sprintf("%dx%d", pw, ph) + ":force_style='" + style + "'"
		}
		out := filepath.Join(tmp, fmt.Sprintf("c%03d.mp4", i))
		args := []string{"-i", c.Path}
		var af []string
		inputs := 1
		base := "[0:a]"
		if !c.Info.hasAudio {
			args = append(args, "-f", "lavfi", "-t", fmt.Sprintf("%.3f", c.Info.dur), "-i", "anullsrc=channel_layout=stereo:sample_rate=48000")
			base = fmt.Sprintf("[%d:a]", inputs)
			inputs++
		}
		af = append(af, fmt.Sprintf("%saresample=48000,aformat=channel_layouts=stereo,volume=%.3f[base]", base, origVol))
		mix := "[base]"
		n := 1
		if c.Shot.SfxFile != "" {
			args = append(args, "-i", filepath.Join(dataDir, "audio", c.Shot.SfxFile))
			af = append(af, fmt.Sprintf("[%d:a]aresample=48000,aformat=channel_layouts=stereo,volume=%.3f[sfx]", inputs, c.Shot.SfxVolume))
			inputs++
			mix += "[sfx]"
			n++
		}
		if n > 1 {
			af = append(af, fmt.Sprintf("%samix=inputs=%d:duration=first:normalize=0[aout]", mix, n))
		} else {
			af = append(af, "[base]anull[aout]")
		}
		args = append(args, "-filter_complex", strings.Join(af, ";"), "-vf", vf, "-map", "0:v:0", "-map", "[aout]", "-t", fmt.Sprintf("%.3f", c.Info.dur),
			"-c:v", "libx264", "-preset", "veryfast", "-crf", "19", "-c:a", "aac", "-b:a", "192k", "-ar", "48000", "-ac", "2", "-movflags", "+faststart", out)
		if err := runFF(ctx, fmt.Sprintf("处理镜头 %s ", c.Shot.Code), args...); err != nil {
			return "", err
		}
		fmt.Fprintf(&list, "file '%s'\n", out)
	}
	listFile := filepath.Join(tmp, "list.txt")
	_ = os.WriteFile(listFile, []byte(list.String()), 0o644)
	joined := filepath.Join(tmp, "joined.mp4")
	if err := runFF(ctx, "拼接", "-f", "concat", "-safe", "0", "-i", listFile, "-c", "copy", "-movflags", "+faststart", joined); err != nil {
		return "", err
	}
	name := fmt.Sprintf("final-%d-e%d-%s.mp4", p.ID, e.No, randHex(4))
	dst := filepath.Join(dataDir, "videos", name)

	// 配音轨
	voice := ""
	if tl.Dub {
		var vin []string
		var vf []string
		k := 0
		for _, c := range tl.Clips {
			for _, ln := range c.Lines {
				vin = append(vin, "-i", filepath.Join(dataDir, "audio", ln.L.AudioFile))
				f := fmt.Sprintf("[%d:a]aresample=48000,aformat=channel_layouts=stereo", k)
				if ln.Tempo > 1.001 {
					f += "," + atempoChain(ln.Tempo)
				}
				f += fmt.Sprintf(",adelay=delays=%d:all=1[v%d]", int((c.Start+ln.Start)*1000), k)
				vf = append(vf, f)
				k++
			}
		}
		if k > 0 {
			var ins strings.Builder
			for i := 0; i < k; i++ {
				fmt.Fprintf(&ins, "[v%d]", i)
			}
			vf = append(vf, fmt.Sprintf("%samix=inputs=%d:duration=longest:normalize=0,apad,atrim=0:%.3f[vo]", ins.String(), k, tl.Total))
			voice = filepath.Join(tmp, "voice.wav")
			args := append(vin, "-filter_complex", strings.Join(vf, ";"), "-map", "[vo]", "-ar", "48000", voice)
			if err := runFF(ctx, "合成配音轨", args...); err != nil {
				return "", err
			}
		}
	}
	var bgm *Bgm
	if e.BgmID > 0 {
		bgm, _ = getBgm(e.BgmID)
	}
	if voice == "" && bgm == nil {
		if err := os.Rename(joined, dst); err != nil {
			return "", err
		}
	} else {
		args := []string{"-i", joined}
		fc := []string{"[0:a]aresample=48000,aformat=channel_layouts=stereo[clip]"}
		mix := []string{"[clip]"}
		idx := 1
		key := ""
		if voice != "" {
			args = append(args, "-i", voice)
			if bgm != nil && e.BgmDuck {
				fc = append(fc, fmt.Sprintf("[%d:a]asplit=2[vo1][vk]", idx))
				key = "[vk]"
			} else {
				fc = append(fc, fmt.Sprintf("[%d:a]anull[vo1]", idx))
			}
			mix = append(mix, "[vo1]")
			idx++
		}
		if bgm != nil {
			args = append(args, "-stream_loop", "-1", "-i", filepath.Join(dataDir, "audio", bgm.File))
			f := fmt.Sprintf("[%d:a]aresample=48000,aformat=channel_layouts=stereo,atrim=0:%.3f,asetpts=N/SR/TB,volume=%.3f", idx, tl.Total, e.BgmVolume)
			if e.BgmFadeIn > 0 {
				f += fmt.Sprintf(",afade=t=in:st=0:d=%.2f", e.BgmFadeIn)
			}
			if e.BgmFadeOut > 0 && tl.Total > e.BgmFadeOut {
				f += fmt.Sprintf(",afade=t=out:st=%.3f:d=%.2f", tl.Total-e.BgmFadeOut, e.BgmFadeOut)
			}
			idx++
			if e.BgmDuck {
				if key == "" {
					// 原声模式：用镜头声（含 H3 对白）做压低的触发
					fc[0] = "[0:a]aresample=48000,aformat=channel_layouts=stereo,asplit=2[clip][ck]"
					key = "[ck]"
				}
				fc = append(fc, f+"[bg]", "[bg]"+key+"sidechaincompress=threshold=0.02:ratio=6:attack=15:release=450:makeup=1[bgd]")
				mix = append(mix, "[bgd]")
			} else {
				fc = append(fc, f+"[bgd]")
				mix = append(mix, "[bgd]")
			}
		}
		fc = append(fc, fmt.Sprintf("%samix=inputs=%d:duration=first:normalize=0,alimiter=limit=0.95[out]", strings.Join(mix, ""), len(mix)))
		args = append(args, "-filter_complex", strings.Join(fc, ";"), "-map", "0:v:0", "-map", "[out]", "-t", fmt.Sprintf("%.3f", tl.Total),
			"-c:v", "copy", "-c:a", "aac", "-b:a", "192k", "-movflags", "+faststart", dst)
		if err := runFF(ctx, "混音", args...); err != nil {
			return "", err
		}
	}
	if srt.Len() > 0 {
		_ = os.WriteFile(strings.TrimSuffix(dst, ".mp4")+".srt", []byte(srt.String()), 0o644)
	}
	return name, nil
}

func ffEscape(p string) string {
	r := strings.NewReplacer(`\`, `\\\\`, `:`, `\\:`, `'`, `\\\'`, `,`, `\,`, `[`, `\[`, `]`, `\]`)
	return r.Replace(p)
}

func lastLines(s string, n int) string {
	ls := strings.Split(strings.TrimSpace(s), "\n")
	if len(ls) > n {
		ls = ls[len(ls)-n:]
	}
	return strings.Join(ls, " ")
}

// ---------- 打包下载 ----------

var unsafeName = regexp.MustCompile(`[\\/:*?"<>|\s]+`)

func safeName(s string) string {
	s = unsafeName.ReplaceAllString(strings.TrimSpace(s), "_")
	if s == "" {
		return "未命名"
	}
	if r := []rune(s); len(r) > 40 {
		s = string(r[:40])
	}
	return s
}

type zipW struct{ *zip.Writer }

func (z zipW) add(name string, data []byte) {
	f, err := z.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate, Modified: time.Now()})
	if err == nil {
		_, _ = f.Write(data)
	}
}

func (z zipW) addFile(name, path string) {
	src, err := os.Open(path)
	if err != nil {
		return
	}
	defer src.Close()
	f, err := z.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Store, Modified: time.Now()})
	if err == nil {
		_, _ = io.Copy(f, src)
	}
}

func handleEpisodeExport(w http.ResponseWriter, r *http.Request) {
	p, e, err := epCtx(pathID(r))
	if err != nil {
		httpErr(w, 404, err.Error())
		return
	}
	assets := listAssets(p.ID)
	shots := listShots(e.ID, true)
	fname := safeName(p.Title) + fmt.Sprintf("_第%d集.zip", e.No)
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", "attachment; filename*=UTF-8''"+url.PathEscape(fname))
	z := zipW{zip.NewWriter(w)}
	defer z.Close()
	var md strings.Builder
	fmt.Fprintf(&md, "# %s · %s\n\n- 创意：%s\n- 本集梗概：%s\n- 风格：%s\n- 画幅：%s\n\n## 前情摘要\n\n%s\n\n---\n\n%s\n", p.Title, epLabel(e), p.Idea, e.Idea, p.Style, p.Aspect, e.Summary, e.Script)
	z.add("剧本.md", []byte(md.String()))
	if p.World+p.Bios+p.Outline != "" {
		z.add("设定集.md", []byte(fmt.Sprintf("# %s 设定集\n\n## 世界观\n\n%s\n\n## 人物小传\n\n%s\n\n## 分集总纲\n\n%s\n", p.Title, p.World, p.Bios, p.Outline)))
	}
	var buf bytes.Buffer
	buf.WriteString("\uFEFF")
	cw := csv.NewWriter(&buf)
	_ = cw.Write([]string{"镜头", "时长", "镜头语言", "画面", "台词", "资产", "衔接", "关键帧提示词", "视频描述"})
	chainName := map[string]string{"off": "", "ref": "参考上一镜", "first": "首尾帧"}
	for _, s := range shots {
		_ = cw.Write([]string{s.Code, strconv.FormatFloat(s.Duration, 'f', -1, 64), s.Camera, s.Action, s.Dialogue, strings.Join(s.Assets, " "), chainName[s.Chain], s.KeyframePrompt, s.VideoPrompt})
	}
	cw.Flush()
	z.add("分镜表.csv", buf.Bytes())
	kinds := map[string]string{"character": "角色", "scene": "场景", "prop": "道具"}
	tags := map[string]string{"": "设定图", "sheet": "三视图", "expr": "表情表"}
	for _, a := range assets {
		for k, im := range a.Images {
			tag := ""
			if im.ID == a.PickedImageID || im.ID == a.SheetImageID || im.ID == a.ExprImageID {
				tag = "_选定"
			}
			z.addFile(fmt.Sprintf("资产/%s_%s_%s/%s_%02d%s%s", a.Code, kinds[a.Kind], safeName(a.Name), tags[im.Tag], k+1, tag, filepath.Ext(im.File)), filepath.Join(dataDir, "images", im.File))
		}
	}
	for _, s := range shots {
		for _, im := range s.Images {
			if im.ID == s.PickedImageID {
				z.addFile(fmt.Sprintf("关键帧/%s%s", s.Code, filepath.Ext(im.File)), filepath.Join(dataDir, "images", im.File))
			}
		}
		if v := shotVideoFor(s); v != nil {
			z.addFile(fmt.Sprintf("镜头视频/%s%s", s.Code, filepath.Ext(v.useFile())), filepath.Join(dataDir, "videos", v.useFile()))
		}
	}
	for _, l := range listLines(`WHERE episode_id=? ORDER BY shot_id, sort`, e.ID) {
		if l.AudioFile != "" {
			code := ""
			for _, s := range shots {
				if s.ID == l.ShotID {
					code = s.Code
				}
			}
			z.addFile(fmt.Sprintf("配音/%s_%02d_%s%s", code, l.Sort+1, safeName(l.Speaker), filepath.Ext(l.AudioFile)), filepath.Join(dataDir, "audio", l.AudioFile))
		}
	}
	if e.CoverImageID > 0 {
		if im, err := getImage(e.CoverImageID); err == nil {
			z.addFile("封面"+filepath.Ext(im.File), filepath.Join(dataDir, "images", im.File))
		}
	}
	if e.FinalFile != "" {
		z.addFile("成片.mp4", filepath.Join(dataDir, "videos", e.FinalFile))
		z.addFile("字幕.srt", filepath.Join(dataDir, "videos", strings.TrimSuffix(e.FinalFile, ".mp4")+".srt"))
	}
}
