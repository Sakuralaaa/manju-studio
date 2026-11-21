package main

// 生成后统一音色（0.2.11）：视频（FL2VA，H3 原生出声）出来后，可选地把人声转成说话角色的「音色母带」。
// 流程跑在视频运行时（同一台 Colab GPU）上：demucs 分离人声 / 环境声 → Seed-VC 转人声 → 和环境声混回 → 存为这条视频的转换版。
// 原版一直保留，转换版单独存（videos.vc_file）；videos.vc_use 决定成片 / 下载 / 剪映草稿用哪一版。
// 开关：作品级（projects.voice_unify，默认关）+ 镜头级覆盖（shots.voice_unify = '' 跟随 / on / off）；
// 也可以对已有的视频手动「重做统一音色」（界面按钮 / MCP convert_take_voice / REST）。
// 母带：角色可指定（assets.vc_master）；不指定时用最长的原声参考（voice_refs），没有就用音色样本（MiMo / Fish 克隆样本）。
// 依赖第一次用到时才装（后台，视频渲染期间就开始装），权重打包缓存到云端硬盘（和视频权重同一个文件夹）。

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha1"
	_ "embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

//go:embed colab/vc.py
var vcPy string

// ---------- 参数（设置 → 算力 → 统一音色） ----------

type VCCfg struct {
	DiffusionSteps int     `json:"diffusion_steps"` // Seed-VC 扩散步数，实测 30 够用
	CfgRate        float64 `json:"cfg_rate"`        // inference-cfg-rate
	F0             bool    `json:"f0"`              // 带音高条件（44k 歌声模型，更慢；说话一般不用）
	AutoF0         bool    `json:"auto_f0"`         // f0 开时自动对齐音高
	SemiTone       int     `json:"semi_tone"`       // f0 开时整体升降半音
	LengthAdjust   float64 `json:"length_adjust"`   // 语速（1 = 不变，保持口型对齐）
	SeparateMaster bool    `json:"separate_master"` // 母带先用 demucs 去掉配乐 / 环境声
	MaxMasterSec   float64 `json:"max_master_sec"`  // 母带最长秒数（Seed-VC 参考音频本来就只用前 25 秒左右）
	VocalGain      float64 `json:"vocal_gain"`      // 转换后人声的音量倍数
	AutoUse        bool    `json:"auto_use"`        // 转换完成后自动选用转换版
	Repo           string  `json:"repo"`            // Seed-VC 仓库
}

// VCOverride 按角色或单次转换覆盖全局变声参数（只覆盖非 nil 字段）
type VCOverride struct {
	DiffusionSteps *int     `json:"diffusion_steps,omitempty"`
	CfgRate        *float64 `json:"cfg_rate,omitempty"`
	F0             *bool    `json:"f0,omitempty"`
	AutoF0         *bool    `json:"auto_f0,omitempty"`
	SemiTone       *int     `json:"semi_tone,omitempty"`
}

func (o VCOverride) empty() bool {
	return o.DiffusionSteps == nil && o.CfgRate == nil && o.F0 == nil && o.AutoF0 == nil && o.SemiTone == nil
}

func (c VCCfg) applyOverride(o VCOverride) VCCfg {
	if o.DiffusionSteps != nil {
		c.DiffusionSteps = *o.DiffusionSteps
	}
	if o.CfgRate != nil {
		c.CfgRate = *o.CfgRate
	}
	if o.F0 != nil {
		c.F0 = *o.F0
	}
	if o.AutoF0 != nil {
		c.AutoF0 = *o.AutoF0
	}
	if o.SemiTone != nil {
		c.SemiTone = *o.SemiTone
	}
	return c.norm()
}

func defaultVCCfg() VCCfg {
	return VCCfg{DiffusionSteps: 30, CfgRate: 0.7, F0: false, AutoF0: true, LengthAdjust: 1.0, SeparateMaster: true, MaxMasterSec: 25, VocalGain: 1.0, AutoUse: true, Repo: "https://github.com/Plachtaa/seed-vc"}
}

func (c VCCfg) norm() VCCfg {
	d := defaultVCCfg()
	c.DiffusionSteps = clampInt(c.DiffusionSteps, 4, 100)
	c.CfgRate = clampF(c.CfgRate, 0, 1)
	c.SemiTone = clampInt(c.SemiTone, -12, 12)
	if c.LengthAdjust <= 0 {
		c.LengthAdjust = d.LengthAdjust
	}
	c.LengthAdjust = clampF(c.LengthAdjust, 0.5, 2)
	if c.MaxMasterSec <= 0 {
		c.MaxMasterSec = d.MaxMasterSec
	}
	c.MaxMasterSec = clampF(c.MaxMasterSec, 3, 60)
	if c.VocalGain <= 0 {
		c.VocalGain = d.VocalGain
	}
	c.VocalGain = clampF(c.VocalGain, 0.1, 4)
	if strings.TrimSpace(c.Repo) == "" || !strings.HasPrefix(c.Repo, "https://") {
		c.Repo = d.Repo
	}
	return c
}

func loadVCCfg() VCCfg {
	c := defaultVCCfg()
	_ = json.Unmarshal([]byte(getSetting("vc_cfg", "{}")), &c)
	return c.norm()
}

