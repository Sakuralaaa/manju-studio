package main

// 0.3.0 丝滑成片后期引擎与自动质检：
//   - 波形自动裁首尾静音（EDT-19）：消除 MiniMax H3 开头 1.3–2.4 秒静默僵硬起势与尾部拖沓；
//   - 同场景 RGB 亮度/白平衡自动匹配（EDT-20）：按 scene_group（默认自动按 S* 场景资产分段）拉齐中位曝光与色偏；
//   - 音频黏合层（EDT-23）：逐镜响度均衡至 -18 dBFS、切点平滑 crossfade（默认 0.12s）+ 真 J-Cut / L-Cut 声画错位；
//   - 成品自动质检 verify_final：逐切点检测画面亮度突变（>20）与音频电平跳变（>10 dB）；
//   - 整集关键帧连贯性拼图 view_episode_keyframes：0 算力一次审阅整集人物、轴线与光线。

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	xdraw "golang.org/x/image/draw"
)

// ---------- 1. 波形自动裁首尾静音（EDT-19） ----------

type audibleSpan struct {
	HeadSec float64
	TailSec float64
	PeakDB  float64
	RmsDB   float64 // 有效人声/有声段落的平均 RMS（dBFS），用于逐镜音量均衡
	OK      bool
}

// analyzeAudiblePCM 分析 8kHz 单声道 s16le PCM，定位真实有效声段（抗环境底噪与单帧爆音）
func analyzeAudiblePCM(pcm []int16, sampleRate int) audibleSpan {
	if sampleRate <= 0 || len(pcm) < sampleRate/5 {
		return audibleSpan{}
	}
	win := sampleRate / 50 // 20ms
	if win < 16 {
		win = 16
	}
	nWin := len(pcm) / win
	if nWin < 5 {
		return audibleSpan{}
	}
	rms := make([]float64, nWin)
	for i := 0; i < nWin; i++ {
		var sum float64
		base := i * win
		for j := 0; j < win; j++ {
			v := float64(pcm[base+j])
			sum += v * v
		}
		rms[i] = math.Sqrt(sum / float64(win))
	}
	// 3 帧滑动均值抗单点毛刺
	smooth := make([]float64, nWin)
	for i := 0; i < nWin; i++ {
		s, cnt := 0.0, 0
		for d := -1; d <= 1; d++ {
			if k := i + d; k >= 0 && k < nWin {
				s += rms[k]
				cnt++
			}
		}
		smooth[i] = s / float64(cnt)
	}
	sorted := append([]float64(nil), smooth...)
	sort.Float64s(sorted)
	peak := sorted[int(float64(len(sorted)-1)*0.96)]
	if peak < 280 {
		// 整段基本静音（< -41 dBFS）
		return audibleSpan{}
	}
	noiseFloor := sorted[int(float64(len(sorted)-1)*0.20)]
	thresh := math.Max(peak*0.16, math.Max(noiseFloor*2.3, 420.0))
	if thresh > peak*0.75 {
		thresh = peak * 0.45
	}
	// 连续 3 个窗口（60ms）超阈值视为有效声段
	const minRun = 3
	headIdx := -1
	run := 0
	for i := 0; i < nWin; i++ {
		if smooth[i] >= thresh {
			run++
			if run >= minRun {
				headIdx = i - minRun + 1
				break
			}
		} else {
			run = 0
		}
	}
	if headIdx < 0 {
		return audibleSpan{}
	}
	tailIdx := headIdx
	run = 0
	for i := nWin - 1; i >= headIdx; i-- {
		if smooth[i] >= thresh {
			run++
			if run >= minRun {
				tailIdx = i + minRun - 1
				break
			}
		} else {
			run = 0
		}
	}
	// 统计有效段落 RMS（用于响度均衡）
	var activeSq float64
	activeCnt := 0
	for i := headIdx; i <= tailIdx; i++ {
		if rms[i] >= thresh*0.6 {
			activeSq += rms[i] * rms[i]
			activeCnt++
		}
	}
	activeRMS := peak * 0.5
	if activeCnt > 0 {
		activeRMS = math.Sqrt(activeSq / float64(activeCnt))
	}
	toDB := func(v float64) float64 {
		if v <= 1 {
			return -90
		}
		return 20 * math.Log10(v/32768.0)
	}
	step := float64(win) / float64(sampleRate)
	return audibleSpan{
		HeadSec: float64(headIdx) * step,
		TailSec: math.Min(float64(len(pcm))/float64(sampleRate), float64(tailIdx+1)*step),
		PeakDB:  toDB(peak),
		RmsDB:   toDB(activeRMS),
		OK:      true,
	}
}

