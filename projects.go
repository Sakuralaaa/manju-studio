package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"
)

// ---------- 资产（跨集共享） ----------

type Asset struct {
	ID            int64     `json:"id"`
	ProjectID     int64     `json:"project_id"`
	Kind          string    `json:"kind"`
	Code          string    `json:"code"`
	Name          string    `json:"name"`
	Description   string    `json:"description"`
	Prompt        string    `json:"prompt"`
	PickedImageID int64     `json:"picked_image_id"`
	SheetImageID  int64     `json:"sheet_image_id"`
	ExprImageID   int64     `json:"expr_image_id"`
	Voice         VoiceSpec `json:"voice"`
	BaseID        int64     `json:"base_id"` // 同一个人的另一个时期：生图时拿本体的图锁脸
	Sort          int       `json:"sort"`
	Images        []*Image  `json:"images"`
	Appear        int       `json:"appear"`
}

const assetCols = `id,project_id,kind,code,name,description,prompt,picked_image_id,sheet_image_id,expr_image_id,voice,sort,base_id`

func scanAsset(sc interface{ Scan(...any) error }) (*Asset, error) {
	var a Asset
	var voice string
	if err := sc.Scan(&a.ID, &a.ProjectID, &a.Kind, &a.Code, &a.Name, &a.Description, &a.Prompt, &a.PickedImageID, &a.SheetImageID, &a.ExprImageID, &voice, &a.Sort, &a.BaseID); err != nil {
		return nil, err
	}
	_ = json.Unmarshal([]byte(voice), &a.Voice)
	a.Images = []*Image{}
	return &a, nil
}

func getAsset(id int64) (*Asset, error) {
	return scanAsset(db.QueryRow(`SELECT `+assetCols+` FROM assets WHERE id=?`, id))
}

// appearCounts 每个资产编号在全剧分镜里出现的次数（判断主角用）
func appearCounts(pid int64) map[string]int {
	out := map[string]int{}
	rows, err := db.Query(`SELECT asset_codes FROM shots WHERE project_id=?`, pid)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var s string
		var codes []string
		if rows.Scan(&s) == nil && json.Unmarshal([]byte(s), &codes) == nil {
			for _, c := range codes {
				out[c]++
			}
		}
	}
	return out
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
	counts := appearCounts(pid)
	for _, a := range out {
		a.Images = listImages(`WHERE owner_type='asset' AND owner_id=? ORDER BY id`, a.ID)
		a.Appear = counts[a.Code]
	}
	return out
}

// ---------- 镜头 ----------

type Shot struct {
	ID             int64    `json:"id"`
	ProjectID      int64    `json:"project_id"`
	EpisodeID      int64    `json:"episode_id"`
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
	Chain          string   `json:"chain"`
	SfxFile        string   `json:"-"`
	SfxURL         string   `json:"sfx_url"`
	SfxName        string   `json:"sfx_name"`
	SfxVolume      float64  `json:"sfx_volume"`
	Images         []*Image `json:"images"`
	Videos         []*Video `json:"videos"`
}

const shotCols = `id,project_id,episode_id,sort,duration,camera,action,dialogue,asset_codes,keyframe_prompt,video_prompt,picked_image_id,picked_video_id,chain,sfx_file,sfx_name,sfx_volume`

func scanShot(sc interface{ Scan(...any) error }) (*Shot, error) {
	var s Shot
	var codes string
	if err := sc.Scan(&s.ID, &s.ProjectID, &s.EpisodeID, &s.Sort, &s.Duration, &s.Camera, &s.Action, &s.Dialogue, &codes, &s.KeyframePrompt, &s.VideoPrompt, &s.PickedImageID, &s.PickedVideoID, &s.Chain, &s.SfxFile, &s.SfxName, &s.SfxVolume); err != nil {
		return nil, err
	}
	_ = json.Unmarshal([]byte(codes), &s.Assets)
	if s.Assets == nil {
		s.Assets = []string{}
	}
	if s.SfxFile != "" {
		s.SfxURL = "/files/audio/" + s.SfxFile
	}
	if s.Chain == "" {
		s.Chain = "off"
	}
	s.Images = []*Image{}
	s.Videos = []*Video{}
	return &s, nil
}

func getShot(id int64) (*Shot, error) {
	s, err := scanShot(db.QueryRow(`SELECT `+shotCols+` FROM shots WHERE id=?`, id))
	if err != nil {
		return nil, err
	}
	var n int
	_ = db.QueryRow(`SELECT COUNT(*) FROM shots WHERE episode_id=? AND (sort<? OR (sort=? AND id<?))`, s.EpisodeID, s.Sort, s.Sort, s.ID).Scan(&n)
	s.Code = fmt.Sprintf("%02d", n+1)
	return s, nil
}

