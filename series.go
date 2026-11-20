package main

// 剧集：作品 = 设定集（世界观 / 人物小传 / 总纲）+ 多集；资产库跨集共享，每集走七步流程。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

type Project struct {
	ID             int64     `json:"id"`
	Title          string    `json:"title"`
	Idea           string    `json:"idea"`
	Style          string    `json:"style"`
	Kind           string    `json:"kind"`
	Aspect         string    `json:"aspect"`
	EpisodeSec     int       `json:"episode_sec"`
	ShotTarget     int       `json:"shot_target"`
	TextChannelID  int64     `json:"text_channel_id"`
	TextModel      string    `json:"text_model"`
	ImageChannelID int64     `json:"image_channel_id"`
	ImageModel     string    `json:"image_model"`
	VideoSec       int       `json:"video_sec"`
	VideoRes       string    `json:"video_res"`
	VideoTurbo     bool      `json:"video_turbo"`
	World          string    `json:"world"`
	Bios           string    `json:"bios"`
	Outline        string    `json:"outline"`
	EpisodesPlan   int       `json:"episodes_plan"`
	AudioMode      string    `json:"audio_mode"`
	OrigVolume     float64   `json:"orig_volume"`
	Takes          int       `json:"takes"`
	NarratorVoice  VoiceSpec `json:"narrator_voice"`
	VoiceChannelID int64     `json:"voice_channel_id"`
	Deliver        string    `json:"deliver"`
	PostUpscale    bool      `json:"post_upscale"`
	PostInterp     bool      `json:"post_interp"`
	CreatedAt      int64     `json:"created_at"`
	UpdatedAt      int64     `json:"updated_at"`
}

const projCols = `id,title,idea,style,aspect,episode_sec,shot_target,text_channel_id,text_model,image_channel_id,image_model,video_sec,video_res,video_turbo,world,bios,outline,episodes_plan,audio_mode,orig_volume,takes,narrator_voice,voice_channel_id,deliver,post_upscale,post_interp,kind,created_at,updated_at`

func scanProject(sc interface{ Scan(...any) error }) (*Project, error) {
	var p Project
	var turbo, up, interp int
	var nv string
	if err := sc.Scan(&p.ID, &p.Title, &p.Idea, &p.Style, &p.Aspect, &p.EpisodeSec, &p.ShotTarget, &p.TextChannelID, &p.TextModel, &p.ImageChannelID, &p.ImageModel, &p.VideoSec, &p.VideoRes, &turbo,
		&p.World, &p.Bios, &p.Outline, &p.EpisodesPlan, &p.AudioMode, &p.OrigVolume, &p.Takes, &nv, &p.VoiceChannelID, &p.Deliver, &up, &interp, &p.Kind, &p.CreatedAt, &p.UpdatedAt); err != nil {
		return nil, err
	}
	p.VideoTurbo, p.PostUpscale, p.PostInterp = turbo == 1, up == 1, interp == 1
	_ = json.Unmarshal([]byte(nv), &p.NarratorVoice)
	if p.Takes < 1 {
		p.Takes = 1
	}
	return &p, nil
}

func getProject(id int64) (*Project, error) {
	return scanProject(db.QueryRow(`SELECT `+projCols+` FROM projects WHERE id=?`, id))
}

