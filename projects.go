package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

type Project struct {
	ID             int64  `json:"id"`
	Title          string `json:"title"`
	Idea           string `json:"idea"`
	Style          string `json:"style"`
	Aspect         string `json:"aspect"`
	EpisodeSec     int    `json:"episode_sec"`
	ShotTarget     int    `json:"shot_target"`
	Script         string `json:"script"`
	TextChannelID  int64  `json:"text_channel_id"`
	TextModel      string `json:"text_model"`
	ImageChannelID int64  `json:"image_channel_id"`
	ImageModel     string `json:"image_model"`
	VideoSec       int    `json:"video_sec"`
	VideoRes       string `json:"video_res"`
	VideoTurbo     bool   `json:"video_turbo"`
	FinalFile      string `json:"final_file"`
	FinalURL       string `json:"final_url"`
	FinalAt        int64  `json:"final_at"`
	CreatedAt      int64  `json:"created_at"`
	UpdatedAt      int64  `json:"updated_at"`
}

const projCols = `id,title,idea,style,aspect,episode_sec,shot_target,script,text_channel_id,text_model,image_channel_id,image_model,video_sec,video_res,video_turbo,final_file,final_at,created_at,updated_at`

func scanProject(sc interface{ Scan(...any) error }) (*Project, error) {
	var p Project
	var turbo int
	if err := sc.Scan(&p.ID, &p.Title, &p.Idea, &p.Style, &p.Aspect, &p.EpisodeSec, &p.ShotTarget, &p.Script, &p.TextChannelID, &p.TextModel, &p.ImageChannelID, &p.ImageModel, &p.VideoSec, &p.VideoRes, &turbo, &p.FinalFile, &p.FinalAt, &p.CreatedAt, &p.UpdatedAt); err != nil {
		return nil, err
	}
	p.VideoTurbo = turbo == 1
	if p.FinalFile != "" {
		p.FinalURL = "/files/videos/" + p.FinalFile
	}
	return &p, nil
}

func getProject(id int64) (*Project, error) {
	return scanProject(db.QueryRow(`SELECT `+projCols+` FROM projects WHERE id=?`, id))
}

func touchProject(id int64) {
	if id > 0 {
		_, _ = db.Exec(`UPDATE projects SET updated_at=? WHERE id=?`, now(), id)
	}
}

type Asset struct {
	ID            int64    `json:"id"`
	ProjectID     int64    `json:"project_id"`
	Kind          string   `json:"kind"`
	Code          string   `json:"code"`
	Name          string   `json:"name"`
	Description   string   `json:"description"`
	Prompt        string   `json:"prompt"`
	PickedImageID int64    `json:"picked_image_id"`
	Sort          int      `json:"sort"`
	Images        []*Image `json:"images"`
}

const assetCols = `id,project_id,kind,code,name,description,prompt,picked_image_id,sort`

func scanAsset(sc interface{ Scan(...any) error }) (*Asset, error) {
	var a Asset
	if err := sc.Scan(&a.ID, &a.ProjectID, &a.Kind, &a.Code, &a.Name, &a.Description, &a.Prompt, &a.PickedImageID, &a.Sort); err != nil {
		return nil, err
	}
	a.Images = []*Image{}
	return &a, nil
}

func getAsset(id int64) (*Asset, error) {
	return scanAsset(db.QueryRow(`SELECT `+assetCols+` FROM assets WHERE id=?`, id))
}

func listAssets(pid int64) []*Asset {
	rows, err := db.Query(`SELECT `+assetCols+` FROM assets WHERE project_id=? ORDER BY CASE kind WHEN 'character' THEN 0 WHEN 'scene' THEN 1 ELSE 2 END, sort, id`, pid)
	if err != nil {
		return []*Asset{}
	}
	out := []*Asset{}
	for rows.Next() {
		if a, err := scanAsset(rows); err == nil {
			out = append(out, a)
		}
	}
	rows.Close()
	for _, a := range out {
		a.Images = listImages(`WHERE owner_type='asset' AND owner_id=? ORDER BY id`, a.ID)
	}
	return out
}

type Shot struct {
	ID             int64    `json:"id"`
	ProjectID      int64    `json:"project_id"`
	Sort           int      `json:"sort"`
	Code           string   `json:"code"`
	Duration       float64  `json:"duration"`
	Camera         string   `json:"camera"`
	Action         string   `json:"action"`
	Dialogue       string   `json:"dialogue"`
	Assets         []string `json:"assets"`
	KeyframePrompt string   `json:"keyframe_prompt"`
	VideoPrompt    string   `json:"video_prompt"`
	PickedImageID  int64    `json:"picked_image_id"`
	PickedVideoID  int64    `json:"picked_video_id"`
	Images         []*Image `json:"images"`
	Videos         []*Video `json:"videos"`
}