func probeAudibleSpan(path string) audibleSpan {
	out, err := exec.Command("ffmpeg", "-v", "error", "-i", path, "-vn", "-ac", "1", "-ar", "8000", "-f", "s16le", "pipe:1").Output()
	if err != nil || len(out) < 3200 {
		return audibleSpan{}
	}
	n := len(out) / 2
	pcm := make([]int16, n)
	for i := 0; i < n; i++ {
		pcm[i] = int16(binary.LittleEndian.Uint16(out[i*2 : i*2+2]))
	}
	return analyzeAudiblePCM(pcm, 8000)
}

// computeClipTrim 计算镜头最终画面裁切区间 [trimStart, trimEnd]
// manualStart < 0 表示未手动指定开头；manualEnd <= 0 表示未手动指定结尾。
func computeClipTrim(rawDur float64, hasDialogue bool, manualStart, manualEnd, lCut float64, autoTrim bool, span audibleSpan) (trimStart, trimEnd float64) {
	if rawDur <= 0.4 {
		return 0, rawDur
	}
	// 手动指定优先
	if manualStart >= 0 || manualEnd > 0 {
		st := 0.0
		if manualStart >= 0 {
			st = clampF(manualStart, 0, math.Max(0, rawDur-0.5))
		}
		en := rawDur
		if manualEnd > 0 {
			en = clampF(manualEnd, st+0.5, rawDur)
		}
		return round3(st), round3(en)
	}
	if !autoTrim {
		return 0, round3(rawDur)
	}
	const minDur = 1.4
	if hasDialogue && span.OK {
		st := math.Max(0, span.HeadSec-0.22)
		maxHeadTrim := math.Min(math.Max(0, rawDur-minDur), 2.8)
		if st > maxHeadTrim {
			st = maxHeadTrim
		}
		en := math.Min(rawDur, span.TailSec+0.32)
		// 若该镜设置了 L-Cut，画面比台词尾稍早切入下一镜（保留至少 0.65×lCut 让对白自然延伸到反应镜）
		if lCut > 0.05 {
			cutEarly := math.Min(lCut*0.8, 1.2)
			if en-cutEarly >= st+minDur {
				en -= cutEarly
			}
		}
		if en-st < minDur {
			mid := (st + en) / 2
			st = math.Max(0, mid-minDur/2)
			en = math.Min(rawDur, st+minDur)
			if en-st < minDur {
				st = math.Max(0, en-minDur)
			}
		}
		return round3(st), round3(en)
	}
	// 无台词镜头：切掉开头 0.25s AI 起步微抖，保持紧凑节奏
	st, en := 0.0, rawDur
	if rawDur > 3.6 {
		st = 0.25
	}
	if rawDur > 5.2 {
		en = rawDur - 0.25
	}
	if en-st < 2.0 && rawDur >= 2.0 {
		en = math.Min(rawDur, st+2.0)
	}
	return round3(st), round3(en)
}

func round3(v float64) float64 { return math.Round(v*1000) / 1000 }

// ---------- 2. 同场景 RGB 亮度/色偏匹配（EDT-20）与全局调色 ----------

type rgbStats struct {
	MeanR, MeanG, MeanB float64
	StdR, StdG, StdB    float64
	Valid               bool
}

func computeRGBStats(raw []byte) rgbStats {
	n := len(raw) / 3
	if n < 64 {
		return rgbStats{}
	}
	var sr, sg, sb float64
	var sqr, sqg, sqb float64
	for i := 0; i < n*3; i += 3 {
		r, g, b := float64(raw[i]), float64(raw[i+1]), float64(raw[i+2])
		sr += r
		sg += g
		sb += b
		sqr += r * r
		sqg += g * g
		sqb += b * b
	}
	fn := float64(n)
	mr, mg, mb := sr/fn, sg/fn, sb/fn
	std := func(sq, m float64) float64 {
		v := sq/fn - m*m
		if v < 0 {
			return 0
		}
		return math.Sqrt(v)
	}
	return rgbStats{
		MeanR: mr, MeanG: mg, MeanB: mb,
		StdR: std(sqr, mr), StdG: std(sqg, mg), StdB: std(sqb, mb),
		Valid: true,
	}
}

