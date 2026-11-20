package main

// 外部编剧：让 Antigravity 里的 Gemini（或任何 MCP 客户端）通过 MCP 直接写设定集、梗概、剧本、分镜、本集摘要和资产外观，
// 并能「看」资产图、关键帧、视频首尾帧，「听」台词配音。应用本身不调用任何文本模型。
// 写入的格式与应用自己的模型路径产出的完全一致，后面的关键帧、配音、视频、成片步骤不用改。

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	xdraw "golang.org/x/image/draw"
)

// ---------- 通用 ----------

// mcpRich 工具返回多种内容块（图片、音频）时用；mcpHandle 原样放进 content
type mcpRich struct {
	Blocks     []any
	Structured any
}

func textBlock(s string) map[string]any { return map[string]any{"type": "text", "text": s} }

func runeLen(s string) int { return utf8.RuneCountInString(strings.TrimSpace(s)) }

// flexText 字段既可以是字符串，也可以是字符串数组
type flexText string

func (f *flexText) UnmarshalJSON(b []byte) error {
	var s string
	if json.Unmarshal(b, &s) == nil {
		*f = flexText(s)
		return nil
	}
	var arr []any
	if json.Unmarshal(b, &arr) == nil {
		*f = flexText(flatText(arr))
		return nil
	}
	var m map[string]any
	if json.Unmarshal(b, &m) == nil {
		*f = flexText(flatText(m))
		return nil
	}
	return errors.New("应为字符串")
}

func argID(raw json.RawMessage, key string) int64 {
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil {
		return 0
	}
	v, ok := m[key]
	if !ok {
		return 0
	}
	s := strings.Trim(strings.TrimSpace(string(v)), `"`)
	id, _ := strconv.ParseInt(s, 10, 64)
	return id
}

func strictArgs(raw json.RawMessage, v any) error {
	if err := json.Unmarshal(raw, v); err != nil {
		return errors.New("参数格式不对：" + shortErr(err))
	}
	return nil
}

// ---------- 外部编剧在一键成集里的等待点 ----------

// writerResume 写入后，如果这一集的一键成集正在等外部编剧，就接着跑
func writerResume(eid int64, what string) string {
	lp := latestPipe(eid)
	if lp == nil || lp.Status != "waiting_writer" {
		return ""
	}
	lp.Status, lp.Message = "running", ""
	lp.say("外部编剧已交回%s，继续", what)
	go runPipe(lp.ID)
	return "一键成集已自动继续"
}

func (pp *Pipe) waitWriter(stage, msg string) error {
	pp.Status, pp.Stage, pp.Message = "waiting_writer", stage, msg
	pp.say("等待外部编剧：%s", msg)
	return errPipePause
}

// ---------- 写：设定集 ----------

type bioIn struct {
	Name        string `json:"name"`
	Age         string `json:"age"`
	Gender      string `json:"gender"`
	Look        string `json:"look"`
	Outfit      string `json:"outfit"`
	Personality string `json:"personality"`
	Motivation  string `json:"motivation"`
	Relations   string `json:"relations"`
	Speech      string `json:"speech"`
}

type outlineIn struct {
	No          int    `json:"no"`
	Plot        string `json:"plot"`
	Cliffhanger string `json:"cliffhanger"`
}

type bibleArgs struct {
	ProjectID int64           `json:"project_id"`
	Idea      *string         `json:"idea"`
	World     *flexText       `json:"world"`
	Bios      json.RawMessage `json:"bios"`
	Outline   json.RawMessage `json:"outline"`
}

func renderBios(raw json.RawMessage) (string, bool, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", false, nil
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return strings.TrimSpace(s), true, nil
	}
	var list []bioIn
	if err := json.Unmarshal(raw, &list); err != nil {
		return "", false, errors.New("bios 应为文本，或人物数组 [{name, age, look, outfit, personality, motivation, relations, speech}]")
	}
	var paras []string
	for i, b := range list {
		name := strings.TrimSpace(b.Name)
		if name == "" {
			return "", false, fmt.Errorf("bios 第 %d 个人物缺 name", i+1)
		}
		if strings.TrimSpace(b.Look+b.Outfit) == "" {
			return "", false, fmt.Errorf("人物「%s」缺外貌（look / outfit），做角色设定图要用", name)
		}
		parts := []string{name}
		if a := strings.TrimSpace(b.Age); a != "" {
			if _, err := strconv.Atoi(a); err == nil {
				a += " 岁"
			}
			parts = append(parts, a)
		}
		if g := strings.TrimSpace(b.Gender); g != "" {
			parts = append(parts, g)
		}
		head := strings.Join(parts, "，")
		var sent []string
		if l := strings.TrimSpace(b.Look); l != "" {
			sent = append(sent, "外貌："+strings.TrimRight(l, "。"))
		}
		if o := strings.TrimSpace(b.Outfit); o != "" {
			sent = append(sent, "常穿"+strings.TrimPrefix(strings.TrimRight(o, "。"), "穿"))
		}
		for _, kv := range [][2]string{{"性格", b.Personality}, {"动机", b.Motivation}, {"关系", b.Relations}, {"说话方式", b.Speech}} {
			if v := strings.TrimSpace(kv[1]); v != "" {
				sent = append(sent, kv[0]+"："+strings.TrimRight(v, "。"))
			}
		}
		paras = append(paras, head+"。"+strings.Join(sent, "。")+"。")
	}
	return strings.Join(paras, "\n"), true, nil
}

func renderOutline(raw json.RawMessage) (string, bool, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", false, nil
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return strings.TrimSpace(s), true, nil
	}
	var list []outlineIn
	if err := json.Unmarshal(raw, &list); err != nil {
		return "", false, errors.New("outline 应为文本，或分集数组 [{no, plot, cliffhanger}]")
	}
	var lines []string
	for i, o := range list {
		no := o.No
		if no <= 0 {
			no = i + 1
		}
		if strings.TrimSpace(o.Plot) == "" {
			return "", false, fmt.Errorf("outline 第 %d 集缺 plot", no)
		}
		ln := fmt.Sprintf("第 %d 集：%s", no, strings.TrimRight(strings.TrimSpace(o.Plot), "。"))
		if c := strings.TrimSpace(o.Cliffhanger); c != "" {
			ln += "。卡点：" + strings.TrimRight(c, "。")
		}
		lines = append(lines, ln+"。")
	}
	return strings.Join(lines, "\n"), true, nil
}

func toolSaveBible(ctx context.Context, raw json.RawMessage) (any, error) {
	var a bibleArgs
	if err := strictArgs(raw, &a); err != nil {
		return nil, err
	}
	p, err := getProject(a.ProjectID)
	if err != nil {
		return nil, errors.New("作品不存在，先用 list_projects 查作品编号")
	}
	bios, hasBios, err := renderBios(a.Bios)
	if err != nil {
		return nil, err
	}
	outline, hasOutline, err := renderOutline(a.Outline)
	if err != nil {
		return nil, err
	}
	if a.World == nil && !hasBios && !hasOutline && a.Idea == nil {
		return nil, errors.New("至少给出 world、bios、outline、idea 之一")
	}
	changed := []string{}
	if a.Idea != nil {
		if strings.TrimSpace(*a.Idea) == "" {
			return nil, errors.New("idea 不能为空字符串")
		}
		_, _ = db.Exec(`UPDATE projects SET idea=? WHERE id=?`, strings.TrimSpace(*a.Idea), p.ID)
		changed = append(changed, "故事创意")
	}
	if a.World != nil {
		w := strings.TrimSpace(string(*a.World))
		if runeLen(w) < 20 {
			return nil, errors.New("world 太短：写清时代、地点、社会规则和核心冲突的来源（建议 200–400 字）")
		}
		_, _ = db.Exec(`UPDATE projects SET world=? WHERE id=?`, w, p.ID)
		changed = append(changed, "世界观")
	}
	if hasBios {
		if runeLen(bios) < 10 {
			return nil, errors.New("bios 太短")
		}
		_, _ = db.Exec(`UPDATE projects SET bios=? WHERE id=?`, bios, p.ID)
		changed = append(changed, "人物小传")
	}
	if hasOutline {
		if runeLen(outline) < 10 {
			return nil, errors.New("outline 太短")
		}
		_, _ = db.Exec(`UPDATE projects SET outline=? WHERE id=?`, outline, p.ID)
		changed = append(changed, "分集总纲")
	}
	touchProject(p.ID)
	p, _ = getProject(p.ID)
	return map[string]any{"ok": true, "saved": changed, "project_id": p.ID, "world": p.World, "bios": p.Bios, "outline": p.Outline,
		"next": "用 upsert_asset 把主要角色的固定外貌写进资产库，再给要写的那一集 save_synopsis / save_script。"}, nil
}