const shotCols = `id,project_id,sort,duration,camera,action,dialogue,asset_codes,keyframe_prompt,video_prompt,picked_image_id,picked_video_id`

func scanShot(sc interface{ Scan(...any) error }) (*Shot, error) {
	var s Shot
	var codes string
	if err := sc.Scan(&s.ID, &s.ProjectID, &s.Sort, &s.Duration, &s.Camera, &s.Action, &s.Dialogue, &codes, &s.KeyframePrompt, &s.VideoPrompt, &s.PickedImageID, &s.PickedVideoID); err != nil {
		return nil, err
	}
	_ = json.Unmarshal([]byte(codes), &s.Assets)
	if s.Assets == nil {
		s.Assets = []string{}
	}
	s.Images = []*Image{}
	s.Videos = []*Video{}
	return &s, nil
}

func getShot(id int64) (*Shot, error) {
	return scanShot(db.QueryRow(`SELECT `+shotCols+` FROM shots WHERE id=?`, id))
}

func listShots(pid int64, withMedia bool) []*Shot {
	rows, err := db.Query(`SELECT `+shotCols+` FROM shots WHERE project_id=? ORDER BY sort, id`, pid)
	if err != nil {
		return []*Shot{}
	}
	out := []*Shot{}
	for rows.Next() {
		if s, err := scanShot(rows); err == nil {
			out = append(out, s)
		}
	}
	rows.Close()
	for i, s := range out {
		s.Code = fmt.Sprintf("%02d", i+1)
		if withMedia {
			s.Images = listImages(`WHERE owner_type='shot' AND owner_id=? ORDER BY id`, s.ID)
			s.Videos = listVideos(`WHERE shot_id=? ORDER BY id`, s.ID)
		}
	}
	return out
}

// ---------- 列表 / 创建 / 详情 ----------

type ProjectCard struct {
	*Project
	Cover      string `json:"cover"`
	Shots      int    `json:"shots"`
	Assets     int    `json:"assets"`
	Keyframes  int    `json:"keyframes"`
	Videos     int    `json:"videos"`
	Steps      []bool `json:"steps"`
	ScriptSize int    `json:"script_size"`
}

func handleProjectList(w http.ResponseWriter, r *http.Request) {
	rows, err := db.Query(`SELECT ` + projCols + ` FROM projects ORDER BY updated_at DESC`)
	if err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	var ps []*Project
	for rows.Next() {
		if p, err := scanProject(rows); err == nil {
			ps = append(ps, p)
		}
	}
	rows.Close()
	out := []*ProjectCard{}
	for _, p := range ps {
		c := &ProjectCard{Project: p}
		st := projectSteps(p)
		c.Steps = st.Done
		c.Shots, c.Assets, c.Keyframes, c.Videos = st.Shots, st.Assets, st.Keyframes, st.Videos
		c.ScriptSize = len([]rune(p.Script))
		c.Script = ""
		// 封面：第一张选定的关键帧，否则第一张选定的资产图
		var file string
		_ = db.QueryRow(`SELECT i.file FROM shots s JOIN images i ON i.id=s.picked_image_id WHERE s.project_id=? ORDER BY s.sort, s.id LIMIT 1`, p.ID).Scan(&file)
		if file == "" {
			_ = db.QueryRow(`SELECT i.file FROM assets a JOIN images i ON i.id=a.picked_image_id WHERE a.project_id=? ORDER BY CASE a.kind WHEN 'scene' THEN 0 WHEN 'character' THEN 1 ELSE 2 END, a.id LIMIT 1`, p.ID).Scan(&file)
		}
		if file != "" {
			c.Cover = "/files/images/" + file
		}
		out = append(out, c)
	}
	writeJSON(w, out)
}