func probeClipRGB(path string, trimStart, trimEnd float64) rgbStats {
	dur := trimEnd - trimStart
	if dur <= 0.1 {
		dur = 1.0
	}
	args := []string{"-v", "error"}
	if trimStart > 0.01 {
		args = append(args, "-ss", fmt.Sprintf("%.3f", trimStart))
	}
	args = append(args, "-t", fmt.Sprintf("%.3f", dur), "-i", path, "-vf", "fps=2,scale=64:-2,format=rgb24", "-f", "rawvideo", "pipe:1")
	out, err := exec.Command("ffmpeg", args...).Output()
	if err != nil {
		return rgbStats{}
	}
	return computeRGBStats(out)
}

func medianF(vals []float64) float64 {
	if len(vals) == 0 {
		return 0
	}
	cp := append([]float64(nil), vals...)
	sort.Float64s(cp)
	m := len(cp) / 2
	if len(cp)%2 == 1 {
		return cp[m]
	}
	return (cp[m-1] + cp[m]) / 2
}

// effectiveSceneGroup 返回镜头所属场景调色分组；explicit=true 表示用户/编剧显式指定
func effectiveSceneGroup(s *Shot) (group string, explicit bool) {
	if g := strings.TrimSpace(s.SceneGroup); g != "" {
		return g, true
	}
	for _, code := range s.Assets {
		if strings.HasPrefix(strings.ToUpper(code), "S") {
			return strings.ToUpper(code), false
		}
	}
	return "", false
}

// planShotMatch 为同一场景分组的镜头生成 FFmpeg lutrgb 亮度/白平衡匹配滤镜。
// 显式 scene_group 跨整集同组匹配；自动推导的 S* 仅按连续同场景分段匹配（避免把白天与黄昏混在一起拉平）。
func planShotMatch(groups []string, explicit []bool, stats []rgbStats, strength float64) []string {
	n := len(stats)
	out := make([]string, n)
	if n < 2 || strength <= 0 {
		return out
	}
	strength = clampF(strength, 0.1, 1.0)
	// 归类每个镜头的 bucket key
	buckets := map[string][]int{}
	runID := 0
	prevAuto := ""
	for i := 0; i < n; i++ {
		if !stats[i].Valid || groups[i] == "" {
			prevAuto = ""
			continue
		}
		if explicit[i] {
			key := "exp:" + groups[i]
			buckets[key] = append(buckets[key], i)
			prevAuto = ""
		} else {
			if groups[i] != prevAuto {
				runID++
				prevAuto = groups[i]
			}
			key := fmt.Sprintf("auto:%d:%s", runID, groups[i])
			buckets[key] = append(buckets[key], i)
		}
	}
	for _, idxs := range buckets {
		if len(idxs) < 2 {
			continue
		}
		var mr, mg, mb, sr, sg, sb []float64
		for _, i := range idxs {
			st := stats[i]
			mr = append(mr, st.MeanR)
			mg = append(mg, st.MeanG)
			mb = append(mb, st.MeanB)
			sr = append(sr, st.StdR)
			sg = append(sg, st.StdG)
			sb = append(sb, st.StdB)
		}
		tMR, tMG, tMB := medianF(mr), medianF(mg), medianF(mb)
		tSR, tSG, tSB := medianF(sr), medianF(sg), medianF(sb)
		calc := func(mean, std, tMean, tStd float64) (gain, off float64) {
			rawGain := tStd / math.Max(std, 8.0)
			rawGain = clampF(rawGain, 0.72, 1.38)
			gain = 1.0 + strength*(rawGain-1.0)
			off = clampF(strength*(tMean-mean*gain), -36.0, 36.0)
			return gain, off
		}
		for _, i := range idxs {
			st := stats[i]
			gR, oR := calc(st.MeanR, st.StdR, tMR, tSR)
			gG, oG := calc(st.MeanG, st.StdG, tMG, tSG)
			gB, oB := calc(st.MeanB, st.StdB, tMB, tSB)
			if math.Abs(gR-1) < 0.025 && math.Abs(gG-1) < 0.025 && math.Abs(gB-1) < 0.025 &&
				math.Abs(oR) < 2.5 && math.Abs(oG) < 2.5 && math.Abs(oB) < 2.5 {
				continue
			}
			out[i] = fmt.Sprintf("format=rgb24,lutrgb=r='clip(val*%.4f%+.2f,0,255)':g='clip(val*%.4f%+.2f,0,255)':b='clip(val*%.4f%+.2f,0,255)',format=yuv420p",
				gR, oR, gG, oG, gB, oB)
		}
	}
	return out
}

