package main

// 0.2.12：音频的文字版下载链接。
// 有的 MCP 宿主（如 Antigravity）不会把音频内容块交给模型，模型只看到「样本 xxx.wav」这样的文字，
// 而 /v1/audio/ 要密钥，模型也下不了。所以凡是返回音频的工具，都在文字里写明每段的文件名、时长、
// 念的文字和一个 2 小时有效、不需要密钥的下载地址（/v1/dl/，与 get_video_link 同一套签名），
// 智能体用 curl / Invoke-WebRequest 下到自己的工作区再 read_file 听。
// 另外：设计候选按角色存下来（voice_cands），界面的音色面板能看到、能选；get_audio_link 单独取链接；
// export_voice_candidates 把一部作品各角色的候选、当前音色、原声参考打成一个 ASCII 文件名的压缩包。

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const schemaVoiceCands = `
CREATE TABLE IF NOT EXISTS voice_cands (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id INTEGER NOT NULL DEFAULT 0,
  asset_id INTEGER NOT NULL DEFAULT 0,
  sample TEXT NOT NULL,
  channel_id INTEGER NOT NULL DEFAULT 0,
  channel_type TEXT NOT NULL DEFAULT '',
  description TEXT NOT NULL DEFAULT '',
  text TEXT NOT NULL DEFAULT '',
  duration REAL NOT NULL DEFAULT 0,
  source TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS voice_cands_u ON voice_cands(project_id, asset_id, sample);
`

const audioLinkTTL = 2 * time.Hour

// ---------- 签名下载地址 ----------

var asciiRe = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// asciiName 只留 ASCII 字母数字和 . _ -（Windows 解压中文文件名会乱码）
func asciiName(s string) string {
	s = strings.Trim(asciiRe.ReplaceAllString(s, "_"), "_")
	if s == "" {
		return "file"
	}
	return s
}

func audioDLURL(base, name string) string {
	return base + "/v1/dl/" + signDLName("a", filepath.Base(name), audioLinkTTL)
}

// AudioClip 一段音频的文字说明（给看不到音频块的模型）
type AudioClip struct {
	Label    string  `json:"label,omitempty"`
	File     string  `json:"file"`
	Duration float64 `json:"duration"`
	Text     string  `json:"text,omitempty"`
	URL      string  `json:"download_url"`
	Exists   bool    `json:"exists"`
}

func sampleText(name string) string {
	if b, err := os.ReadFile(filepath.Join(dataDir, "audio", filepath.Base(name)+".txt")); err == nil {
		return strings.TrimSpace(string(b))
	}
	return ""
}

func makeClip(base, name, label, text string, dur float64) AudioClip {
	name = filepath.Base(strings.TrimSpace(name))
	c := AudioClip{Label: label, File: name, Duration: dur, Text: strings.TrimSpace(text), Exists: audioExists(name)}
	if !c.Exists {
		return c
	}
	if c.Duration <= 0 {
		c.Duration = audioDur(name)
	}
	if c.Text == "" {
		c.Text = sampleText(name)
	}
	c.Duration = float64(int(c.Duration*10+0.5)) / 10
	c.URL = audioDLURL(base, name)
	return c
}

// clipsText 文字版清单 + 下载说明
func clipsText(clips []AudioClip) string {
	if len(clips) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("音频文件（如果你看不到 / 听不到音频内容块，按下面的地址下载到工作区再 read_file 听；地址 2 小时内有效，不需要密钥）：\n")
	first := ""
	for i, c := range clips {
		label := c.Label
		if label == "" {
			label = fmt.Sprintf("第 %d 段", i+1)
		}
		if !c.Exists {
			fmt.Fprintf(&b, "%d. %s · %s：文件不存在\n", i+1, label, c.File)
			continue
		}
		fmt.Fprintf(&b, "%d. %s · %s · %.1f 秒", i+1, label, c.File, c.Duration)
		if c.Text != "" {
			fmt.Fprintf(&b, " · 念的是「%s」", c.Text)
		}
		fmt.Fprintf(&b, "\n   下载：%s\n", c.URL)
		if first == "" {
			first = fmt.Sprintf("curl -fL -o %s \"%s\"\n  PowerShell：Invoke-WebRequest -Uri \"%s\" -OutFile %s", c.File, c.URL, c.URL, c.File)
		}
	}
	if first != "" {
		b.WriteString("下载示例（其余同理）：\n  " + first + "\n批量下载一部作品所有角色的候选：export_voice_candidates（一个压缩包 + 清单）。")
	}
	return b.String()
}