// ---------- 写：梗概 ----------

type synopsisArgs struct {
	EpisodeID int64  `json:"episode_id"`
	Synopsis  string `json:"synopsis"`
	Title     string `json:"title"`
}

func toolSaveSynopsis(ctx context.Context, raw json.RawMessage) (any, error) {
	var a synopsisArgs
	if err := strictArgs(raw, &a); err != nil {
		return nil, err
	}
	_, e, err := epCtx(a.EpisodeID)
	if err != nil {
		return nil, err
	}
	s := strings.TrimSpace(a.Synopsis)
	if n := runeLen(s); n < 15 || n > 800 {
		return nil, fmt.Errorf("synopsis 要 15–800 字（现在 %d 字）：2–4 句话写清本集核心事件、反转和结尾卡点", n)
	}
	_, _ = db.Exec(`UPDATE episodes SET idea=? WHERE id=?`, s, e.ID)
	if t := strings.TrimSpace(a.Title); t != "" {
		_, _ = db.Exec(`UPDATE episodes SET title=? WHERE id=?`, t, e.ID)
	}
	touchEpisode(e.ID)
	out := map[string]any{"ok": true, "episode_id": e.ID, "no": e.No, "next": "接着用 save_script 交剧本。"}
	if m := writerResume(e.ID, "梗概"); m != "" {
		out["autopilot"] = m
	}
	return out, nil
}

// ---------- 写：剧本 ----------

type beatIn struct {
	Type    string `json:"type"` // action / line / narration
	Text    string `json:"text"`
	Speaker string `json:"speaker"`
	Emotion string `json:"emotion"`
}

type sceneIn struct {
	Location string   `json:"location"`
	Time     string   `json:"time"`
	Beats    []beatIn `json:"beats"`
}

type castIn struct {
	Name string `json:"name"`
	Desc string `json:"desc"`
}

type scriptArgs struct {
	EpisodeID   int64     `json:"episode_id"`
	Title       string    `json:"title"`
	Characters  []castIn  `json:"characters"`
	Scenes      []sceneIn `json:"scenes"`
	Cliffhanger string    `json:"cliffhanger"`
	Markdown    string    `json:"markdown"`
}

// renderScript 结构化剧本 → 与 scriptSystem 约定一致的 Markdown
func renderScript(a scriptArgs) (string, []string, error) {
	var warn []string
	title := strings.TrimSpace(a.Title)
	if title == "" {
		return "", nil, errors.New("title 不能为空")
	}
	if len(a.Scenes) == 0 {
		return "", nil, errors.New("scenes 至少一场")
	}
	if strings.TrimSpace(a.Cliffhanger) == "" {
		return "", nil, errors.New("cliffhanger 不能为空：漫剧每集结尾必须停在悬念上")
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n", title)
	if len(a.Characters) > 0 {
		b.WriteString("## 人物\n")
		for _, c := range a.Characters {
			if strings.TrimSpace(c.Name) == "" {
				return "", nil, errors.New("characters 里有人物缺 name")
			}
			fmt.Fprintf(&b, "- %s——%s\n", strings.TrimSpace(c.Name), strings.TrimSpace(c.Desc))
		}
		b.WriteString("\n")
	}
	lines := 0
	for i, sc := range a.Scenes {
		loc := strings.TrimSpace(sc.Location)
		if loc == "" {
			return "", nil, fmt.Errorf("第 %d 场缺 location", i+1)
		}
		if len(sc.Beats) == 0 {
			return "", nil, fmt.Errorf("第 %d 场没有内容（beats）", i+1)
		}
		head := fmt.Sprintf("## 第 %d 场 · %s", i+1, loc)
		if t := strings.TrimSpace(sc.Time); t != "" {
			head += " · " + t
		}
		b.WriteString(head + "\n")
		for j, bt := range sc.Beats {
			txt := strings.TrimSpace(bt.Text)
			if txt == "" {
				return "", nil, fmt.Errorf("第 %d 场第 %d 条 text 为空", i+1, j+1)
			}
			emo := strings.Trim(strings.TrimSpace(bt.Emotion), "（）()")
			typ := bt.Type
			if typ == "" {
				typ = "action"
				if strings.TrimSpace(bt.Speaker) != "" {
					typ = "line"
				}
			}
			switch typ {
			case "action", "picture":
				fmt.Fprintf(&b, "【画面】%s\n", txt)
				continue
			case "narration":
				if emo != "" {
					txt = "（" + emo + "）" + txt
				}
				fmt.Fprintf(&b, "（旁白）%s\n", txt)
				lines++
				continue
			case "line", "dialogue":
			default:
				return "", nil, fmt.Errorf("第 %d 场第 %d 条 type 只能是 action / line / narration", i+1, j+1)
			}
			sp := strings.TrimSpace(bt.Speaker)
			if sp == "" {
				return "", nil, fmt.Errorf("第 %d 场第 %d 条台词缺 speaker", i+1, j+1)
			}
			if n := runeLen(txt); n > 20 {
				warn = append(warn, fmt.Sprintf("第 %d 场「%s」的台词 %d 字，漫剧建议一句不超过 20 字", i+1, sp, n))
			}
			if emo != "" {
				fmt.Fprintf(&b, "%s：（%s）%s\n", sp, emo, txt)
			} else {
				fmt.Fprintf(&b, "%s：%s\n", sp, txt)
			}
			lines++
		}
		b.WriteString("\n")
	}
	if lines == 0 {
		warn = append(warn, "整集没有一句台词或旁白，配音和字幕会是空的")
	}
	fmt.Fprintf(&b, "【卡点】%s\n", strings.TrimSpace(a.Cliffhanger))
	if first := a.Scenes[0].Beats; len(first) > 0 && first[0].Type != "action" && first[0].Type != "picture" && first[0].Type != "" {
		warn = append(warn, "第一场没有先给【画面】：开场 3 秒要有能画出来的强钩子")
	}
	return b.String(), warn, nil
}

func toolSaveScript(ctx context.Context, raw json.RawMessage) (any, error) {
	var a scriptArgs
	if err := strictArgs(raw, &a); err != nil {
		return nil, err
	}
	p, e, err := epCtx(a.EpisodeID)
	if err != nil {
		return nil, err
	}
	var text string
	var warn []string
	if md := strings.TrimSpace(a.Markdown); md != "" {
		if len(a.Scenes) > 0 {
			return nil, errors.New("markdown 和 scenes 只能给一个")
		}
		if !strings.HasPrefix(md, "# ") {
			if t := strings.TrimSpace(a.Title); t != "" {
				md = "# " + t + "\n\n" + md
			} else {
				return nil, errors.New("markdown 剧本第一行要是「# 标题」，或另外传 title")
			}
		}
		if !strings.Contains(md, "## 第") {
			return nil, errors.New("markdown 剧本要按「## 第 N 场 · 地点 · 时间」分场")
		}
		if cliffOf(md) == "" {
			if c := strings.TrimSpace(a.Cliffhanger); c != "" {
				md += "\n\n【卡点】" + c
			} else {
				return nil, errors.New("剧本末尾缺「【卡点】……」一行（或传 cliffhanger）")
			}
		}
		for _, pl := range parseDialogue(strings.Join(dialogueLinesOf(md), "\n")) {
			if runeLen(pl.text) > 20 {
				warn = append(warn, fmt.Sprintf("「%s」的台词 %d 字，漫剧建议一句不超过 20 字", pl.speaker, runeLen(pl.text)))
			}
		}
		text = md + "\n"
	} else {
		text, warn, err = renderScript(a)
		if err != nil {
			return nil, err
		}
	}
	title := ""
	for _, ln := range strings.Split(text, "\n") {
		if strings.HasPrefix(strings.TrimSpace(ln), "# ") {
			title = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(ln), "# "))
			break
		}
	}
	// 与 scriptTask 写法一致：剧本、卡点、空标题时补标题
	if _, err := db.Exec(`UPDATE episodes SET script=?, cliffhanger=?, title=CASE WHEN title='' THEN ? ELSE title END WHERE id=?`, text, cliffOf(text), title, e.ID); err != nil {
		return nil, err
	}
	touchEpisode(e.ID)
	// 剧本里出现但资产库还没有的人物
	names := map[string]bool{}
	for _, as := range listAssets(p.ID) {
		if as.Kind == "character" {
			names[as.Name] = true
		}
	}
	var missing []string
	seen := map[string]bool{}
	for _, pl := range parseDialogue(strings.Join(dialogueLinesOf(text), "\n")) {
		if pl.kind == "dialogue" && !names[pl.speaker] && !seen[pl.speaker] {
			seen[pl.speaker] = true
			missing = append(missing, pl.speaker)
		}
	}
	out := map[string]any{"ok": true, "episode_id": e.ID, "title": title, "cliffhanger": cliffOf(text), "chars": runeLen(text), "warnings": nonNil(warn)}
	if len(missing) > 0 {
		out["characters_not_in_assets"] = missing
		out["next"] = "这些说话的人物还不在资产库：先用 upsert_asset 补上（含固定外貌），再 save_shots 交分镜。"
	} else {
		out["next"] = "检查场景、道具是否都在资产库（upsert_asset），然后 save_shots 交分镜。"
	}
	if m := writerResume(e.ID, "剧本"); m != "" {
		out["autopilot"] = m
	}
	return out, nil
}