type projectIn struct {
	Title          *string `json:"title"`
	Idea           *string `json:"idea"`
	Style          *string `json:"style"`
	Aspect         *string `json:"aspect"`
	EpisodeSec     *int    `json:"episode_sec"`
	ShotTarget     *int    `json:"shot_target"`
	Script         *string `json:"script"`
	TextChannelID  *int64  `json:"text_channel_id"`
	TextModel      *string `json:"text_model"`
	ImageChannelID *int64  `json:"image_channel_id"`
	ImageModel     *string `json:"image_model"`
	VideoSec       *int    `json:"video_sec"`
	VideoRes       *string `json:"video_res"`
	VideoTurbo     *bool   `json:"video_turbo"`
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
	p, _ := getProject(id)
	writeJSON(w, p)
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
	if in.Idea != nil {
		set("idea", strings.TrimSpace(*in.Idea))
	}
	if in.Style != nil {
		set("style", strings.TrimSpace(*in.Style))
	}
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
	if in.Script != nil {
		set("script", *in.Script)
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
	if p, err := getProject(id); err == nil && p.FinalFile != "" {
		_ = os.Remove(filepath.Join(dataDir, "videos", p.FinalFile))
	}
	for _, q := range []string{`DELETE FROM assets WHERE project_id=?`, `DELETE FROM shots WHERE project_id=?`, `DELETE FROM videos WHERE project_id=? AND status<>'running'`, `DELETE FROM tasks WHERE project_id=?`, `DELETE FROM projects WHERE id=?`} {
		_, _ = db.Exec(q, id)
	}
	writeJSON(w, map[string]any{"ok": true})
}

type Steps struct {
	Done      []bool `json:"done"`
	Assets    int    `json:"assets"`
	AssetsOK  int    `json:"assets_ok"`
	Shots     int    `json:"shots"`
	Keyframes int    `json:"keyframes"`
	Videos    int    `json:"videos"`
	Queued    int    `json:"queued"`
}

func projectSteps(p *Project) Steps {
	var s Steps
	_ = db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(picked_image_id>0),0) FROM assets WHERE project_id=?`, p.ID).Scan(&s.Assets, &s.AssetsOK)
	_ = db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(picked_image_id>0),0) FROM shots WHERE project_id=?`, p.ID).Scan(&s.Shots, &s.Keyframes)
	_ = db.QueryRow(`SELECT COUNT(DISTINCT shot_id) FROM videos WHERE project_id=? AND status='done' AND shot_id IN (SELECT id FROM shots WHERE project_id=?)`, p.ID, p.ID).Scan(&s.Videos)
	_ = db.QueryRow(`SELECT COUNT(*) FROM videos WHERE project_id=? AND status IN ('queued','running')`, p.ID).Scan(&s.Queued)
	s.Done = []bool{
		p.Title != "" && p.Idea != "",
		strings.TrimSpace(p.Script) != "",
		s.Assets > 0 && s.AssetsOK == s.Assets,
		s.Shots > 0,
		s.Shots > 0 && s.Keyframes == s.Shots,
		s.Shots > 0 && s.Videos == s.Shots,
		p.FinalFile != "",
	}
	return s
}

func handleProjectGet(w http.ResponseWriter, r *http.Request) {
	p, err := getProject(pathID(r))
	if err != nil {
		httpErr(w, 404, "作品不存在")
		return
	}
	writeJSON(w, map[string]any{
		"project": p,
		"assets":  listAssets(p.ID),
		"shots":   listShots(p.ID, true),
		"tasks":   listTasks(p.ID),
		"steps":   projectSteps(p),
		"colab":   runner.brief(),
	})
}

// ---------- ② 剧本 ----------

const scriptSystem = `你是一名专写竖屏漫剧（AI 生成的短篇动态漫画剧）的编剧。你写的单集剧本要做到：
- 开场 3 秒内给出强钩子（冲突、悬念或反常画面），直接进入事件，不铺垫世界观；
- 节拍紧凑：每 5–10 秒一个情绪或信息推进，台词短、口语化，一句不超过 20 字；
- 中段有一次反转；
- 结尾停在悬念（卡点），让观众想看下一集；
- 画面可拍：每一场写清地点、人物、动作和表情，避免心理描写和无法画出来的抽象内容；
- 角色数量少而鲜明（主角 1–2 人，配角不超过 3 人），外貌特征写清楚，方便后续做角色设定图。
只输出剧本正文，用 Markdown：先写「# 标题」，再写「## 人物」（每人一行：姓名——年龄、外貌、服装、性格），然后按「## 第 N 场 · 地点 · 时间」分场，场内用「【画面】」写动作与镜头感，用「角色名：台词」写对白，用「（旁白）」写旁白。最后单独一行写「【卡点】」说明结尾悬念。`