// ---------- 兜底：任何工具结果里提到的音频都补上下载地址 ----------

var audioURLRe = regexp.MustCompile(`/(?:v1/audio|files/audio)/([A-Za-z0-9._-]+\.(?:wav|mp3|m4a|ogg|opus|flac|aac))`)
var audioKeyRe = regexp.MustCompile(`^[A-Za-z0-9._-]+\.(?:wav|mp3|m4a|ogg|opus|flac|aac)$`)

var audioKeys = map[string]bool{"sample": true, "preview": true, "chosen": true, "vc_master": true, "master": true, "audio_file": true, "file": true}

// audioRefsIn 从任意结果里找出提到的本地音频文件（按出现顺序，去重）
func audioRefsIn(v any) []string {
	b, _ := json.Marshal(v)
	var x any
	_ = json.Unmarshal(b, &x)
	seen := map[string]bool{}
	out := []string{}
	add := func(n string) {
		if n != "" && !seen[n] && audioExists(n) {
			seen[n] = true
			out = append(out, n)
		}
	}
	var walk func(k string, v any)
	walk = func(k string, v any) {
		switch t := v.(type) {
		case map[string]any:
			keys := make([]string, 0, len(t))
			for kk := range t {
				keys = append(keys, kk)
			}
			sort.Strings(keys)
			for _, kk := range keys {
				walk(kk, t[kk])
			}
		case []any:
			for _, e := range t {
				walk(k, e)
			}
		case string:
			for _, m := range audioURLRe.FindAllStringSubmatch(t, -1) {
				add(m[1])
			}
			if audioKeys[k] && audioKeyRe.MatchString(t) {
				add(t)
			}
		}
	}
	walk("", x)
	return out
}

// withAudioLinks 给 MCP 结果补一段「音频下载地址」文字（已经自带的工具不重复）
func withAudioLinks(v any, base string) any {
	if rich, ok := v.(*mcpRich); ok {
		if rich.Linked {
			return v
		}
		names := audioRefsIn(rich.Blocks)
		for _, n := range audioRefsIn(rich.Structured) {
			if !contains(names, n) {
				names = append(names, n)
			}
		}
		if len(names) == 0 {
			return v
		}
		rich.Blocks = append(rich.Blocks, textBlock(clipsText(namesToClips(base, names))))
		return rich
	}
	names := audioRefsIn(v)
	if len(names) == 0 {
		return v
	}
	b, _ := json.MarshalIndent(v, "", "  ")
	return &mcpRich{Blocks: []any{textBlock(string(b)), textBlock(clipsText(namesToClips(base, names)))}, Structured: v, Linked: true}
}

func namesToClips(base string, names []string) []AudioClip {
	if len(names) > 40 {
		names = names[:40]
	}
	out := []AudioClip{}
	for _, n := range names {
		out = append(out, makeClip(base, n, "", "", 0))
	}
	return out
}

// ---------- 设计候选按角色存下来 ----------

type VoiceCand struct {
	ID          int64   `json:"id"`
	ProjectID   int64   `json:"project_id"`
	AssetID     int64   `json:"asset_id"` // 0 = 旁白
	Sample      string  `json:"sample"`
	ChannelID   int64   `json:"channel_id"`
	ChannelType string  `json:"channel_type"`
	Description string  `json:"description"`
	Text        string  `json:"text"`
	Duration    float64 `json:"duration"`
	Source      string  `json:"source"` // web / mcp / auto / adopt
	CreatedAt   int64   `json:"created_at"`
	URL         string  `json:"url"`
	Current     bool    `json:"current"`
}