// dialogueLinesOf 剧本里「角色名：台词」和「（旁白）」行
func dialogueLinesOf(script string) []string {
	var out []string
	for _, ln := range strings.Split(script, "\n") {
		t := strings.TrimSpace(ln)
		if t == "" || strings.HasPrefix(t, "#") || strings.HasPrefix(t, "【") || strings.HasPrefix(t, "- ") {
			continue
		}
		if strings.HasPrefix(t, "（旁白）") || strings.HasPrefix(t, "(旁白)") || speakerRe.MatchString(t) {
			out = append(out, t)
		}
	}
	return out
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// ---------- 写：分镜 ----------

type lineIn struct {
	Speaker string `json:"speaker"`
	Text    string `json:"text"`
	Emotion string `json:"emotion"`
}

type shotIn struct {
	Duration       float64         `json:"duration"`
	Camera         string          `json:"camera"`
	Picture        string          `json:"picture"`
	Action         string          `json:"action"`
	Dialogue       json.RawMessage `json:"dialogue"`
	Assets         []string        `json:"assets"`
	Transition     string          `json:"transition"`
	KeyframePrompt string          `json:"keyframe_prompt"`
	VideoPrompt    string          `json:"video_prompt"`
}

type shotsArgs struct {
	EpisodeID int64    `json:"episode_id"`
	Shots     []shotIn `json:"shots"`
	Overwrite bool     `json:"overwrite"`
}

var transitionChain = map[string]string{"": "off", "cut": "off", "off": "off", "continuous": "ref", "ref": "ref", "first_last": "first", "first": "first"}

func renderDialogue(raw json.RawMessage, idx int) (string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", nil
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return strings.TrimSpace(s), nil
	}
	var list []lineIn
	if err := json.Unmarshal(raw, &list); err != nil {
		return "", fmt.Errorf("第 %d 镜 dialogue 应为文本或 [{speaker, text, emotion}]", idx)
	}
	var out []string
	for j, l := range list {
		txt := strings.Trim(strings.TrimSpace(l.Text), "“”\"「」")
		if txt == "" {
			return "", fmt.Errorf("第 %d 镜第 %d 句台词 text 为空", idx, j+1)
		}
		sp := strings.TrimSpace(l.Speaker)
		if sp == "" {
			return "", fmt.Errorf("第 %d 镜第 %d 句缺 speaker（旁白写「旁白」）", idx, j+1)
		}
		if sp == "画外音" {
			sp = "旁白"
		}
		emo := strings.Trim(strings.TrimSpace(l.Emotion), "（）()")
		if emo != "" {
			if utf8.RuneCountInString(emo) > 16 {
				return "", fmt.Errorf("第 %d 镜第 %d 句 emotion 太长（最多 16 字）", idx, j+1)
			}
			txt = "（" + emo + "）" + txt
		}
		out = append(out, sp+"："+txt)
	}
	return strings.Join(out, "\n"), nil
}

func toolSaveShots(ctx context.Context, raw json.RawMessage) (any, error) {
	var a shotsArgs
	if err := strictArgs(raw, &a); err != nil {
		return nil, err
	}
	p, e, err := epCtx(a.EpisodeID)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(e.Script) == "" {
		return nil, errors.New("这一集还没有剧本，先 save_script")
	}
	if len(a.Shots) == 0 || len(a.Shots) > 40 {
		return nil, fmt.Errorf("shots 要 1–40 个（现在 %d 个）", len(a.Shots))
	}
	assets := listAssets(p.ID)
	byCode, byName := map[string]*Asset{}, map[string]*Asset{}
	var table []string
	for _, as := range assets {
		byCode[as.Code] = as
		byName[as.Name] = as
		table = append(table, as.Code+"="+as.Name)
	}
	old := listShots(e.ID, true)
	media := 0
	for _, s := range old {
		if s.PickedImageID > 0 || len(s.Videos) > 0 {
			media++
		}
	}
	if media > 0 && !a.Overwrite {
		return nil, fmt.Errorf("这一集已有 %d 个镜头做了关键帧或视频；确认整体替换分镜请传 overwrite=true", media)
	}
	type row struct {
		d                                       float64
		cam, act, dia, kp, vp, chain, codesJSON string
	}
	var rows []row
	var warn []string
	total := 0.0
	for i, s := range a.Shots {
		n := i + 1
		act := strings.TrimSpace(orDefault(s.Picture, s.Action))
		if act == "" {
			return nil, fmt.Errorf("第 %d 镜缺 picture（画面里发生什么）", n)
		}
		if strings.TrimSpace(s.Camera) == "" {
			return nil, fmt.Errorf("第 %d 镜缺 camera（景别 + 机位/运镜，如「近景 · 缓慢推近」）", n)
		}
		kp := strings.TrimSpace(s.KeyframePrompt)
		if runeLen(kp) < 8 {
			return nil, fmt.Errorf("第 %d 镜 keyframe_prompt 太短：写清构图、景别、人物位置与表情、光线", n)
		}
		vp := strings.TrimSpace(s.VideoPrompt)
		if vp == "" {
			return nil, fmt.Errorf("第 %d 镜缺 video_prompt（从关键帧开始的动作、运镜、环境动态和声音）", n)
		}
		d := math.Round(s.Duration)
		if d == 0 {
			d = 5
		}
		if d < 5 || d > 10 {
			return nil, fmt.Errorf("第 %d 镜 duration=%v 不行：视频模型每段只能 5–10 秒", n, s.Duration)
		}
		total += d
		dia, err := renderDialogue(s.Dialogue, n)
		if err != nil {
			return nil, err
		}
		chars := 0
		for _, pl := range parseDialogue(dia) {
			chars += runeLen(pl.text)
			if pl.kind == "dialogue" && matchSpeaker(assets, pl.speaker) == nil {
				warn = append(warn, fmt.Sprintf("第 %d 镜说话人「%s」不在资产库，配音时找不到音色", n, pl.speaker))
			}
		}
		if float64(chars) > d*5 {
			warn = append(warn, fmt.Sprintf("第 %d 镜台词 %d 字，%v 秒可能念不完（约每秒 4–5 字）", n, chars, d))
		}
		codes := []string{}
		seen := map[string]bool{}
		for _, ref := range s.Assets {
			ref = strings.TrimSpace(ref)
			as := byCode[strings.ToUpper(ref)]
			if as == nil {
				as = byName[ref]
			}
			if as == nil {
				return nil, fmt.Errorf("第 %d 镜引用的资产「%s」不存在。资产库：%s。先用 upsert_asset 建好再引用", n, ref, orDefault(strings.Join(table, "、"), "（空）"))
			}
			if !seen[as.Code] {
				seen[as.Code] = true
				codes = append(codes, as.Code)
			}
		}
		// 角色在前、场景其次、道具最后（与模型路径一致）
		order := map[string]int{"C": 0, "S": 1, "P": 2}
		for x := 1; x < len(codes); x++ {
			for y := x; y > 0 && order[codes[y][:1]] < order[codes[y-1][:1]]; y-- {
				codes[y], codes[y-1] = codes[y-1], codes[y]
			}
		}
		chain, ok := transitionChain[strings.TrimSpace(s.Transition)]
		if !ok {
			return nil, fmt.Errorf("第 %d 镜 transition 只能是 cut / continuous / first_last", n)
		}
		if i == 0 && chain != "off" {
			chain = "off"
			warn = append(warn, "第 1 镜没有上一镜头，transition 已改为 cut")
		}
		rows = append(rows, row{d, strings.TrimSpace(s.Camera), act, dia, kp, vp, chain, mustJSON(codes)})
	}
	if p.EpisodeSec > 0 && (total < float64(p.EpisodeSec)*0.6 || total > float64(p.EpisodeSec)*1.5) {
		warn = append(warn, fmt.Sprintf("分镜总时长 %.0f 秒，作品设定单集约 %d 秒", total, p.EpisodeSec))
	}
	// 与 splitTask 一致：整集替换镜头和台词
	for _, s := range old {
		_, _ = db.Exec(`UPDATE videos SET status='canceled', finished_at=? WHERE shot_id=? AND status='queued'`, now(), s.ID)
	}
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM shots WHERE episode_id=?`, e.ID); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`DELETE FROM lines WHERE episode_id=?`, e.ID); err != nil {
		return nil, err
	}
	for i, r := range rows {
		if _, err := tx.Exec(`INSERT INTO shots(project_id,episode_id,sort,duration,camera,action,dialogue,asset_codes,keyframe_prompt,video_prompt,chain,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
			p.ID, e.ID, i+1, r.d, r.cam, r.act, r.dia, r.codesJSON, r.kp, r.vp, r.chain, now()); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	syncLines(p, e)
	touchEpisode(e.ID)
	shots := []map[string]any{}
	for _, s := range listShots(e.ID, false) {
		shots = append(shots, map[string]any{"id": s.ID, "code": s.Code, "duration": s.Duration, "assets": s.Assets, "chain": s.Chain})
	}
	var lines int
	_ = db.QueryRow(`SELECT COUNT(*) FROM lines WHERE episode_id=?`, e.ID).Scan(&lines)
	out := map[string]any{"ok": true, "episode_id": e.ID, "shots": shots, "lines": lines, "total_seconds": total, "warnings": nonNil(warn),
		"next": "分镜已交。之后可以用 view_shot 看关键帧、listen_dub 听配音；本集写完后用 save_episode_summary 交摘要和卡点。"}
	if m := writerResume(e.ID, "分镜"); m != "" {
		out["autopilot"] = m
	}
	return out, nil
}