func handleScriptGenerate(w http.ResponseWriter, r *http.Request) {
	p, err := getProject(pathID(r))
	if err != nil {
		httpErr(w, 404, "作品不存在")
		return
	}
	var in struct {
		Note string `json:"note"`
	}
	_ = readJSON(r, &in)
	ch, model, err := textChannelFor(p)
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	if strings.TrimSpace(p.Idea) == "" {
		httpErr(w, 400, "先在「设定」里写下故事创意")
		return
	}
	user := fmt.Sprintf("剧名：%s\n故事创意：%s\n画面风格：%s\n画幅：%s\n单集时长：约 %d 秒\n目标镜头数：约 %d 个\n",
		p.Title, p.Idea, orDefault(p.Style, "不限"), p.Aspect, p.EpisodeSec, p.ShotTarget)
	if strings.TrimSpace(in.Note) != "" {
		user += "\n额外要求：" + strings.TrimSpace(in.Note) + "\n"
	}
	if strings.TrimSpace(p.Script) != "" && strings.TrimSpace(in.Note) != "" {
		user += "\n这是上一版剧本，请在它的基础上按额外要求修改：\n" + p.Script
	}
	tid, err := startTask(p.ID, "script", "project", p.ID, func(ctx context.Context) error {
		text, err := callChat(ctx, ch, model, scriptSystem, user, 0.9)
		if err != nil {
			return err
		}
		_, err = db.Exec(`UPDATE projects SET script=? WHERE id=?`, text, p.ID)
		return err
	})
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	writeJSON(w, map[string]any{"task_id": tid})
}

func orDefault(s, d string) string {
	if strings.TrimSpace(s) == "" {
		return d
	}
	return s
}

// ---------- ③ 资产 ----------

const extractSystem = `你是漫剧美术统筹。阅读剧本，列出需要提前做设定图的视觉资产：角色、场景、关键道具。
要求：
- 角色：每个有台词或反复出现的人物一项；description 写固定外貌（年龄感、发型发色、五官特征、体型、服装配色），不写剧情；
- 场景：每个独立地点一项；description 写空间结构、时代、光线、主色调；
- 道具：只列剧情关键、会被特写的物件，没有就留空数组；
- prompt：写给生图模型的中文提示词，单独描述这个资产本身，不要出现其他角色。
只输出 JSON：{"characters":[{"name":"","description":"","prompt":""}],"scenes":[...],"props":[...]}`

func handleAssetsExtract(w http.ResponseWriter, r *http.Request) {
	p, err := getProject(pathID(r))
	if err != nil {
		httpErr(w, 404, "作品不存在")
		return
	}
	if strings.TrimSpace(p.Script) == "" {
		httpErr(w, 400, "先写好剧本")
		return
	}
	ch, model, err := textChannelFor(p)
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	tid, err := startTask(p.ID, "extract", "project", p.ID, func(ctx context.Context) error {
		text, err := callChat(ctx, ch, model, extractSystem, "画面风格："+orDefault(p.Style, "不限")+"\n\n剧本：\n"+p.Script, 0.4)
		if err != nil {
			return err
		}
		var out struct {
			Characters []assetDraft `json:"characters"`
			Scenes     []assetDraft `json:"scenes"`
			Props      []assetDraft `json:"props"`
		}
		if err := extractJSON(text, &out); err != nil {
			return err
		}
		n := 0
		for kind, list := range map[string][]assetDraft{"character": out.Characters, "scene": out.Scenes, "prop": out.Props} {
			for _, d := range list {
				if strings.TrimSpace(d.Name) == "" {
					continue
				}
				var exists int
				_ = db.QueryRow(`SELECT COUNT(*) FROM assets WHERE project_id=? AND kind=? AND name=?`, p.ID, kind, strings.TrimSpace(d.Name)).Scan(&exists)
				if exists > 0 {
					continue
				}
				if _, err := insertAsset(p.ID, kind, d); err == nil {
					n++
				}
			}
		}
		if n == 0 && len(listAssets(p.ID)) == 0 {
			return errors.New("没有提取到资产")
		}
		return nil
	})
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	writeJSON(w, map[string]any{"task_id": tid})
}

type assetDraft struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Prompt      string `json:"prompt"`
}

var kindPrefix = map[string]string{"character": "C", "scene": "S", "prop": "P"}

func insertAsset(pid int64, kind string, d assetDraft) (int64, error) {
	if kindPrefix[kind] == "" {
		return 0, errors.New("资产类型不对")
	}
	var maxN int
	rows, _ := db.Query(`SELECT code FROM assets WHERE project_id=? AND kind=?`, pid, kind)
	if rows != nil {
		for rows.Next() {
			var c string
			var x int
			if rows.Scan(&c) == nil {
				fmt.Sscanf(c[1:], "%d", &x)
				if x > maxN {
					maxN = x
				}
			}
		}
		rows.Close()
	}
	code := fmt.Sprintf("%s%02d", kindPrefix[kind], maxN+1)
	res, err := db.Exec(`INSERT INTO assets(project_id,kind,code,name,description,prompt,sort,created_at) VALUES(?,?,?,?,?,?,?,?)`,
		pid, kind, code, strings.TrimSpace(d.Name), strings.TrimSpace(d.Description), strings.TrimSpace(d.Prompt), maxN+1, now())
	if err != nil {
		return 0, err
	}
	touchProject(pid)
	return res.LastInsertId()
}