// colorGradeFilter 全局电影感调色预设（作用于所有镜头统一质感）
func colorGradeFilter(grade string) string {
	switch strings.ToLower(strings.TrimSpace(grade)) {
	case "cinema_cool":
		return "eq=contrast=1.06:saturation=0.92:brightness=-0.01,colorbalance=rs=-0.03:gs=0.0:bs=0.04:rm=-0.02:bm=0.03"
	case "cinema_warm":
		return "eq=contrast=1.05:saturation=1.04:brightness=0.0,colorbalance=rs=0.02:gs=0.01:bs=-0.03:rh=0.03:bh=-0.02"
	case "moody":
		return "eq=contrast=1.10:saturation=0.86:brightness=-0.02,colorbalance=rs=-0.03:gs=-0.01:bs=0.03:rh=0.02:bh=-0.02,vignette=PI/5"
	case "noir":
		return "eq=contrast=1.14:saturation=0.72:brightness=-0.03,vignette=PI/4.5"
	default:
		return ""
	}
}

func validColorGrade(g string) bool {
	switch strings.ToLower(strings.TrimSpace(g)) {
	case "", "none", "cinema_cool", "cinema_warm", "moody", "noir":
		return true
	}
	return false
}

// ---------- 3. 音频黏合层：响度均衡 + 切点 Crossfade + J-Cut / L-Cut ----------

type audioPlacement struct {
	SrcStart  float64 // 原始视频音频读取起点
	SrcDur    float64 // 读取时长
	TimeStart float64 // 在成片时间线上的起点（毫秒由 adelay 放置）
	FadeIn    float64 // 淡入时长
	FadeOut   float64 // 淡出时长
	GainLin   float64 // 综合音量增益（origVol × 响度均衡）
}

// computeNormGainDB 计算单镜头响度均衡增益（目标 -18 dBFS），避免切点音量 ±10 dB 跳变
func computeNormGainDB(span audibleSpan, hasDialogue bool) float64 {
	if !span.OK {
		return 0
	}
	const targetDB = -18.0
	diff := targetDB - span.RmsDB
	if !hasDialogue {
		// 纯环境音镜头不大幅拉高底噪
		return clampF(diff, -8.0, 3.0)
	}
	return clampF(diff, -8.0, 10.0)
}

func dbToLinear(db float64) float64 {
	return math.Pow(10, db/20.0)
}

// computeAudioPlacement 为第 i 个镜头计算严格唇音同步的音频读取与时间线放置参数。
// 满足恒等式：TimeStart - SrcStart == clipStart - trimStart，确保 J-Cut/L-Cut 与 crossfade 零口型漂移。
func computeAudioPlacement(i, total int, clipStart, trimStart, trimEnd, rawDur, prevDur, nextDur, jCut, lCut, xfade, origVol float64, normDB float64) audioPlacement {
	dur := trimEnd - trimStart
	if dur <= 0 {
		return audioPlacement{}
	}
	half := clampF(xfade, 0, 0.5) / 2.0
	var lead, trail float64
	if i > 0 {
		req := math.Max(clampF(jCut, 0, 2.0), half)
		maxLead := math.Min(trimStart, math.Min(clipStart, prevDur*0.45))
		lead = clampF(req, 0, math.Max(0, maxLead))
	}
	if i < total-1 {
		req := math.Max(clampF(lCut, 0, 2.0), half)
		maxTrail := math.Min(math.Max(0, rawDur-trimEnd), nextDur*0.45)
		trail = clampF(req, 0, math.Max(0, maxTrail))
	}
	srcStart := trimStart - lead
	srcDur := dur + lead + trail
	timeStart := clipStart - lead
	fadeIn := 0.0
	fadeOut := 0.0
	if i > 0 {
		if lead >= 0.02 {
			fadeIn = math.Min(math.Max(xfade, 0.06), lead+half)
		} else if xfade > 0 {
			fadeIn = math.Min(xfade*0.5, dur*0.25)
		}
	} else {
		fadeIn = math.Min(0.03, dur*0.1)
	}
	if i < total-1 {
		if trail >= 0.02 {
			fadeOut = math.Min(math.Max(xfade, 0.06), trail+half)
		} else if xfade > 0 {
			fadeOut = math.Min(xfade*0.5, dur*0.25)
		}
	} else {
		fadeOut = math.Min(0.08, dur*0.15)
	}
	if fadeIn+fadeOut > srcDur*0.8 {
		fadeIn = srcDur * 0.35
		fadeOut = srcDur * 0.35
	}
	gain := origVol * dbToLinear(normDB)
	return audioPlacement{
		SrcStart:  round3(srcStart),
		SrcDur:    round3(srcDur),
		TimeStart: round3(timeStart),
		FadeIn:    round3(fadeIn),
		FadeOut:   round3(fadeOut),
		GainLin:   math.Round(gain*10000) / 10000,
	}
}