// ---------- 写：本集摘要 ----------

type summaryArgs struct {
	EpisodeID   int64  `json:"episode_id"`
	Summary     string `json:"summary"`
	Cliffhanger string `json:"cliffhanger"`
}

func toolSaveSummary(ctx context.Context, raw json.RawMessage) (any, error) {
	var a summaryArgs
	if err := strictArgs(raw, &a); err != nil {
		return nil, err
	}
	_, e, err := epCtx(a.EpisodeID)
	if err != nil {
		return nil, err
	}
	s := strings.TrimSpace(a.Summary)
	if n := runeLen(s); n < 30 || n > 400 {
		return nil, fmt.Errorf("summary 要 30–400 字（建议 80–150 字，现在 %d 字）", n)
	}
	c := strings.TrimSpace(a.Cliffhanger)
	if c == "" {
		c = cliffOf(e.Script)
	}
	if c == "" {
		return nil, errors.New("cliffhanger 不能为空：写一句本集结尾的悬念")
	}
	_, _ = db.Exec(`UPDATE episodes SET summary=?, cliffhanger=? WHERE id=?`, s, c, e.ID)
	touchEpisode(e.ID)
	return map[string]any{"ok": true, "episode_id": e.ID, "summary": s, "cliffhanger": c}, nil
}

// ---------- 写：资产 ----------

type lookIn struct {
	Age         string `json:"age"`
	Gender      string `json:"gender"`
	Hair        string `json:"hair"`
	Face        string `json:"face"`
	Body        string `json:"body"`
	Outfit      string `json:"outfit"`
	Accessories string `json:"accessories"`
	Palette     string `json:"palette"`
}

type assetArgs struct {
	ProjectID        int64   `json:"project_id"`
	AssetID          int64   `json:"asset_id"`
	Code             string  `json:"code"`
	Kind             string  `json:"kind"`
	Name             string  `json:"name"`
	Description      string  `json:"description"`
	Look             *lookIn `json:"look"`
	Prompt           string  `json:"prompt"`
	VoiceDescription string  `json:"voice_description"`
	VoiceSampleText  string  `json:"voice_sample_text"`
}

// lookDescription 固定造型字段 → 外观描述。每句都带 consistency.go 认得的外貌词（发/眉/身形/穿/戴），
// 这样 charLook 能把它们原样抽出来写进关键帧、封面、定妆图的提示词。
func lookDescription(l *lookIn) string {
	if l == nil {
		return ""
	}
	var s []string
	if v := strings.TrimSpace(strings.TrimSpace(l.Age) + " " + strings.TrimSpace(l.Gender)); v != "" {
		s = append(s, v)
	}
	add := func(prefix, v string) {
		v = strings.TrimRight(strings.TrimSpace(v), "。；;")
		if v != "" {
			s = append(s, prefix+v)
		}
	}
	add("发型：", l.Hair)
	add("眉眼五官：", l.Face)
	add("身形：", l.Body)
	if o := strings.TrimSpace(l.Outfit); o != "" {
		add("穿", strings.TrimPrefix(strings.TrimPrefix(o, "穿着"), "穿"))
	}
	if ac := strings.TrimSpace(l.Accessories); ac != "" {
		add("戴", strings.TrimPrefix(strings.TrimPrefix(ac, "佩戴"), "戴"))
	}
	add("主色调：", l.Palette)
	return strings.Join(s, "。")
}

func toolUpsertAsset(ctx context.Context, raw json.RawMessage) (any, error) {
	var a assetArgs
	if err := strictArgs(raw, &a); err != nil {
		return nil, err
	}
	var cur *Asset
	switch {
	case a.AssetID > 0:
		x, err := getAsset(a.AssetID)
		if err != nil {
			return nil, errors.New("资产不存在")
		}
		cur = x
		a.ProjectID = x.ProjectID
	case a.ProjectID > 0 && strings.TrimSpace(a.Code) != "":
		for _, x := range listAssets(a.ProjectID) {
			if strings.EqualFold(x.Code, strings.TrimSpace(a.Code)) {
				cur = x
			}
		}
		if cur == nil {
			return nil, fmt.Errorf("资产编号 %s 不存在", a.Code)
		}
	}
	p, err := getProject(a.ProjectID)
	if err != nil {
		return nil, errors.New("作品不存在，先用 list_projects 查作品编号")
	}
	kind := strings.TrimSpace(a.Kind)
	if cur != nil && kind == "" {
		kind = cur.Kind
	}
	if kindPrefix[kind] == "" {
		return nil, errors.New("kind 只能是 character（角色）/ scene（场景）/ prop（道具）")
	}
	name := strings.TrimSpace(a.Name)
	if cur == nil && name != "" {
		for _, x := range listAssets(p.ID) {
			if x.Kind == kind && x.Name == name {
				cur = x
			}
		}
	}
	if cur == nil && name == "" {
		return nil, errors.New("新资产要给 name")
	}
	if cur != nil && cur.Kind != kind {
		return nil, fmt.Errorf("「%s」已经是%s，不能改成别的类型", cur.Name, map[string]string{"character": "角色", "scene": "场景", "prop": "道具"}[cur.Kind])
	}
	desc := strings.TrimSpace(a.Description)
	if ld := lookDescription(a.Look); ld != "" {
		if a.Look != nil && kind != "character" {
			return nil, errors.New("look 只用于角色；场景和道具直接写 description")
		}
		if desc != "" {
			desc = strings.TrimRight(desc, "。") + "。" + ld
		} else {
			desc = ld
		}
	}
	if cur == nil && runeLen(desc) < 6 {
		return nil, errors.New("description 太短：角色写固定外貌（年龄感、发型发色、五官、体型、服装配色），场景写空间、时代、光线、主色调，道具写外形材质")
	}
	if kind == "character" && desc != "" && charLookOf(name, desc) == "" {
		return nil, errors.New("角色外观里没有可固定的造型：至少写清发型和服装（如「黑色齐肩短发」「穿明黄色骑手外套」），或用 look 字段")
	}
	created := false
	if cur == nil {
		id, err := insertAsset(p.ID, kind, assetDraft{Name: name, Description: desc, Prompt: a.Prompt})
		if err != nil {
			return nil, err
		}
		cur, _ = getAsset(id)
		created = true
	} else {
		if name == "" {
			name = cur.Name
		}
		if desc == "" {
			desc = cur.Description
		}
		prompt := cur.Prompt
		if strings.TrimSpace(a.Prompt) != "" {
			prompt = strings.TrimSpace(a.Prompt)
		}
		_, _ = db.Exec(`UPDATE assets SET name=?, description=?, prompt=? WHERE id=?`, name, desc, prompt, cur.ID)
		touchProject(p.ID)
	}
	var warn []string
	if vd := strings.TrimSpace(a.VoiceDescription); vd != "" {
		if kind != "character" {
			return nil, errors.New("voice_description 只用于角色")
		}
		cur, _ = getAsset(cur.ID)
		if cur.Voice.empty() {
			// 只存描述：「设计音色」时直接用，不再调用文本模型
			_, _ = db.Exec(`UPDATE assets SET voice=? WHERE id=?`, mustJSON(VoiceSpec{Desc: vd, SampleText: strings.TrimSpace(a.VoiceSampleText)}), cur.ID)
		} else {
			warn = append(warn, "这个角色已经有音色，没有覆盖；要换请在应用里改")
		}
	}
	cur, _ = getAsset(cur.ID)
	if !created && cur.PickedImageID > 0 && strings.TrimSpace(a.Description+lookDescription(a.Look)) != "" {
		warn = append(warn, "外观已更新，已有的设定图不会自动重画；需要的话在应用的「资产」里重新生成")
	}
	return map[string]any{"ok": true, "created": created, "asset_id": cur.ID, "code": cur.Code, "kind": cur.Kind, "name": cur.Name,
		"description": cur.Description, "fixed_look": charLook(p, cur), "has_image": cur.PickedImageID > 0, "warnings": nonNil(warn)}, nil
}