func handleAssetCreate(w http.ResponseWriter, r *http.Request) {
	pid := pathID(r)
	var in struct {
		Kind string `json:"kind"`
		assetDraft
	}
	if err := readJSON(r, &in); err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	if strings.TrimSpace(in.Name) == "" {
		httpErr(w, 400, "名称不能为空")
		return
	}
	id, err := insertAsset(pid, in.Kind, in.assetDraft)
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	a, _ := getAsset(id)
	writeJSON(w, a)
}

func handleAssetUpdate(w http.ResponseWriter, r *http.Request) {
	a, err := getAsset(pathID(r))
	if err != nil {
		httpErr(w, 404, "资产不存在")
		return
	}
	var in assetDraft
	if err := readJSON(r, &in); err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	if strings.TrimSpace(in.Name) == "" {
		in.Name = a.Name
	}
	_, _ = db.Exec(`UPDATE assets SET name=?,description=?,prompt=? WHERE id=?`, strings.TrimSpace(in.Name), strings.TrimSpace(in.Description), strings.TrimSpace(in.Prompt), a.ID)
	touchProject(a.ProjectID)
	writeJSON(w, map[string]any{"ok": true})
}

func handleAssetDelete(w http.ResponseWriter, r *http.Request) {
	a, err := getAsset(pathID(r))
	if err != nil {
		httpErr(w, 404, "资产不存在")
		return
	}
	for _, im := range listImages(`WHERE owner_type='asset' AND owner_id=?`, a.ID) {
		deleteImageRow(im)
	}
	_, _ = db.Exec(`DELETE FROM assets WHERE id=?`, a.ID)
	touchProject(a.ProjectID)
	writeJSON(w, map[string]any{"ok": true})
}

var assetFrame = map[string]string{
	"character": "角色设定图：单人，全身正面站姿，表情自然，干净的浅灰纯色背景，无文字，人物居中完整入画",
	"scene":     "场景概念图：空镜，无人物，构图完整，体现空间纵深与光线氛围",
	"prop":      "道具设定图：单个物件居中，干净的浅灰纯色背景，无文字，细节清晰",
}

func assetPrompt(p *Project, a *Asset) string {
	body := a.Prompt
	if strings.TrimSpace(body) == "" {
		body = a.Name + "，" + a.Description
	}
	parts := []string{}
	if s := strings.TrimSpace(p.Style); s != "" {
		parts = append(parts, "画面风格："+s)
	}
	parts = append(parts, assetFrame[a.Kind], body)
	if a.Kind == "character" && a.Description != "" && !strings.Contains(body, a.Description) {
		parts = append(parts, "外貌："+a.Description)
	}
	return strings.Join(parts, "。")
}

func assetAspect(kind, projectAspect string) string {
	switch kind {
	case "character":
		return "3:4"
	case "prop":
		return "1:1"
	}
	return projectAspect
}

func genAssetTask(p *Project, a *Asset, n int) (int64, error) {
	ch, model, err := imageChannelFor(p)
	if err != nil {
		return 0, err
	}
	prompt := assetPrompt(p, a)
	params := paramsFor(ch, assetAspect(a.Kind, p.Aspect), n)
	return startTask(p.ID, "image", "asset", a.ID, func(ctx context.Context) error {
		imgs, sent, err := generateImages(ctx, ch, model, prompt, params, nil)
		if err != nil {
			return err
		}
		var first int64
		for _, data := range imgs {
			im, err := storeImage(p.ID, "asset", a.ID, data, "gen", sent, ch.ID, model)
			if err == nil && first == 0 {
				first = im.ID
			}
		}
		if first == 0 {
			return errors.New("图片保存失败")
		}
		// 还没选过就自动选第一张
		_, _ = db.Exec(`UPDATE assets SET picked_image_id=? WHERE id=? AND picked_image_id=0`, first, a.ID)
		return nil
	})
}

func handleAssetGenerate(w http.ResponseWriter, r *http.Request) {
	a, err := getAsset(pathID(r))
	if err != nil {
		httpErr(w, 404, "资产不存在")
		return
	}
	p, _ := getProject(a.ProjectID)
	var in struct {
		N int `json:"n"`
	}
	_ = readJSON(r, &in)
	tid, err := genAssetTask(p, a, clampInt(in.N, 1, 4))
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	writeJSON(w, map[string]any{"task_id": tid})
}