// ---------- 4. 成片自动质检 verify_final（EDT-20 + EDT-23） ----------

type cutMetric struct {
	Between     string  `json:"between"`
	At          float64 `json:"at"`
	SameScene   bool    `json:"same_scene"`
	LumaBefore  float64 `json:"luma_before"`
	LumaAfter   float64 `json:"luma_after"`
	LumaJump    float64 `json:"luma_jump"`
	AudioBefore float64 `json:"audio_db_before"`
	AudioAfter  float64 `json:"audio_db_after"`
	AudioDBJump float64 `json:"audio_db_jump"`
	Status      string  `json:"status"`
	Note        string  `json:"note,omitempty"`
}

func frameMeanLuma(path string, sec float64) (float64, bool) {
	if sec < 0 {
		sec = 0
	}
	out, err := exec.Command("ffmpeg", "-v", "error", "-ss", fmt.Sprintf("%.3f", sec), "-i", path,
		"-frames:v", "1", "-vf", "scale=64:-2,format=gray", "-f", "rawvideo", "pipe:1").Output()
	if err != nil || len(out) < 64 {
		return 0, false
	}
	var sum float64
	for _, b := range out {
		sum += float64(b)
	}
	return math.Round(sum/float64(len(out))*10) / 10, true
}

func audioWindowDB(path string, start, dur float64) (float64, bool) {
	if start < 0 {
		start = 0
	}
	if dur <= 0.05 {
		dur = 0.3
	}
	out, err := exec.Command("ffmpeg", "-v", "error", "-ss", fmt.Sprintf("%.3f", start), "-t", fmt.Sprintf("%.3f", dur),
		"-i", path, "-vn", "-ac", "1", "-ar", "8000", "-f", "s16le", "pipe:1").Output()
	if err != nil || len(out) < 160 {
		return -60, false
	}
	return pcmRMSDB(out), true
}

func pcmRMSDB(raw []byte) float64 {
	n := len(raw) / 2
	if n == 0 {
		return -60
	}
	var sum float64
	for i := 0; i < n; i++ {
		v := float64(int16(binary.LittleEndian.Uint16(raw[i*2 : i*2+2])))
		sum += v * v
	}
	rms := math.Sqrt(sum / float64(n))
	if rms <= 1 {
		return -60
	}
	db := 20 * math.Log10(rms/32768.0)
	if db < -60 {
		db = -60
	}
	return math.Round(db*10) / 10
}

// evaluateCutJump 判定单个切点的亮度与音频跳变是否超标
func evaluateCutJump(sameScene bool, lumaBefore, lumaAfter, dbBefore, dbAfter float64) (lumaJump, dbJump float64, status, note string) {
	lumaJump = math.Round(math.Abs(lumaAfter-lumaBefore)*10) / 10
	dbJump = math.Round(math.Abs(dbAfter-dbBefore)*10) / 10
	var issues []string
	if sameScene && lumaJump > 20.0 {
		issues = append(issues, fmt.Sprintf("同场景亮度跳变 %.1f（>20）", lumaJump))
	}
	// 仅在至少一侧非纯静音（> -42 dBFS）时告警音量跳变
	if (dbBefore > -42 || dbAfter > -42) && dbJump > 10.0 {
		issues = append(issues, fmt.Sprintf("切点音频跳变 %.1f dB（>10 dB）", dbJump))
	}
	if len(issues) > 0 {
		return lumaJump, dbJump, "WARN", strings.Join(issues, "；")
	}
	return lumaJump, dbJump, "PASS", ""
}