// saveVoiceCands 记下设计候选（同一角色同一样本只记一次）
func saveVoiceCands(pid, aid int64, ch *Channel, desc, text string, names []string, durs []float64, source string) {
	if pid == 0 && aid > 0 {
		if as, err := getAsset(aid); err == nil {
			pid = as.ProjectID
		}
	}
	if pid == 0 {
		return
	}
	var chID int64
	chType := ""
	if ch != nil {
		chID, chType = ch.ID, ch.APIType
	}
	for i, n := range names {
		d := 0.0
		if i < len(durs) {
			d = durs[i]
		}
		if t := strings.TrimSpace(text); t != "" {
			p := filepath.Join(dataDir, "audio", n+".txt")
			if _, err := os.Stat(p); err != nil {
				_ = os.WriteFile(p, []byte(t), 0o644)
			}
		}
		_, _ = db.Exec(`INSERT OR IGNORE INTO voice_cands(project_id,asset_id,sample,channel_id,channel_type,description,text,duration,source,created_at) VALUES(?,?,?,?,?,?,?,?,?,?)`,
			pid, aid, n, chID, chType, strings.TrimSpace(desc), strings.TrimSpace(text), d, source, now())
	}
}

func listVoiceCands(pid, aid int64) []*VoiceCand {
	rows, err := db.Query(`SELECT id,project_id,asset_id,sample,channel_id,channel_type,description,text,duration,source,created_at FROM voice_cands WHERE project_id=? AND asset_id=? ORDER BY created_at, id`, pid, aid)
	if err != nil {
		return []*VoiceCand{}
	}
	defer rows.Close()
	out := []*VoiceCand{}
	for rows.Next() {
		var c VoiceCand
		if rows.Scan(&c.ID, &c.ProjectID, &c.AssetID, &c.Sample, &c.ChannelID, &c.ChannelType, &c.Description, &c.Text, &c.Duration, &c.Source, &c.CreatedAt) == nil {
			if !audioExists(c.Sample) {
				continue
			}
			c.URL = "/files/audio/" + c.Sample
			if c.Text == "" {
				c.Text = sampleText(c.Sample)
			}
			out = append(out, &c)
		}
	}
	return out
}

// findVoiceCand 按样本名找候选（优先这个角色 / 旁白的）
func findVoiceCand(pid, aid int64, sample string) *VoiceCand {
	var c VoiceCand
	q := `SELECT id,project_id,asset_id,sample,channel_id,channel_type,description,text,duration,source,created_at FROM voice_cands WHERE sample=? ORDER BY (asset_id=? AND project_id=?) DESC, id DESC LIMIT 1`
	if db.QueryRow(q, sample, aid, pid).Scan(&c.ID, &c.ProjectID, &c.AssetID, &c.Sample, &c.ChannelID, &c.ChannelType, &c.Description, &c.Text, &c.Duration, &c.Source, &c.CreatedAt) != nil {
		return nil
	}
	return &c
}

func candTarget(r *http.Request) (int64, int64, string, error) {
	q := r.URL.Query()
	aid, _ := strconv.ParseInt(q.Get("asset_id"), 10, 64)
	pid, _ := strconv.ParseInt(q.Get("project_id"), 10, 64)
	if aid > 0 {
		as, err := getAsset(aid)
		if err != nil {
			return 0, 0, "", errors.New("资产不存在")
		}
		return as.ProjectID, as.ID, as.Voice.Sample, nil
	}
	if pid > 0 {
		p, err := getProject(pid)
		if err != nil {
			return 0, 0, "", errors.New("作品不存在")
		}
		return p.ID, 0, p.NarratorVoice.Sample, nil
	}
	return 0, 0, "", errors.New("需要 asset_id 或 project_id")
}

// GET /api/voice/candidates?asset_id= | project_id=（旁白）
func handleVoiceCandsList(w http.ResponseWriter, r *http.Request) {
	pid, aid, cur, err := candTarget(r)
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	cs := listVoiceCands(pid, aid)
	for _, c := range cs {
		c.Current = c.Sample == cur
	}
	writeJSON(w, map[string]any{"candidates": cs})
}

// DELETE /api/voice/candidates/{id}：只从候选列表里拿掉（文件保留，角色正在用的不受影响）
func handleVoiceCandDelete(w http.ResponseWriter, r *http.Request) {
	_, _ = db.Exec(`DELETE FROM voice_cands WHERE id=?`, pathID(r))
	writeJSON(w, map[string]any{"ok": true})
}

// ---------- 目标解析（角色 / 旁白） ----------

type voiceOwner struct {
	pid   int64
	aid   int64
	name  string
	code  string
	voice VoiceSpec
	refs  []VoiceRef
	as    *Asset
}

