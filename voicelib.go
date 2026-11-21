package main

// 音色库（0.2.10）：跨作品收藏音色。设计候选、角色当前音色、克隆结果、Fish 音色都能收进来，
// 再一键用到任意作品的任意角色或旁白上。
// 样本文件复制一份到音色库自己的文件（audio/lib-*，连同 .txt 台词与 .fish 模型编号），删作品不影响；
// 用到角色上时再复制一份给角色（audio/vl-*），删音色库条目也不影响已经用上的角色。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const schemaVoiceLib = `
CREATE TABLE IF NOT EXISTS voice_library (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL DEFAULT '',
  description TEXT NOT NULL DEFAULT '',
  tags TEXT NOT NULL DEFAULT '[]',
  gender TEXT NOT NULL DEFAULT '',
  age TEXT NOT NULL DEFAULT '',
  source TEXT NOT NULL DEFAULT '',
  voice TEXT NOT NULL DEFAULT '{}',
  channel_id INTEGER NOT NULL DEFAULT 0,
  mode TEXT NOT NULL DEFAULT '',
  voice_id TEXT NOT NULL DEFAULT '',
  sample TEXT NOT NULL DEFAULT '',
  synth_channel_id INTEGER NOT NULL DEFAULT 0,
  speed REAL NOT NULL DEFAULT 1,
  pitch REAL NOT NULL DEFAULT 0,
  fish_model_id TEXT NOT NULL DEFAULT '',
  preview TEXT NOT NULL DEFAULT '',
  refs TEXT NOT NULL DEFAULT '[]',
  favorite INTEGER NOT NULL DEFAULT 0,
  origin_project_id INTEGER NOT NULL DEFAULT 0,
  origin_asset_id INTEGER NOT NULL DEFAULT 0,
  origin TEXT NOT NULL DEFAULT '',
  src_key TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS voice_library_fav ON voice_library(favorite, updated_at);
`

// LibVoice 音色库里的一条音色
type LibVoice struct {
	ID              int64      `json:"id"`
	Name            string     `json:"name"`
	Description     string     `json:"description"`
	Tags            []string   `json:"tags"`
	Gender          string     `json:"gender"`
	Age             string     `json:"age"`
	Source          string     `json:"source"` // design / clone / fish / builtin / openai
	Voice           VoiceSpec  `json:"voice"`  // 完整音色；sample 指向音色库自己的文件
	ChannelID       int64      `json:"channel_id"`
	Mode            string     `json:"mode"`
	VoiceID         string     `json:"voice_id"`
	Sample          string     `json:"sample"`
	SynthChannelID  int64      `json:"synth_channel_id"`
	Speed           float64    `json:"speed"`
	Pitch           float64    `json:"pitch"`
	FishModelID     string     `json:"fish_model_id"`
	Preview         string     `json:"preview"`
	Refs            []VoiceRef `json:"voice_refs"`
	Favorite        bool       `json:"favorite"`
	OriginProjectID int64      `json:"origin_project_id"`
	OriginAssetID   int64      `json:"origin_asset_id"`
	Origin          string     `json:"origin"`
	CreatedAt       int64      `json:"created_at"`
	UpdatedAt       int64      `json:"updated_at"`
	SampleURL       string     `json:"sample_url"`
	PreviewURL      string     `json:"preview_url"`
	RefURLs         []string   `json:"ref_urls"`
	srcKey          string
}

const libCols = `id,name,description,tags,gender,age,source,voice,channel_id,mode,voice_id,sample,synth_channel_id,speed,pitch,fish_model_id,preview,refs,favorite,origin_project_id,origin_asset_id,origin,src_key,created_at,updated_at`

func scanLib(row interface{ Scan(...any) error }) (*LibVoice, error) {
	var l LibVoice
	var tags, voice, refs string
	var fav int
	if err := row.Scan(&l.ID, &l.Name, &l.Description, &tags, &l.Gender, &l.Age, &l.Source, &voice, &l.ChannelID, &l.Mode, &l.VoiceID, &l.Sample,
		&l.SynthChannelID, &l.Speed, &l.Pitch, &l.FishModelID, &l.Preview, &refs, &fav, &l.OriginProjectID, &l.OriginAssetID, &l.Origin, &l.srcKey, &l.CreatedAt, &l.UpdatedAt); err != nil {
		return nil, err
	}
	_ = json.Unmarshal([]byte(tags), &l.Tags)
	_ = json.Unmarshal([]byte(voice), &l.Voice)
	_ = json.Unmarshal([]byte(refs), &l.Refs)
	if l.Tags == nil {
		l.Tags = []string{}
	}
	if l.Refs == nil {
		l.Refs = []VoiceRef{}
	}
	l.Favorite = fav != 0
	l.setURLs("/files/audio/")
	return &l, nil
}

// setURLs 按访问方式（界面 /files/audio/，外部 {base}/v1/audio/）填试听地址
func (l *LibVoice) setURLs(prefix string) {
	l.SampleURL, l.PreviewURL = "", ""
	if l.Sample != "" {
		l.SampleURL = prefix + l.Sample
	}
	if l.Preview != "" {
		l.PreviewURL = prefix + l.Preview
	} else if strings.HasPrefix(l.Voice.Preview, "http") {
		l.PreviewURL = l.Voice.Preview
	}
	l.RefURLs = []string{}
	for _, r := range l.Refs {
		l.RefURLs = append(l.RefURLs, prefix+r.Sample)
	}
}

func getLibVoice(id int64) (*LibVoice, error) {
	l, err := scanLib(db.QueryRow(`SELECT `+libCols+` FROM voice_library WHERE id=?`, id))
	if err != nil {
		return nil, errors.New("音色库里没有这个音色")
	}
	return l, nil
}

func listLibVoices(query string, favOnly bool) []*LibVoice {
	where := []string{"1=1"}
	var args []any
	if favOnly {
		where = append(where, "favorite=1")
	}
	for _, w := range strings.Fields(query) {
		like := "%" + w + "%"
		where = append(where, "(name LIKE ? OR description LIKE ? OR tags LIKE ? OR gender LIKE ? OR age LIKE ? OR source LIKE ? OR origin LIKE ?)")
		args = append(args, like, like, like, like, like, like, like)
	}
	rows, err := db.Query(`SELECT `+libCols+` FROM voice_library WHERE `+strings.Join(where, " AND ")+` ORDER BY favorite DESC, updated_at DESC, id DESC`, args...)
	if err != nil {
		return []*LibVoice{}
	}
	defer rows.Close()
	out := []*LibVoice{}
	for rows.Next() {
		if l, err := scanLib(rows); err == nil {
			out = append(out, l)
		}
	}
	return out
}

// ---------- 文件 ----------

func safeAudioName(n string) bool {
	return n != "" && !strings.ContainsAny(n, "/\\") && !strings.Contains(n, "..")
}

// copyAudio 复制一个音频文件（连同 .txt 台词、.fish 模型编号），返回新文件名
func copyAudio(src, prefix string) (string, error) {
	if !safeAudioName(src) {
		return "", errors.New("样本名不对")
	}
	dir := filepath.Join(dataDir, "audio")
	data, err := os.ReadFile(filepath.Join(dir, src))
	if err != nil {
		return "", fmt.Errorf("样本文件 %s 不存在", src)
	}
	ext := filepath.Ext(src)
	if ext == "" {
		ext = ".wav"
	}
	name := fmt.Sprintf("%s-%d-%s%s", prefix, time.Now().Unix(), randHex(4), ext)
	if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
		return "", err
	}
	for _, side := range []string{".txt", ".fish"} {
		if b, err := os.ReadFile(filepath.Join(dir, src+side)); err == nil {
			_ = os.WriteFile(filepath.Join(dir, name+side), b, 0o644)
		}
	}
	return name, nil
}

func removeAudio(name string) {
	if !safeAudioName(name) {
		return
	}
	dir := filepath.Join(dataDir, "audio")
	for _, side := range []string{"", ".txt", ".fish"} {
		_ = os.Remove(filepath.Join(dir, name+side))
	}
}

// fishIDOf 设计 / 克隆样本在 Fish 上建过的私有音色编号
func fishIDOf(sample string) string {
	if !safeAudioName(sample) {
		return ""
	}
	b, _ := os.ReadFile(filepath.Join(dataDir, "audio", sample+".fish"))
	return strings.TrimSpace(string(b))
}

// downloadPreview 把远程试听（如 Fish 音色的样本地址）存成音色库文件；失败返回空
func downloadPreview(ctx context.Context, u string) string {
	if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
		return ""
	}
	data, err := fetchAudioURL(ctx, u)
	if err != nil || len(data) < 200 {
		return ""
	}
	ext := strings.ToLower(filepath.Ext(strings.SplitN(u, "?", 2)[0]))
	if audioMimes[ext] == "" {
		ext = ".mp3"
	}
	name := fmt.Sprintf("lib-%d-%s%s", time.Now().Unix(), randHex(4), ext)
	if os.WriteFile(filepath.Join(dataDir, "audio", name), data, 0o644) != nil {
		return ""
	}
	return name
}

// ---------- 收藏 ----------

type libSaveIn struct {
	AssetID        int64      `json:"asset_id"`
	ProjectID      int64      `json:"project_id"`
	Sample         string     `json:"sample"`
	ChannelID      int64      `json:"channel_id"`
	SynthChannelID int64      `json:"synth_channel_id"`
	Voice          *VoiceSpec `json:"voice"`
	FishVoiceID    string     `json:"fish_voice_id"`
	PreviewURL     string     `json:"preview_url"`
	Name           string     `json:"name"`
	Description    string     `json:"description"`
	Tags           []string   `json:"tags"`
	Gender         string     `json:"gender"`
	Age            string     `json:"age"`
	Favorite       *bool      `json:"favorite"`
	Source         string     `json:"source"`
}

var libSources = []string{"design", "clone", "fish", "builtin", "openai"}

// sourceOf 由音色推断来源
func sourceOf(v VoiceSpec) string {
	switch {
	case strings.HasPrefix(v.Sample, "vc-"):
		return "clone"
	case v.Mode == "fish" && v.Sample != "":
		return "design"
	case contains(libSources, v.Mode):
		return v.Mode
	}
	return "design"
}

func cleanTags(tags []string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, t := range tags {
		for _, x := range strings.FieldsFunc(t, func(r rune) bool { return r == ',' || r == '，' || r == '、' }) {
			x = strings.TrimSpace(x)
			if x != "" && !seen[x] && len(out) < 20 {
				seen[x] = true
				out = append(out, x)
			}
		}
	}
	return out
}

// saveLibVoice 收进音色库。来源（优先级从高到低）：voice 对象 / sample（设计候选或克隆样本）/ fish_voice_id / asset_id（角色当前音色）/ project_id（旁白）。
// 同一段样本（或同一个远程音色）已在库里时不重复收藏，返回已有的那条（exists=true）。
func saveLibVoice(ctx context.Context, in libSaveIn, extraRefs []VoiceRef) (*LibVoice, bool, error) {
	var spec VoiceSpec
	var refs []VoiceRef
	var as *Asset
	var p *Project
	if in.AssetID > 0 {
		x, err := getAsset(in.AssetID)
		if err != nil || x.Kind != "character" {
			return nil, false, errors.New("角色不存在")
		}
		as = x
		p, _ = getProject(x.ProjectID)
	} else if in.ProjectID > 0 {
		x, err := getProject(in.ProjectID)
		if err != nil {
			return nil, false, errors.New("作品不存在")
		}
		p = x
	}
	createdOn := int64(0)
	sample := strings.TrimSpace(in.Sample)
	switch {
	case in.Voice != nil:
		spec = *in.Voice
	case sample != "":
		if !safeAudioName(sample) {
			return nil, false, errors.New("样本名不对")
		}
		if _, err := os.Stat(filepath.Join(dataDir, "audio", sample)); err != nil {
			return nil, false, fmt.Errorf("样本 %s 不存在", sample)
		}
		var ch *Channel
		if c, err := voiceChannel(in.ChannelID); err == nil && (c.APIType == "mimo" || c.APIType == "fish") {
			ch = c
		} else {
			c, err := designVoiceChannel(0)
			if err != nil {
				return nil, false, err
			}
			ch = c
		}
		mode := "design"
		if strings.HasPrefix(sample, "vc-") {
			mode = "clone"
		}
		text := ""
		if t, err := os.ReadFile(filepath.Join(dataDir, "audio", sample+".txt")); err == nil {
			text = strings.TrimSpace(string(t))
		}
		spec = VoiceSpec{ChannelID: ch.ID, Mode: mode, Sample: sample, SampleText: text, Desc: strings.TrimSpace(in.Description), Speed: 1}
	case strings.TrimSpace(in.FishVoiceID) != "":
		ch, err := fishChannelFor(in.ChannelID)
		if err != nil {
			return nil, false, err
		}
		spec = VoiceSpec{ChannelID: ch.ID, Mode: "fish", Voice: strings.TrimSpace(in.FishVoiceID), Title: strings.TrimSpace(in.Name), Speed: 1}
	case as != nil:
		spec = as.Voice
	case p != nil:
		spec = p.NarratorVoice
	default:
		return nil, false, errors.New("要收藏哪个音色：asset_id（角色当前音色）、sample + channel_id（设计候选 / 克隆样本）、project_id（旁白）、fish_voice_id 或 voice 对象")
	}
	if spec.empty() {
		if as != nil && in.Voice == nil && sample == "" {
			return nil, false, fmt.Errorf("角色「%s」还没有音色", as.Name)
		}
		if p != nil && as == nil && in.Voice == nil && sample == "" {
			return nil, false, errors.New("这个作品还没有旁白音色")
		}
		return nil, false, errors.New("音色不完整，没法收藏")
	}
	createdOn = spec.ChannelID
	if in.SynthChannelID > 0 && spec.Sample != "" {
		sc, err := voiceChannel(in.SynthChannelID)
		if err != nil || (sc.APIType != "mimo" && sc.APIType != "fish") {
			return nil, false, errors.New("配音渠道需要是小米 MiMo 或 Fish Audio")
		}
		spec.ChannelID = sc.ID
		if spec.Mode == "fish" {
			spec.Mode = "design"
		}
		spec.Voice = ""
	}
	if spec.Speed <= 0 {
		spec.Speed = 1
	}
	// 角色的原声参考：音色就是角色当前音色时一起收
	if as != nil && (in.Voice == nil && sample == "" || spec.Sample != "" && spec.Sample == as.Voice.Sample) {
		refs = append(refs, as.VoiceRefs...)
	}
	refs = append(refs, extraRefs...)
	source := strings.TrimSpace(in.Source)
	if !contains(libSources, source) {
		source = sourceOf(spec)
	}
	srcKey := ""
	if spec.Sample != "" {
		srcKey = "sample:" + spec.Sample
	} else {
		srcKey = fmt.Sprintf("%s:%d:%s", spec.Mode, spec.ChannelID, spec.Voice)
	}
	if strings.HasPrefix(spec.Sample, "lib-") {
		srcKey = ""
	}
	if srcKey != "" {
		var id int64
		if db.QueryRow(`SELECT id FROM voice_library WHERE src_key=? ORDER BY id LIMIT 1`, srcKey).Scan(&id) == nil {
			l, err := getLibVoice(id)
			return l, true, err
		}
	}
	name := strings.TrimSpace(in.Name)
	origin := ""
	if p != nil {
		origin = p.Title
		if as != nil {
			origin += " · " + as.Name
		} else if in.AssetID == 0 && in.ProjectID > 0 {
			origin += " · 旁白"
		}
	}
	if name == "" {
		switch {
		case as != nil:
			name = as.Name + "的声音"
		case p != nil:
			name = p.Title + "的旁白"
		case spec.Title != "":
			name = spec.Title
		default:
			name = "未命名音色"
		}
	}
	desc := orDefault(strings.TrimSpace(in.Description), spec.Desc)
	fishID := ""
	if spec.Mode == "fish" && spec.Voice != "" {
		fishID = spec.Voice
	} else if spec.Sample != "" {
		fishID = fishIDOf(spec.Sample)
	}
	// 复制样本与原声参考
	var made []string
	fail := func(err error) (*LibVoice, bool, error) {
		for _, f := range made {
			removeAudio(f)
		}
		return nil, false, err
	}
	orig := spec.Sample
	if spec.Sample != "" {
		n, err := copyAudio(spec.Sample, "lib")
		if err != nil {
			return fail(err)
		}
		made = append(made, n)
		spec.Sample = n
	}
	libRefs := []VoiceRef{}
	seen := map[string]bool{}
	for _, r := range refs {
		if r.Sample == "" || seen[r.Sample] {
			continue
		}
		seen[r.Sample] = true
		nr := r
		if r.Sample == orig && spec.Sample != "" {
			nr.Sample = spec.Sample
		} else {
			n, err := copyAudio(r.Sample, "lib")
			if err != nil {
				continue
			}
			made = append(made, n)
			nr.Sample = n
		}
		libRefs = append(libRefs, nr)
	}
	preview := ""
	pu := strings.TrimSpace(in.PreviewURL)
	if pu == "" {
		pu = spec.Preview
	}
	if pu != "" && spec.Sample == "" {
		c, cancel := context.WithTimeout(ctx, 60*time.Second)
		preview = downloadPreview(c, pu)
		cancel()
		if preview != "" {
			made = append(made, preview)
		}
		if strings.HasPrefix(pu, "http") {
			spec.Preview = pu
		}
	}
	spec.Title = name
	if spec.Desc == "" {
		spec.Desc = desc
	}
	fav := 0
	if in.Favorite != nil && *in.Favorite {
		fav = 1
	}
	var oaid, opid int64
	if as != nil {
		oaid, opid = as.ID, as.ProjectID
	} else if p != nil {
		opid = p.ID
	}
	if createdOn == 0 {
		createdOn = spec.ChannelID
	}
	t := now()
	res, err := db.Exec(`INSERT INTO voice_library(name,description,tags,gender,age,source,voice,channel_id,mode,voice_id,sample,synth_channel_id,speed,pitch,fish_model_id,preview,refs,favorite,origin_project_id,origin_asset_id,origin,src_key,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		name, desc, mustJSON(cleanTags(in.Tags)), strings.TrimSpace(in.Gender), strings.TrimSpace(in.Age), source, mustJSON(spec), createdOn, spec.Mode, spec.Voice, spec.Sample,
		spec.ChannelID, spec.Speed, spec.Pitch, fishID, preview, mustJSON(libRefs), fav, opid, oaid, origin, srcKey, t, t)
	if err != nil {
		return fail(err)
	}
	id, _ := res.LastInsertId()
	l, err := getLibVoice(id)
	return l, false, err
}