func verifyFinalEpisode(ctx context.Context, p *Project, e *Episode) (map[string]any, error) {
	if e.FinalFile == "" {
		return nil, errors.New("这一集还没有成片，请先调用 build_final")
	}
	finalPath := filepath.Join(dataDir, "videos", e.FinalFile)
	if _, err := os.Stat(finalPath); err != nil {
		return nil, errors.New("成片文件不存在，请重新 build_final")
	}
	tl, err := buildTimeline(p, e)
	if err != nil {
		return nil, err
	}
	ci, err := probe(finalPath)
	if err != nil {
		return nil, err
	}
	var cuts []cutMetric
	var warns []string
	for i := 1; i < len(tl.Clips); i++ {
		prev := tl.Clips[i-1]
		cur := tl.Clips[i]
		at := cur.Start
		g1, _ := effectiveSceneGroup(prev.Shot)
		g2, _ := effectiveSceneGroup(cur.Shot)
		same := g1 != "" && g1 == g2
		lb, okLB := frameMeanLuma(finalPath, math.Max(0, at-0.08))
		la, okLA := frameMeanLuma(finalPath, math.Min(ci.dur-0.05, at+0.08))
		dbB, _ := audioWindowDB(finalPath, math.Max(0, at-0.40), 0.40)
		dbA, _ := audioWindowDB(finalPath, at, 0.40)
		if !okLB || !okLA {
			lb, la = 0, 0
		}
		lj, dj, st, note := evaluateCutJump(same, lb, la, dbB, dbA)
		cm := cutMetric{
			Between:     prev.Shot.Code + "->" + cur.Shot.Code,
			At:          round3(at),
			SameScene:   same,
			LumaBefore:  lb,
			LumaAfter:   la,
			LumaJump:    lj,
			AudioBefore: dbB,
			AudioAfter:  dbA,
			AudioDBJump: dj,
			Status:      st,
			Note:        note,
		}
		cuts = append(cuts, cm)
		if st != "PASS" {
			warns = append(warns, fmt.Sprintf("切点 %s (%.2fs)：%s", cm.Between, cm.At, note))
		}
	}
	clips := make([]map[string]any, 0, len(tl.Clips))
	for _, c := range tl.Clips {
		g, _ := effectiveSceneGroup(c.Shot)
		clips = append(clips, map[string]any{
			"code":        c.Shot.Code,
			"shot_id":     c.Shot.ID,
			"scene_group": g,
			"raw_dur":     round3(c.Info.dur),
			"trim_start":  c.TrimStart,
			"trim_end":    c.TrimEnd,
			"final_start": round3(c.Start),
			"final_dur":   round3(c.Dur),
			"j_cut":       c.Shot.JCut,
			"l_cut":       c.Shot.LCut,
		})
	}
	summary := "PASS"
	if len(warns) > 0 {
		summary = "WARN"
	}
	return map[string]any{
		"ok":             true,
		"episode_id":     e.ID,
		"final_file":     e.FinalFile,
		"final_duration": round3(ci.dur),
		"auto_trim":      e.AutoTrim,
		"shot_match":     e.ShotMatch,
		"audio_xfade":    e.AudioXfade,
		"audio_norm":     e.AudioNorm,
		"color_grade":    e.ColorGrade,
		"summary":        summary,
		"clips":          clips,
		"cuts":           cuts,
		"warnings":       nonNil(warns),
	}, nil
}

// ---------- 5. 整集关键帧连贯性拼图（纯 Go，0 算力审全集） ----------