func listShots(eid int64, withMedia bool) []*Shot {
	rows, err := db.Query(`SELECT `+shotCols+` FROM shots WHERE episode_id=? ORDER BY sort, id`, eid)
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

// prevShot 同一集里的上一个镜头
func prevShot(s *Shot) *Shot {
	shots := listShots(s.EpisodeID, true)
	for i, x := range shots {
		if x.ID == s.ID && i > 0 {
			return shots[i-1]
		}
	}
	return nil
}

// ---------- ② 剧本 ----------

const scriptSystem = `你是一名专写竖屏漫剧（AI 生成的连载短篇动态漫画剧）的编剧。你写的单集剧本要做到：
- 开场 3 秒内给出强钩子（冲突、悬念或反常画面）；如果有上一集卡点，开场直接接住它；
- 节拍紧凑：每 5–10 秒一个情绪或信息推进，台词短、口语化，一句不超过 20 字；
- 中段有一次反转；
- 结尾停在悬念（卡点），让观众想看下一集；
- 画面可拍：每一场写清地点、人物、动作和表情，避免心理描写和无法画出来的抽象内容；
- 人物沿用设定集里的姓名、外貌和说话方式，不要改名；新人物要少而鲜明。
只输出剧本正文，用 Markdown：先写「# 标题」，再写「## 人物」（本集出场的人，每人一行：姓名——年龄、外貌、服装、性格），然后按「## 第 N 场 · 地点 · 时间」分场，场内用「【画面】」写动作与镜头感，用「角色名：台词」写对白，用「（旁白）」写旁白。最后单独一行写「【卡点】」说明结尾悬念。`

func scriptTask(p *Project, e *Episode, note string) (int64, error) {
	ch, model, err := textChannelFor(p)
	if err != nil {
		return 0, err
	}
	idea := orDefault(e.Idea, p.Idea)
	if strings.TrimSpace(idea) == "" {
		return 0, errors.New("先在「设定」里写下故事创意或本集梗概")
	}
	user := fmt.Sprintf("剧名：%s\n整体创意：%s\n本集：第 %d 集\n本集梗概：%s\n画面风格：%s\n画幅：%s\n单集时长：约 %d 秒\n目标镜头数：约 %d 个\n\n%s",
		p.Title, p.Idea, e.No, orDefault(e.Idea, "（按总纲和前情自行安排）"), orDefault(p.Style, "不限"), p.Aspect, p.EpisodeSec, p.ShotTarget, seriesContext(p, e))
	if strings.TrimSpace(note) != "" {
		user += "\n额外要求：" + strings.TrimSpace(note) + "\n"
	}
	if strings.TrimSpace(e.Script) != "" && strings.TrimSpace(note) != "" {
		user += "\n这是上一版剧本，请在它的基础上按额外要求修改：\n" + e.Script
	}
	return startTask(p.ID, e.ID, "script", "episode", e.ID, func(ctx context.Context) error {
		text, err := callChatL(ctx, p.ID, e.ID, ch, model, sysFor(p, scriptSystem), user, 0.9)
		if err != nil {
			return err
		}
		title := ""
		for _, ln := range strings.Split(text, "\n") {
			if strings.HasPrefix(strings.TrimSpace(ln), "# ") {
				title = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(ln), "# "))
				break
			}
		}
		_, err = db.Exec(`UPDATE episodes SET script=?, cliffhanger=?, title=CASE WHEN title='' THEN ? ELSE title END WHERE id=?`, text, cliffOf(text), title, e.ID)
		return err
	})
}