func charLookOf(name, desc string) string {
	return strings.Join(appearanceSentences(desc, name), "，")
}

// ---------- 读：写作上下文 ----------

func assetBrief(p *Project, a *Asset) map[string]any {
	m := map[string]any{"asset_id": a.ID, "code": a.Code, "kind": a.Kind, "name": a.Name, "description": a.Description,
		"has_image": a.PickedImageID > 0, "has_sheet": a.SheetImageID > 0, "has_expressions": a.ExprImageID > 0, "appear_in_shots": a.Appear}
	if a.Kind == "character" {
		m["fixed_look"] = charLook(p, a)
		m["has_voice"] = !a.Voice.empty()
	}
	return m
}

func nextWriterAction(p *Project, e *Episode) string {
	switch {
	case p.World == "" && p.Bios == "":
		return "设定集还是空的：先 save_bible（world、bios、outline），再 upsert_asset 建主要角色"
	case strings.TrimSpace(e.Idea) == "" && strings.TrimSpace(e.Script) == "" && (e.No > 1 || strings.TrimSpace(p.Idea) == ""):
		return "写本集梗概：save_synopsis"
	case strings.TrimSpace(e.Script) == "":
		return "写本集剧本：save_script"
	case len(listShots(e.ID, false)) == 0:
		return "补齐资产（upsert_asset），然后拆分镜：save_shots"
	case strings.TrimSpace(e.Summary) == "":
		return "分镜已交；本集定稿后写摘要：save_episode_summary"
	}
	return "本集文字部分都已完成；可以 view_shot / listen_dub 检查成果"
}

func toolWritingContext(ctx context.Context, raw json.RawMessage) (any, error) {
	eid := argID(raw, "episode_id")
	pid := argID(raw, "project_id")
	var p *Project
	var e *Episode
	var err error
	if eid > 0 {
		p, e, err = epCtx(eid)
		if err != nil {
			return nil, err
		}
	} else if pid > 0 {
		p, err = getProject(pid)
		if err != nil {
			return nil, errors.New("作品不存在")
		}
		eps := listEpisodes(p.ID)
		for _, x := range eps { // 第一个没成片的集
			if x.FinalFile == "" {
				e = x
				break
			}
		}
		if e == nil && len(eps) > 0 {
			e = eps[len(eps)-1]
		}
	} else {
		return nil, errors.New("需要 episode_id 或 project_id")
	}
	assets := []map[string]any{}
	for _, a := range listAssets(p.ID) {
		assets = append(assets, assetBrief(p, a))
	}
	prev := []map[string]any{}
	all := []map[string]any{}
	lastCliff := ""
	for _, x := range listEpisodes(p.ID) {
		all = append(all, map[string]any{"episode_id": x.ID, "no": x.No, "title": x.Title, "has_script": x.Script != "", "final": x.FinalFile != ""})
		if e != nil && x.No < e.No {
			c := x.Cliffhanger
			if c == "" {
				c = cliffOf(x.Script)
			}
			prev = append(prev, map[string]any{"no": x.No, "title": x.Title, "summary": orDefault(x.Summary, x.Idea), "cliffhanger": c})
			lastCliff = c
		}
	}
	out := map[string]any{
		"project": map[string]any{"project_id": p.ID, "title": p.Title, "idea": p.Idea, "style": p.Style, "aspect": p.Aspect, "episode_seconds": p.EpisodeSec,
			"shot_target": p.ShotTarget, "episodes_plan": p.EpisodesPlan, "audio_mode": p.AudioMode},
		"bible":             map[string]any{"world": p.World, "bios": p.Bios, "outline": p.Outline},
		"assets":            assets,
		"episodes":          all,
		"previous_episodes": prev,
		"format": map[string]any{
			"script":   "save_script 用 scenes 结构，或 markdown：「# 标题」→「## 人物」→「## 第 N 场 · 地点 · 时间」，场内「【画面】动作」「角色名：（语气）台词」「（旁白）旁白」，最后一行「【卡点】悬念」",
			"shots":    "save_shots：每镜 duration 5–10 秒、camera、picture、dialogue[{speaker,text,emotion}]、assets[资产编号或名称]、transition（cut / continuous / first_last）、keyframe_prompt、video_prompt",
			"language": "全部用简体中文",
		},
	}
	if e != nil {
		steps := episodeSteps(p, e)
		shots := []map[string]any{}
		for _, s := range listShots(e.ID, false) {
			shots = append(shots, map[string]any{"shot_id": s.ID, "code": s.Code, "duration": s.Duration, "camera": s.Camera, "picture": s.Action, "dialogue": s.Dialogue,
				"assets": s.Assets, "chain": s.Chain, "has_keyframe": s.PickedImageID > 0})
		}
		out["episode"] = map[string]any{"episode_id": e.ID, "no": e.No, "title": e.Title, "synopsis": e.Idea, "script": e.Script, "summary": e.Summary,
			"cliffhanger": e.Cliffhanger, "shots": shots, "missing_steps": missingSteps(steps), "autopilot": pipeBrief(latestPipe(e.ID))}
		out["previous_cliffhanger"] = lastCliff
		out["next_action"] = nextWriterAction(p, e)
	}
	return out, nil
}

func pipeBrief(lp *Pipe) any {
	if lp == nil {
		return nil
	}
	return map[string]any{"mode": lp.Mode, "status": lp.Status, "stage": lp.Stage, "message": lp.Message}
}

// ---------- 读：图片与音频 ----------

const defaultMaxEdge = 1536

// imageBlock 读出图片；默认缩到长边 ≤ maxEdge 并转 JPEG，original=true 时原样返回（≤ 12 MB）
func imageBlock(data []byte, mime string, maxEdge int, original bool) (map[string]any, error) {
	if original {
		if len(data) > 12<<20 {
			return nil, errors.New("原图超过 12 MB，请不要用 original")
		}
		if mime == "" {
			mime = http.DetectContentType(data)
		}
		return map[string]any{"type": "image", "data": base64.StdEncoding.EncodeToString(data), "mimeType": mime}, nil
	}
	if maxEdge <= 0 {
		maxEdge = defaultMaxEdge
	}
	maxEdge = clampInt(maxEdge, 256, 4096)
	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, errors.New("图片解码失败")
	}
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if w > maxEdge || h > maxEdge {
		if w >= h {
			h, w = int(math.Round(float64(h)*float64(maxEdge)/float64(w))), maxEdge
		} else {
			w, h = int(math.Round(float64(w)*float64(maxEdge)/float64(h))), maxEdge
		}
	}
	dst := image.NewRGBA(image.Rect(0, 0, max(w, 1), max(h, 1)))
	xdraw.CatmullRom.Scale(dst, dst.Bounds(), src, b, xdraw.Src, nil)
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, dst, &jpeg.Options{Quality: 85}); err != nil {
		return nil, err
	}
	return map[string]any{"type": "image", "data": base64.StdEncoding.EncodeToString(buf.Bytes()), "mimeType": "image/jpeg"}, nil
}

func imageBlockByID(id int64, maxEdge int, original bool) (map[string]any, *Image, error) {
	im, err := getImage(id)
	if err != nil {
		return nil, nil, errors.New("图片不存在")
	}
	data, err := os.ReadFile(filepath.Join(dataDir, "images", im.File))
	if err != nil {
		return nil, nil, errors.New("图片文件丢失")
	}
	blk, err := imageBlock(data, im.Mime, maxEdge, original)
	return blk, im, err
}