func handleAssetsGenerateAll(w http.ResponseWriter, r *http.Request) {
	p, err := getProject(pathID(r))
	if err != nil {
		httpErr(w, 404, "作品不存在")
		return
	}
	n := 0
	var lastErr error
	for _, a := range listAssets(p.ID) {
		if len(a.Images) > 0 {
			continue
		}
		if _, err := genAssetTask(p, a, 1); err != nil {
			lastErr = err
			continue
		}
		n++
	}
	if n == 0 && lastErr != nil {
		httpErr(w, 400, lastErr.Error())
		return
	}
	writeJSON(w, map[string]any{"started": n})
}

func handleAssetUpload(w http.ResponseWriter, r *http.Request) {
	a, err := getAsset(pathID(r))
	if err != nil {
		httpErr(w, 404, "资产不存在")
		return
	}
	data, err := readUpload(r)
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	im, err := storeImage(a.ProjectID, "asset", a.ID, data, "upload", "", 0, "")
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	_, _ = db.Exec(`UPDATE assets SET picked_image_id=? WHERE id=?`, im.ID, a.ID)
	touchProject(a.ProjectID)
	writeJSON(w, im)
}

// ---------- ④ 分镜 ----------

const storyboardSystem = `你是漫剧分镜师。把剧本拆成按顺序播放的镜头，每个镜头会先生成一张关键帧，再用图生视频模型生成 5–10 秒的动态镜头。
要求：
- 镜头总数接近目标数量，单个镜头 duration 取 5 到 10 的整数秒，总时长接近单集时长；
- camera：景别 + 机位/运镜，如「近景 · 缓慢推近」「全景 · 固定」「过肩 · 轻微手持」；
- action：画面里发生什么（人物动作、表情、环境变化），一个镜头只做一件事；
- dialogue：这个镜头里的台词或旁白，格式「角色名：台词」，没有就留空；
- assets：这个镜头出现的资产编号（只能用下面资产表里的编号，角色在前、场景其次、道具最后）；
- keyframe_prompt：关键帧画面的中文生图提示词，写清构图、景别、人物位置与表情、光线，直接用资产名称指代角色和场景；
- video_prompt：给图生视频模型的中文描述，只写从这张关键帧开始的动作、镜头运动、环境动态和声音（台词、音效、配乐氛围），不重复静态外观。
只输出 JSON：{"shots":[{"duration":5,"camera":"","action":"","dialogue":"","assets":["C01","S01"],"keyframe_prompt":"","video_prompt":""}]}`

type shotDraft struct {
	Duration       float64  `json:"duration"`
	Camera         string   `json:"camera"`
	Action         string   `json:"action"`
	Dialogue       string   `json:"dialogue"`
	Assets         []string `json:"assets"`
	KeyframePrompt string   `json:"keyframe_prompt"`
	VideoPrompt    string   `json:"video_prompt"`
}

func assetTable(assets []*Asset) string {
	kinds := map[string]string{"character": "角色", "scene": "场景", "prop": "道具"}
	var b strings.Builder
	for _, a := range assets {
		fmt.Fprintf(&b, "%s｜%s｜%s｜%s\n", a.Code, kinds[a.Kind], a.Name, a.Description)
	}
	return b.String()
}

func handleShotsSplit(w http.ResponseWriter, r *http.Request) {
	p, err := getProject(pathID(r))
	if err != nil {
		httpErr(w, 404, "作品不存在")
		return
	}
	if strings.TrimSpace(p.Script) == "" {
		httpErr(w, 400, "先写好剧本")
		return
	}
	ch, model, err := textChannelFor(p)
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	assets := listAssets(p.ID)
	user := fmt.Sprintf("目标镜头数：%d\n单集时长：约 %d 秒\n画幅：%s\n画面风格：%s\n\n资产表（编号｜类型｜名称｜外观）：\n%s\n剧本：\n%s",
		p.ShotTarget, p.EpisodeSec, p.Aspect, orDefault(p.Style, "不限"), orDefault(assetTable(assets), "（暂无）\n"), p.Script)
	tid, err := startTask(p.ID, "storyboard", "project", p.ID, func(ctx context.Context) error {
		text, err := callChat(ctx, ch, model, storyboardSystem, user, 0.5)
		if err != nil {
			return err
		}
		var out struct {
			Shots []shotDraft `json:"shots"`
		}
		if err := extractJSON(text, &out); err != nil {
			return err
		}
		if len(out.Shots) == 0 {
			return errors.New("没有拆出镜头")
		}
		valid := map[string]bool{}
		for _, a := range assets {
			valid[a.Code] = true
		}
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		defer tx.Rollback()
		if _, err := tx.Exec(`DELETE FROM shots WHERE project_id=?`, p.ID); err != nil {
			return err
		}
		for i, s := range out.Shots {
			codes := []string{}
			for _, c := range s.Assets {
				c = strings.ToUpper(strings.TrimSpace(c))
				if valid[c] {
					codes = append(codes, c)
				}
			}
			d := math.Round(s.Duration)
			if d < 5 {
				d = 5
			}
			if d > 10 {
				d = 10
			}
			if _, err := tx.Exec(`INSERT INTO shots(project_id,sort,duration,camera,action,dialogue,asset_codes,keyframe_prompt,video_prompt,created_at) VALUES(?,?,?,?,?,?,?,?,?,?)`,
				p.ID, i+1, d, s.Camera, s.Action, s.Dialogue, mustJSON(codes), s.KeyframePrompt, s.VideoPrompt, now()); err != nil {
				return err
			}
		}
		return tx.Commit()
	})
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	writeJSON(w, map[string]any{"task_id": tid})
}