func ownerOf(assetID, projectID int64) (*voiceOwner, error) {
	if assetID > 0 {
		as, err := getAsset(assetID)
		if err != nil || as.Kind != "character" {
			return nil, errors.New("角色不存在")
		}
		return &voiceOwner{pid: as.ProjectID, aid: as.ID, name: as.Name, code: as.Code, voice: as.Voice, refs: as.VoiceRefs, as: as}, nil
	}
	if projectID > 0 {
		p, err := getProject(projectID)
		if err != nil {
			return nil, errors.New("作品不存在")
		}
		return &voiceOwner{pid: p.ID, name: "旁白", code: "N00", voice: p.NarratorVoice}, nil
	}
	return nil, errors.New("需要 asset_id 或 project_id")
}

// ---------- get_audio_link ----------

type audioLinkArgs struct {
	Sample    string   `json:"sample"`
	Samples   []string `json:"samples"`
	AssetID   int64    `json:"asset_id"`
	ProjectID int64    `json:"project_id"`
	LibraryID int64    `json:"library_id"`
	LineID    int64    `json:"line_id"`
	Which     string   `json:"which"` // all / sample / refs / master / candidates
}

func toolAudioLink(ctx context.Context, raw json.RawMessage, base string) (any, error) {
	var a audioLinkArgs
	if err := strictArgs(raw, &a); err != nil {
		return nil, err
	}
	which := strings.TrimSpace(a.Which)
	if which == "" {
		which = "all"
	}
	if !contains([]string{"all", "sample", "refs", "master", "candidates"}, which) {
		return nil, errors.New("which 只能是 all / sample / refs / master / candidates")
	}
	want := func(w string) bool { return which == "all" || which == w }
	clips := []AudioClip{}
	seen := map[string]bool{}
	add := func(name, label, text string, dur float64) {
		name = strings.TrimSpace(name)
		if name == "" || seen[name] {
			return
		}
		if strings.ContainsAny(name, `/\`) || strings.Contains(name, "..") {
			clips = append(clips, AudioClip{Label: label, File: name})
			return
		}
		seen[name] = true
		clips = append(clips, makeClip(base, name, label, text, dur))
	}
	for _, s := range append([]string{a.Sample}, a.Samples...) {
		add(s, "样本", "", 0)
	}
	if a.LineID > 0 {
		ls := listLines(`WHERE id=?`, a.LineID)
		if len(ls) == 0 {
			return nil, errors.New("台词不存在")
		}
		if ls[0].AudioFile == "" {
			return nil, errors.New("这句台词还没有配音")
		}
		add(ls[0].AudioFile, fmt.Sprintf("台词 #%d · %s", ls[0].ID, ls[0].Speaker), ls[0].Text, ls[0].Duration)
	}
	if a.LibraryID > 0 {
		l, err := getLibVoice(a.LibraryID)
		if err != nil {
			return nil, err
		}
		if want("sample") {
			add(l.Sample, fmt.Sprintf("音色库 #%d「%s」样本", l.ID, l.Name), "", 0)
			add(l.Preview, fmt.Sprintf("音色库 #%d「%s」试听", l.ID, l.Name), "", 0)
		}
		if want("refs") {
			for i, r := range l.Refs {
				add(r.Sample, fmt.Sprintf("音色库 #%d 原声参考 %d", l.ID, i+1), "", r.Duration)
			}
		}
	}
	if a.AssetID > 0 || (a.ProjectID > 0 && a.LibraryID == 0) {
		o, err := ownerOf(a.AssetID, a.ProjectID)
		if err != nil {
			return nil, err
		}
		if want("sample") {
			add(o.voice.Sample, o.name+" · 当前音色样本（"+orDefault(o.voice.Mode, "?")+"）", o.voice.SampleText, 0)
		}
		if want("refs") {
			for i, r := range o.refs {
				add(r.Sample, fmt.Sprintf("%s · 原声参考 %d", o.name, i+1), "", r.Duration)
			}
		}
		if want("master") && o.as != nil {
			if c, how := vcMasterFor(o.as); c != nil {
				add(c.Sample, o.name+" · 统一音色母带（"+how+"）", "", c.Duration)
			}
		}
		if want("candidates") {
			for i, c := range listVoiceCands(o.pid, o.aid) {
				add(c.Sample, fmt.Sprintf("%s · 设计候选 %d", o.name, i+1), c.Text, c.Duration)
			}
		}
	}
	if len(clips) == 0 {
		if a.Sample == "" && len(a.Samples) == 0 && a.AssetID == 0 && a.ProjectID == 0 && a.LibraryID == 0 && a.LineID == 0 {
			return nil, errors.New("需要 sample / samples、asset_id、project_id（旁白）、library_id 或 line_id")
		}
		return nil, errors.New("没有找到本地音频（Fish 音色 / 内置音色没有本地样本，用 tts 合成一句试听）")
	}
	return &mcpRich{Blocks: []any{textBlock(clipsText(clips))}, Structured: map[string]any{"clips": clips, "expires_in_sec": int(audioLinkTTL.Seconds())}, Linked: true}, nil
}

// ---------- add_voice_candidates：把已有样本登记为某角色的设计候选 ----------

type addCandArgs struct {
	AssetID     int64    `json:"asset_id"`
	ProjectID   int64    `json:"project_id"`
	Samples     []string `json:"samples"`
	ChannelID   int64    `json:"channel_id"`
	Description string   `json:"description"`
	SampleText  string   `json:"sample_text"`
}

func toolAddVoiceCands(ctx context.Context, raw json.RawMessage, base string) (any, error) {
	var a addCandArgs
	if err := strictArgs(raw, &a); err != nil {
		return nil, err
	}
	o, err := ownerOf(a.AssetID, a.ProjectID)
	if err != nil {
		return nil, err
	}
	if len(a.Samples) == 0 {
		return nil, errors.New("需要 samples（design_voice 返回的样本名）")
	}
	var ch *Channel
	if a.ChannelID > 0 {
		if ch, err = voiceChannel(a.ChannelID); err != nil {
			return nil, errors.New("语音渠道不存在")
		}
	} else if ch, err = designVoiceChannel(0); err != nil {
		ch = nil
	}
	var ok, missing []string
	for _, s := range a.Samples {
		s = strings.TrimSpace(s)
		if !audioExists(s) {
			missing = append(missing, s)
			continue
		}
		ok = append(ok, s)
	}
	durs := make([]float64, len(ok))
	for i, s := range ok {
		durs[i] = audioDur(s)
	}
	desc := strings.TrimSpace(a.Description)
	if desc == "" {
		desc = o.voice.Desc
	}
	saveVoiceCands(o.pid, o.aid, ch, desc, a.SampleText, ok, durs, "adopt")
	clips := []AudioClip{}
	for i, c := range listVoiceCands(o.pid, o.aid) {
		clips = append(clips, makeClip(base, c.Sample, fmt.Sprintf("%s · 设计候选 %d", o.name, i+1), c.Text, c.Duration))
	}
	head := fmt.Sprintf("已登记 %d 个样本为「%s」的设计候选（界面音色面板「设计音色」里能看到、能选）。", len(ok), o.name)
	if len(missing) > 0 {
		head += "找不到这些文件：" + strings.Join(missing, "、") + "。"
	}
	return &mcpRich{Blocks: []any{textBlock(head + "\n\n" + clipsText(clips))}, Structured: map[string]any{"added": ok, "missing": nonNil(missing), "candidates": clips}, Linked: true}, nil
}

// ---------- export_voice_candidates ----------

type exportCandArgs struct {
	ProjectID       int64   `json:"project_id"`
	AssetIDs        []int64 `json:"asset_ids"`
	IncludeNarrator *bool   `json:"include_narrator"`
	IncludeRefs     *bool   `json:"include_refs"`
}

type exportEntry struct {
	File        string   `json:"file"`
	AssetID     int64    `json:"asset_id"`
	AssetCode   string   `json:"asset_code"`
	Character   string   `json:"character"`
	Roles       []string `json:"roles"` // candidate / current / ref / master
	CandIndex   int      `json:"candidate_index,omitempty"`
	RefIndex    int      `json:"ref_index,omitempty"`
	Sample      string   `json:"sample"`
	Duration    float64  `json:"duration"`
	Text        string   `json:"text,omitempty"`
	Description string   `json:"description,omitempty"`
	ChannelID   int64    `json:"channel_id,omitempty"`
	Bound       bool     `json:"bound"`
}

func cleanExports() {
	dir := filepath.Join(dataDir, "exports")
	es, _ := os.ReadDir(dir)
	for _, e := range es {
		if fi, err := e.Info(); err == nil && time.Since(fi.ModTime()) > 3*time.Hour {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}

func toolExportVoiceCands(ctx context.Context, raw json.RawMessage, base string) (any, error) {
	var a exportCandArgs
	if err := strictArgs(raw, &a); err != nil {
		return nil, err
	}
	p, err := getProject(a.ProjectID)
	if err != nil {
		return nil, errors.New("作品不存在")
	}
	inclRefs := a.IncludeRefs == nil || *a.IncludeRefs
	inclNarr := a.IncludeNarrator == nil || *a.IncludeNarrator
	if len(a.AssetIDs) > 0 && a.IncludeNarrator == nil {
		inclNarr = false
	}
	var owners []*voiceOwner
	idx := 0
	prefixes := map[int64]string{}
	for _, as := range listAssets(p.ID) {
		if as.Kind != "character" {
			continue
		}
		idx++
		if len(a.AssetIDs) > 0 && !containsID(a.AssetIDs, as.ID) {
			continue
		}
		code := asciiName(as.Code)
		if as.Code == "" || code != as.Code {
			code = fmt.Sprintf("C%02d", idx)
		}
		prefixes[as.ID] = fmt.Sprintf("%s_asset%d", code, as.ID)
		owners = append(owners, &voiceOwner{pid: p.ID, aid: as.ID, name: as.Name, code: as.Code, voice: as.Voice, refs: as.VoiceRefs, as: as})
	}
	for _, id := range a.AssetIDs {
		if _, ok := prefixes[id]; !ok {
			return nil, fmt.Errorf("资产 %d 不是这部作品的角色", id)
		}
	}
	if inclNarr {
		owners = append(owners, &voiceOwner{pid: p.ID, name: "旁白", code: "N00", voice: p.NarratorVoice})
		prefixes[0] = "N00_narrator"
	}
	cleanExports()
	dir := filepath.Join(dataDir, "exports")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	zipName := fmt.Sprintf("manju_voices_p%d_%s.zip", p.ID, time.Now().Format("20060102_150405"))
	f, err := os.Create(filepath.Join(dir, zipName))
	if err != nil {
		return nil, err
	}
	zw := zip.NewWriter(f)
	var entries []*exportEntry
	type charInfo struct {
		AssetID      int64      `json:"asset_id"`
		AssetCode    string     `json:"asset_code"`
		Character    string     `json:"character"`
		FilePrefix   string     `json:"file_prefix"`
		CurrentVoice VoiceSpec  `json:"current_voice"`
		VoiceRefs    []VoiceRef `json:"voice_refs,omitempty"`
		Candidates   int        `json:"candidates"`
		Master       string     `json:"vc_master,omitempty"`
	}
	var chars []charInfo
	put := func(src, dst string) error {
		in, err := os.Open(filepath.Join(dataDir, "audio", src))
		if err != nil {
			return err
		}
		defer in.Close()
		hw, err := zw.CreateHeader(&zip.FileHeader{Name: dst, Method: zip.Deflate, Modified: time.Now()})
		if err != nil {
			return err
		}
		_, err = io.Copy(hw, in)
		return err
	}
	for _, o := range owners {
		pre := prefixes[o.aid]
		bySample := map[string]*exportEntry{}
		addE := func(sample, role string, ci, ri int, text, desc string, chID int64, dur float64) {
			if !audioExists(sample) {
				return
			}
			if e := bySample[sample]; e != nil {
				if !contains(e.Roles, role) {
					e.Roles = append(e.Roles, role)
				}
				if ci > 0 && e.CandIndex == 0 {
					e.CandIndex = ci
				}
				if ri > 0 && e.RefIndex == 0 {
					e.RefIndex = ri
				}
				return
			}
			tag := role
			switch role {
			case "candidate":
				tag = fmt.Sprintf("cand%d", ci)
			case "ref":
				tag = fmt.Sprintf("ref%d", ri)
			}
			if dur <= 0 {
				dur = audioDur(sample)
			}
			if text == "" {
				text = sampleText(sample)
			}
			e := &exportEntry{File: fmt.Sprintf("%s_%s_%s", pre, tag, asciiName(sample)), AssetID: o.aid, AssetCode: o.code, Character: o.name, Roles: []string{role},
				CandIndex: ci, RefIndex: ri, Sample: sample, Duration: float64(int(dur*10+0.5)) / 10, Text: text, Description: desc, ChannelID: chID, Bound: sample == o.voice.Sample}
			bySample[sample] = e
			entries = append(entries, e)
		}
		cands := listVoiceCands(o.pid, o.aid)
		for i, c := range cands {
			addE(c.Sample, "candidate", i+1, 0, c.Text, c.Description, c.ChannelID, c.Duration)
		}
		addE(o.voice.Sample, "current", 0, 0, o.voice.SampleText, o.voice.Desc, o.voice.ChannelID, 0)
		ci := charInfo{AssetID: o.aid, AssetCode: o.code, Character: o.name, FilePrefix: pre, CurrentVoice: o.voice, Candidates: len(cands)}
		if inclRefs {
			for i, r := range o.refs {
				addE(r.Sample, "ref", 0, i+1, "", "", 0, r.Duration)
			}
			ci.VoiceRefs = o.refs
			if o.as != nil {
				if c, _ := vcMasterFor(o.as); c != nil {
					addE(c.Sample, "master", 0, 0, "", "", 0, c.Duration)
					ci.Master = c.Sample
				}
			}
		}
		chars = append(chars, ci)
	}
	for _, e := range entries {
		if err := put(e.Sample, e.File); err != nil {
			zw.Close()
			f.Close()
			return nil, fmt.Errorf("打包 %s 失败：%s", e.Sample, err.Error())
		}
	}
	manifest := map[string]any{"project_id": p.ID, "project_title": p.Title, "generated_at": time.Now().Format(time.RFC3339), "files": entries, "characters": chars,
		"how_to_bind": "选定后调用 set_voice：asset_id = 角色编号，sample = 清单里的 sample（原始样本名，不是压缩包里的文件名）；channel_id 不填会用这个候选当初的设计渠道。旁白用 project_id、不填 asset_id。"}
	mj, _ := json.MarshalIndent(manifest, "", "  ")
	if w, err := zw.Create("manifest.json"); err == nil {
		_, _ = w.Write(mj)
	}
	var tb strings.Builder
	fmt.Fprintf(&tb, "作品 #%d《%s》音色候选清单（%s）\n\n", p.ID, p.Title, time.Now().Format("2006-01-02 15:04"))
	for _, c := range chars {
		fmt.Fprintf(&tb, "== %s · %s（asset_id=%d）当前音色：%s ==\n", c.FilePrefix, c.Character, c.AssetID, orDefault(voiceBrief(c.CurrentVoice), "未设置"))
		for _, e := range entries {
			if e.AssetID != c.AssetID || prefixes[e.AssetID] != c.FilePrefix {
				continue
			}
			fmt.Fprintf(&tb, "%s\t%s\t%.1f 秒\tsample=%s", e.File, strings.Join(e.Roles, "+"), e.Duration, e.Sample)
			if e.Bound {
				tb.WriteString("\t[当前绑定]")
			}
			if e.Text != "" {
				fmt.Fprintf(&tb, "\t念：「%s」", e.Text)
			}
			tb.WriteString("\n")
		}
		tb.WriteString("\n")
	}
	tb.WriteString("选定：set_voice（asset_id + sample = 上面的 sample= 值）。\n")
	if w, err := zw.Create("manifest.txt"); err == nil {
		_, _ = w.Write([]byte(tb.String()))
	}
	if err := zw.Close(); err != nil {
		f.Close()
		return nil, err
	}
	f.Close()
	fi, _ := os.Stat(filepath.Join(dir, zipName))
	var size int64
	if fi != nil {
		size = fi.Size()
	}
	url := base + "/v1/dl/" + signDLName("z", zipName, audioLinkTTL)
	nc := 0
	for _, c := range chars {
		nc += c.Candidates
	}
	text := fmt.Sprintf("已打包 %d 个角色 / 旁白的 %d 个音频（其中设计候选 %d 个），%.1f MB。\n下载（2 小时内有效，不需要密钥）：%s\n"+
		"  curl -fL -o %s \"%s\"\n  PowerShell：Invoke-WebRequest -Uri \"%s\" -OutFile %s; Expand-Archive %s -DestinationPath %s\n"+
		"压缩包里的文件名都是 ASCII（如 C01_asset10_cand1_vd-xxx.wav），manifest.txt / manifest.json 对应角色、候选序号、原始样本名、时长、念的文字和当前绑定。听完用 set_voice（asset_id + 清单里的 sample）选定。\n\n%s",
		len(chars), len(entries), nc, float64(size)/(1<<20), url, zipName, url, url, zipName, zipName, strings.TrimSuffix(zipName, ".zip"), tb.String())
	return &mcpRich{Blocks: []any{textBlock(text)}, Structured: map[string]any{"url": url, "filename": zipName, "bytes": size, "files": entries, "characters": chars, "expires_in_sec": int(audioLinkTTL.Seconds())}, Linked: true}, nil
}

func voiceBrief(v VoiceSpec) string {
	if v.empty() {
		return ""
	}
	s := orDefault(v.Mode, "?")
	if v.Title != "" {
		s += " · " + v.Title
	}
	if v.Sample != "" {
		s += " · sample=" + v.Sample
	} else if v.Voice != "" {
		s += " · voice=" + v.Voice
	}
	return s
}

func containsID(xs []int64, id int64) bool {
	for _, x := range xs {
		if x == id {
			return true
		}
	}
	return false
}

// ---------- MCP ----------

var linkTools = []mcpTool{
	{"get_audio_link", "取音频的限时下载地址（2 小时，不需要密钥）+ 文件名、时长、念的文字。宿主不把音频内容块交给你时（只看到「样本 xxx.wav」），用它拿地址，curl / Invoke-WebRequest 下到工作区再 read_file 听。来源：sample / samples（样本名）；asset_id（角色：当前音色样本、原声参考、统一音色母带、设计候选）；project_id 不填 asset_id（旁白）；library_id（音色库）；line_id（某句配音）。which 只取一类：sample / refs / master / candidates（默认 all）。", obj(map[string]any{
		"sample": str("样本名"), "samples": arr("多个样本名", map[string]any{"type": "string"}), "asset_id": num("角色资产编号"),
		"project_id": num("作品编号（旁白，且不填 asset_id）"), "library_id": num("音色库编号"), "line_id": num("台词编号（取这句的配音）"),
		"which": enumS("只取哪一类（默认 all）", "all", "sample", "refs", "master", "candidates"),
	})},
	{"export_voice_candidates", "把一部作品各角色的音色打成一个压缩包，返回一个限时下载地址（2 小时，不需要密钥）：每个角色的全部设计候选（design_voice 存下的）、当前绑定的音色样本、原声参考和统一音色母带，旁白也算。压缩包里文件名全是 ASCII（如 C01_asset10_cand1_vd-xxx.wav，Windows 解压不乱码），附 manifest.txt / manifest.json：文件 → 角色编号、角色名、候选序号、原始样本名、时长、念的文字、是否当前绑定。批量试听候选用这个；选定用 set_voice（asset_id + 清单里的 sample）。", obj(map[string]any{
		"project_id": num("作品编号"), "asset_ids": map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "description": "只要这些角色（可选；默认全部角色 + 旁白）"},
		"include_narrator": boolp("包含旁白（默认：不填 asset_ids 时包含）"), "include_refs": boolp("包含原声参考和母带（默认 true）"),
	}, "project_id")},
	{"add_voice_candidates", "把已经生成的样本登记为某个角色（或旁白）的设计候选，让它出现在界面音色面板的候选列表里、能被 export_voice_candidates 打包。0.2.12 之前用 design_voice 生成的候选没有按角色记下来，用这个补登。之后用 set_voice 选定。", obj(map[string]any{
		"asset_id": num("角色资产编号"), "project_id": num("作品编号（旁白，且不填 asset_id）"), "samples": arr("样本名（design_voice 返回的 sample）", map[string]any{"type": "string"}),
		"channel_id": num("设计时用的语音渠道编号（可选）"), "description": str("音色描述（可选）"), "sample_text": str("试音台词（可选，没记下时补上）"),
	}, "samples")},
}

func callLinkTool(ctx context.Context, name string, raw json.RawMessage, base string) (any, error) {
	switch name {
	case "get_audio_link":
		return toolAudioLink(ctx, raw, base)
	case "export_voice_candidates":
		return toolExportVoiceCands(ctx, raw, base)
	case "add_voice_candidates":
		return toolAddVoiceCands(ctx, raw, base)
	}
	return nil, errUnknownTool
}