// 3x5 迷你像素点阵数字（0-9），用于给拼图每格左上角打清晰的镜头序号角标
var miniDigits = [10][5]string{
	{"111", "101", "101", "101", "111"}, // 0
	{"010", "110", "010", "010", "111"}, // 1
	{"111", "001", "111", "100", "111"}, // 2
	{"111", "001", "111", "001", "111"}, // 3
	{"101", "101", "111", "001", "001"}, // 4
	{"111", "100", "111", "001", "111"}, // 5
	{"111", "100", "111", "101", "111"}, // 6
	{"111", "001", "001", "010", "010"}, // 7
	{"111", "101", "111", "101", "111"}, // 8
	{"111", "101", "111", "001", "111"}, // 9
}

func drawBadge(dst *image.RGBA, x0, y0 int, code string) {
	scale := 4
	padX, padY := 6, 5
	charW := 3 * scale
	charH := 5 * scale
	gap := scale
	digits := make([]int, 0, len(code))
	for _, r := range code {
		if r >= '0' && r <= '9' {
			digits = append(digits, int(r-'0'))
		}
	}
	if len(digits) == 0 {
		return
	}
	bw := padX*2 + len(digits)*charW + (len(digits)-1)*gap
	bh := padY*2 + charH
	bg := image.NewUniform(color.RGBA{15, 18, 26, 230})
	draw.Draw(dst, image.Rect(x0, y0, x0+bw, y0+bh), bg, image.Point{}, draw.Src)
	fg := color.RGBA{255, 224, 102, 255}
	for di, d := range digits {
		ox := x0 + padX + di*(charW+gap)
		oy := y0 + padY
		for ry, row := range miniDigits[d] {
			for rx, ch := range row {
				if ch == '1' {
					for sy := 0; sy < scale; sy++ {
						for sx := 0; sx < scale; sx++ {
							dst.SetRGBA(ox+rx*scale+sx, oy+ry*scale+sy, fg)
						}
					}
				}
			}
		}
	}
}

type labeledFrame struct {
	Code string
	Data []byte
}

// buildLabeledContactSheet 将带序号的关键帧拼成一张高对比编号网格 JPEG（纯 Go）
func buildLabeledContactSheet(items []labeledFrame, cols, maxEdge int) ([]byte, error) {
	if len(items) == 0 {
		return nil, errors.New("没有可拼接的关键帧")
	}
	type decoded struct {
		code string
		im   image.Image
	}
	var imgs []decoded
	for _, it := range items {
		im, _, err := image.Decode(bytes.NewReader(it.Data))
		if err == nil {
			imgs = append(imgs, decoded{it.Code, im})
		}
	}
	if len(imgs) == 0 {
		return nil, errors.New("关键帧解码失败")
	}
	if cols <= 0 {
		switch {
		case len(imgs) <= 4:
			cols = 2
		case len(imgs) <= 9:
			cols = 3
		case len(imgs) <= 16:
			cols = 4
		default:
			cols = 5
		}
	}
	cols = clampInt(cols, 1, 6)
	rows := (len(imgs) + cols - 1) / cols
	b0 := imgs[0].im.Bounds()
	sw, sh := max(b0.Dx(), 1), max(b0.Dy(), 1)
	if maxEdge <= 0 {
		maxEdge = 2048
	}
	maxEdge = clampInt(maxEdge, 512, 4096)
	gap := 6
	totalW := cols*sw + (cols-1)*gap
	totalH := rows*sh + (rows-1)*gap
	scale := 1.0
	if totalW > maxEdge || totalH > maxEdge {
		scale = math.Min(float64(maxEdge)/float64(totalW), float64(maxEdge)/float64(totalH))
	}
	cw := max(int(math.Round(float64(sw)*scale)), 48)
	ch := max(int(math.Round(float64(sh)*scale)), 48)
	outW := cols*cw + (cols-1)*gap
	outH := rows*ch + (rows-1)*gap
	dst := image.NewRGBA(image.Rect(0, 0, outW, outH))
	draw.Draw(dst, dst.Bounds(), image.NewUniform(color.RGBA{18, 20, 26, 255}), image.Point{}, draw.Src)
	for i, it := range imgs {
		r, c := i/cols, i%cols
		x0, y0 := c*(cw+gap), r*(ch+gap)
		xdraw.CatmullRom.Scale(dst, image.Rect(x0, y0, x0+cw, y0+ch), it.im, it.im.Bounds(), xdraw.Src, nil)
		drawBadge(dst, x0+4, y0+4, it.code)
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, dst, &jpeg.Options{Quality: 86}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