func handleScriptGenerate(w http.ResponseWriter, r *http.Request) {
	p, e, err := epCtx(pathID(r))
	if err != nil {
		httpErr(w, 404, err.Error())
		return
	}
	var in struct {
		Note string `json:"note"`
	}
	_ = readJSON(r, &in)
	tid, err := scriptTask(p, e, in.Note)
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

const extractSystem = `你是漫剧美术统筹。阅读本集剧本，列出需要提前做设定图的视觉资产：角色、场景、关键道具。资产库在各集之间共享。
要求：
- 角色：每个有台词或反复出现的人物一项；description 写固定外貌（年龄感、性别、发型发色、五官特征、体型、服装配色），不写剧情；人物小传里有的人物，外貌以小传为准；
- 场景：每个独立地点一项；description 写空间结构、时代、光线、主色调；
- 道具：只列剧情关键、会被特写的物件，没有就留空数组；
- prompt：写给生图模型的中文提示词，单独描述这个资产本身，不要出现其他角色；
- 同一个人物的不同时期 / 不同造型（少年时、多年前、换装后）单独列一项，name 写成「本名（时期）」，如「阿策（千禧年）」，description 只写这个时期的年龄感、发型和服装；应用会自动拿本名那个角色的图锁脸；
- 已在资产库里的（见下方名单）不要重复列出。
只输出 JSON：{"characters":[{"name":"","description":"","prompt":""}],"scenes":[...],"props":[...]}`

func extractTask(p *Project, e *Episode) (int64, error) {
	if strings.TrimSpace(e.Script) == "" {
		return 0, errors.New("先写好剧本")
	}
	ch, model, err := textChannelFor(p)
	if err != nil {
		return 0, err
	}
	existing := listAssets(p.ID)
	var names []string
	for _, a := range existing {
		names = append(names, a.Name)
	}
	user := "画面风格：" + orDefault(p.Style, "不限") + "\n\n人物小传：\n" + orDefault(p.Bios, "（无）") + "\n\n资产库已有：" + orDefault(strings.Join(names, "、"), "（空）") + "\n\n本集剧本：\n" + e.Script
	return startTask(p.ID, e.ID, "extract", "project", p.ID, func(ctx context.Context) error {
		text, err := callChatL(ctx, p.ID, e.ID, ch, model, sysFor(p, extractSystem), user, 0.4)
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
		for _, kind := range []string{"character", "scene", "prop"} {
			list := map[string][]assetDraft{"character": out.Characters, "scene": out.Scenes, "prop": out.Props}[kind]
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
}

func handleAssetsExtract(w http.ResponseWriter, r *http.Request) {
	p, e, err := epCtx(pathID(r))
	if err != nil {
		httpErr(w, 404, err.Error())
		return
	}
	tid, err := extractTask(p, e)
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	writeJSON(w, map[string]any{"task_id": tid})
}

type assetDraft struct {
	BaseID      *int64 `json:"base_id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Prompt      string `json:"prompt"`
}

// tagCol 资产图片标签 → 选定列（ref 参考图没有选定列）
var tagCol = map[string]string{"": "picked_image_id", "sheet": "sheet_image_id", "expr": "expr_image_id"}

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
			if rows.Scan(&c) == nil && len(c) > 1 {
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
	id, err := res.LastInsertId()
	if err == nil && d.BaseID == nil && kind == "character" {
		if b := guessBase(pid, d.Name); b > 0 {
			d.BaseID = &b
		}
	}
	if err == nil && d.BaseID != nil && *d.BaseID > 0 {
		if a, e := getAsset(id); e == nil {
			if e := setAssetBase(a, *d.BaseID); e != nil {
				return id, e
			}
		}
	}
	return id, err
}

var periodSuffixRe = regexp.MustCompile(`^(.+?)\s*(?:[（(【\[].+[）)】\]]|[·・\-—]\s*\S+)$`)

// guessBase 「阿策（千禧年）」「阿策·少年」这种名字：同作品里已有「阿策」时自动挂到它名下
func guessBase(pid int64, name string) int64 {
	m := periodSuffixRe.FindStringSubmatch(strings.TrimSpace(name))
	if m == nil {
		return 0
	}
	root := strings.TrimSpace(m[1])
	var id int64
	_ = db.QueryRow(`SELECT id FROM assets WHERE project_id=? AND kind='character' AND name=? ORDER BY id LIMIT 1`, pid, root).Scan(&id)
	return id
}

// setAssetBase 把角色挂到另一个角色名下，作为同一个人的另一个时期（0 = 取消）
func setAssetBase(a *Asset, baseID int64) error {
	if baseID == 0 {
		_, _ = db.Exec(`UPDATE assets SET base_id=0 WHERE id=?`, a.ID)
		return nil
	}
	if a.Kind != "character" {
		return errors.New("只有角色可以设为另一个时期")
	}
	b, err := getAsset(baseID)
	if err != nil || b.ProjectID != a.ProjectID || b.Kind != "character" {
		return errors.New("本体角色不存在（要在同一个作品里）")
	}
	if b.ID == a.ID {
		return errors.New("不能把角色挂在自己名下")
	}
	// 链到底：本体本身也是某人的另一时期时，直接挂到最初的本体上
	for i := 0; i < 5 && b.BaseID > 0; i++ {
		nb, err := getAsset(b.BaseID)
		if err != nil || nb.ID == a.ID {
			break
		}
		b = nb
	}
	if b.ID == a.ID {
		return errors.New("不能互相挂靠")
	}
	_, _ = db.Exec(`UPDATE assets SET base_id=? WHERE id=?`, b.ID, a.ID)
	// 原来挂在这个角色名下的时期，跟着挂到新本体
	_, _ = db.Exec(`UPDATE assets SET base_id=? WHERE base_id=?`, b.ID, a.ID)
	return nil
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
	var in struct {
		assetDraft
		Voice *VoiceSpec `json:"voice"`
	}
	if err := readJSON(r, &in); err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	if strings.TrimSpace(in.Name) == "" {
		in.Name = a.Name
	}
	_, _ = db.Exec(`UPDATE assets SET name=?,description=?,prompt=? WHERE id=?`, strings.TrimSpace(in.Name), strings.TrimSpace(in.Description), strings.TrimSpace(in.Prompt), a.ID)
	if in.BaseID != nil && *in.BaseID != a.BaseID {
		if err := setAssetBase(a, *in.BaseID); err != nil {
			httpErr(w, 400, err.Error())
			return
		}
	}
	if in.Voice != nil {
		_, _ = db.Exec(`UPDATE assets SET voice=? WHERE id=?`, mustJSON(in.Voice), a.ID)
	}
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
	_, _ = db.Exec(`UPDATE assets SET base_id=0 WHERE base_id=?`, a.ID)
	touchProject(a.ProjectID)
	writeJSON(w, map[string]any{"ok": true})
}

var assetFrame = map[string]string{
	"character": "角色设定图：单人，全身正面站姿，表情自然，干净的浅灰纯色背景，无文字，人物居中完整入画",
	"scene":     "场景概念图：空镜，无人物，构图完整，体现空间纵深与光线氛围",
	"prop":      "道具设定图：单个物件居中，干净的浅灰纯色背景，无文字，细节清晰",
}

// 定妆图模板：三视图 / 表情表（以选定的设定图为参考）
var lookFrame = map[string]string{
	"sheet": "角色定妆三视图：同一个角色的正面、侧面、背面三个全身站姿并排，比例一致，同一套服装与发型，干净的浅灰纯色背景，无文字，平光，细节清晰",
	"expr":  "角色表情表：同一个角色的头肩特写 3×2 网格，分别是平静、微笑、大笑、愤怒、悲伤、惊讶六种表情，五官与发型保持一致，干净的浅灰纯色背景，无文字",
}

func assetPrompt(p *Project, a *Asset, tag string) string {
	body := a.Prompt
	if strings.TrimSpace(body) == "" {
		body = a.Name + "，" + a.Description
	}
	parts := []string{}
	if s := strings.TrimSpace(p.Style); s != "" {
		parts = append(parts, "画面风格："+s)
	}
	if f := lookFrame[tag]; f != "" {
		if tag == "sheet" && isRealistic(p) {
			f += "；" + realSheetExtra
		}
		parts = append(parts, f, body)
	} else {
		parts = append(parts, assetFrame[a.Kind], body)
	}
	if l := lookLine(p, a); l != "" {
		parts = append(parts, l)
	} else if a.Kind == "character" && a.Description != "" && !strings.Contains(body, a.Description) {
		parts = append(parts, "外貌："+a.Description)
	}
	if n := assetRefNoteText(a, tag); n != "" {
		parts = append(parts, n)
	}
	if isRealistic(p) {
		parts = append(parts, realTail)
	}
	return strings.Join(parts, "。")
}

// assetRef 资产出图用的一张参考图
type assetRef struct {
	ID   int64
	Kind string // own：自己的定妆图（脸、发型、服装都一致）；face：只锁脸；look：只参考外形风格
	Note string
}

// assetRefs 资产出图的参考图，按优先级排列（调用方按渠道上限截断）：
//   - 三视图以设定图为参考；表情表与重画设定图优先用三视图，再加设定图（自己的图：脸、发型、服装都一致）
//   - 手动指定的参考图（tag=ref）：角色只锁脸，场景、道具参考外形与风格
//   - 另一时期（base_id）：拿本体的三视图 / 设定图只锁脸，年龄、发型、服装按自己的描述
func assetRefs(a *Asset, tag string) []assetRef {
	var out []assetRef
	isChar := a.Kind == "character"
	if isChar {
		switch tag {
		case "sheet":
			if a.PickedImageID > 0 {
				out = append(out, assetRef{a.PickedImageID, "own", "设定图"})
			}
		default:
			if a.SheetImageID > 0 {
				out = append(out, assetRef{a.SheetImageID, "own", "定妆三视图"})
			}
			if a.PickedImageID > 0 && tag == "expr" {
				out = append(out, assetRef{a.PickedImageID, "own", "设定图"})
			}
		}
	}
	// 手动参考图：最新的两张
	refs := listImages(`WHERE owner_type='asset' AND owner_id=? AND tag='ref' ORDER BY id DESC LIMIT 2`, a.ID)
	for _, im := range refs {
		k := "look"
		if isChar {
			k = "face"
		}
		out = append(out, assetRef{im.ID, k, "参考照"})
	}
	if isChar && a.BaseID > 0 && a.BaseID != a.ID {
		if b, err := getAsset(a.BaseID); err == nil {
			if id, note := charRefImage(b); id > 0 {
				out = append(out, assetRef{id, "base:" + b.Name, note})
			}
		}
	}
	return out
}

// assetRefNoteText 说明每张参考图是什么、该参考什么
func assetRefNoteText(a *Asset, tag string) string {
	return refNotesFor(a, assetRefs(a, tag))
}

func refNotesFor(a *Asset, refs []assetRef) string {
	var own, other []string
	for i, r := range refs {
		n := i + 1
		switch {
		case r.Kind == "own":
			own = append(own, refNote(n, "这个角色", a.Name, r.Note, false))
		case r.Kind == "face":
			other = append(other, fmt.Sprintf("参考图%d是「%s」的参考照：脸型、五官、骨相、肤色以它为准，画成同一个人", n, a.Name))
		case r.Kind == "look":
			other = append(other, fmt.Sprintf("参考图%d是「%s」的参考图：外形、材质和风格以它为准", n, a.Name))
		case strings.HasPrefix(r.Kind, "base:"):
			bn := strings.TrimPrefix(r.Kind, "base:")
			s := fmt.Sprintf("参考图%d是同一个人「%s」另一个时期的%s", n, bn, r.Note)
			if r.Note == "定妆三视图" {
				s += "（同一个人的正面、侧面、背面）"
			}
			s += "：必须是同一个人，脸型、五官比例、眼型、鼻型、唇形、骨相和肤色特征保持一致；年龄感、发型、妆容、服装和配饰按本次描述来画，不要照搬参考图的发型和服装"
			other = append(other, s)
		}
	}
	var parts []string
	if len(own) > 0 {
		parts = append(parts, strings.Join(own, "，")+"；脸、发型、服装必须与这些参考图完全一致")
	}
	parts = append(parts, other...)
	return strings.Join(parts, "。")
}

func assetAspect(kind, tag, projectAspect string) string {
	switch tag {
	case "sheet":
		return "16:9"
	case "expr":
		return "3:2"
	}
	switch kind {
	case "character":
		return "3:4"
	case "prop":
		return "1:1"
	}
	return projectAspect
}

// genAssetTask tag 为空 = 设定图；sheet = 三视图；expr = 表情表。chID/model 不为空时覆盖作品默认的生图渠道
func genAssetTask(p *Project, a *Asset, n int, tag string, ov ...imgOverride) (int64, error) {
	ch, model, err := imageChannelFor(p)
	if err != nil {
		return 0, err
	}
	if len(ov) > 0 {
		if ch, model, err = applyOverride(ch, model, ov[0]); err != nil {
			return 0, err
		}
	}
	if tag != "" && a.Kind != "character" {
		return 0, errors.New("只有角色可以做三视图和表情表")
	}
	params := paramsFor(ch, assetAspect(a.Kind, tag, p.Aspect), n)
	kind := "image"
	if tag != "" {
		kind = "image-" + tag
	}
	return startTask(p.ID, 0, kind, "asset", a.ID, func(ctx context.Context) error {
		waitBaseImage(ctx, a)
		if x, err := getAsset(a.ID); err == nil {
			a = x
		}
		all := assetRefs(a, tag)
		if lim := refLimit(ch); len(all) > lim {
			all = all[:lim]
		}
		var refs []refFile
		var kept []assetRef
		for _, r := range all {
			if im, err := getImage(r.ID); err == nil {
				if rf, err := readImageRef(im); err == nil {
					refs = append(refs, rf)
					kept = append(kept, r)
				}
			}
		}
		prompt := assetPromptWith(p, a, tag, kept)
		imgs, sent, err := generateImages(ctx, ch, model, prompt, params, refs)
		ledgerAdd(p.ID, 0, "image", len(imgs), 0, 0, 0, "")
		if err != nil {
			return err
		}
		var first int64
		for _, data := range imgs {
			im, err := storeImageX(p.ID, 0, "asset", a.ID, tag, data, "gen", sent, ch.ID, model)
			if err == nil && first == 0 {
				first = im.ID
			}
		}
		if first == 0 {
			return errors.New("图片保存失败")
		}
		col := map[string]string{"": "picked_image_id", "sheet": "sheet_image_id", "expr": "expr_image_id"}[tag]
		_, _ = db.Exec(`UPDATE assets SET `+col+`=? WHERE id=? AND `+col+`=0`, first, a.ID)
		return nil
	})
}

// waitBaseImage 另一时期的角色要拿本体的图锁脸：本体还在出图（比如「全部生成」同时开始）时，先等它出完，最多 5 分钟
func waitBaseImage(ctx context.Context, a *Asset) {
	if a.Kind != "character" || a.BaseID == 0 {
		return
	}
	for i := 0; i < 100; i++ {
		b, err := getAsset(a.BaseID)
		if err != nil {
			return
		}
		if id, _ := charRefImage(b); id > 0 {
			return
		}
		var busy int
		_ = db.QueryRow(`SELECT COUNT(*) FROM tasks WHERE status='running' AND owner_type='asset' AND owner_id=?`, b.ID).Scan(&busy)
		if busy == 0 {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(3 * time.Second):
		}
	}
}

// assetPromptWith 与 assetPrompt 相同，但参考图说明只写实际发出去的那几张
func assetPromptWith(p *Project, a *Asset, tag string, refs []assetRef) string {
	full := assetPrompt(p, a, tag)
	if all := assetRefNoteText(a, tag); all != "" {
		full = strings.Replace(full, all, refNotesFor(a, refs), 1)
	}
	return strings.ReplaceAll(full, "。。", "。")
}

// imgOverride 单次出图指定渠道和模型（界面下拉 / 智能体参数）
type imgOverride struct {
	ChannelID int64  `json:"channel_id"`
	Model     string `json:"model"`
}

func applyOverride(ch *Channel, model string, o imgOverride) (*Channel, string, error) {
	if o.ChannelID > 0 {
		c, err := getChannel(o.ChannelID)
		if err != nil || c.Kind != "image" {
			return nil, "", fmt.Errorf("生图渠道 %d 不存在", o.ChannelID)
		}
		if c.ID != ch.ID {
			model = ""
		}
		ch = c
	}
	if m := strings.TrimSpace(o.Model); m != "" {
		if len(ch.Models) > 0 && !contains(ch.Models, m) {
			return nil, "", fmt.Errorf("渠道「%s」没有模型 %s，可选：%s", ch.Name, m, strings.Join(ch.Models, "、"))
		}
		model = m
	}
	if model == "" && len(ch.Models) > 0 {
		model = ch.Models[0]
	}
	return ch, model, nil
}

func handleAssetGenerate(w http.ResponseWriter, r *http.Request) {
	a, err := getAsset(pathID(r))
	if err != nil {
		httpErr(w, 404, "资产不存在")
		return
	}
	p, _ := getProject(a.ProjectID)
	var in struct {
		N   int    `json:"n"`
		Tag string `json:"tag"`
		imgOverride
	}
	_ = readJSON(r, &in)
	if in.Tag != "sheet" && in.Tag != "expr" {
		in.Tag = ""
	}
	tid, err := genAssetTask(p, a, clampInt(in.N, 1, 4), in.Tag, in.imgOverride)
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	writeJSON(w, map[string]any{"task_id": tid})
}

// startMissingAssets 给还没有设定图的资产各生成一张；返回启动数量
func startMissingAssets(p *Project) (int, error) {
	n := 0
	var lastErr error
	for _, a := range listAssets(p.ID) {
		if hasTag(a.Images, "") {
			continue
		}
		if _, err := genAssetTask(p, a, 1, ""); err != nil {
			if err.Error() != "正在进行中" {
				lastErr = err
			}
			continue
		}
		n++
	}
	return n, lastErr
}

// startMissingSheets 给已有设定图、还没有三视图的角色各做一张三视图（关键帧锁脸主要靠它）
func startMissingSheets(p *Project) (int, error) {
	n := 0
	var lastErr error
	for _, a := range listAssets(p.ID) {
		if a.Kind != "character" || a.PickedImageID == 0 || a.SheetImageID > 0 || hasTag(a.Images, "sheet") {
			continue
		}
		if _, err := genAssetTask(p, a, 1, "sheet"); err != nil {
			if err.Error() != "正在进行中" {
				lastErr = err
			}
			continue
		}
		n++
	}
	return n, lastErr
}

func handleSheetsGenerateAll(w http.ResponseWriter, r *http.Request) {
	p, err := getProject(pathID(r))
	if err != nil {
		httpErr(w, 404, "作品不存在")
		return
	}
	n, lastErr := startMissingSheets(p)
	if n == 0 && lastErr != nil {
		httpErr(w, 400, lastErr.Error())
		return
	}
	writeJSON(w, map[string]any{"started": n})
}

func hasTag(ims []*Image, tag string) bool {
	for _, im := range ims {
		if im.Tag == tag {
			return true
		}
	}
	return false
}

func handleAssetsGenerateAll(w http.ResponseWriter, r *http.Request) {
	p, err := getProject(pathID(r))
	if err != nil {
		httpErr(w, 404, "作品不存在")
		return
	}
	n, lastErr := startMissingAssets(p)
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
	tag := r.URL.Query().Get("tag")
	if tag != "sheet" && tag != "expr" && tag != "ref" {
		tag = ""
	}
	if (tag == "sheet" || tag == "expr") && a.Kind != "character" {
		httpErr(w, 400, "只有角色有三视图和表情表")
		return
	}
	data, err := readUpload(r)
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	im, err := storeImageX(a.ProjectID, 0, "asset", a.ID, tag, data, "upload", "", 0, "")
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	if col := tagCol[tag]; col != "" {
		_, _ = db.Exec(`UPDATE assets SET `+col+`=? WHERE id=?`, im.ID, a.ID)
	}
	touchProject(a.ProjectID)
	writeJSON(w, im)
}

// ---------- ④ 分镜 ----------

const storyboardSystem = `你是漫剧分镜师。把剧本拆成按顺序播放的镜头，每个镜头会先生成一张关键帧，再用图生视频模型生成 5–10 秒的动态镜头。
要求：
- 镜头总数接近目标数量，单个镜头 duration 取 5 到 10 的整数秒，总时长接近单集时长；
- camera：景别 + 机位/运镜，如「近景 · 缓慢推近」「全景 · 固定」「过肩 · 轻微手持」；
- action：画面里发生什么（人物动作、表情、环境变化），一个镜头只做一件事；
- dialogue：这个镜头里的台词或旁白，每句一行，格式「角色名：台词」，旁白写「旁白：……」，需要特别语气时在台词前用全角括号注明，如「林晚：（压低声音）别出声」；没有就留空；一个镜头的台词念完不要超过镜头时长；
- assets：这个镜头出现的资产编号（只能用下面资产表里的编号，角色在前、场景其次、道具最后）；
- continuous：和上一个镜头是同一场景里连续的动作时为 true（会用上一镜头的最后一帧衔接），否则 false；
- keyframe_prompt：关键帧画面的中文生图提示词，写清构图、景别、人物位置与表情、光线，直接用资产名称指代角色和场景；
- video_prompt：给图生视频模型的中文描述，只写从这张关键帧开始的动作、镜头运动、环境动态和声音（台词、音效、配乐氛围），不重复静态外观。
只输出 JSON：{"shots":[{"duration":5,"camera":"","action":"","dialogue":"","assets":["C01","S01"],"continuous":false,"keyframe_prompt":"","video_prompt":""}]}`

type shotDraft struct {
	Duration       float64  `json:"duration"`
	Camera         string   `json:"camera"`
	Action         string   `json:"action"`
	Dialogue       string   `json:"dialogue"`
	Assets         []string `json:"assets"`
	Continuous     bool     `json:"continuous"`
	KeyframePrompt string   `json:"keyframe_prompt"`
	VideoPrompt    string   `json:"video_prompt"`
	Chain          string   `json:"chain"`
	SfxVolume      *float64 `json:"sfx_volume"`
}

func assetTable(assets []*Asset) string {
	kinds := map[string]string{"character": "角色", "scene": "场景", "prop": "道具"}
	var b strings.Builder
	for _, a := range assets {
		fmt.Fprintf(&b, "%s｜%s｜%s｜%s\n", a.Code, kinds[a.Kind], a.Name, a.Description)
	}
	return b.String()
}

func splitTask(p *Project, e *Episode) (int64, error) {
	if strings.TrimSpace(e.Script) == "" {
		return 0, errors.New("先写好剧本")
	}
	ch, model, err := textChannelFor(p)
	if err != nil {
		return 0, err
	}
	assets := listAssets(p.ID)
	user := fmt.Sprintf("目标镜头数：%d\n单集时长：约 %d 秒\n画幅：%s\n画面风格：%s\n\n资产表（编号｜类型｜名称｜外观）：\n%s\n剧本：\n%s",
		p.ShotTarget, p.EpisodeSec, p.Aspect, orDefault(p.Style, "不限"), orDefault(assetTable(assets), "（暂无）\n"), e.Script)
	return startTask(p.ID, e.ID, "storyboard", "episode", e.ID, func(ctx context.Context) error {
		text, err := callChatL(ctx, p.ID, e.ID, ch, model, sysFor(p, storyboardSystem), user, 0.5)
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
		if _, err := tx.Exec(`DELETE FROM shots WHERE episode_id=?`, e.ID); err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM lines WHERE episode_id=?`, e.ID); err != nil {
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
			d := math.Max(5, math.Min(10, math.Round(s.Duration)))
			chain := "off"
			if s.Continuous && i > 0 {
				chain = "ref"
			}
			if _, err := tx.Exec(`INSERT INTO shots(project_id,episode_id,sort,duration,camera,action,dialogue,asset_codes,keyframe_prompt,video_prompt,chain,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
				p.ID, e.ID, i+1, d, s.Camera, s.Action, s.Dialogue, mustJSON(codes), s.KeyframePrompt, s.VideoPrompt, chain, now()); err != nil {
				return err
			}
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		syncLines(p, e)
		return nil
	})
}

func handleShotsSplit(w http.ResponseWriter, r *http.Request) {
	p, e, err := epCtx(pathID(r))
	if err != nil {
		httpErr(w, 404, err.Error())
		return
	}
	tid, err := splitTask(p, e)
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	writeJSON(w, map[string]any{"task_id": tid})
}

func handleShotCreate(w http.ResponseWriter, r *http.Request) {
	_, e, err := epCtx(pathID(r))
	if err != nil {
		httpErr(w, 404, err.Error())
		return
	}
	var in struct {
		After int64 `json:"after"`
	}
	_ = readJSON(r, &in)
	shots := listShots(e.ID, false)
	sort := len(shots) + 1
	if in.After > 0 {
		for _, s := range shots {
			if s.ID == in.After {
				sort = s.Sort + 1
			}
		}
		_, _ = db.Exec(`UPDATE shots SET sort=sort+1 WHERE episode_id=? AND sort>=?`, e.ID, sort)
	}
	res, err := db.Exec(`INSERT INTO shots(project_id,episode_id,sort,duration,created_at) VALUES(?,?,?,5,?)`, e.ProjectID, e.ID, sort, now())
	if err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	id, _ := res.LastInsertId()
	renumberShots(e.ID)
	s, _ := getShot(id)
	writeJSON(w, s)
}

func renumberShots(eid int64) {
	for i, s := range listShots(eid, false) {
		_, _ = db.Exec(`UPDATE shots SET sort=? WHERE id=?`, i+1, s.ID)
	}
	touchEpisode(eid)
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
	chain := in.Chain
	if chain != "ref" && chain != "first" {
		chain = "off"
	}
	_, _ = db.Exec(`UPDATE shots SET duration=?,camera=?,action=?,dialogue=?,asset_codes=?,keyframe_prompt=?,video_prompt=?,chain=? WHERE id=?`,
		d, in.Camera, in.Action, in.Dialogue, mustJSON(in.Assets), in.KeyframePrompt, in.VideoPrompt, chain, s.ID)
	if in.SfxVolume != nil {
		_, _ = db.Exec(`UPDATE shots SET sfx_volume=? WHERE id=?`, clampF(*in.SfxVolume, 0, 2), s.ID)
	}
	if in.Dialogue != s.Dialogue {
		if p, e, err := epCtx(s.EpisodeID); err == nil {
			syncLines(p, e)
		}
	}
	touchEpisode(s.EpisodeID)
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
	_, _ = db.Exec(`DELETE FROM lines WHERE shot_id=?`, s.ID)
	_, _ = db.Exec(`DELETE FROM shots WHERE id=?`, s.ID)
	renumberShots(s.EpisodeID)
	writeJSON(w, map[string]any{"ok": true})
}

func handleShotOrder(w http.ResponseWriter, r *http.Request) {
	var ids []int64
	if err := readJSON(r, &ids); err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	for i, id := range ids {
		_, _ = db.Exec(`UPDATE shots SET sort=? WHERE id=? AND episode_id=?`, i+1, id, pathID(r))
	}
	touchEpisode(pathID(r))
	writeJSON(w, map[string]any{"ok": true})
}

// ---------- ⑤ 关键帧 ----------

type refPick struct {
	Code  string `json:"code"`
	Name  string `json:"name"`
	Kind  string `json:"kind"`
	Image string `json:"image"`
	Used  bool   `json:"used"`
	Note  string `json:"note"`
}

// planRefs 按相关度排序参考图：本镜头里最重要的角色（台词多、全剧出场多）优先用定妆三视图，
// 然后其余角色、场景、道具；上一镜头的最后一帧（衔接时）排在主角之后。超出渠道上限的改用文字描述。
func planRefs(p *Project, s *Shot, limit int) ([]refPick, []*Image) {
	assets := listAssets(p.ID)
	byCode := map[string]*Asset{}
	for _, a := range assets {
		byCode[a.Code] = a
	}
	type cand struct {
		a     *Asset
		score float64
	}
	var chars, others []cand
	for i, c := range s.Assets {
		a := byCode[c]
		if id, _ := charRefImage(a); id == 0 {
			continue
		}
		sc := float64(a.Appear) - float64(i)*0.01
		if a.Kind == "character" {
			if strings.Contains(s.Dialogue, a.Name) {
				sc += 100
			}
			if strings.Contains(s.Action, a.Name) || strings.Contains(s.KeyframePrompt, a.Name) {
				sc += 10
			}
			chars = append(chars, cand{a, sc})
		} else {
			if a.Kind == "scene" {
				sc += 1000
			}
			others = append(others, cand{a, sc})
		}
	}
	sort.SliceStable(chars, func(i, j int) bool { return chars[i].score > chars[j].score })
	sort.SliceStable(others, func(i, j int) bool { return others[i].score > others[j].score })
	kinds := map[string]string{"character": "角色", "scene": "场景", "prop": "道具"}
	var picks []refPick
	var ims []*Image
	add := func(code, name, kind string, imID int64, note string) {
		im, err := getImage(imID)
		if err != nil {
			return
		}
		used := len(ims) < limit
		picks = append(picks, refPick{Code: code, Name: name, Kind: kind, Image: im.URL, Used: used, Note: note})
		if used {
			ims = append(ims, im)
		}
	}
	for i, c := range chars {
		// 主角、以及参考图位置够用时每个角色都用定妆三视图；否则用设定图（单人全身，占画面更少）
		img, note := charRefImage(c.a)
		if c.a.PickedImageID > 0 && note == "定妆三视图" && !(i == 0 || limit >= len(chars)+len(others)+1) {
			img, note = c.a.PickedImageID, "设定图"
		}
		add(c.a.Code, c.a.Name, kinds[c.a.Kind], img, note)
		if i == 0 && s.Chain == "ref" {
			if lf := prevLastFrame(s); lf != nil {
				add("上一镜", "上一镜头最后一帧", "衔接", lf.ID, "衔接")
			}
		}
	}
	if len(chars) == 0 && s.Chain == "ref" {
		if lf := prevLastFrame(s); lf != nil {
			add("上一镜", "上一镜头最后一帧", "衔接", lf.ID, "衔接")
		}
	}
	for _, c := range others {
		add(c.a.Code, c.a.Name, kinds[c.a.Kind], c.a.PickedImageID, "设定图")
	}
	return picks, ims
}

func refLimit(ch *Channel) int {
	if ch == nil || ch.Caps.Refs.Mode == "off" {
		return 0
	}
	if ch.Caps.Refs.Max == 0 {
		return 4
	}
	return ch.Caps.Refs.Max
}

// keyframePlan 按渠道参考图上限挑选参考图，并在提示词里说明每张参考图是谁
func keyframePlan(p *Project, s *Shot, ch *Channel) (string, []refFile, []refPick) {
	assets := listAssets(p.ID)
	byCode := map[string]*Asset{}
	for _, a := range assets {
		byCode[a.Code] = a
	}
	picks, ims := planRefs(p, s, refLimit(ch))
	var refs []refFile
	var notes []string
	used := map[string]bool{}
	k := 0
	for _, pk := range picks {
		if !pk.Used {
			continue
		}
		rf, err := readImageRef(ims[k])
		k++
		if err != nil {
			continue
		}
		refs = append(refs, rf)
		used[pk.Code] = true
		if pk.Kind == "衔接" {
			notes = append(notes, fmt.Sprintf("参考图%d是上一个镜头的最后一帧，人物位置、光线和环境要与它连贯", len(refs)))
		} else {
			notes = append(notes, refNote(len(refs), pk.Kind, pk.Name, pk.Note, true))
		}
	}
	kinds := map[string]string{"character": "角色", "scene": "场景", "prop": "道具"}
	parts := []string{}
	if st := strings.TrimSpace(p.Style); st != "" {
		parts = append(parts, "画面风格："+st)
	}
	body := s.KeyframePrompt
	if strings.TrimSpace(body) == "" {
		body = s.Camera + "，" + s.Action
	}
	parts = append(parts, body)
	for _, c := range s.Assets {
		a := byCode[c]
		if a == nil {
			continue
		}
		// 角色：不管有没有参考图，都把固定造型（发型、服装）写明
		if l := lookLine(p, a); l != "" {
			parts = append(parts, l)
			continue
		}
		if used[c] || a.Description == "" {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s「%s」：%s", kinds[a.Kind], a.Name, a.Description))
	}
	if len(notes) > 0 {
		parts = append(parts, strings.Join(notes, "，")+"；保持角色的脸、发型和服装与参考图一致，场景沿用参考图的空间与色调")
	}
	if isRealistic(p) {
		parts = append(parts, realTail)
	}
	parts = append(parts, "电影感单帧画面，无字幕，无文字")
	return strings.Join(parts, "。"), refs, picks
}

func genKeyframeTask(p *Project, s *Shot, n int, ov ...imgOverride) (int64, error) {
	ch, model, err := imageChannelFor(p)
	if err != nil {
		return 0, err
	}
	if len(ov) > 0 {
		if ch, model, err = applyOverride(ch, model, ov[0]); err != nil {
			return 0, err
		}
	}
	if s.Chain == "ref" {
		_, _ = ensurePrevLastFrame(s)
	}
	prompt, refs, _ := keyframePlan(p, s, ch)
	params := paramsFor(ch, p.Aspect, n)
	return startTask(p.ID, s.EpisodeID, "image", "shot", s.ID, func(ctx context.Context) error {
		imgs, sent, err := generateImages(ctx, ch, model, prompt, params, refs)
		ledgerAdd(p.ID, s.EpisodeID, "image", len(imgs), 0, 0, 0, "")
		if err != nil {
			return err
		}
		var first int64
		for _, data := range imgs {
			im, err := storeImageX(p.ID, s.EpisodeID, "shot", s.ID, "", data, "gen", sent, ch.ID, model)
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
		imgOverride
	}
	_ = readJSON(r, &in)
	tid, err := genKeyframeTask(p, s, clampInt(in.N, 1, 4), in.imgOverride)
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	writeJSON(w, map[string]any{"task_id": tid})
}

func handleShotRefs(w http.ResponseWriter, r *http.Request) {
	s, err := getShot(pathID(r))
	if err != nil {
		httpErr(w, 404, "镜头不存在")
		return
	}
	p, _ := getProject(s.ProjectID)
	ch, _, _ := imageChannelFor(p)
	picks, _ := planRefs(p, s, refLimit(ch))
	if picks == nil {
		picks = []refPick{}
	}
	writeJSON(w, map[string]any{"refs": picks, "limit": refLimit(ch)})
}

func startMissingKeyframes(p *Project, e *Episode) (int, error) {
	n := 0
	var lastErr error
	for _, s := range listShots(e.ID, true) {
		if len(s.Images) > 0 {
			continue
		}
		if _, err := genKeyframeTask(p, s, 1); err != nil {
			if err.Error() != "正在进行中" {
				lastErr = err
			}
			continue
		}
		n++
	}
	return n, lastErr
}

func handleKeyframesGenerateAll(w http.ResponseWriter, r *http.Request) {
	p, e, err := epCtx(pathID(r))
	if err != nil {
		httpErr(w, 404, err.Error())
		return
	}
	n, lastErr := startMissingKeyframes(p, e)
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
	im, err := storeImageX(s.ProjectID, s.EpisodeID, "shot", s.ID, "", data, "upload", "", 0, "")
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	_, _ = db.Exec(`UPDATE shots SET picked_image_id=? WHERE id=?`, im.ID, s.ID)
	touchEpisode(s.EpisodeID)
	writeJSON(w, im)
}