type viewArgs struct {
	AssetID   int64    `json:"asset_id"`
	ProjectID int64    `json:"project_id"`
	Name      string   `json:"name"`
	Which     []string `json:"which"`
	Original  bool     `json:"original"`
	MaxEdge   int      `json:"max_edge"`
}

func toolViewAsset(ctx context.Context, raw json.RawMessage) (any, error) {
	var a viewArgs
	if err := strictArgs(raw, &a); err != nil {
		return nil, err
	}
	var as *Asset
	if a.AssetID > 0 {
		x, err := getAsset(a.AssetID)
		if err != nil {
			return nil, errors.New("资产不存在")
		}
		as = x
	} else if a.ProjectID > 0 && strings.TrimSpace(a.Name) != "" {
		for _, x := range listAssets(a.ProjectID) {
			if x.Name == strings.TrimSpace(a.Name) || strings.EqualFold(x.Code, strings.TrimSpace(a.Name)) {
				as = x
			}
		}
		if as == nil {
			return nil, fmt.Errorf("作品 %d 里没有资产「%s」", a.ProjectID, a.Name)
		}
	} else {
		return nil, errors.New("需要 asset_id，或 project_id + name（名称或编号）")
	}
	p, _ := getProject(as.ProjectID)
	which := a.Which
	if len(which) == 0 {
		which = []string{"main", "sheet", "expressions"}
	}
	labels := map[string]string{"main": "设定图", "sheet": "定妆三视图", "expressions": "表情表"}
	ids := map[string]int64{"main": as.PickedImageID, "sheet": as.SheetImageID, "expressions": as.ExprImageID}
	blocks := []any{}
	shown := []string{}
	for _, w := range which {
		id, ok := ids[w]
		if !ok {
			return nil, errors.New("which 只能包含 main / sheet / expressions")
		}
		if id == 0 {
			continue
		}
		blk, im, err := imageBlockByID(id, a.MaxEdge, a.Original)
		if err != nil {
			return nil, err
		}
		blocks = append(blocks, textBlock(fmt.Sprintf("%s · %s（%d×%d）", as.Name, labels[w], im.Width, im.Height)), blk)
		shown = append(shown, labels[w])
	}
	info := assetBrief(p, as)
	head := fmt.Sprintf("%s %s「%s」\n外观：%s", as.Code, map[string]string{"character": "角色", "scene": "场景", "prop": "道具"}[as.Kind], as.Name, orDefault(as.Description, "（未写）"))
	if l, _ := info["fixed_look"].(string); l != "" {
		head += "\n固定造型：" + l
	}
	if len(shown) == 0 {
		head += "\n这个资产还没有生成图片。"
	}
	return &mcpRich{Blocks: append([]any{textBlock(head)}, blocks...), Structured: map[string]any{"asset": info, "images": shown}}, nil
}

type shotViewArgs struct {
	ShotID    int64  `json:"shot_id"`
	EpisodeID int64  `json:"episode_id"`
	Code      string `json:"code"`
	Original  bool   `json:"original"`
	MaxEdge   int    `json:"max_edge"`
	Frames    *bool  `json:"video_frames"`
}

func findShot(shotID, eid int64, code string) (*Shot, error) {
	if shotID > 0 {
		s, err := getShot(shotID)
		if err != nil {
			return nil, errors.New("镜头不存在")
		}
		return s, nil
	}
	if eid <= 0 || strings.TrimSpace(code) == "" {
		return nil, errors.New("需要 shot_id，或 episode_id + code（如 03）")
	}
	c := strings.TrimSpace(code)
	if n, err := strconv.Atoi(c); err == nil {
		c = fmt.Sprintf("%02d", n)
	}
	for _, s := range listShots(eid, false) {
		if s.Code == c {
			return getShot(s.ID)
		}
	}
	return nil, fmt.Errorf("这一集没有镜头 %s", code)
}

func videoFrame(path string, last bool) ([]byte, error) {
	if last {
		return lastFrame(path)
	}
	out, err := exec.Command("ffmpeg", "-v", "error", "-i", path, "-frames:v", "1", "-f", "image2", "-c:v", "png", "pipe:1").Output()
	if err != nil || len(out) < 100 {
		return nil, errors.New("抽取第一帧失败")
	}
	return out, nil
}

func toolViewShot(ctx context.Context, raw json.RawMessage) (any, error) {
	var a shotViewArgs
	if err := strictArgs(raw, &a); err != nil {
		return nil, err
	}
	s, err := findShot(a.ShotID, a.EpisodeID, a.Code)
	if err != nil {
		return nil, err
	}
	for _, x := range listShots(s.EpisodeID, true) {
		if x.ID == s.ID {
			s = x
		}
	}
	head := fmt.Sprintf("镜头 %s · %v 秒 · %s\n画面：%s\n台词：%s\n资产：%s\n衔接：%s\n关键帧提示词：%s\n视频提示词：%s",
		s.Code, s.Duration, s.Camera, s.Action, orDefault(s.Dialogue, "（无）"), orDefault(strings.Join(s.Assets, "、"), "（无）"), chainZh[s.Chain], s.KeyframePrompt, s.VideoPrompt)
	blocks := []any{textBlock(head)}
	info := map[string]any{"shot_id": s.ID, "episode_id": s.EpisodeID, "code": s.Code, "keyframe": false, "video_id": 0}
	if s.PickedImageID > 0 {
		blk, im, err := imageBlockByID(s.PickedImageID, a.MaxEdge, a.Original)
		if err == nil {
			blocks = append(blocks, textBlock(fmt.Sprintf("关键帧（%d×%d）", im.Width, im.Height)), blk)
			info["keyframe"] = true
		}
	} else {
		blocks = append(blocks, textBlock("还没有关键帧。"))
	}
	if v := shotVideoFor(s); v != nil {
		info["video_id"] = v.ID
		if a.Frames == nil || *a.Frames {
			path := filepath.Join(dataDir, "videos", v.File)
			for _, last := range []bool{false, true} {
				data, err := videoFrame(path, last)
				if err != nil {
					continue
				}
				blk, err := imageBlock(data, "image/png", a.MaxEdge, a.Original)
				if err != nil {
					continue
				}
				blocks = append(blocks, textBlock(map[bool]string{false: fmt.Sprintf("选定视频 #%d 第一帧", v.ID), true: fmt.Sprintf("选定视频 #%d 最后一帧", v.ID)}[last]), blk)
			}
		}
		blocks = append(blocks, textBlock("要看完整视频：调用 get_video_link（shot_id）拿下载地址，下载到工作区再看。"))
	} else {
		blocks = append(blocks, textBlock("还没有选定的视频。"))
	}
	return &mcpRich{Blocks: blocks, Structured: info}, nil
}

var chainZh = map[string]string{"off": "直接切", "ref": "参考上一镜", "first": "首尾帧"}

var audioMimes = map[string]string{".mp3": "audio/mpeg", ".wav": "audio/wav", ".ogg": "audio/ogg", ".opus": "audio/ogg", ".m4a": "audio/mp4", ".aac": "audio/aac", ".flac": "audio/flac"}

type dubArgs struct {
	EpisodeID int64 `json:"episode_id"`
	ShotID    int64 `json:"shot_id"`
	LineID    int64 `json:"line_id"`
	Limit     int   `json:"limit"`
}