// ---------- 修改 / 删除 ----------

type libUpdateIn struct {
	Name        *string   `json:"name"`
	Description *string   `json:"description"`
	Tags        *[]string `json:"tags"`
	Gender      *string   `json:"gender"`
	Age         *string   `json:"age"`
	Favorite    *bool     `json:"favorite"`
	Speed       *float64  `json:"speed"`
	Pitch       *float64  `json:"pitch"`
}

func updateLibVoice(id int64, in libUpdateIn) (*LibVoice, error) {
	l, err := getLibVoice(id)
	if err != nil {
		return nil, err
	}
	if in.Name != nil {
		if strings.TrimSpace(*in.Name) == "" {
			return nil, errors.New("名称不能为空")
		}
		l.Name = strings.TrimSpace(*in.Name)
		l.Voice.Title = l.Name
	}
	if in.Description != nil {
		l.Description = strings.TrimSpace(*in.Description)
	}
	if in.Tags != nil {
		l.Tags = cleanTags(*in.Tags)
	}
	if in.Gender != nil {
		l.Gender = strings.TrimSpace(*in.Gender)
	}
	if in.Age != nil {
		l.Age = strings.TrimSpace(*in.Age)
	}
	if in.Favorite != nil {
		l.Favorite = *in.Favorite
	}
	if in.Speed != nil {
		l.Voice.Speed = clampF(*in.Speed, 0.5, 2)
	}
	if in.Pitch != nil {
		l.Voice.Pitch = clampF(*in.Pitch, -12, 12)
	}
	fav := 0
	if l.Favorite {
		fav = 1
	}
	_, err = db.Exec(`UPDATE voice_library SET name=?, description=?, tags=?, gender=?, age=?, favorite=?, voice=?, speed=?, pitch=?, updated_at=? WHERE id=?`,
		l.Name, l.Description, mustJSON(l.Tags), l.Gender, l.Age, fav, mustJSON(l.Voice), l.Voice.Speed, l.Voice.Pitch, now(), l.ID)
	if err != nil {
		return nil, err
	}
	return getLibVoice(id)
}