func handleShotCreate(w http.ResponseWriter, r *http.Request) {
	pid := pathID(r)
	var in struct {
		After int64 `json:"after"`
	}
	_ = readJSON(r, &in)
	shots := listShots(pid, false)
	sort := len(shots) + 1
	if in.After > 0 {
		for _, s := range shots {
			if s.ID == in.After {
				sort = s.Sort + 1
			}
		}
		_, _ = db.Exec(`UPDATE shots SET sort=sort+1 WHERE project_id=? AND sort>=?`, pid, sort)
	}
	res, err := db.Exec(`INSERT INTO shots(project_id,sort,duration,created_at) VALUES(?,?,5,?)`, pid, sort, now())
	if err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	id, _ := res.LastInsertId()
	renumberShots(pid)
	s, _ := getShot(id)
	writeJSON(w, s)
}

func renumberShots(pid int64) {
	for i, s := range listShots(pid, false) {
		_, _ = db.Exec(`UPDATE shots SET sort=? WHERE id=?`, i+1, s.ID)
	}
	touchProject(pid)
}

func handleShotUpdate(w http.ResponseWriter, r *http.Request) {
	s, err := getShot(pathID(r))
	if err != nil {
		httpErr(w, 404, "镜头不存在")
		return
	}
	var in shotDraft
	if err := readJSON(r, &in); err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	if in.Assets == nil {
		in.Assets = []string{}
	}
	d := in.Duration
	if d < 1 {
		d = 5
	}
	if d > 10 {
		d = 10
	}
	_, _ = db.Exec(`UPDATE shots SET duration=?,camera=?,action=?,dialogue=?,asset_codes=?,keyframe_prompt=?,video_prompt=? WHERE id=?`,
		d, in.Camera, in.Action, in.Dialogue, mustJSON(in.Assets), in.KeyframePrompt, in.VideoPrompt, s.ID)
	touchProject(s.ProjectID)
	writeJSON(w, map[string]any{"ok": true})
}

func handleShotDelete(w http.ResponseWriter, r *http.Request) {
	s, err := getShot(pathID(r))
	if err != nil {
		httpErr(w, 404, "镜头不存在")
		return
	}
	for _, im := range listImages(`WHERE owner_type='shot' AND owner_id=?`, s.ID) {
		deleteImageRow(im)
	}
	_, _ = db.Exec(`UPDATE videos SET status='canceled', finished_at=? WHERE shot_id=? AND status='queued'`, now(), s.ID)
	_, _ = db.Exec(`DELETE FROM shots WHERE id=?`, s.ID)
	renumberShots(s.ProjectID)
	writeJSON(w, map[string]any{"ok": true})
}

func handleShotOrder(w http.ResponseWriter, r *http.Request) {
	var ids []int64
	if err := readJSON(r, &ids); err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	for i, id := range ids {
		_, _ = db.Exec(`UPDATE shots SET sort=? WHERE id=? AND project_id=?`, i+1, id, pathID(r))
	}
	touchProject(pathID(r))
	writeJSON(w, map[string]any{"ok": true})
}

// ---------- ⑤ 关键帧 ----------

