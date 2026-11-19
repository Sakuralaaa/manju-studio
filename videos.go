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

type Video struct {
	ID         int64   `json:"id"`
	ProjectID  int64   `json:"project_id"`
	ShotID     int64   `json:"shot_id"`
	Source     string  `json:"source"`
	Status     string  `json:"status"`
	Progress   float64 `json:"progress"`
	Stage      string  `json:"stage"`
	Prompt     string  `json:"prompt"`
	ImageFile  string  `json:"-"`
	ImageURL   string  `json:"image_url"`
	Seconds    int     `json:"seconds"`
	Res        string  `json:"res"`
	Aspect     string  `json:"aspect"`
	Turbo      bool    `json:"turbo"`
	Seed       int64   `json:"seed"`
	File       string  `json:"-"`
	URL        string  `json:"url"`
	Bytes      int64   `json:"bytes"`
	Error      string  `json:"error"`
	CreatedAt  int64   `json:"created_at"`
	StartedAt  int64   `json:"started_at"`
	FinishedAt int64   `json:"finished_at"`
	Position   int     `json:"position,omitempty"`
}

const videoCols = `id,project_id,shot_id,source,status,progress,stage,prompt,image_file,seconds,res,aspect,turbo,seed,file,bytes,error,created_at,started_at,finished_at`