func deleteLibVoice(id int64) error {
	l, err := getLibVoice(id)
	if err != nil {
		return err
	}
	if _, err := db.Exec(`DELETE FROM voice_library WHERE id=?`, id); err != nil {
		return err
	}
	// 只删音色库自己的文件（lib-*）；已经用到角色上的是另一份副本
	for _, f := range append([]string{l.Sample, l.Preview, l.Voice.Sample}, func() []string {
		var s []string
		for _, r := range l.Refs {
			s = append(s, r.Sample)
		}
		return s
	}()...) {
		if strings.HasPrefix(f, "lib-") {
			removeAudio(f)
		}
	}
	return nil
}

// ---------- 用到角色 / 旁白上 ----------

// libApplyChannel 音色原来的渠道不在了时换一个能用的
func libApplyChannel(v VoiceSpec) (int64, string, error) {
	if ch, err := voiceChannel(v.ChannelID); err == nil {
		return ch.ID, "", nil
	}
	var ch *Channel
	switch {
	case v.Sample != "":
		if ch = firstVoiceChannel("mimo"); ch == nil {
			ch = firstVoiceChannel("fish")
		}
	case v.Mode == "fish":
		ch = firstVoiceChannel("fish")
	case v.Mode == "builtin":
		ch = firstVoiceChannel("mimo")
	case v.Mode == "openai":
		ch = firstVoiceChannel("openai")
	}
	if ch == nil {
		return 0, "", errors.New("音色原来的语音渠道已经删除，也没有能用这个音色的其他语音渠道")
	}
	warn := fmt.Sprintf("音色原来的语音渠道已删除，改用「%s」", ch.Name)
	if v.Mode == "fish" && v.Sample == "" {
		warn += "（Fish 私有音色只在建它的账号下可用）"
	}
	return ch.ID, warn, nil
}