func toolListenDub(ctx context.Context, raw json.RawMessage, base string, audioOK bool) (any, error) {
	var a dubArgs
	if err := strictArgs(raw, &a); err != nil {
		return nil, err
	}
	var ls []*Line
	switch {
	case a.LineID > 0:
		ls = listLines(`WHERE id=?`, a.LineID)
	case a.ShotID > 0:
		ls = listLines(`WHERE shot_id=? ORDER BY sort, id`, a.ShotID)
	case a.EpisodeID > 0:
		ls = listLines(`WHERE episode_id=? ORDER BY shot_id, sort, id`, a.EpisodeID)
	default:
		return nil, errors.New("需要 line_id、shot_id 或 episode_id")
	}
	if len(ls) == 0 {
		return nil, errors.New("没有找到台词")
	}
	limit := clampInt(a.Limit, 1, 8)
	if a.Limit == 0 {
		limit = 4
	}
	blocks := []any{}
	items := []map[string]any{}
	total, sent := 0, 0
	for _, l := range ls {
		it := map[string]any{"line_id": l.ID, "shot_id": l.ShotID, "speaker": l.Speaker, "text": l.Text, "emotion": l.Emotion, "duration": l.Duration, "status": l.Status}
		items = append(items, it)
		if l.AudioFile == "" || sent >= limit {
			continue
		}
		label := fmt.Sprintf("台词 #%d · %s%s：%s（%.1f 秒）", l.ID, l.Speaker, map[bool]string{true: "（" + l.Emotion + "）", false: ""}[l.Emotion != ""], l.Text, l.Duration)
		if !audioOK {
			blocks = append(blocks, textBlock(label+"\n音频："+base+"/v1/audio/"+l.AudioFile))
			sent++
			continue
		}
		data, err := os.ReadFile(filepath.Join(dataDir, "audio", l.AudioFile))
		if err != nil || total+len(data) > 10<<20 {
			continue
		}
		mime := audioMimes[strings.ToLower(filepath.Ext(l.AudioFile))]
		if mime == "" {
			mime = "audio/mpeg"
		}
		total += len(data)
		blocks = append(blocks, textBlock(label), map[string]any{"type": "audio", "data": base64.StdEncoding.EncodeToString(data), "mimeType": mime})
		it["included"] = true
		sent++
	}
	head := fmt.Sprintf("共 %d 句台词，附上 %d 段配音。", len(ls), sent)
	if sent == 0 {
		head += "还没有生成配音（可以先 generate_dub）。"
	} else if sent < len(ls) {
		head += "要听其余的，按 shot_id 或 line_id 再调用。"
	}
	return &mcpRich{Blocks: append([]any{textBlock(head)}, blocks...), Structured: map[string]any{"lines": items}}, nil
}

// ---------- 读：视频下载地址（MCP 没有视频内容类型） ----------

func dlSecret() []byte {
	s := getSetting("dl_secret", "")
	if s == "" {
		s = randHex(32)
		_ = setSetting("dl_secret", s)
	}
	return []byte(s)
}

// signDL 生成限时下载令牌：kind=v（视频编号）/ f（某集成片），有效 ttl
func signDL(kind string, id int64, ttl time.Duration) string {
	payload := fmt.Sprintf("%s.%d.%d", kind, id, time.Now().Add(ttl).Unix())
	m := hmac.New(sha256.New, dlSecret())
	m.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." + base64.RawURLEncoding.EncodeToString(m.Sum(nil)[:18])
}

func verifyDL(tok string) (string, int64, bool) {
	i := strings.LastIndex(tok, ".")
	if i < 0 {
		return "", 0, false
	}
	pb, err1 := base64.RawURLEncoding.DecodeString(tok[:i])
	sig, err2 := base64.RawURLEncoding.DecodeString(tok[i+1:])
	if err1 != nil || err2 != nil {
		return "", 0, false
	}
	m := hmac.New(sha256.New, dlSecret())
	m.Write(pb)
	if !hmac.Equal(sig, m.Sum(nil)[:18]) {
		return "", 0, false
	}
	parts := strings.Split(string(pb), ".")
	if len(parts) != 3 {
		return "", 0, false
	}
	id, _ := strconv.ParseInt(parts[1], 10, 64)
	exp, _ := strconv.ParseInt(parts[2], 10, 64)
	if time.Now().Unix() > exp {
		return "", 0, false
	}
	return parts[0], id, true
}

func handleSignedDL(w http.ResponseWriter, r *http.Request) {
	kind, id, ok := verifyDL(r.PathValue("token"))
	if !ok {
		httpErr(w, 403, "链接无效或已过期，请重新调用 get_video_link")
		return
	}
	var file string
	switch kind {
	case "v":
		v, err := getVideo(id)
		if err != nil || v.File == "" {
			httpErr(w, 404, "视频不存在")
			return
		}
		file = v.File
	case "f":
		e, err := getEpisode(id)
		if err != nil || e.FinalFile == "" {
			httpErr(w, 404, "这一集还没有成片")
			return
		}
		file = e.FinalFile
	default:
		httpErr(w, 404, "不存在")
		return
	}
	w.Header().Set("Content-Type", "video/mp4")
	http.ServeFile(w, r, filepath.Join(dataDir, "videos", file))
}

type videoLinkArgs struct {
	VideoID   int64 `json:"video_id"`
	ShotID    int64 `json:"shot_id"`
	EpisodeID int64 `json:"episode_id"`
}

func toolVideoLink(ctx context.Context, raw json.RawMessage, base string) (any, error) {
	var a videoLinkArgs
	if err := strictArgs(raw, &a); err != nil {
		return nil, err
	}
	const ttl = 2 * time.Hour
	var url, name string
	var bytesN int64
	var secs float64
	switch {
	case a.VideoID > 0 || a.ShotID > 0:
		var v *Video
		if a.VideoID > 0 {
			x, err := getVideo(a.VideoID)
			if err != nil {
				return nil, errors.New("视频不存在")
			}
			v = x
		} else {
			s, err := findShot(a.ShotID, 0, "")
			if err != nil {
				return nil, err
			}
			for _, x := range listShots(s.EpisodeID, true) {
				if x.ID == s.ID {
					v = shotVideoFor(x)
				}
			}
			if v == nil {
				return nil, errors.New("这个镜头还没有生成好的视频")
			}
		}
		if v.Status != "done" || v.File == "" {
			return nil, fmt.Errorf("视频 #%d 还没有完成（%s）", v.ID, v.Status)
		}
		url = base + "/v1/dl/" + signDL("v", v.ID, ttl)
		name = fmt.Sprintf("manju_video_%d.mp4", v.ID)
		if v.EpisodeID > 0 {
			if e, err := getEpisode(v.EpisodeID); err == nil {
				name = fmt.Sprintf("manju_ep%02d_shot%d_v%d.mp4", e.No, v.ShotID, v.ID)
			}
		}
		bytesN, secs = v.Bytes, float64(v.Seconds)
	case a.EpisodeID > 0:
		_, e, err := epCtx(a.EpisodeID)
		if err != nil {
			return nil, err
		}
		if e.FinalFile == "" {
			return nil, errors.New("这一集还没有成片")
		}
		url = base + "/v1/dl/" + signDL("f", e.ID, ttl)
		name = fmt.Sprintf("manju_ep%02d_final.mp4", e.No)
		if fi, err := os.Stat(filepath.Join(dataDir, "videos", e.FinalFile)); err == nil {
			bytesN = fi.Size()
		}
	default:
		return nil, errors.New("需要 video_id、shot_id 或 episode_id（成片）")
	}
	return map[string]any{"url": url, "filename": name, "bytes": bytesN, "seconds": secs, "mime": "video/mp4", "expires_in_sec": int(ttl.Seconds()),
		"how_to_watch": fmt.Sprintf("MCP 没有视频内容类型。请在终端里下载到工作区再看：curl -fL -o %s \"%s\"（链接 2 小时内有效，不需要密钥）", name, url)}, nil
}

// ---------- 工具清单 ----------

func arr(desc string, items map[string]any) map[string]any {
	return map[string]any{"type": "array", "description": desc, "items": items}
}