func scanVideo(sc interface{ Scan(...any) error }) (*Video, error) {
	var v Video
	var turbo int
	if err := sc.Scan(&v.ID, &v.ProjectID, &v.ShotID, &v.Source, &v.Status, &v.Progress, &v.Stage, &v.Prompt, &v.ImageFile, &v.Seconds, &v.Res, &v.Aspect, &turbo, &v.Seed, &v.File, &v.Bytes, &v.Error, &v.CreatedAt, &v.StartedAt, &v.FinishedAt); err != nil {
		return nil, err
	}
	v.Turbo = turbo == 1
	if v.File != "" {
		v.URL = "/files/videos/" + v.File
	}
	if v.ImageFile != "" {
		v.ImageURL = "/files/images/" + v.ImageFile
	}
	if v.Status == "queued" {
		_ = db.QueryRow(`SELECT COUNT(*) FROM videos WHERE status='queued' AND id<?`, v.ID).Scan(&v.Position)
		v.Position++
	}
	return &v, nil
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

func scanVideoNoPos(sc interface{ Scan(...any) error }) (*Video, error) {
	var v Video
	var turbo int
	if err := sc.Scan(&v.ID, &v.ProjectID, &v.ShotID, &v.Source, &v.Status, &v.Progress, &v.Stage, &v.Prompt, &v.ImageFile, &v.Seconds, &v.Res, &v.Aspect, &turbo, &v.Seed, &v.File, &v.Bytes, &v.Error, &v.CreatedAt, &v.StartedAt, &v.FinishedAt); err != nil {
		return nil, err
	}
	v.Turbo = turbo == 1
	if v.File != "" {
		v.URL = "/files/videos/" + v.File
	}
	if v.ImageFile != "" {
		v.ImageURL = "/files/images/" + v.ImageFile
	}
	return &v, nil
}

type videoIn struct {
	ProjectID int64
	ShotID    int64
	Source    string
	Prompt    string
	ImageFile string
	Seconds   int
	Res       string
	Aspect    string
	Turbo     bool
	Seed      int64
}

func enqueueVideo(in videoIn) (int64, error) {
	if strings.TrimSpace(in.ImageFile) == "" {
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
	res, err := db.Exec(`INSERT INTO videos(project_id,shot_id,source,status,prompt,image_file,seconds,res,aspect,turbo,seed,created_at) VALUES(?,?,?, 'queued', ?,?,?,?,?,?,?,?)`,
		in.ProjectID, in.ShotID, in.Source, in.Prompt, in.ImageFile, in.Seconds, in.Res, in.Aspect, b2i(in.Turbo), in.Seed, now())
	if err != nil {
		return 0, err
	}
	runner.poke()
	return res.LastInsertId()
}

// shotVideoPrompt 视频描述：画风 + 运镜 + 动作 + 台词（H3 同时生成声音）
func shotVideoPrompt(p *Project, s *Shot) string {
	parts := []string{}
	if st := strings.TrimSpace(p.Style); st != "" {
		parts = append(parts, st+"风格")
	}
	if s.Camera != "" {
		parts = append(parts, "镜头："+s.Camera)
	}
	if s.VideoPrompt != "" {
		parts = append(parts, s.VideoPrompt)
	} else if s.Action != "" {
		parts = append(parts, s.Action)
	}
	if d := strings.TrimSpace(s.Dialogue); d != "" {
		parts = append(parts, "台词（中文配音，口型对上）："+d)
	}
	parts = append(parts, "画面稳定连贯，人物外貌与首帧保持一致，不出现字幕")
	return strings.Join(parts, "。")
}

func queueShot(p *Project, s *Shot, opts struct {
	Seconds int
	Turbo   *bool
	Res     string
}) (int64, error) {
	if s.PickedImageID == 0 {
		return 0, fmt.Errorf("镜头 %s 还没有关键帧", s.Code)
	}
	im, err := getImage(s.PickedImageID)
	if err != nil {
		return 0, errors.New("关键帧不存在")
	}
	var busy int
	_ = db.QueryRow(`SELECT COUNT(*) FROM videos WHERE shot_id=? AND status IN ('queued','running')`, s.ID).Scan(&busy)
	if busy > 0 {
		return 0, fmt.Errorf("镜头 %s 已在队列中", s.Code)
	}
	sec := opts.Seconds
	if sec == 0 {
		sec = int(s.Duration + 0.5)
	}
	turbo := p.VideoTurbo
	if opts.Turbo != nil {
		turbo = *opts.Turbo
	}
	res := p.VideoRes
	if opts.Res != "" {
		res = opts.Res
	}
	return enqueueVideo(videoIn{ProjectID: p.ID, ShotID: s.ID, Source: "project", Prompt: shotVideoPrompt(p, s), ImageFile: im.File, Seconds: sec, Res: res, Aspect: p.Aspect, Turbo: turbo})
}

func handleShotVideo(w http.ResponseWriter, r *http.Request) {
	s, err := getShot(pathID(r))
	if err != nil {
		httpErr(w, 404, "镜头不存在")
		return
	}
	p, _ := getProject(s.ProjectID)
	for _, x := range listShots(p.ID, false) {
		if x.ID == s.ID {
			s.Code = x.Code
		}
	}
	var in struct {
		Seconds int    `json:"seconds"`
		Turbo   *bool  `json:"turbo"`
		Res     string `json:"res"`
	}
	_ = readJSON(r, &in)
	id, err := queueShot(p, s, struct {
		Seconds int
		Turbo   *bool
		Res     string
	}{in.Seconds, in.Turbo, in.Res})
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	touchProject(p.ID)
	writeJSON(w, map[string]any{"id": id})
}

func handleProjectVideosQueue(w http.ResponseWriter, r *http.Request) {
	p, err := getProject(pathID(r))
	if err != nil {
		httpErr(w, 404, "作品不存在")
		return
	}
	var in struct {
		Mode  string `json:"mode"` // missing（默认）/ all
		Start bool   `json:"start"`
	}
	_ = readJSON(r, &in)
	n := 0
	skipped := 0
	for _, s := range listShots(p.ID, true) {
		hasDone := false
		busy := false
		for _, v := range s.Videos {
			if v.Status == "done" {
				hasDone = true
			}
			if v.Status == "queued" || v.Status == "running" {
				busy = true
			}
		}
		if busy || (in.Mode != "all" && hasDone) {
			continue
		}
		if s.PickedImageID == 0 {
			skipped++
			continue
		}
		if _, err := queueShot(p, s, struct {
			Seconds int
			Turbo   *bool
			Res     string
		}{}); err == nil {
			n++
		}
	}
	if n == 0 {
		if skipped > 0 {
			httpErr(w, 400, fmt.Sprintf("有 %d 个镜头还没有关键帧", skipped))
		} else {
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
	touchProject(p.ID)
	writeJSON(w, map[string]any{"queued": n, "skipped": skipped})
}

func handleVideoPick(w http.ResponseWriter, r *http.Request) {
	v, err := getVideo(pathID(r))
	if err != nil || v.Status != "done" {
		httpErr(w, 404, "视频不存在")
		return
	}
	_, _ = db.Exec(`UPDATE shots SET picked_video_id=? WHERE id=?`, v.ID, v.ShotID)
	touchProject(v.ProjectID)
	writeJSON(w, map[string]any{"ok": true})
}

func cancelVideo(id int64) error {
	v, err := getVideo(id)
	if err != nil {
		return errors.New("视频不存在")
	}
	switch v.Status {
	case "queued":
		_, _ = db.Exec(`UPDATE videos SET status='canceled', finished_at=? WHERE id=?`, now(), id)
	case "running":
		_, _ = db.Exec(`UPDATE videos SET status='canceled', finished_at=? WHERE id=?`, now(), id)
		runner.mu.Lock()
		if runner.CurrentJob == id && runner.cancelJob != nil {
			runner.cancelJob()
		}
		runner.mu.Unlock()
	default:
		// 已结束：删除记录和文件
		if v.File != "" {
			_ = os.Remove(filepath.Join(dataDir, "videos", v.File))
		}
		_, _ = db.Exec(`UPDATE shots SET picked_video_id=0 WHERE picked_video_id=?`, id)
		_, _ = db.Exec(`DELETE FROM videos WHERE id=?`, id)
	}
	touchProject(v.ProjectID)
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
	vs := listVideos(`WHERE status IN ('queued','running') OR finished_at>? ORDER BY CASE status WHEN 'running' THEN 0 WHEN 'queued' THEN 1 ELSE 2 END, id DESC LIMIT 60`, now()-24*3600_000)
	titles := map[int64]string{}
	for _, v := range vs {
		if v.ProjectID > 0 && titles[v.ProjectID] == "" {
			var t string
			_ = db.QueryRow(`SELECT title FROM projects WHERE id=?`, v.ProjectID).Scan(&t)
			titles[v.ProjectID] = t
		}
	}
	cfg := loadColabCfg()
	var queuedTurbo, queuedStd int
	_ = db.QueryRow(`SELECT COALESCE(SUM(turbo=1),0), COALESCE(SUM(turbo=0),0) FROM videos WHERE status='queued'`).Scan(&queuedTurbo, &queuedStd)
	mins := float64(queuedStd)*cfg.ClipMin + float64(queuedTurbo)*cfg.ClipMinTurbo
	writeJSON(w, map[string]any{"videos": vs, "titles": titles, "queue_minutes": mins})
}

// ---------- ⑦ 成片 ----------

type clipInfo struct {
	path     string
	dur      float64
	hasAudio bool
}

func probe(path string) (clipInfo, error) {
	out, err := exec.Command("ffprobe", "-v", "error", "-show_entries", "format=duration:stream=codec_type", "-of", "json", path).Output()
	if err != nil {
		return clipInfo{}, errors.New("读取视频信息失败")
	}
	var p struct {
		Format struct {
			Duration string `json:"duration"`
		} `json:"format"`
		Streams []struct {
			CodecType string `json:"codec_type"`
		} `json:"streams"`
	}
	_ = json.Unmarshal(out, &p)
	ci := clipInfo{path: path}
	ci.dur, _ = strconv.ParseFloat(p.Format.Duration, 64)
	for _, s := range p.Streams {
		if s.CodecType == "audio" {
			ci.hasAudio = true
		}
	}
	return ci, nil
}

var speakerRe = regexp.MustCompile(`^\s*[（(]?[^：:\n]{1,8}[)）]?\s*[：:]\s*`)

func subtitleLines(dialogue string) []string {
	var out []string
	for _, ln := range strings.Split(dialogue, "\n") {
		ln = strings.TrimSpace(speakerRe.ReplaceAllString(ln, ""))
		ln = strings.Trim(ln, "“”\"")
		if ln != "" {
			out = append(out, ln)
		}
	}
	return out
}

func srtTime(t float64) string {
	ms := int64(t*1000 + 0.5)
	return fmt.Sprintf("%02d:%02d:%02d,%03d", ms/3600000, ms/60000%60, ms/1000%60, ms%1000)
}

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

func handleFinalBuild(w http.ResponseWriter, r *http.Request) {
	p, err := getProject(pathID(r))
	if err != nil {
		httpErr(w, 404, "作品不存在")
		return
	}
	var in struct {
		Subtitles bool `json:"subtitles"`
	}
	_ = readJSON(r, &in)
	shots := listShots(p.ID, true)
	var clips []*Video
	missing := 0
	for _, s := range shots {
		v := shotVideoFor(s)
		if v == nil {
			missing++
			continue
		}
		clips = append(clips, v)
	}
	if len(shots) == 0 {
		httpErr(w, 400, "还没有分镜")
		return
	}
	if missing > 0 {
		httpErr(w, 400, fmt.Sprintf("还有 %d 个镜头没有视频", missing))
		return
	}
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		httpErr(w, 500, "服务器上没有 ffmpeg")
		return
	}
	tid, err := startTask(p.ID, "final", "project", p.ID, func(ctx context.Context) error {
		name, err := buildFinal(ctx, p, shots, clips, in.Subtitles)
		if err != nil {
			return err
		}
		old := p.FinalFile
		_, _ = db.Exec(`UPDATE projects SET final_file=?, final_at=? WHERE id=?`, name, now(), p.ID)
		if old != "" && old != name {
			_ = os.Remove(filepath.Join(dataDir, "videos", old))
			_ = os.Remove(filepath.Join(dataDir, "videos", strings.TrimSuffix(old, ".mp4")+".srt"))
		}
		return nil
	})
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	writeJSON(w, map[string]any{"task_id": tid})
}

func buildFinal(ctx context.Context, p *Project, shots []*Shot, clips []*Video, burn bool) (string, error) {
	tmp, err := os.MkdirTemp(filepath.Join(dataDir, "tmp"), "final-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)
	W, H := videoDims(p.Aspect, "768p")
	var list strings.Builder
	var srt strings.Builder
	t := 0.0
	cue := 1
	for i, v := range clips {
		src := filepath.Join(dataDir, "videos", v.File)
		ci, err := probe(src)
		if err != nil {
			return "", fmt.Errorf("镜头 %02d：%s", i+1, err.Error())
		}
		lines := subtitleLines(shots[i].Dialogue)
		var clipSrt string
		if len(lines) > 0 && ci.dur > 0 {
			seg := ci.dur / float64(len(lines))
			var cs strings.Builder
			for k, ln := range lines {
				a, b := float64(k)*seg, float64(k+1)*seg-0.05
				fmt.Fprintf(&cs, "%d\n%s --> %s\n%s\n\n", k+1, srtTime(a), srtTime(b), ln)
				fmt.Fprintf(&srt, "%d\n%s --> %s\n%s\n\n", cue, srtTime(t+a), srtTime(t+b), ln)
				cue++
			}
			clipSrt = filepath.Join(tmp, fmt.Sprintf("c%03d.srt", i))
			_ = os.WriteFile(clipSrt, []byte(cs.String()), 0o644)
		}
		t += ci.dur
		vf := fmt.Sprintf("scale=%d:%d:force_original_aspect_ratio=decrease,pad=%d:%d:(ow-iw)/2:(oh-ih)/2:color=black,fps=24,format=yuv420p", W, H, W, H)
		if burn && clipSrt != "" {
			style := "FontName=Noto Sans CJK SC,FontSize=14,PrimaryColour=&H00FFFFFF,OutlineColour=&H00000000,BorderStyle=1,Outline=1.2,Shadow=0,MarginV=40"
			vf += ",subtitles=" + ffEscape(clipSrt) + ":original_size=" + fmt.Sprintf("%dx%d", W/2, H/2) + ":force_style='" + style + "'"
		}
		out := filepath.Join(tmp, fmt.Sprintf("c%03d.mp4", i))
		args := []string{"-y", "-v", "error", "-i", src}
		if !ci.hasAudio {
			args = append(args, "-f", "lavfi", "-i", "anullsrc=channel_layout=stereo:sample_rate=48000")
		}
		args = append(args, "-vf", vf, "-map", "0:v:0")
		if ci.hasAudio {
			args = append(args, "-map", "0:a:0")
		} else {
			args = append(args, "-map", "1:a:0", "-shortest")
		}
		args = append(args, "-c:v", "libx264", "-preset", "veryfast", "-crf", "19", "-c:a", "aac", "-b:a", "160k", "-ar", "48000", "-ac", "2", "-movflags", "+faststart", out)
		if b, err := exec.CommandContext(ctx, "ffmpeg", args...).CombinedOutput(); err != nil {
			return "", fmt.Errorf("处理镜头 %02d 失败：%s", i+1, lastLines(string(b), 3))
		}
		fmt.Fprintf(&list, "file '%s'\n", out)
	}
	listFile := filepath.Join(tmp, "list.txt")
	_ = os.WriteFile(listFile, []byte(list.String()), 0o644)
	name := fmt.Sprintf("final-%d-%s.mp4", p.ID, randHex(4))
	dst := filepath.Join(dataDir, "videos", name)
	if b, err := exec.CommandContext(ctx, "ffmpeg", "-y", "-v", "error", "-f", "concat", "-safe", "0", "-i", listFile, "-c", "copy", "-movflags", "+faststart", dst).CombinedOutput(); err != nil {
		return "", fmt.Errorf("拼接失败：%s", lastLines(string(b), 3))
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

func handleProjectExport(w http.ResponseWriter, r *http.Request) {
	p, err := getProject(pathID(r))
	if err != nil {
		httpErr(w, 404, "作品不存在")
		return
	}
	assets := listAssets(p.ID)
	shots := listShots(p.ID, true)
	fname := safeName(p.Title) + ".zip"
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", "attachment; filename*=UTF-8''"+url.PathEscape(fname))
	zw := zip.NewWriter(w)
	defer zw.Close()
	add := func(name string, data []byte) {
		f, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate, Modified: time.Now()})
		if err == nil {
			_, _ = f.Write(data)
		}
	}
	addFile := func(name, path string) {
		src, err := os.Open(path)
		if err != nil {
			return
		}
		defer src.Close()
		f, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Store, Modified: time.Now()})
		if err == nil {
			_, _ = io.Copy(f, src)
		}
	}
	// 设定 + 剧本
	var md strings.Builder
	fmt.Fprintf(&md, "# %s\n\n- 创意：%s\n- 风格：%s\n- 画幅：%s\n- 单集时长：%d 秒\n\n---\n\n%s\n", p.Title, p.Idea, p.Style, p.Aspect, p.EpisodeSec, p.Script)
	add("剧本.md", []byte(md.String()))
	// 分镜表
	var buf bytes.Buffer
	buf.WriteString("\uFEFF")
	cw := csv.NewWriter(&buf)
	_ = cw.Write([]string{"镜头", "时长", "镜头语言", "画面", "台词", "资产", "关键帧提示词", "视频描述"})
	for _, s := range shots {
		_ = cw.Write([]string{s.Code, strconv.FormatFloat(s.Duration, 'f', -1, 64), s.Camera, s.Action, s.Dialogue, strings.Join(s.Assets, " "), s.KeyframePrompt, s.VideoPrompt})
	}
	cw.Flush()
	add("分镜表.csv", buf.Bytes())
	kinds := map[string]string{"character": "角色", "scene": "场景", "prop": "道具"}
	for _, a := range assets {
		for k, im := range a.Images {
			tag := ""
			if im.ID == a.PickedImageID {
				tag = "_选定"
			}
			addFile(fmt.Sprintf("资产/%s_%s_%s/%02d%s%s", a.Code, kinds[a.Kind], safeName(a.Name), k+1, tag, filepath.Ext(im.File)), filepath.Join(dataDir, "images", im.File))
		}
	}
	for _, s := range shots {
		for _, im := range s.Images {
			if im.ID == s.PickedImageID {
				addFile(fmt.Sprintf("关键帧/%s%s", s.Code, filepath.Ext(im.File)), filepath.Join(dataDir, "images", im.File))
			}
		}
		if v := shotVideoFor(s); v != nil {
			addFile(fmt.Sprintf("镜头视频/%s%s", s.Code, filepath.Ext(v.File)), filepath.Join(dataDir, "videos", v.File))
		}
	}
	if p.FinalFile != "" {
		addFile("成片.mp4", filepath.Join(dataDir, "videos", p.FinalFile))
		addFile("字幕.srt", filepath.Join(dataDir, "videos", strings.TrimSuffix(p.FinalFile, ".mp4")+".srt"))
	}
}