func handleVCSettingsGet(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{"cfg": loadVCCfg(), "defaults": defaultVCCfg()})
}

func handleVCSettingsPut(w http.ResponseWriter, r *http.Request) {
	c := loadVCCfg()
	if err := readJSON(r, &c); err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	c = c.norm()
	_ = setSetting("vc_cfg", mustJSON(c))
	writeJSON(w, map[string]any{"cfg": c, "defaults": defaultVCCfg()})
}

// ---------- 开关 ----------

// voiceUnifyFor 这个镜头生成后要不要统一音色（镜头覆盖优先，否则跟随作品）
func voiceUnifyFor(p *Project, s *Shot) bool {
	switch s.VoiceUnify {
	case "on":
		return true
	case "off":
		return false
	}
	return p != nil && p.VoiceUnify
}

func shotWantsVC(shotID int64) bool {
	s, err := getShot(shotID)
	if err != nil {
		return false
	}
	p, err := getProject(s.ProjectID)
	if err != nil {
		return false
	}
	return voiceUnifyFor(p, s)
}

func setShotVoiceUnify(s *Shot, mode string) error {
	switch mode {
	case "", "inherit":
		mode = ""
	case "on", "off":
	default:
		return errors.New("mode 只能是 inherit（跟随作品）/ on / off")
	}
	_, err := db.Exec(`UPDATE shots SET voice_unify=? WHERE id=?`, mode, s.ID)
	touchEpisode(s.EpisodeID)
	return err
}