var writerTools = []mcpTool{
	{"get_writing_context", "外部编剧先调用这个：一次拿到作品设定、设定集（世界观 / 人物小传 / 分集总纲）、前几集摘要与卡点、资产库（含角色固定造型）、本集当前状态（梗概、剧本、分镜、一键成集进度）和下一步该做什么。给 episode_id 看某一集；只给 project_id 时取第一个没成片的集。", obj(map[string]any{
		"episode_id": num("集编号"), "project_id": num("作品编号"),
	})},
	{"save_bible", "写入或更新设定集。只更新给出的字段。bios 可以是文本，或人物数组（每人 name、age、look、outfit、personality、motivation、relations、speech，外貌要具体，便于做角色设定图）；outline 可以是文本，或 [{no, plot, cliffhanger}]。", obj(map[string]any{
		"project_id": num("作品编号"), "idea": str("故事创意（可选）"), "world": str("世界观：时代、地点、社会规则、核心冲突来源，200–400 字"),
		"bios": map[string]any{"description": "人物小传：文本或人物数组", "oneOf": []any{map[string]any{"type": "string"}, arr("", obj(map[string]any{
			"name": str("姓名"), "age": str("年龄"), "gender": str("性别"), "look": str("外貌：发型发色、五官、体型"), "outfit": str("常穿服装与配色"),
			"personality": str("性格"), "motivation": str("动机"), "relations": str("与其他人的关系"), "speech": str("说话方式"),
		}, "name"))}},
		"outline": map[string]any{"description": "分集总纲：文本或分集数组", "oneOf": []any{map[string]any{"type": "string"}, arr("", obj(map[string]any{
			"no": num("集数"), "plot": str("一句话剧情"), "cliffhanger": str("本集卡点"),
		}, "plot"))}},
	}, "project_id")},
	{"save_synopsis", "写入本集梗概（2–4 句：核心事件、反转、结尾卡点，开头接住上一集卡点）。一键成集在「外部编剧」模式下等梗概时，写入后自动继续。", obj(map[string]any{
		"episode_id": num("集编号"), "synopsis": str("本集梗概"), "title": str("本集标题（可选）"),
	}, "episode_id", "synopsis")},
	{"save_script", "写入本集剧本。推荐用结构化 scenes（应用会排成标准剧本格式，台词按「角色名：（语气）台词」写入，配音据此按角色和语气合成）；也可以直接给 markdown。必须有结尾卡点。写入后若一键成集在等剧本，会自动继续。", obj(map[string]any{
		"episode_id": num("集编号"), "title": str("本集标题"),
		"characters": arr("本集出场人物（可选）", obj(map[string]any{"name": str("姓名"), "desc": str("年龄、外貌、服装、性格，一行")}, "name")),
		"scenes": arr("分场", obj(map[string]any{
			"location": str("地点"), "time": str("时间，如：深夜"),
			"beats": arr("场内按顺序的内容", obj(map[string]any{
				"type":    map[string]any{"type": "string", "enum": []string{"action", "line", "narration"}, "description": "action=【画面】动作与镜头感；line=角色台词；narration=旁白"},
				"text":    str("内容；台词一句不超过 20 字"),
				"speaker": str("说话的角色名（type=line 时必填，与资产库里的角色名一致）"),
				"emotion": str("语气，如：压低声音、冷笑（可选，最多 16 字）"),
			}, "type", "text")),
		}, "location", "beats")),
		"cliffhanger": str("结尾卡点（一句话）"),
		"markdown":    str("直接给整份剧本（与 scenes 二选一）"),
	}, "episode_id")},
	{"save_shots", "写入（整体替换）本集分镜。每个镜头会先出一张关键帧，再做 5–10 秒图生视频。assets 用资产编号（C01 / S01 / P01）或资产名称，必须已在资产库（不在就先 upsert_asset）。transition：cut 直接切；continuous 同场景连续动作，参考上一镜；first_last 用上一镜视频的最后一帧做首帧。已有关键帧或视频时要传 overwrite=true。写入后若一键成集在等分镜，会自动继续去出资产图和关键帧。", obj(map[string]any{
		"episode_id": num("集编号"), "overwrite": boolp("已有关键帧 / 视频时确认整体替换"),
		"shots": arr("按播放顺序的镜头", obj(map[string]any{
			"duration": num("时长 5–10 秒"), "camera": str("景别 + 机位/运镜，如「近景 · 缓慢推近」"), "picture": str("画面：人物动作、表情、环境变化，一个镜头只做一件事"),
			"dialogue": map[string]any{"description": "这个镜头的台词或旁白；念完不超过镜头时长", "oneOf": []any{map[string]any{"type": "string"}, arr("", obj(map[string]any{
				"speaker": str("角色名；旁白写「旁白」"), "text": str("台词"), "emotion": str("语气（可选）"),
			}, "speaker", "text"))}},
			"assets":          arr("出现的资产编号或名称", map[string]any{"type": "string"}),
			"transition":      map[string]any{"type": "string", "enum": []string{"cut", "continuous", "first_last"}},
			"keyframe_prompt": str("关键帧中文生图提示词：构图、景别、人物位置与表情、光线，直接用资产名称指代角色和场景"),
			"video_prompt":    str("图生视频中文描述：从关键帧开始的动作、镜头运动、环境动态和声音，不重复静态外观"),
		}, "duration", "camera", "picture", "keyframe_prompt", "video_prompt")),
	}, "episode_id", "shots")},
	{"save_episode_summary", "写入本集摘要（80–150 字，按时间顺序写清发生了什么、关系和处境变化、新人物与线索）和卡点，供后面各集参考。", obj(map[string]any{
		"episode_id": num("集编号"), "summary": str("前情摘要"), "cliffhanger": str("结尾悬念，一句话"),
	}, "episode_id", "summary")},
	{"upsert_asset", "新建或更新资产（角色 / 场景 / 道具，跨集共享）。按 asset_id、code，或同作品同类型同名匹配已有资产。角色请用 look 写固定造型（发型、五官、体型、服装、配饰），这些会原样写进每张关键帧和封面的提示词，保证人物前后一致。voice_description 可选，用来设计角色音色。", obj(map[string]any{
		"project_id": num("作品编号"), "asset_id": num("已有资产编号（更新时）"), "code": str("已有资产编号，如 C01（更新时）"),
		"kind": map[string]any{"type": "string", "enum": []string{"character", "scene", "prop"}}, "name": str("名称"),
		"description": str("外观描述（场景：空间、时代、光线、主色调；道具：外形材质）"),
		"look": obj(map[string]any{
			"age": str("年龄感"), "gender": str("性别"), "hair": str("发型发色"), "face": str("眉眼五官、标志特征"), "body": str("体型身形"),
			"outfit": str("服装与配色（固定不变的那套）"), "accessories": str("配饰"), "palette": str("主色调"),
		}),
		"prompt":            str("生图提示词（可选，单独描述这个资产本身）"),
		"voice_description": str("音色描述（可选，角色）：年龄感、音色质感、语速、口音"),
		"voice_sample_text": str("试音台词（可选）"),
	}, "project_id")},
	{"view_asset_images", "看资产的图片：设定图、定妆三视图、表情表。返回图片内容块（默认缩到长边 1536 像素的 JPEG；original=true 返回原图）。", obj(map[string]any{
		"asset_id": num("资产编号"), "project_id": num("作品编号（与 name 一起用）"), "name": str("资产名称或编号"),
		"which":    arr("要看哪些，默认全部", map[string]any{"type": "string", "enum": []string{"main", "sheet", "expressions"}}),
		"original": boolp("返回原图"), "max_edge": num("缩放后的长边像素，256–4096，默认 1536"),
	})},
	{"view_shot", "看一个镜头：文字信息 + 关键帧图片 + 选定视频的第一帧和最后一帧（图片内容块）。完整视频用 get_video_link。", obj(map[string]any{
		"shot_id": num("镜头编号"), "episode_id": num("集编号（与 code 一起用）"), "code": str("镜头序号，如 03"),
		"video_frames": boolp("是否附视频首尾帧，默认是"), "original": boolp("返回原图"), "max_edge": num("缩放后的长边像素，默认 1536"),
	})},
	{"listen_dub", "听台词配音：返回音频内容块（audio/mpeg 等，按 MCP 规范 base64）。按 line_id、shot_id 或 episode_id 取，每次最多 8 段。", obj(map[string]any{
		"episode_id": num("集编号"), "shot_id": num("镜头编号"), "line_id": num("台词编号"), "limit": num("最多返回几段音频，1–8，默认 4"),
	})},
	{"get_video_link", "拿视频的限时下载地址（2 小时有效，不需要密钥）和建议文件名。MCP 没有视频内容类型：请用终端下载到工作区（curl -fL -o 文件名 地址）再看。给 video_id、shot_id（选定视频）或 episode_id（成片）。", obj(map[string]any{
		"video_id": num("视频编号"), "shot_id": num("镜头编号"), "episode_id": num("集编号（取成片）"),
	})},
}

func callWriterTool(ctx context.Context, name string, args json.RawMessage, base string, audioOK bool) (any, error) {
	switch name {
	case "get_writing_context":
		return toolWritingContext(ctx, args)
	case "save_bible":
		return toolSaveBible(ctx, args)
	case "save_synopsis":
		return toolSaveSynopsis(ctx, args)
	case "save_script":
		return toolSaveScript(ctx, args)
	case "save_shots":
		return toolSaveShots(ctx, args)
	case "save_episode_summary":
		return toolSaveSummary(ctx, args)
	case "upsert_asset":
		return toolUpsertAsset(ctx, args)
	case "view_asset_images":
		return toolViewAsset(ctx, args)
	case "view_shot":
		return toolViewShot(ctx, args)
	case "listen_dub":
		return toolListenDub(ctx, args, base, audioOK)
	case "get_video_link":
		return toolVideoLink(ctx, args, base)
	}
	return nil, errUnknownTool
}

// ---------- Antigravity 规则文件 ----------

//go:embed docs/antigravity/AGENTS.md
var agentRules string

func handleAgentRules(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	_, _ = w.Write([]byte(agentRules))
}