// keyframePlan 按渠道参考图上限挑选参考图，并在提示词里说明每张参考图是谁
func keyframePlan(p *Project, s *Shot, ch *Channel) (string, []refFile, []string) {
	assets := listAssets(p.ID)
	byCode := map[string]*Asset{}
	for _, a := range assets {
		byCode[a.Code] = a
	}
	limit := 0
	if ch.Caps.Refs.Mode != "off" {
		limit = ch.Caps.Refs.Max
		if limit == 0 {
			limit = 4
		}
	}
	// 角色 → 场景 → 道具
	ordered := []*Asset{}
	for _, kind := range []string{"character", "scene", "prop"} {
		for _, c := range s.Assets {
			if a := byCode[c]; a != nil && a.Kind == kind && a.PickedImageID > 0 {
				ordered = append(ordered, a)
			}
		}
	}
	var refs []refFile
	var used []string
	var notes []string
	kinds := map[string]string{"character": "角色", "scene": "场景", "prop": "道具"}
	for _, a := range ordered {
		if len(refs) >= limit {
			break
		}
		im, err := getImage(a.PickedImageID)
		if err != nil {
			continue
		}
		rf, err := readImageRef(im)
		if err != nil {
			continue
		}
		refs = append(refs, rf)
		used = append(used, a.Code)
		notes = append(notes, fmt.Sprintf("参考图%d是%s「%s」", len(refs), kinds[a.Kind], a.Name))
	}
	parts := []string{}
	if st := strings.TrimSpace(p.Style); st != "" {
		parts = append(parts, "画面风格："+st)
	}
	body := s.KeyframePrompt
	if strings.TrimSpace(body) == "" {
		body = s.Camera + "，" + s.Action
	}
	parts = append(parts, body)
	// 没进参考图的资产用文字描述补上
	for _, c := range s.Assets {
		a := byCode[c]
		if a == nil || contains(used, c) || a.Description == "" {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s「%s」：%s", kinds[a.Kind], a.Name, a.Description))
	}
	if len(notes) > 0 {
		parts = append(parts, strings.Join(notes, "，")+"；保持角色的脸、发型和服装与参考图一致，场景沿用参考图的空间与色调")
	}
	parts = append(parts, "电影感单帧画面，无字幕，无文字")
	return strings.Join(parts, "。"), refs, used
}

func genKeyframeTask(p *Project, s *Shot, n int) (int64, error) {
	ch, model, err := imageChannelFor(p)
	if err != nil {
		return 0, err
	}
	prompt, refs, _ := keyframePlan(p, s, ch)
	params := paramsFor(ch, p.Aspect, n)
	return startTask(p.ID, "image", "shot", s.ID, func(ctx context.Context) error {
		imgs, sent, err := generateImages(ctx, ch, model, prompt, params, refs)
		if err != nil {
			return err
		}
		var first int64
		for _, data := range imgs {
			im, err := storeImage(p.ID, "shot", s.ID, data, "gen", sent, ch.ID, model)
			if err == nil && first == 0 {
				first = im.ID
			}
		}
		if first == 0 {
			return errors.New("图片保存失败")
		}
		_, _ = db.Exec(`UPDATE shots SET picked_image_id=? WHERE id=? AND picked_image_id=0`, first, s.ID)
		return nil
	})
}

func handleKeyframeGenerate(w http.ResponseWriter, r *http.Request) {
	s, err := getShot(pathID(r))
	if err != nil {
		httpErr(w, 404, "镜头不存在")
		return
	}
	p, _ := getProject(s.ProjectID)
	var in struct {
		N int `json:"n"`
	}
	_ = readJSON(r, &in)
	tid, err := genKeyframeTask(p, s, clampInt(in.N, 1, 4))
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	writeJSON(w, map[string]any{"task_id": tid})
}

func handleKeyframesGenerateAll(w http.ResponseWriter, r *http.Request) {
	p, err := getProject(pathID(r))
	if err != nil {
		httpErr(w, 404, "作品不存在")
		return
	}
	n := 0
	var lastErr error
	for _, s := range listShots(p.ID, true) {
		if len(s.Images) > 0 {
			continue
		}
		if _, err := genKeyframeTask(p, s, 1); err != nil {
			lastErr = err
			continue
		}
		n++
	}
	if n == 0 && lastErr != nil {
		httpErr(w, 400, lastErr.Error())
		return
	}
	writeJSON(w, map[string]any{"started": n})
}

func handleKeyframeUpload(w http.ResponseWriter, r *http.Request) {
	s, err := getShot(pathID(r))
	if err != nil {
		httpErr(w, 404, "镜头不存在")
		return
	}
	data, err := readUpload(r)
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	im, err := storeImage(s.ProjectID, "shot", s.ID, data, "upload", "", 0, "")
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	_, _ = db.Exec(`UPDATE shots SET picked_image_id=? WHERE id=?`, im.ID, s.ID)
	touchProject(s.ProjectID)
	writeJSON(w, im)
}

var _ = sql.ErrNoRows