func applyLibVoice(id, assetID, projectID int64) (map[string]any, error) {
	l, err := getLibVoice(id)
	if err != nil {
		return nil, err
	}
	var as *Asset
	var p *Project
	if assetID > 0 {
		x, err := getAsset(assetID)
		if err != nil || x.Kind != "character" {
			return nil, errors.New("角色不存在")
		}
		as = x
	} else if projectID > 0 {
		x, err := getProject(projectID)
		if err != nil {
			return nil, errors.New("作品不存在")
		}
		p = x
	} else {
		return nil, errors.New("用到哪里：asset_id（角色）或 project_id（旁白）")
	}
	spec := l.Voice
	warn := []string{}
	cid, w, err := libApplyChannel(spec)
	if err != nil {
		return nil, err
	}
	spec.ChannelID = cid
	if w != "" {
		warn = append(warn, w)
	}
	var made []string
	if spec.Sample != "" {
		n, err := copyAudio(spec.Sample, "vl")
		if err != nil {
			return nil, fmt.Errorf("音色库样本丢失：%s", err.Error())
		}
		made = append(made, n)
		spec.Sample = n
	}
	spec.Title = l.Name
	if spec.Desc == "" {
		spec.Desc = l.Description
	}
	if spec.Speed <= 0 {
		spec.Speed = 1
	}
	out := map[string]any{"ok": true, "library_id": l.ID, "name": l.Name, "voice": spec}
	if as != nil {
		newRefs := []VoiceRef{}
		for _, r := range l.Refs {
			nr := r
			if r.Sample == l.Voice.Sample && spec.Sample != "" {
				nr.Sample = spec.Sample
			} else {
				n, err := copyAudio(r.Sample, "vl")
				if err != nil {
					continue
				}
				made = append(made, n)
				nr.Sample = n
			}
			newRefs = append(newRefs, nr)
		}
		refsOut := as.VoiceRefs
		if len(newRefs) > 0 {
			refsOut = newRefs
			if len(as.VoiceRefs) > 0 {
				warn = append(warn, fmt.Sprintf("角色原来的 %d 段原声参考换成了音色库里这条的 %d 段", len(as.VoiceRefs), len(newRefs)))
			}
			if _, err := db.Exec(`UPDATE assets SET voice=?, voice_refs=? WHERE id=?`, mustJSON(spec), mustJSON(refsOut), as.ID); err != nil {
				for _, f := range made {
					removeAudio(f)
				}
				return nil, err
			}
		} else if _, err := db.Exec(`UPDATE assets SET voice=? WHERE id=?`, mustJSON(spec), as.ID); err != nil {
			for _, f := range made {
				removeAudio(f)
			}
			return nil, err
		}
		touchProject(as.ProjectID)
		out["asset_id"], out["asset_name"], out["voice_refs"] = as.ID, as.Name, refsOut
		out["next"] = "已设为角色音色。用 tts（asset_id）念一句试听；已生成的配音要重做用 generate_dub（force=true）。"
	} else {
		if _, err := db.Exec(`UPDATE projects SET narrator_voice=? WHERE id=?`, mustJSON(spec), p.ID); err != nil {
			for _, f := range made {
				removeAudio(f)
			}
			return nil, err
		}
		if len(l.Refs) > 0 {
			warn = append(warn, "旁白不存原声参考，只用了音色")
		}
		touchProject(p.ID)
		out["project_id"] = p.ID
		out["next"] = "已设为旁白音色。"
	}
	_, _ = db.Exec(`UPDATE voice_library SET updated_at=? WHERE id=?`, now(), l.ID)
	out["warnings"] = warn
	return out, nil
}