func listProjects() []*Project {
	rows, err := db.Query(`SELECT ` + projCols + ` FROM projects ORDER BY updated_at DESC`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var ps []*Project
	for rows.Next() {
		if p, err := scanProject(rows); err == nil {
			ps = append(ps, p)
		}
	}
	return ps
}

func touchProject(id int64) {
	if id > 0 {
		_, _ = db.Exec(`UPDATE projects SET updated_at=? WHERE id=?`, now(), id)
	}
}

// ---------- 集 ----------

type Episode struct {
	ID           int64   `json:"id"`
	ProjectID    int64   `json:"project_id"`
	No           int     `json:"no"`
	Title        string  `json:"title"`
	Idea         string  `json:"idea"`
	Script       string  `json:"script"`
	Summary      string  `json:"summary"`
	Cliffhanger  string  `json:"cliffhanger"`
	FinalFile    string  `json:"final_file"`
	FinalURL     string  `json:"final_url"`
	FinalAt      int64   `json:"final_at"`
	CoverImageID int64   `json:"cover_image_id"`
	CoverURL     string  `json:"cover_url"`
	BgmID        int64   `json:"bgm_id"`
	BgmVolume    float64 `json:"bgm_volume"`
	BgmFadeIn    float64 `json:"bgm_fade_in"`
	BgmFadeOut   float64 `json:"bgm_fade_out"`
	BgmDuck      bool    `json:"bgm_duck"`
	Subtitles    bool    `json:"subtitles"`
	CreatedAt    int64   `json:"created_at"`
	UpdatedAt    int64   `json:"updated_at"`
}

const epCols = `id,project_id,no,title,idea,script,summary,cliffhanger,final_file,final_at,cover_image_id,bgm_id,bgm_volume,bgm_fade_in,bgm_fade_out,bgm_duck,subtitles,created_at,updated_at`

func scanEpisode(sc interface{ Scan(...any) error }) (*Episode, error) {
	var e Episode
	var duck, subs int
	if err := sc.Scan(&e.ID, &e.ProjectID, &e.No, &e.Title, &e.Idea, &e.Script, &e.Summary, &e.Cliffhanger, &e.FinalFile, &e.FinalAt, &e.CoverImageID, &e.BgmID, &e.BgmVolume, &e.BgmFadeIn, &e.BgmFadeOut, &duck, &subs, &e.CreatedAt, &e.UpdatedAt); err != nil {
		return nil, err
	}
	e.BgmDuck, e.Subtitles = duck == 1, subs == 1
	if e.FinalFile != "" {
		e.FinalURL = "/files/videos/" + e.FinalFile
	}
	if e.CoverImageID > 0 {
		if im, err := getImage(e.CoverImageID); err == nil {
			e.CoverURL = im.URL
		}
	}
	return &e, nil
}

func getEpisode(id int64) (*Episode, error) {
	return scanEpisode(db.QueryRow(`SELECT `+epCols+` FROM episodes WHERE id=?`, id))
}

func listEpisodes(pid int64) []*Episode {
	rows, err := db.Query(`SELECT `+epCols+` FROM episodes WHERE project_id=? ORDER BY no, id`, pid)
	if err != nil {
		return []*Episode{}
	}
	defer rows.Close()
	out := []*Episode{}
	for rows.Next() {
		if e, err := scanEpisode(rows); err == nil {
			out = append(out, e)
		}
	}
	return out
}

func epLabel(e *Episode) string {
	if strings.TrimSpace(e.Title) != "" {
		return fmt.Sprintf("第 %d 集 · %s", e.No, e.Title)
	}
	return fmt.Sprintf("第 %d 集", e.No)
}

func touchEpisode(id int64) {
	if id <= 0 {
		return
	}
	_, _ = db.Exec(`UPDATE episodes SET updated_at=? WHERE id=?`, now(), id)
	var pid int64
	_ = db.QueryRow(`SELECT project_id FROM episodes WHERE id=?`, id).Scan(&pid)
	touchProject(pid)
}

// epCtx 按集编号取作品与集
func epCtx(eid int64) (*Project, *Episode, error) {
	e, err := getEpisode(eid)
	if err != nil {
		return nil, nil, errors.New("这一集不存在")
	}
	p, err := getProject(e.ProjectID)
	if err != nil {
		return nil, nil, errors.New("作品不存在")
	}
	return p, e, nil
}

func insertEpisode(pid int64, idea string) (*Episode, error) {
	var maxNo int
	_ = db.QueryRow(`SELECT COALESCE(MAX(no),0) FROM episodes WHERE project_id=?`, pid).Scan(&maxNo)
	res, err := db.Exec(`INSERT INTO episodes(project_id,no,idea,created_at,updated_at) VALUES(?,?,?,?,?)`, pid, maxNo+1, strings.TrimSpace(idea), now(), now())
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	touchProject(pid)
	return getEpisode(id)
}

var cliffRe = regexp.MustCompile(`【卡点】[：:\s]*(.+)`)

// cliffOf 从剧本里取「【卡点】」那一行
func cliffOf(script string) string {
	m := cliffRe.FindAllStringSubmatch(script, -1)
	if len(m) == 0 {
		return ""
	}
	return strings.TrimSpace(m[len(m)-1][1])
}

// ---------- 步骤 ----------

type Steps struct {
	Done      []bool `json:"done"`
	Assets    int    `json:"assets"`
	AssetsOK  int    `json:"assets_ok"`
	Shots     int    `json:"shots"`
	Keyframes int    `json:"keyframes"`
	Videos    int    `json:"videos"`
	Queued    int    `json:"queued"`
	Lines     int    `json:"lines"`
	LinesOK   int    `json:"lines_ok"`
}

func episodeSteps(p *Project, e *Episode) Steps {
	var s Steps
	_ = db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(picked_image_id>0),0) FROM assets WHERE project_id=?`, p.ID).Scan(&s.Assets, &s.AssetsOK)
	_ = db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(picked_image_id>0),0) FROM shots WHERE episode_id=?`, e.ID).Scan(&s.Shots, &s.Keyframes)
	_ = db.QueryRow(`SELECT COUNT(DISTINCT shot_id) FROM videos WHERE episode_id=? AND status='done' AND shot_id IN (SELECT id FROM shots WHERE episode_id=?)`, e.ID, e.ID).Scan(&s.Videos)
	_ = db.QueryRow(`SELECT COUNT(*) FROM videos WHERE episode_id=? AND status IN ('queued','running')`, e.ID).Scan(&s.Queued)
	_ = db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(status='done'),0) FROM lines WHERE episode_id=?`, e.ID).Scan(&s.Lines, &s.LinesOK)
	s.Done = []bool{
		p.Title != "" && (strings.TrimSpace(e.Idea) != "" || strings.TrimSpace(p.Idea) != ""),
		strings.TrimSpace(e.Script) != "",
		s.Assets > 0 && s.AssetsOK == s.Assets,
		s.Shots > 0,
		s.Shots > 0 && s.Keyframes == s.Shots,
		s.Shots > 0 && s.Videos == s.Shots,
		e.FinalFile != "",
	}
	return s
}

// ---------- 作品列表 / 创建 / 详情 ----------

type ProjectCard struct {
	*Project
	Cover    string `json:"cover"`
	Episodes int    `json:"episodes"`
	Finished int    `json:"finished"`
	Assets   int    `json:"assets"`
	Steps    []bool `json:"steps"`
	NextEp   int64  `json:"next_episode"`
	NextNo   int    `json:"next_no"`
}

func projectCover(pid int64) string {
	var file string
	_ = db.QueryRow(`SELECT i.file FROM episodes e JOIN images i ON i.id=e.cover_image_id WHERE e.project_id=? ORDER BY e.no LIMIT 1`, pid).Scan(&file)
	if file == "" {
		_ = db.QueryRow(`SELECT i.file FROM shots s JOIN images i ON i.id=s.picked_image_id WHERE s.project_id=? ORDER BY s.episode_id, s.sort, s.id LIMIT 1`, pid).Scan(&file)
	}
	if file == "" {
		_ = db.QueryRow(`SELECT i.file FROM assets a JOIN images i ON i.id=a.picked_image_id WHERE a.project_id=? ORDER BY CASE a.kind WHEN 'scene' THEN 0 WHEN 'character' THEN 1 ELSE 2 END, a.id LIMIT 1`, pid).Scan(&file)
	}
	if file != "" {
		return "/files/images/" + file
	}
	return ""
}

func handleProjectList(w http.ResponseWriter, r *http.Request) {
	out := []*ProjectCard{}
	for _, p := range listProjects() {
		c := &ProjectCard{Project: p, Cover: projectCover(p.ID)}
		eps := listEpisodes(p.ID)
		c.Episodes = len(eps)
		_ = db.QueryRow(`SELECT COUNT(*) FROM assets WHERE project_id=?`, p.ID).Scan(&c.Assets)
		for _, e := range eps {
			if e.FinalFile != "" {
				c.Finished++
			}
		}
		// 当前集：第一个没合成的，否则最后一集
		var cur *Episode
		for _, e := range eps {
			if e.FinalFile == "" {
				cur = e
				break
			}
		}
		if cur == nil && len(eps) > 0 {
			cur = eps[len(eps)-1]
		}
		if cur != nil {
			c.Steps = episodeSteps(p, cur).Done
			c.NextEp, c.NextNo = cur.ID, cur.No
		} else {
			c.Steps = make([]bool, 7)
		}
		out = append(out, c)
	}
	writeJSON(w, out)
}

type projectIn struct {
	Title          *string    `json:"title"`
	Idea           *string    `json:"idea"`
	Style          *string    `json:"style"`
	Kind           *string    `json:"kind"`
	Aspect         *string    `json:"aspect"`
	EpisodeSec     *int       `json:"episode_sec"`
	ShotTarget     *int       `json:"shot_target"`
	TextChannelID  *int64     `json:"text_channel_id"`
	TextModel      *string    `json:"text_model"`
	ImageChannelID *int64     `json:"image_channel_id"`
	ImageModel     *string    `json:"image_model"`
	VideoSec       *int       `json:"video_sec"`
	VideoRes       *string    `json:"video_res"`
	VideoTurbo     *bool      `json:"video_turbo"`
	World          *string    `json:"world"`
	Bios           *string    `json:"bios"`
	Outline        *string    `json:"outline"`
	EpisodesPlan   *int       `json:"episodes_plan"`
	AudioMode      *string    `json:"audio_mode"`
	OrigVolume     *float64   `json:"orig_volume"`
	Takes          *int       `json:"takes"`
	NarratorVoice  *VoiceSpec `json:"narrator_voice"`
	VoiceChannelID *int64     `json:"voice_channel_id"`
	Deliver        *string    `json:"deliver"`
	PostUpscale    *bool      `json:"post_upscale"`
	PostInterp     *bool      `json:"post_interp"`
}

func handleProjectCreate(w http.ResponseWriter, r *http.Request) {
	var in projectIn
	_ = readJSON(r, &in)
	title := "未命名作品"
	if in.Title != nil && strings.TrimSpace(*in.Title) != "" {
		title = strings.TrimSpace(*in.Title)
	}
	res, err := db.Exec(`INSERT INTO projects(title,created_at,updated_at) VALUES(?,?,?)`, title, now(), now())
	if err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	id, _ := res.LastInsertId()
	if err := applyProject(id, in); err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	e, _ := insertEpisode(id, "")
	p, _ := getProject(id)
	writeJSON(w, map[string]any{"project": p, "episode": e, "id": id})
}

func applyProject(id int64, in projectIn) error {
	set := func(col string, v any) { _, _ = db.Exec(`UPDATE projects SET `+col+`=? WHERE id=?`, v, id) }
	if in.Title != nil {
		t := strings.TrimSpace(*in.Title)
		if t == "" {
			return errors.New("标题不能为空")
		}
		set("title", t)
	}
	str := func(col string, v *string) {
		if v != nil {
			set(col, strings.TrimSpace(*v))
		}
	}
	str("idea", in.Idea)
	str("style", in.Style)
	if in.Kind != nil {
		set("kind", normKind(*in.Kind))
	}
	str("world", in.World)
	str("bios", in.Bios)
	str("outline", in.Outline)
	if in.Aspect != nil {
		a := *in.Aspect
		if a != "9:16" && a != "16:9" && a != "1:1" && a != "3:4" && a != "4:3" {
			a = "9:16"
		}
		set("aspect", a)
	}
	if in.EpisodeSec != nil {
		set("episode_sec", clampInt(*in.EpisodeSec, 10, 600))
	}
	if in.ShotTarget != nil {
		set("shot_target", clampInt(*in.ShotTarget, 1, 60))
	}
	if in.EpisodesPlan != nil {
		set("episodes_plan", clampInt(*in.EpisodesPlan, 1, 200))
	}
	if in.TextChannelID != nil {
		set("text_channel_id", *in.TextChannelID)
	}
	if in.TextModel != nil {
		set("text_model", *in.TextModel)
	}
	if in.ImageChannelID != nil {
		set("image_channel_id", *in.ImageChannelID)
	}
	if in.ImageModel != nil {
		set("image_model", *in.ImageModel)
	}
	if in.VoiceChannelID != nil {
		set("voice_channel_id", *in.VoiceChannelID)
	}
	if in.VideoSec != nil {
		set("video_sec", clampInt(*in.VideoSec, 5, 10))
	}
	if in.VideoRes != nil {
		v := *in.VideoRes
		if v != "480p" && v != "768p" {
			v = "768p"
		}
		set("video_res", v)
	}
	if in.VideoTurbo != nil {
		set("video_turbo", b2i(*in.VideoTurbo))
	}
	if in.AudioMode != nil {
		m := *in.AudioMode
		if m != "dub" {
			m = "native"
		}
		set("audio_mode", m)
	}
	if in.OrigVolume != nil {
		set("orig_volume", clampF(*in.OrigVolume, 0, 1))
	}
	if in.Takes != nil {
		set("takes", clampInt(*in.Takes, 1, 4))
	}
	if in.NarratorVoice != nil {
		set("narrator_voice", mustJSON(in.NarratorVoice))
	}
	if in.Deliver != nil {
		d := *in.Deliver
		if d != "1080p" {
			d = "source"
		}
		set("deliver", d)
	}
	if in.PostUpscale != nil {
		set("post_upscale", b2i(*in.PostUpscale))
	}
	if in.PostInterp != nil {
		set("post_interp", b2i(*in.PostInterp))
	}
	touchProject(id)
	return nil
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func clampF(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func handleProjectUpdate(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	if _, err := getProject(id); err != nil {
		httpErr(w, 404, "作品不存在")
		return
	}
	var in projectIn
	if err := readJSON(r, &in); err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	if err := applyProject(id, in); err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	p, _ := getProject(id)
	writeJSON(w, p)
}

func handleProjectDelete(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	for _, im := range listImages(`WHERE project_id=?`, id) {
		deleteImageRow(im)
	}
	for _, v := range listVideos(`WHERE project_id=? AND status IN ('done','failed','canceled')`, id) {
		if v.File != "" {
			_ = os.Remove(filepath.Join(dataDir, "videos", v.File))
		}
	}
	_, _ = db.Exec(`UPDATE videos SET status='canceled' WHERE project_id=? AND status='queued'`, id)
	for _, e := range listEpisodes(id) {
		removeEpisodeFiles(e)
	}
	for _, q := range []string{`DELETE FROM assets WHERE project_id=?`, `DELETE FROM shots WHERE project_id=?`, `DELETE FROM videos WHERE project_id=? AND status<>'running'`,
		`DELETE FROM tasks WHERE project_id=?`, `DELETE FROM lines WHERE project_id=?`, `DELETE FROM pipelines WHERE episode_id IN (SELECT id FROM episodes WHERE project_id=?)`, `DELETE FROM episodes WHERE project_id=?`, `DELETE FROM projects WHERE id=?`} {
		_, _ = db.Exec(q, id)
	}
	writeJSON(w, map[string]any{"ok": true})
}

func removeEpisodeFiles(e *Episode) {
	if e.FinalFile != "" {
		_ = os.Remove(filepath.Join(dataDir, "videos", e.FinalFile))
		_ = os.Remove(filepath.Join(dataDir, "videos", strings.TrimSuffix(e.FinalFile, ".mp4")+".srt"))
	}
	rows, _ := db.Query(`SELECT audio_file FROM lines WHERE episode_id=? AND audio_file<>''`, e.ID)
	if rows != nil {
		for rows.Next() {
			var f string
			if rows.Scan(&f) == nil {
				_ = os.Remove(filepath.Join(dataDir, "audio", f))
			}
		}
		rows.Close()
	}
}

// 作品总览：设定集 + 各集进度
func handleProjectGet(w http.ResponseWriter, r *http.Request) {
	p, err := getProject(pathID(r))
	if err != nil {
		httpErr(w, 404, "作品不存在")
		return
	}
	type epCard struct {
		*Episode
		Steps    Steps   `json:"steps"`
		Duration float64 `json:"duration"`
		Thumb    string  `json:"thumb"`
		Pipeline *Pipe   `json:"pipeline"`
	}
	eps := []epCard{}
	for _, e := range listEpisodes(p.ID) {
		c := epCard{Episode: e, Steps: episodeSteps(p, e), Pipeline: latestPipe(e.ID)}
		e.Script = ""
		_ = db.QueryRow(`SELECT COALESCE(SUM(duration),0) FROM shots WHERE episode_id=?`, e.ID).Scan(&c.Duration)
		var file string
		_ = db.QueryRow(`SELECT i.file FROM shots s JOIN images i ON i.id=s.picked_image_id WHERE s.episode_id=? ORDER BY s.sort LIMIT 1`, e.ID).Scan(&file)
		if e.CoverURL != "" {
			c.Thumb = e.CoverURL
		} else if file != "" {
			c.Thumb = "/files/images/" + file
		}
		eps = append(eps, c)
	}
	writeJSON(w, map[string]any{
		"project":  p,
		"episodes": eps,
		"assets":   listAssets(p.ID),
		"tasks":    listTasks(p.ID),
		"ledger":   ledgerSummary(p.ID),
	})
}

// 单集工作台
func handleEpisodeGet(w http.ResponseWriter, r *http.Request) {
	p, e, err := epCtx(pathID(r))
	if err != nil {
		httpErr(w, 404, err.Error())
		return
	}
	eps := []map[string]any{}
	for _, x := range listEpisodes(p.ID) {
		eps = append(eps, map[string]any{"id": x.ID, "no": x.No, "title": x.Title, "done": x.FinalFile != ""})
	}
	writeJSON(w, map[string]any{
		"project":  p,
		"episode":  e,
		"episodes": eps,
		"assets":   listAssets(p.ID),
		"shots":    listShots(e.ID, true),
		"lines":    listLines(`WHERE episode_id=? ORDER BY shot_id, sort, id`, e.ID),
		"tasks":    listTasks(p.ID),
		"steps":    episodeSteps(p, e),
		"colab":    runner.brief(),
		"pipeline": latestPipe(e.ID),
	})
}

type episodeIn struct {
	Title      *string  `json:"title"`
	Idea       *string  `json:"idea"`
	Script     *string  `json:"script"`
	Summary    *string  `json:"summary"`
	Cliff      *string  `json:"cliffhanger"`
	BgmID      *int64   `json:"bgm_id"`
	BgmVolume  *float64 `json:"bgm_volume"`
	BgmFadeIn  *float64 `json:"bgm_fade_in"`
	BgmFadeOut *float64 `json:"bgm_fade_out"`
	BgmDuck    *bool    `json:"bgm_duck"`
	Subtitles  *bool    `json:"subtitles"`
}

func handleEpisodeUpdate(w http.ResponseWriter, r *http.Request) {
	_, e, err := epCtx(pathID(r))
	if err != nil {
		httpErr(w, 404, err.Error())
		return
	}
	var in episodeIn
	if err := readJSON(r, &in); err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	set := func(col string, v any) { _, _ = db.Exec(`UPDATE episodes SET `+col+`=? WHERE id=?`, v, e.ID) }
	if in.Title != nil {
		set("title", strings.TrimSpace(*in.Title))
	}
	if in.Idea != nil {
		set("idea", strings.TrimSpace(*in.Idea))
	}
	if in.Script != nil {
		set("script", *in.Script)
		if c := cliffOf(*in.Script); c != "" {
			set("cliffhanger", c)
		}
	}
	if in.Summary != nil {
		set("summary", strings.TrimSpace(*in.Summary))
	}
	if in.Cliff != nil {
		set("cliffhanger", strings.TrimSpace(*in.Cliff))
	}
	if in.BgmID != nil {
		set("bgm_id", *in.BgmID)
	}
	if in.BgmVolume != nil {
		set("bgm_volume", clampF(*in.BgmVolume, 0, 1))
	}
	if in.BgmFadeIn != nil {
		set("bgm_fade_in", clampF(*in.BgmFadeIn, 0, 15))
	}
	if in.BgmFadeOut != nil {
		set("bgm_fade_out", clampF(*in.BgmFadeOut, 0, 15))
	}
	if in.BgmDuck != nil {
		set("bgm_duck", b2i(*in.BgmDuck))
	}
	if in.Subtitles != nil {
		set("subtitles", b2i(*in.Subtitles))
	}
	touchEpisode(e.ID)
	e, _ = getEpisode(e.ID)
	writeJSON(w, e)
}

func handleEpisodeDelete(w http.ResponseWriter, r *http.Request) {
	_, e, err := epCtx(pathID(r))
	if err != nil {
		httpErr(w, 404, err.Error())
		return
	}
	var n int
	_ = db.QueryRow(`SELECT COUNT(*) FROM episodes WHERE project_id=?`, e.ProjectID).Scan(&n)
	if n <= 1 {
		httpErr(w, 400, "至少保留一集")
		return
	}
	cancelPipe(e.ID)
	for _, s := range listShots(e.ID, false) {
		for _, im := range listImages(`WHERE owner_type='shot' AND owner_id=?`, s.ID) {
			deleteImageRow(im)
		}
	}
	for _, v := range listVideos(`WHERE episode_id=? AND status IN ('done','failed','canceled')`, e.ID) {
		if v.File != "" {
			_ = os.Remove(filepath.Join(dataDir, "videos", v.File))
		}
	}
	_, _ = db.Exec(`UPDATE videos SET status='canceled', finished_at=? WHERE episode_id=? AND status='queued'`, now(), e.ID)
	removeEpisodeFiles(e)
	for _, q := range []string{`DELETE FROM shots WHERE episode_id=?`, `DELETE FROM videos WHERE episode_id=? AND status<>'running'`, `DELETE FROM lines WHERE episode_id=?`, `DELETE FROM pipelines WHERE episode_id=?`, `DELETE FROM episodes WHERE id=?`} {
		_, _ = db.Exec(q, e.ID)
	}
	// 重新编号
	for i, x := range listEpisodes(e.ProjectID) {
		_, _ = db.Exec(`UPDATE episodes SET no=? WHERE id=?`, i+1, x.ID)
	}
	touchProject(e.ProjectID)
	writeJSON(w, map[string]any{"ok": true})
}

// ---------- 设定集 ----------

const bibleSystem = `你是连载竖屏漫剧的总编剧。根据作品创意写出整部剧的设定集，供后续逐集写剧本时反复参考。
- world：世界观，时代、地点、社会规则、核心冲突的来源，200–400 字；
- bios：人物小传，主角 1–2 人、重要配角不超过 5 人，每人一段：姓名、年龄、外貌与常穿服装（要具体，便于做角色设定图）、性格、动机、与其他人的关系、说话方式；
- outline：分集总纲，按「第 N 集：一句话剧情 + 本集卡点」逐行列出，集数按要求，整体有清晰的主线、阶段性高潮和反转。
只输出 JSON：{"world":"","bios":"","outline":""}`

func handleBibleGenerate(w http.ResponseWriter, r *http.Request) {
	p, err := getProject(pathID(r))
	if err != nil {
		httpErr(w, 404, "作品不存在")
		return
	}
	var in struct {
		Note string `json:"note"`
	}
	_ = readJSON(r, &in)
	if strings.TrimSpace(p.Idea) == "" {
		httpErr(w, 400, "先写下故事创意")
		return
	}
	ch, model, err := textChannelFor(p)
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	user := fmt.Sprintf("剧名：%s\n故事创意：%s\n画面风格：%s\n计划集数：%d 集，每集约 %d 秒\n", p.Title, p.Idea, orDefault(p.Style, "不限"), p.EpisodesPlan, p.EpisodeSec)
	if p.World+p.Bios+p.Outline != "" {
		user += "\n现有设定（在此基础上完善，保留已确定的内容）：\n世界观：" + p.World + "\n人物小传：" + p.Bios + "\n总纲：" + p.Outline + "\n"
	}
	if strings.TrimSpace(in.Note) != "" {
		user += "\n额外要求：" + strings.TrimSpace(in.Note)
	}
	tid, err := startTask(p.ID, 0, "bible", "project", p.ID, func(ctx context.Context) error {
		text, err := callChatL(ctx, p.ID, 0, ch, model, sysFor(p, bibleSystem), user, 0.8)
		if err != nil {
			return err
		}
		var out struct {
			World   any `json:"world"`
			Bios    any `json:"bios"`
			Outline any `json:"outline"`
		}
		if err := extractJSON(text, &out); err != nil {
			return err
		}
		_, err = db.Exec(`UPDATE projects SET world=?, bios=?, outline=? WHERE id=?`, flatText(out.World), flatText(out.Bios), flatText(out.Outline), p.ID)
		return err
	})
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	writeJSON(w, map[string]any{"task_id": tid})
}

// flatText 模型偶尔把字段写成数组或对象，统一转成文本
func flatText(v any) string {
	switch x := v.(type) {
	case string:
		return strings.TrimSpace(x)
	case []any:
		var parts []string
		for _, it := range x {
			parts = append(parts, flatText(it))
		}
		return strings.Join(parts, "\n")
	case map[string]any:
		var parts []string
		for k, it := range x {
			parts = append(parts, k+"："+flatText(it))
		}
		return strings.Join(parts, "\n")
	case nil:
		return ""
	}
	return fmt.Sprint(v)
}

// seriesContext 写第 N 集时交给模型的前情：设定集 + 前几集摘要 + 上一集卡点
func seriesContext(p *Project, e *Episode) string {
	var b strings.Builder
	if p.World != "" {
		b.WriteString("【世界观】\n" + p.World + "\n\n")
	}
	if p.Bios != "" {
		b.WriteString("【人物小传】\n" + p.Bios + "\n\n")
	}
	if p.Outline != "" {
		b.WriteString("【分集总纲】\n" + p.Outline + "\n\n")
	}
	var prev []*Episode
	for _, x := range listEpisodes(p.ID) {
		if x.No < e.No {
			prev = append(prev, x)
		}
	}
	if len(prev) > 0 {
		b.WriteString("【前情摘要】\n")
		for _, x := range prev {
			sum := x.Summary
			if sum == "" {
				sum = x.Idea
			}
			if sum == "" {
				sum = "（未写摘要）"
			}
			fmt.Fprintf(&b, "第 %d 集：%s\n", x.No, sum)
		}
		last := prev[len(prev)-1]
		c := last.Cliffhanger
		if c == "" {
			c = cliffOf(last.Script)
		}
		if c != "" {
			b.WriteString("\n【上一集卡点】\n" + c + "\n本集开场要直接接住这个卡点。\n")
		}
		b.WriteString("\n")
	}
	return b.String()
}

const ideaSystem = `你是连载漫剧的总编剧。根据设定集、分集总纲和前情，写出下一集的剧情梗概：2–4 句话，交代本集要发生的核心事件、反转和结尾卡点，开头必须接住上一集的卡点。只输出梗概正文。`

func episodeIdeaTask(p *Project, e *Episode) (int64, error) {
	ch, model, err := textChannelFor(p)
	if err != nil {
		return 0, err
	}
	user := fmt.Sprintf("剧名：%s\n故事创意：%s\n现在要写第 %d 集。\n\n%s", p.Title, p.Idea, e.No, seriesContext(p, e))
	return startTask(p.ID, e.ID, "idea", "episode", e.ID, func(ctx context.Context) error {
		text, err := callChatL(ctx, p.ID, e.ID, ch, model, sysFor(p, ideaSystem), user, 0.8)
		if err != nil {
			return err
		}
		_, err = db.Exec(`UPDATE episodes SET idea=? WHERE id=?`, strings.TrimSpace(text), e.ID)
		return err
	})
}

func handleEpisodeCreate(w http.ResponseWriter, r *http.Request) {
	p, err := getProject(pathID(r))
	if err != nil {
		httpErr(w, 404, "作品不存在")
		return
	}
	var in struct {
		Idea   string `json:"idea"`
		AIIdea bool   `json:"ai_idea"`
	}
	_ = readJSON(r, &in)
	e, err := insertEpisode(p.ID, in.Idea)
	if err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	out := map[string]any{"episode": e}
	if in.AIIdea && strings.TrimSpace(in.Idea) == "" {
		if tid, err := episodeIdeaTask(p, e); err == nil {
			out["task_id"] = tid
		} else {
			out["idea_error"] = err.Error()
		}
	}
	writeJSON(w, out)
}

func handleEpisodeIdea(w http.ResponseWriter, r *http.Request) {
	p, e, err := epCtx(pathID(r))
	if err != nil {
		httpErr(w, 404, err.Error())
		return
	}
	tid, err := episodeIdeaTask(p, e)
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	writeJSON(w, map[string]any{"task_id": tid})
}

// ---------- 本集摘要 ----------

const summarySystem = `你是剧集编辑。阅读这一集的剧本，写出供后续各集参考的前情摘要。
- summary：80–150 字，按时间顺序写清发生了什么、人物关系和处境的变化、新出现的人物或线索；
- cliffhanger：结尾悬念，一句话。
只输出 JSON：{"summary":"","cliffhanger":""}`

func summaryTask(p *Project, e *Episode) (int64, error) {
	if strings.TrimSpace(e.Script) == "" {
		return 0, errors.New("这一集还没有剧本")
	}
	ch, model, err := textChannelFor(p)
	if err != nil {
		return 0, err
	}
	return startTask(p.ID, e.ID, "summary", "episode", e.ID, func(ctx context.Context) error {
		text, err := callChatL(ctx, p.ID, e.ID, ch, model, summarySystem, "剧本：\n"+e.Script, 0.3)
		if err != nil {
			return err
		}
		var out struct {
			Summary string `json:"summary"`
			Cliff   string `json:"cliffhanger"`
		}
		if err := extractJSON(text, &out); err != nil {
			return err
		}
		c := strings.TrimSpace(out.Cliff)
		if c == "" {
			c = cliffOf(e.Script)
		}
		_, err = db.Exec(`UPDATE episodes SET summary=?, cliffhanger=? WHERE id=?`, strings.TrimSpace(out.Summary), c, e.ID)
		return err
	})
}

func handleEpisodeSummary(w http.ResponseWriter, r *http.Request) {
	p, e, err := epCtx(pathID(r))
	if err != nil {
		httpErr(w, 404, err.Error())
		return
	}
	tid, err := summaryTask(p, e)
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	writeJSON(w, map[string]any{"task_id": tid})
}