func handleShotVoiceUnify(w http.ResponseWriter, r *http.Request) {
	s, err := getShot(pathID(r))
	if err != nil {
		httpErr(w, 404, "镜头不存在")
		return
	}
	var in struct {
		Mode string `json:"mode"`
	}
	_ = readJSON(r, &in)
	if err := setShotVoiceUnify(s, in.Mode); err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

// ---------- 母带 ----------

type vcMasterCand struct {
	Sample   string  `json:"sample"`
	Label    string  `json:"label"`
	Source   string  `json:"source"` // ref / voice
	Duration float64 `json:"duration"`
	URL      string  `json:"url"`
}

func audioExists(name string) bool {
	if name == "" || strings.ContainsAny(name, `/\`) {
		return false
	}
	fi, err := os.Stat(filepath.Join(dataDir, "audio", name))
	return err == nil && !fi.IsDir() && fi.Size() > 0
}

var audioDurCache sync.Map // 样本名|修改时间 → 秒

func audioDur(name string) float64 {
	path := filepath.Join(dataDir, "audio", name)
	fi, err := os.Stat(path)
	if err != nil {
		return 0
	}
	key := fmt.Sprintf("%s|%d", name, fi.ModTime().UnixNano())
	if d, ok := audioDurCache.Load(key); ok {
		return d.(float64)
	}
	ci, err := probe(path)
	if err != nil {
		return 0
	}
	audioDurCache.Store(key, ci.dur)
	return ci.dur
}

// vcMasterCands 角色可做母带的样本：原声参考（从长到短）+ 当前音色样本
func vcMasterCands(a *Asset) []vcMasterCand {
	out := []vcMasterCand{}
	seen := map[string]bool{}
	refs := append([]VoiceRef{}, a.VoiceRefs...)
	sort.SliceStable(refs, func(i, j int) bool { return refs[i].Duration > refs[j].Duration })
	for _, r := range refs {
		if seen[r.Sample] || !audioExists(r.Sample) {
			continue
		}
		seen[r.Sample] = true
		d := r.Duration
		if d <= 0 {
			d = audioDur(r.Sample)
		}
		label := "原声参考"
		switch r.Source {
		case "shot":
			label = "原声参考 · 镜头原声"
		case "upload":
			label = "原声参考 · 上传"
		case "url":
			label = "原声参考 · 链接"
		}
		out = append(out, vcMasterCand{Sample: r.Sample, Label: label, Source: "ref", Duration: d, URL: "/files/audio/" + r.Sample})
	}
	if s := a.Voice.Sample; s != "" && !seen[s] && audioExists(s) {
		label := "音色样本"
		switch a.Voice.Mode {
		case "clone":
			label = "音色样本 · 克隆"
		case "design":
			label = "音色样本 · 设计"
		}
		out = append(out, vcMasterCand{Sample: s, Label: label, Source: "voice", Duration: audioDur(s), URL: "/files/audio/" + s})
	}
	return out
}

// vcMasterFor 选母带：指定的 > 最长的原声参考 > 音色样本；本角色都没有时用「另一时期」本体的
func vcMasterFor(a *Asset) (cand *vcMasterCand, how string) {
	cands := vcMasterCands(a)
	if a.VcMaster != "" {
		for i := range cands {
			if cands[i].Sample == a.VcMaster {
				return &cands[i], "指定"
			}
		}
	}
	for i := range cands {
		if cands[i].Source == "ref" {
			return &cands[i], "最长的原声参考"
		}
	}
	if len(cands) > 0 {
		return &cands[0], "音色样本"
	}
	if a.BaseID > 0 && a.BaseID != a.ID {
		if b, err := getAsset(a.BaseID); err == nil && b.BaseID != a.ID {
			if c, h := vcMasterFor(b); c != nil {
				return c, "本体「" + b.Name + "」的" + h
			}
		}
	}
	return nil, ""
}

func masterView(a *Asset) map[string]any {
	c, how := vcMasterFor(a)
	m := map[string]any{"asset_id": a.ID, "name": a.Name, "chosen": a.VcMaster, "candidates": vcMasterCands(a)}
	if c != nil {
		m["current"] = c
		m["how"] = how
	}
	if !a.VcOverride.empty() {
		m["vc_override"] = a.VcOverride
	}
	return m
}

func setVCMaster(a *Asset, sample string) error {
	sample = strings.TrimSpace(sample)
	if sample == "auto" {
		sample = ""
	}
	if sample != "" {
		ok := false
		for _, c := range vcMasterCands(a) {
			if c.Sample == sample {
				ok = true
			}
		}
		if !ok {
			return errors.New("这个样本不是该角色的原声参考或音色样本")
		}
	}
	_, err := db.Exec(`UPDATE assets SET vc_master=? WHERE id=?`, sample, a.ID)
	return err
}

func handleAssetVCMasterGet(w http.ResponseWriter, r *http.Request) {
	a, err := getAsset(pathID(r))
	if err != nil {
		httpErr(w, 404, "资产不存在")
		return
	}
	writeJSON(w, masterView(a))
}

func handleAssetVCMasterPut(w http.ResponseWriter, r *http.Request) {
	a, err := getAsset(pathID(r))
	if err != nil {
		httpErr(w, 404, "资产不存在")
		return
	}
	var in struct {
		Sample string `json:"sample"`
	}
	_ = readJSON(r, &in)
	if err := setVCMaster(a, in.Sample); err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	a, _ = getAsset(a.ID)
	writeJSON(w, masterView(a))
}

// ---------- 说话角色 ----------

// vcSpeaker 这个镜头的说话角色。asset 不为 0 时直接用它（手动指定）。
// 没有台词、只有旁白、多人对白（无法只换一个人的声音）、角色没母带时返回跳过原因。
func vcSpeaker(s *Shot, assetID int64) (*Asset, *vcMasterCand, string, string) {
	var a *Asset
	if assetID > 0 {
		x, err := getAsset(assetID)
		if err != nil || x.ProjectID != s.ProjectID || x.Kind != "character" {
			return nil, nil, "", "指定的角色不存在或不属于这个作品"
		}
		a = x
	} else {
		lines := parseDialogue(s.Dialogue)
		if len(lines) == 0 {
			return nil, nil, "", "这个镜头没有台词"
		}
		assets := listAssets(s.ProjectID)
		chars := map[int64]int{}
		var order []int64
		unmatched := []string{}
		for _, pl := range lines {
			if pl.kind != "dialogue" {
				continue
			}
			m := matchSpeaker(assets, pl.speaker)
			if m == nil {
				if !contains(unmatched, pl.speaker) {
					unmatched = append(unmatched, pl.speaker)
				}
				continue
			}
			if _, ok := chars[m.ID]; !ok {
				order = append(order, m.ID)
			}
			chars[m.ID] += len([]rune(pl.text))
		}
		switch {
		case len(chars) == 0 && len(unmatched) == 0:
			return nil, nil, "", "这个镜头只有旁白，没有角色台词"
		case len(chars) == 0:
			return nil, nil, "", "说话人「" + strings.Join(unmatched, "、") + "」不是资产里的角色"
		case len(chars)+len(unmatched) > 1:
			return nil, nil, "", "多人对白：统一音色会把所有人声都换成一个人的，已跳过（可手动指定角色重做）"
		}
		for _, x := range assets {
			if x.ID == order[0] {
				a = x
			}
		}
	}
	if a == nil {
		return nil, nil, "", "找不到说话角色"
	}
	c, how := vcMasterFor(a)
	if c == nil {
		return a, nil, "", "角色「" + a.Name + "」还没有音色母带（原声参考或克隆 / 设计的音色样本）"
	}
	return a, c, how, ""
}

// vcPreview 镜头的统一音色状态（给界面 / MCP 显示原因）
func vcPreview(p *Project, s *Shot) map[string]any {
	on := voiceUnifyFor(p, s)
	m := map[string]any{"enabled": on, "mode": map[string]string{"": "inherit", "on": "on", "off": "off"}[s.VoiceUnify]}
	a, c, how, skip := vcSpeaker(s, 0)
	if a != nil {
		m["asset_id"], m["asset_name"] = a.ID, a.Name
	}
	if c != nil {
		m["master"], m["master_how"], m["master_sec"] = c.Sample, how, c.Duration
	}
	if skip != "" {
		m["skip"] = skip
	}
	return m
}

// ---------- 排队 / 选版本 ----------

var vcStatusNames = map[string]string{"": "没做", "auto": "等视频出来后自动做", "queued": "排队中", "running": "转换中", "done": "已完成", "failed": "失败", "skipped": "已跳过"}

func vcStatusText(v *Video) string {
	t := vcStatusNames[v.VcStatus]
	if v.VcError != "" && (v.VcStatus == "failed" || v.VcStatus == "skipped") {
		t += "：" + v.VcError
	}
	return t
}

func vcView(v *Video, base string) map[string]any {
	m := map[string]any{"status": v.VcStatus, "status_text": vcStatusText(v), "use": map[bool]string{true: "converted", false: "original"}[v.VcUse && v.VcFile != ""]}
	if v.VcError != "" {
		m["reason"] = v.VcError
	}
	if v.VcAssetID > 0 {
		m["asset_id"] = v.VcAssetID
	}
	if v.VcFile != "" {
		m["converted_url"] = fmt.Sprintf("%s/v1/videos/%d/file?version=converted", base, v.ID)
		m["original_url"] = fmt.Sprintf("%s/v1/videos/%d/file?version=original", base, v.ID)
	}
	if len(v.VcInfo) > 0 {
		m["info"] = v.VcInfo
	}
	return m
}

// queueVC 给一条已完成的视频排队统一音色（手动重做也走这里）。assetID=0 时自动找说话角色。
func queueVC(v *Video, assetID int64, ov ...VCOverride) error {
	if v.Status != "done" || v.File == "" {
		return errors.New("视频还没有生成好")
	}
	if v.VcStatus == "queued" || v.VcStatus == "running" {
		return errors.New("这条视频已经在统一音色队列里")
	}
	if v.ShotID == 0 {
		return errors.New("这条视频不属于任何镜头，没法确定说话角色")
	}
	s, err := getShot(v.ShotID)
	if err != nil {
		return errors.New("镜头不存在")
	}
	a, _, _, skip := vcSpeaker(s, assetID)
	if skip != "" {
		return errors.New(skip)
	}
	var o VCOverride
	if len(ov) > 0 {
		o = ov[0]
	}
	_, err = db.Exec(`UPDATE videos SET vc_status='queued', vc_error='', vc_asset_id=?, vc_override=? WHERE id=?`, a.ID, mustJSON(o), v.ID)
	runner.poke()
	touchEpisode(v.EpisodeID)
	return err
}

func setVideoVersion(v *Video, use string) error {
	switch use {
	case "converted":
		if v.VcFile == "" {
			return errors.New("这条视频还没有统一音色后的版本")
		}
		_, err := db.Exec(`UPDATE videos SET vc_use=1 WHERE id=?`, v.ID)
		touchEpisode(v.EpisodeID)
		return err
	case "original":
		_, err := db.Exec(`UPDATE videos SET vc_use=0 WHERE id=?`, v.ID)
		touchEpisode(v.EpisodeID)
		return err
	}
	return errors.New("version 只能是 original / converted")
}

func handleVideoVoiceConvert(w http.ResponseWriter, r *http.Request) {
	v, err := getVideo(pathID(r))
	if err != nil {
		httpErr(w, 404, "视频不存在")
		return
	}
	var in struct {
		AssetID int64 `json:"asset_id"`
		Start   bool  `json:"start"`
		VCOverride
	}
	_ = readJSON(r, &in)
	if err := queueVC(v, in.AssetID, in.VCOverride); err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	out := map[string]any{"ok": true}
	if in.Start && runner.brief().Phase == "off" {
		if err := startRuntime("", "", "video"); err != nil {
			out["start_error"] = err.Error()
		}
	}
	writeJSON(w, out)
}

func handleVideoVersion(w http.ResponseWriter, r *http.Request) {
	v, err := getVideo(pathID(r))
	if err != nil {
		httpErr(w, 404, "视频不存在")
		return
	}
	var in struct {
		Use string `json:"use"`
	}
	_ = readJSON(r, &in)
	if err := setVideoVersion(v, in.Use); err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

func vcPendingCount() int {
	var n int
	_ = db.QueryRow(`SELECT COUNT(*) FROM videos WHERE status='done' AND vc_status IN ('auto','queued')`).Scan(&n)
	return n
}

// ---------- 在运行时上执行 ----------

var vcRT struct {
	sync.Mutex
	runtime      string // 下面的状态属于哪台运行时（换机器就重置）
	setupKicked  bool
	driveChecked time.Time
	driveWaiting bool
}

func (r *Runner) vcReset() {
	r.mu.Lock()
	rt := r.Runtime
	r.mu.Unlock()
	vcRT.Lock()
	if vcRT.runtime != rt {
		vcRT.runtime, vcRT.setupKicked, vcRT.driveWaiting = rt, false, false
	}
	vcRT.Unlock()
}

func vcCode(cfg map[string]any) string {
	return strings.Replace(vcPy, "__CFG__", base64.StdEncoding.EncodeToString([]byte(mustJSON(cfg))), 1)
}

// vcDriveCfg 云端硬盘缓存：文件夹、令牌和已缓存的权重包编号
func vcDriveCfg(ctx context.Context, jup *Jupyter, vc VCCfg) map[string]any {
	if !loadColabCfg().DriveCache {
		return map[string]any{"enabled": false}
	}
	d, err := driveBootCfg(ctx, jup)
	if err != nil {
		return map[string]any{"enabled": false}
	}
	name := "mj_vc_weights.tar"
	if vc.F0 {
		name = "mj_vc_weights_f0.tar"
	}
	out := map[string]any{"enabled": true, "folder_id": d["folder_id"], "token_file": d["token_file"], "weights_name": name}
	if f := loadDriveState().Files[name]; f.ID != "" {
		out["weights_id"] = f.ID
	}
	return out
}

// vcDriveSync 把运行时后台上传完成的权重包编号记进云端硬盘缓存状态
func (r *Runner) vcDriveSync(ctx context.Context, jup *Jupyter) {
	res, err := jup.Exec(ctx, vcCode(map[string]any{"mode": "drive_status"}), nil)
	if err != nil || res == nil {
		return
	}
	i := strings.Index(res.Stdout, "[mj] DRIVE ")
	if i < 0 {
		return
	}
	line := strings.SplitN(res.Stdout[i+len("[mj] DRIVE "):], "\n", 2)[0]
	var st struct {
		Files map[string]driveFile `json:"files"`
	}
	if json.Unmarshal([]byte(line), &st) != nil || len(st.Files) == 0 {
		return
	}
	s := loadDriveState()
	if s.Files == nil {
		s.Files = map[string]driveFile{}
	}
	n := 0
	for k, v := range st.Files {
		if v.ID != "" && s.Files[k].ID != v.ID {
			s.Files[k] = v
			n++
		}
	}
	if n > 0 {
		saveDriveState(s)
		r.logf("云端硬盘缓存新增变声权重包")
	}
	vcRT.Lock()
	vcRT.driveWaiting = false
	vcRT.Unlock()
}

// runVoiceNext 调度循环里每次先看有没有要统一音色的视频（运行时已就绪、视频环境已装好时）。
// 做了一件事返回 true（这一轮不再排视频）。
func (r *Runner) runVoiceNext(cfg ColabCfg) bool {
	r.vcReset()
	r.mu.Lock()
	jup := r.jup
	r.mu.Unlock()
	if jup == nil {
		return false
	}
	// 有要统一音色的视频还在排队 / 生成时，提前在后台装 Seed-VC / demucs
	vcRT.Lock()
	kicked := vcRT.setupKicked
	vcRT.Unlock()
	if !kicked {
		var want int
		_ = db.QueryRow(`SELECT COUNT(*) FROM videos WHERE vc_status IN ('auto','queued') AND status IN ('queued','running','done')`).Scan(&want)
		if want > 0 {
			vcRT.Lock()
			vcRT.setupKicked = true
			vcRT.Unlock()
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			vc := loadVCCfg()
			res, err := jup.Exec(ctx, vcCode(map[string]any{"mode": "setup_bg", "repo": vc.Repo}), nil)
			cancel()
			if err == nil && res != nil && res.Error == "" {
				if strings.Contains(res.Stdout, "SETUP started") {
					r.logf("后台安装统一音色组件（Seed-VC / demucs）")
				}
			} else {
				vcRT.Lock()
				vcRT.setupKicked = false
				vcRT.Unlock()
			}
		}
	}
	// 后台上传的权重包：隔一阵问一次传完没有
	vcRT.Lock()
	dw := vcRT.driveWaiting && time.Since(vcRT.driveChecked) > time.Minute
	if dw {
		vcRT.driveChecked = time.Now()
	}
	vcRT.Unlock()
	if dw {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		r.vcDriveSync(ctx, jup)
		cancel()
	}
	for i := 0; i < 50; i++ {
		var id int64
		if err := db.QueryRow(`SELECT id FROM videos WHERE status='done' AND vc_status IN ('auto','queued') ORDER BY finished_at, id LIMIT 1`).Scan(&id); err != nil {
			return false
		}
		v, err := getVideo(id)
		if err != nil {
			return false
		}
		s, err := getShot(v.ShotID)
		if err != nil {
			_, _ = db.Exec(`UPDATE videos SET vc_status='skipped', vc_error='镜头不存在' WHERE id=?`, id)
			continue
		}
		a, master, how, skip := vcSpeaker(s, v.VcAssetID)
		if skip != "" {
			_, _ = db.Exec(`UPDATE videos SET vc_status='skipped', vc_error=? WHERE id=?`, skip, id)
			r.logf("视频 #%d 不做统一音色：%s", id, skip)
			touchEpisode(v.EpisodeID)
			continue
		}
		r.runVC(cfg, jup, v, s, a, master, how)
		return true
	}
	return false
}

func (r *Runner) runVC(cfg ColabCfg, jup *Jupyter, v *Video, s *Shot, a *Asset, master *vcMasterCand, how string) {
	vc := loadVCCfg().applyOverride(a.VcOverride).applyOverride(v.VcOverride)
	r.mu.Lock()
	r.Phase, r.Stage = "running", "统一音色"
	r.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(cfg.JobTimeout+30)*time.Minute)
	defer func() {
		cancel()
		r.mu.Lock()
		if r.Phase == "running" {
			r.Phase = "ready"
		}
		r.Stage = ""
		r.LastActive = now()
		r.mu.Unlock()
		touchEpisode(v.EpisodeID)
		r.poke()
	}()
	_, _ = db.Exec(`UPDATE videos SET vc_status='running', vc_error='', vc_asset_id=? WHERE id=?`, a.ID, v.ID)
	r.logf("视频 #%d 统一音色 → %s（母带：%s）", v.ID, a.Name, how)
	start := time.Now()
	out, info, err := r.renderVC(ctx, jup, v, master, vc)
	if err != nil {
		r.mu.Lock()
		stopping := r.Phase == "stopping" || r.Phase == "off"
		r.mu.Unlock()
		if stopping {
			_, _ = db.Exec(`UPDATE videos SET vc_status='queued' WHERE id=?`, v.ID)
			r.logf("视频 #%d 统一音色退回队列", v.ID)
			return
		}
		msg := err.Error()
		if len([]rune(msg)) > 600 {
			msg = string([]rune(msg)[len([]rune(msg))-600:])
		}
		_, _ = db.Exec(`UPDATE videos SET vc_status='failed', vc_error=? WHERE id=?`, msg, v.ID)
		r.logf("视频 #%d 统一音色失败：%s", v.ID, lastLines(msg, 3))
		if jerr := jup.Ping(context.Background()); jerr != nil {
			r.Stop("运行时失联")
		}
		return
	}
	cur, _ := getVideo(v.ID)
	if cur == nil {
		_ = os.Remove(filepath.Join(dataDir, "videos", out)) // 期间视频被删了
		return
	}
	if cur.VcFile != "" && cur.VcFile != out {
		_ = os.Remove(filepath.Join(dataDir, "videos", cur.VcFile))
	}
	info["asset_name"], info["master"], info["master_how"], info["master_sec"] = a.Name, master.Sample, how, master.Duration
	info["seconds_used"] = time.Since(start).Round(time.Second).Seconds()
	use := b2i(cur.VcUse)
	if vc.AutoUse {
		use = 1
	}
	_, _ = db.Exec(`UPDATE videos SET vc_status='done', vc_file=?, vc_error='', vc_info=?, vc_at=?, vc_use=? WHERE id=?`, out, mustJSON(info), now(), use, v.ID)
	r.logf("视频 #%d 统一音色完成，用时 %s", v.ID, time.Since(start).Round(time.Second))
}

func (r *Runner) renderVC(ctx context.Context, jup *Jupyter, v *Video, master *vcMasterCand, vc VCCfg) (string, map[string]any, error) {
	vid, err := os.ReadFile(filepath.Join(dataDir, "videos", v.File))
	if err != nil {
		return "", nil, errors.New("原版视频文件不存在")
	}
	mdata, err := os.ReadFile(filepath.Join(dataDir, "audio", master.Sample))
	if err != nil {
		return "", nil, errors.New("母带文件不存在")
	}
	h := sha1.Sum(mdata)
	tag := fmt.Sprintf("v%d_%s", v.ID, randHex(3))
	remoteVid := "/content/mj_vc/in/" + tag + filepath.Ext(v.File)
	remoteMaster := "/content/mj_vc/in/m_" + hex.EncodeToString(h[:6]) + filepath.Ext(master.Sample)
	if err := jup.Upload(ctx, remoteVid, vid); err != nil {
		return "", nil, err
	}
	if err := jup.Upload(ctx, remoteMaster, mdata); err != nil {
		return "", nil, err
	}
	params := map[string]any{"diffusion_steps": vc.DiffusionSteps, "cfg_rate": vc.CfgRate, "f0": vc.F0, "auto_f0": vc.AutoF0, "semi_tone": vc.SemiTone,
		"length_adjust": vc.LengthAdjust, "separate_master": vc.SeparateMaster, "max_master_sec": vc.MaxMasterSec, "vocal_gain": vc.VocalGain}
	dctx, dcancel := context.WithTimeout(ctx, time.Minute)
	drive := vcDriveCfg(dctx, jup, vc)
	dcancel()
	code := vcCode(map[string]any{"mode": "run", "repo": vc.Repo, "drive": drive, "job": map[string]any{"video": remoteVid, "master": remoteMaster, "tag": tag, "params": params}})
	var outPath string
	var buf strings.Builder
	handle := func(ln string) {
		ln = strings.TrimSpace(ln)
		if !strings.HasPrefix(ln, "[mj] ") {
			return
		}
		body := strings.TrimPrefix(ln, "[mj] ")
		switch {
		case strings.HasPrefix(body, "STAGE "):
			st := strings.TrimPrefix(body, "STAGE ")
			r.mu.Lock()
			r.Stage = "统一音色 · " + st
			r.mu.Unlock()
		case strings.HasPrefix(body, "OUT "):
			outPath = strings.TrimSpace(strings.TrimPrefix(body, "OUT "))
		case strings.HasPrefix(body, "INFO "), strings.HasPrefix(body, "WARN "):
			r.logf("统一音色：%s", body)
		}
	}
	res, err := jup.Exec(ctx, code, func(stream, text string) {
		r.touch()
		buf.WriteString(text)
		sc := bufio.NewScanner(strings.NewReader(buf.String()))
		complete := strings.HasSuffix(buf.String(), "\n")
		var lines []string
		for sc.Scan() {
			lines = append(lines, sc.Text())
		}
		buf.Reset()
		if !complete && len(lines) > 0 {
			buf.WriteString(lines[len(lines)-1])
			lines = lines[:len(lines)-1]
		}
		for _, ln := range lines {
			handle(ln)
		}
	})
	if err != nil {
		return "", nil, err
	}
	if res.Error != "" {
		return "", nil, errors.New(res.Error)
	}
	if outPath == "" {
		for _, ln := range strings.Split(res.Stdout, "\n") {
			handle(ln)
		}
	}
	if outPath == "" {
		return "", nil, errors.New("没有拿到转换后的文件")
	}
	if drive["enabled"] == true && drive["weights_id"] == nil {
		vcRT.Lock()
		vcRT.driveWaiting, vcRT.driveChecked = true, time.Now()
		vcRT.Unlock()
	}
	name := fmt.Sprintf("v%d-vc-%s.mp4", v.ID, randHex(4))
	var b bytes.Buffer
	if _, err := jup.Download(ctx, outPath, &b); err != nil {
		return "", nil, err
	}
	if b.Len() == 0 {
		return "", nil, errors.New("转换后的文件是空的")
	}
	if err := os.WriteFile(filepath.Join(dataDir, "videos", name), b.Bytes(), 0o644); err != nil {
		return "", nil, err
	}
	info := map[string]any{"params": params}
	return name, info, nil
}

// ---------- MCP ----------

var vcTools = []mcpTool{
	{"convert_take_voice", "生成后统一音色：把一条已生成的视频（take）的人声转成说话角色的音色母带（Colab 上 demucs 分离人声 → Seed-VC 转换 → 和环境声混回），原版保留，转换版另存；完成后默认选用转换版（pick_take_version 可切回原版）。take_id 不填 = 这个镜头当前选用的视频。asset_id 不填 = 自动用镜头台词里的说话角色。可选传 diffusion_steps / cfg_rate / f0 / auto_f0 / semi_tone 做单次覆盖。只排队，运行时开着视频环境（colab_start purpose=video）时执行；start_runtime=true 会在运行时没开时开机（消耗 Colab 算力单元，先征得用户同意）。", obj(map[string]any{
		"shot_id": num("镜头编号"), "take_id": num("视频编号（镜头的某条抽卡结果；不填 = 当前选用的那条）"), "asset_id": num("转成哪个角色的音色（不填 = 自动找说话角色）"),
		"diffusion_steps": num("单次覆盖扩散步数 4–100（可选）"), "cfg_rate": numF("单次覆盖 CFG 0–1（可选）"),
		"f0": boolp("单次覆盖是否启用 f0 音高条件（可选）"), "auto_f0": boolp("f0 开时自动对齐音高（可选）"), "semi_tone": num("f0 开时升降半音 -12 到 12（可选）"),
		"start_runtime": boolp("运行时没开时开机（purpose=video，会消耗算力单元）"),
	}, "shot_id")},
	{"pick_take_version", "选一条视频用原版还是统一音色后的版本：成片、剪映草稿和下载都用选中的那一版。", obj(map[string]any{
		"take_id": num("视频编号"), "version": enumS("original = 原版；converted = 统一音色后的版本", "original", "converted"),
	}, "take_id", "version")},
	{"set_voice_unify", "开关「生成后统一音色」：给 project_id 设作品默认（on / off，默认关）；给 shot_id 设单个镜头的覆盖（inherit = 跟随作品 / on / off）。只影响之后生成的视频；已有视频用 convert_take_voice 手动做。", obj(map[string]any{
		"project_id": num("作品编号"), "shot_id": num("镜头编号"), "mode": enumS("on / off；镜头还可以 inherit（跟随作品）", "on", "off", "inherit"),
	}, "mode")},
	{"set_voice_master", "查看或指定角色的音色母带（统一音色时 Seed-VC 的目标音色）。不给 sample 只返回候选（原声参考 voice_refs、音色样本）和当前用的那段；sample = 候选样本名则指定它；sample = auto 恢复自动（最长的原声参考，没有就用音色样本）。可用 listen_voice（asset_id）试听。", obj(map[string]any{
		"asset_id": num("角色资产编号"), "sample": str("样本名；auto = 自动"),
	}, "asset_id")},
	{"set_voice_conv_override", "为单个角色设置或清除变声（Seed-VC）参数覆盖：支持 diffusion_steps / cfg_rate / f0 / auto_f0 / semi_tone（未给的字段沿用全局设置；reset=true 清除全部覆盖恢复全局）。", obj(map[string]any{
		"asset_id": num("角色资产编号"), "diffusion_steps": num("扩散步数 4–100"), "cfg_rate": numF("CFG 0–1"),
		"f0": boolp("是否启用 f0 音高条件"), "auto_f0": boolp("f0 开时自动对齐音高"), "semi_tone": num("f0 开时升降半音 -12 到 12"),
		"reset": boolp("true = 清除该角色的覆盖参数，恢复全局设置"),
	}, "asset_id")},
}

func callVCTool(ctx context.Context, name string, raw json.RawMessage, base string) (any, error) {
	switch name {
	case "convert_take_voice":
		var a struct {
			ShotID       int64 `json:"shot_id"`
			TakeID       int64 `json:"take_id"`
			AssetID      int64 `json:"asset_id"`
			StartRuntime bool  `json:"start_runtime"`
			VCOverride
		}
		if err := strictArgs(raw, &a); err != nil {
			return nil, err
		}
		s, err := getShot(a.ShotID)
		if err != nil {
			return nil, errors.New("镜头不存在")
		}
		var v *Video
		if a.TakeID > 0 {
			x, err := getVideo(a.TakeID)
			if err != nil || x.ShotID != s.ID {
				return nil, errors.New("这条视频不存在或不属于这个镜头")
			}
			v = x
		} else {
			for _, x := range listShots(s.EpisodeID, true) {
				if x.ID == s.ID {
					v = shotVideoFor(x)
				}
			}
			if v == nil {
				return nil, errors.New("这个镜头还没有生成好的视频")
			}
		}
		if err := queueVC(v, a.AssetID, a.VCOverride); err != nil {
			return nil, err
		}
		v, _ = getVideo(v.ID)
		out := map[string]any{"ok": true, "take_id": v.ID, "voice_conversion": vcView(v, base)}
		st := runner.brief()
		switch {
		case st.Phase == "off" && a.StartRuntime:
			if err := startRuntime("", "", "video"); err != nil {
				out["start_error"] = err.Error()
			} else {
				out["next"] = "已开机（purpose=video），环境就绪后自动执行；用 list_shots / video_status 查看 take_voice。"
			}
		case st.Phase == "off":
			out["next"] = "已排队。运行时没开：开视频运行时（colab_start purpose=video，需用户同意）后自动执行。"
		case st.Purpose != "video":
			out["next"] = "已排队。当前运行时不是视频环境（purpose=" + st.Purpose + "），要在 purpose=video 的运行时上才会执行。"
		default:
			out["next"] = "已排队，运行时空出来就执行（每条约 1 分钟，第一次要先装 Seed-VC / demucs）。"
		}
		return out, nil
	case "pick_take_version":
		var a struct {
			TakeID  int64  `json:"take_id"`
			Version string `json:"version"`
		}
		if err := strictArgs(raw, &a); err != nil {
			return nil, err
		}
		v, err := getVideo(a.TakeID)
		if err != nil {
			return nil, errors.New("视频不存在")
		}
		if err := setVideoVersion(v, a.Version); err != nil {
			return nil, err
		}
		v, _ = getVideo(v.ID)
		return map[string]any{"ok": true, "take_id": v.ID, "voice_conversion": vcView(v, base)}, nil
	case "set_voice_unify":
		var a struct {
			ProjectID int64  `json:"project_id"`
			ShotID    int64  `json:"shot_id"`
			Mode      string `json:"mode"`
		}
		if err := strictArgs(raw, &a); err != nil {
			return nil, err
		}
		switch {
		case a.ShotID > 0:
			s, err := getShot(a.ShotID)
			if err != nil {
				return nil, errors.New("镜头不存在")
			}
			if err := setShotVoiceUnify(s, a.Mode); err != nil {
				return nil, err
			}
			s, _ = getShot(s.ID)
			p, _ := getProject(s.ProjectID)
			return map[string]any{"ok": true, "shot_id": s.ID, "voice_unify": vcPreview(p, s)}, nil
		case a.ProjectID > 0:
			if a.Mode != "on" && a.Mode != "off" {
				return nil, errors.New("作品只能设 on / off")
			}
			if _, err := getProject(a.ProjectID); err != nil {
				return nil, errors.New("作品不存在")
			}
			on := a.Mode == "on"
			if err := applyProject(a.ProjectID, projectIn{VoiceUnify: &on}); err != nil {
				return nil, err
			}
			return map[string]any{"ok": true, "project_id": a.ProjectID, "voice_unify": on}, nil
		}
		return nil, errors.New("需要 project_id 或 shot_id")
	case "set_voice_master":
		var a struct {
			AssetID int64   `json:"asset_id"`
			Sample  *string `json:"sample"`
		}
		if err := strictArgs(raw, &a); err != nil {
			return nil, err
		}
		as, err := getAsset(a.AssetID)
		if err != nil || as.Kind != "character" {
			return nil, errors.New("角色不存在")
		}
		if a.Sample != nil {
			if err := setVCMaster(as, *a.Sample); err != nil {
				return nil, err
			}
			as, _ = getAsset(as.ID)
		}
		m := masterView(as)
		if cs, _ := m["candidates"].([]vcMasterCand); len(cs) == 0 {
			m["next"] = "这个角色还没有可做母带的样本：用 clone_voice 从镜头原声（或上传音频）存一段原声参考，或 design_voice / set_voice 设一个带样本的音色，也可以从音色库 apply_library_voice。"
		}
		return m, nil
	case "set_voice_conv_override":
		var a struct {
			AssetID int64 `json:"asset_id"`
			Reset   bool  `json:"reset"`
			VCOverride
		}
		if err := strictArgs(raw, &a); err != nil {
			return nil, err
		}
		as, err := getAsset(a.AssetID)
		if err != nil || as.Kind != "character" {
			return nil, errors.New("角色不存在")
		}
		ov := as.VcOverride
		if a.Reset {
			ov = VCOverride{}
		} else {
			if a.DiffusionSteps != nil {
				v := clampInt(*a.DiffusionSteps, 4, 100)
				ov.DiffusionSteps = &v
			}
			if a.CfgRate != nil {
				v := clampF(*a.CfgRate, 0, 1)
				ov.CfgRate = &v
			}
			if a.F0 != nil {
				ov.F0 = a.F0
			}
			if a.AutoF0 != nil {
				ov.AutoF0 = a.AutoF0
			}
			if a.SemiTone != nil {
				v := clampInt(*a.SemiTone, -12, 12)
				ov.SemiTone = &v
			}
		}
		_, _ = db.Exec(`UPDATE assets SET vc_override=? WHERE id=?`, mustJSON(ov), as.ID)
		touchProject(as.ProjectID)
		return map[string]any{"ok": true, "asset_id": as.ID, "name": as.Name, "vc_override": ov, "effective": loadVCCfg().applyOverride(ov)}, nil
	}
	return nil, errUnknownTool
}