// ---------- REST ----------

func handleLibList(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	writeJSON(w, map[string]any{"items": listLibVoices(q.Get("q"), q.Get("fav") == "1")})
}

func handleLibSave(w http.ResponseWriter, r *http.Request) {
	var in libSaveIn
	if err := readJSON(r, &in); err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	l, exists, err := saveLibVoice(ctx, in, nil)
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	writeJSON(w, map[string]any{"item": l, "exists": exists})
}

func handleLibUpdate(w http.ResponseWriter, r *http.Request) {
	var in libUpdateIn
	if err := readJSON(r, &in); err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	l, err := updateLibVoice(pathID(r), in)
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	writeJSON(w, map[string]any{"item": l})
}

func handleLibDelete(w http.ResponseWriter, r *http.Request) {
	if err := deleteLibVoice(pathID(r)); err != nil {
		httpErr(w, 404, err.Error())
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

func handleLibApply(w http.ResponseWriter, r *http.Request) {
	var in struct {
		AssetID   int64 `json:"asset_id"`
		ProjectID int64 `json:"project_id"`
	}
	if err := readJSON(r, &in); err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	out, err := applyLibVoice(pathID(r), in.AssetID, in.ProjectID)
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	writeJSON(w, out)
}

// ---------- MCP ----------

var libTools = []mcpTool{
	{"list_voice_library", "列出音色库（跨作品收藏的音色）：编号、名称、描述、标签、性别 / 年龄、来源（design / clone / fish / builtin / openai）、渠道、是否有本地样本和原声参考、收藏星标、来自哪个作品 / 角色。用 listen_voice（library_id）听，apply_library_voice 用到角色或旁白上。", obj(map[string]any{
		"query": str("按名称、描述、标签、性别、年龄、来源搜索（空格分隔多个词，都要匹配）"), "favorites_only": boolp("只看加了星标的"),
	})},
	{"save_voice_to_library", "把一个音色收进音色库（跨作品复用）。来源四选一：asset_id = 角色当前音色（连同 clone_voice 存下的原声参考）；sample + channel_id = design_voice 的候选或克隆样本；project_id（不填 asset_id）= 旁白音色；fish_voice_id = Fish 音色编号。样本文件会复制一份存进音色库，删作品不受影响。同一段样本已经收藏过时不重复收，返回已有的那条（exists=true）。", obj(map[string]any{
		"asset_id": num("角色资产编号"), "project_id": num("作品编号（收旁白音色时只填这个）"), "sample": str("design_voice 候选样本名 / 克隆样本名"),
		"channel_id": num("sample 所在的语音渠道编号（design_voice 返回的 channel_id）；fish_voice_id 时为 Fish 渠道"), "synth_channel_id": num("配音用哪个渠道（可选，设计用 MiMo、配音交给 Fish 时填 Fish 渠道）"),
		"fish_voice_id": str("Fish 音色编号（reference_id）"), "voice": map[string]any{"type": "object", "description": "直接给音色对象：channel_id、mode、voice、sample、speed、pitch"},
		"name": str("名称（不填用「角色名的声音」）"), "description": str("描述（不填用音色已有的描述）"),
		"tags": arr("标签，如 少年、清冷、反派", map[string]any{"type": "string"}), "gender": str("性别，如 男 / 女"), "age": str("年龄感，如 少年、青年、中年"),
		"favorite": boolp("加星标"),
	})},
	{"apply_library_voice", "把音色库里的一个音色用到任意作品的角色（asset_id）或旁白（project_id）上：复制样本给这个角色，设好音色；音色库里存了原声参考时也替换角色的 voice_refs。之后配音都用它；已生成的配音要重做用 generate_dub（force=true）。", obj(map[string]any{
		"library_id": num("音色库编号（list_voice_library）"), "asset_id": num("角色资产编号"), "project_id": num("作品编号（设旁白时填，且不填 asset_id）"),
	}, "library_id")},
	{"delete_library_voice", "从音色库删除一个音色及音色库里的样本文件。已经用到角色上的音色不受影响（那是另一份副本）。不可恢复：必须传 confirm=true；不传时只返回会删掉什么。", obj(map[string]any{
		"library_id": num("音色库编号"), "confirm": boolp("确认删除"),
	}, "library_id")},
}

func libBrief(l *LibVoice, base string) map[string]any {
	l.setURLs(base + "/v1/audio/")
	m := map[string]any{"library_id": l.ID, "name": l.Name, "source": l.Source, "mode": l.Mode, "favorite": l.Favorite,
		"channel_id": l.ChannelID, "synth_channel_id": l.SynthChannelID, "has_sample": l.Sample != "", "voice_refs": len(l.Refs)}
	for k, v := range map[string]string{"description": l.Description, "gender": l.Gender, "age": l.Age, "origin": l.Origin, "voice_id": l.VoiceID, "fish_model_id": l.FishModelID, "sample_url": l.SampleURL, "preview_url": l.PreviewURL} {
		if v != "" {
			m[k] = v
		}
	}
	if len(l.Tags) > 0 {
		m["tags"] = l.Tags
	}
	if l.Voice.Speed > 0 && l.Voice.Speed != 1 {
		m["speed"] = l.Voice.Speed
	}
	if l.Voice.Pitch != 0 {
		m["pitch"] = l.Voice.Pitch
	}
	return m
}

func callLibTool(ctx context.Context, name string, raw json.RawMessage, base string, audioOK bool) (any, error) {
	switch name {
	case "list_voice_library":
		var a struct {
			Query         string `json:"query"`
			FavoritesOnly bool   `json:"favorites_only"`
		}
		if err := strictArgs(raw, &a); err != nil {
			return nil, err
		}
		items := []map[string]any{}
		for _, l := range listLibVoices(a.Query, a.FavoritesOnly) {
			items = append(items, libBrief(l, base))
		}
		return map[string]any{"total": len(items), "items": items}, nil
	case "save_voice_to_library":
		var a libSaveIn
		if err := strictArgs(raw, &a); err != nil {
			return nil, err
		}
		c, cancel := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
		l, exists, err := saveLibVoice(c, a, nil)
		if err != nil {
			return nil, err
		}
		out := map[string]any{"ok": true, "exists": exists, "item": libBrief(l, base)}
		if exists {
			out["next"] = "这个音色已经在音色库里了（没有重复收藏）。要改名 / 加标签请在界面的音色库页面改。"
		} else {
			out["next"] = "已收进音色库。listen_voice（library_id）可以听，apply_library_voice 用到其他作品的角色或旁白上。"
		}
		return out, nil
	case "apply_library_voice":
		var a struct {
			LibraryID int64 `json:"library_id"`
			AssetID   int64 `json:"asset_id"`
			ProjectID int64 `json:"project_id"`
		}
		if err := strictArgs(raw, &a); err != nil {
			return nil, err
		}
		return applyLibVoice(a.LibraryID, a.AssetID, a.ProjectID)
	case "delete_library_voice":
		var a struct {
			LibraryID int64 `json:"library_id"`
			Confirm   bool  `json:"confirm"`
		}
		if err := strictArgs(raw, &a); err != nil {
			return nil, err
		}
		l, err := getLibVoice(a.LibraryID)
		if err != nil {
			return nil, err
		}
		if !a.Confirm {
			return map[string]any{"ok": false, "would_delete": libBrief(l, base), "next": "确认删除请再调用并传 confirm=true。已经用到角色上的音色不受影响。"}, nil
		}
		if err := deleteLibVoice(l.ID); err != nil {
			return nil, err
		}
		return map[string]any{"ok": true, "deleted": l.ID, "name": l.Name}, nil
	}
	return nil, errUnknownTool
}
