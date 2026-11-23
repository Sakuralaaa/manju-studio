package main

// MiniMax H3 官方视频提示词格式（对照 MiniMaxAI/MiniMax-H3 VIDEO_PROMPT_WRITING_GUIDE_base_en.md）：
//   Part 1: 图片对齐首行（I2VA / FL2VA），空一行；
//   Part 2: integrated_multimodal_description / overall_soundscape / non_diegetic_music。
// 如果镜头 video_prompt 本身已按官方格式写好（含 integrated_multimodal_description:），原样透传（仅在缺对齐首行时补上）。
// 注意：H3 的 BasicGuider cfg=1 无 negative 分支，禁止出现「不要人声/不要字幕/不要配乐」等否定词清单，改为显式填写音频字段。

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
)

func h3AlignHeader(seconds int, hasLastFrame bool) string {
	if !hasLastFrame {
		return "For the target video, at 0.00 seconds into the target video, <Picture 1> (from [Shot 1]) is fully referenced."
	}
	effSec := float64(seconds)
	if r := loadFrameRule(); r.FPS > 0 {
		effSec = float64(framesFor(seconds)) / float64(r.FPS)
	}
	if effSec <= 0 {
		effSec = 5.0
	}
	return fmt.Sprintf("How the reference pictures align with the target video — Picture 1 (from Shot 1) aligns with the 0.00-second mark of the target video; Picture 2 (from Shot 1) aligns with the %.2f-second mark of the target video.", effSec)
}

func h3StylePrefix(p *Project) string {
	if isRealistic(p) {
		return "Live-action, cinematic"
	}
	st := strings.ToLower(strings.TrimSpace(p.Style))
	if strings.Contains(st, "3d") || strings.Contains(st, "cg") {
		return "3D CG, cinematic"
	}
	return "2D-animated, cinematic"
}

// h3CameraPhrase 把常见中文景别/运镜映射成官方 H3 英文运镜表达（动势 + 幅度 + 速度）
func h3CameraPhrase(cam string) string {
	c := strings.TrimSpace(cam)
	if c == "" {
		return ""
	}
	var framing, motion string
	switch {
	case strings.Contains(c, "大特写"):
		framing = "an extreme close-up shot"
	case strings.Contains(c, "特写"):
		framing = "a close-up shot"
	case strings.Contains(c, "中近景"):
		framing = "a medium close-up shot"
	case strings.Contains(c, "近景"):
		framing = "a close shot"
	case strings.Contains(c, "中景"):
		framing = "a medium shot"
	case strings.Contains(c, "远景"), strings.Contains(c, "航拍"):
		framing = "a wide shot"
	case strings.Contains(c, "全景"):
		framing = "a medium-wide shot"
	case strings.Contains(c, "过肩"):
		framing = "an over-the-shoulder shot"
	}
	slow := strings.Contains(c, "缓") || strings.Contains(c, "慢") || strings.Contains(c, "微")
	fast := strings.Contains(c, "快") || strings.Contains(c, "急")
	amp := ""
	if strings.Contains(c, "微") || strings.Contains(c, "轻") {
		amp = " with small amplitude"
	}
	spd := ""
	if slow {
		spd = " at slow speed"
	} else if fast {
		spd = " at fast speed"
	}
	switch {
	case strings.Contains(c, "推"):
		motion = "The camera pushes in" + amp + spd + "."
	case strings.Contains(c, "拉"):
		motion = "The camera pulls out" + amp + spd + "."
	case strings.Contains(c, "左摇"):
		motion = "The camera pans left" + amp + spd + "."
	case strings.Contains(c, "右摇"), strings.Contains(c, "横摇"):
		motion = "The camera pans right" + amp + spd + "."
	case strings.Contains(c, "跟拍"):
		motion = "The camera uses a tracking shot" + spd + "."
	case strings.Contains(c, "环绕"):
		motion = "The camera performs an arc shot" + spd + "."
	case strings.Contains(c, "手持"):
		motion = "The camera shakes slightly with natural handheld motion."
	case strings.Contains(c, "固定"):
		motion = "The camera holds a static shot."
	}
	var out []string
	if framing != "" {
		out = append(out, "Framed as "+framing+".")
	}
	if motion != "" {
		out = append(out, motion)
	}
	return strings.Join(out, " ")
}

// cleanH3Dialogue 清理台词文本：保留自然标点并确保句末有标点（官方建议去掉省略号干扰与多余装饰符号）
func cleanH3Dialogue(s string) string {
	s = strings.TrimSpace(s)
	s = strings.Trim(s, `“”"「」`)
	s = strings.ReplaceAll(s, "……", "，")
	s = strings.ReplaceAll(s, "...", "，")
	s = strings.TrimRight(s, "，,、 ")
	if s == "" {
		return ""
	}
	r := []rune(s)
	last := r[len(r)-1]
	switch last {
	case '。', '？', '！', '.', '?', '!':
	default:
		s += "。"
	}
	return s
}

// h3EmotionEnglish 将常见中文语气词映射为英文声演提示，避免在 <d> 外残留中文被 H3 误念
func h3EmotionEnglish(emo string) string {
	e := strings.TrimSpace(emo)
	if e == "" {
		return ""
	}
	table := map[string]string{
		"笑": "with a light smile", "轻笑": "with a soft chuckle", "微笑": "warmly with a slight smile",
		"冷笑": "with a cold smirk", "苦笑": "with a wry smile", "嘲讽": "in a dry, sarcastic tone",
		"低声": "in a low voice", "压低声音": "in a hushed, low voice", "耳语": "in a quiet whisper", "轻声": "softly",
		"平静": "calmly", "冷静": "in a calm, composed tone", "冷淡": "coolly and evenly", "淡然": "matter-of-factly",
		"温柔": "gently and warmly", "严肃": "seriously", "坚定": "firmly", "笃定": "with quiet conviction",
		"急促": "urgently", "紧张": "tensely", "慌张": "anxiously", "惊讶": "in quiet surprise", "震惊": "in stunned disbelief",
		"愤怒": "in a tense, angry tone", "怒": "angrily", "质问": "in a sharp, questioning tone",
		"哽咽": "with a slightly choked, emotional voice", "悲伤": "quietly and sadly", "疲惫": "wearily",
		"无奈": "with a resigned sigh", "激动": "with heightened emotion", "试探": "tentatively", "打趣": "playfully",
	}
	if en, ok := table[e]; ok {
		return en
	}
	return e
}

func speakerHint(assets []*Asset, name string) string {
	a := matchSpeaker(assets, name)
	if a == nil {
		return name
	}
	var bits []string
	if g := strings.TrimSpace(a.Look.Gender); g != "" {
		switch g {
		case "男", "男性":
			bits = append(bits, "male")
		case "女", "女性":
			bits = append(bits, "female")
		default:
			bits = append(bits, g)
		}
	}
	if age := strings.TrimSpace(a.Look.Age); age != "" {
		bits = append(bits, age)
	}
	if strings.TrimSpace(a.Voice.Desc) != "" {
		vd := strings.TrimSpace(a.Voice.Desc)
		if r := []rune(vd); len(r) > 36 {
			vd = string(r[:36])
		}
		bits = append(bits, vd)
	}
	if len(bits) == 0 {
		return a.Name
	}
	return fmt.Sprintf("%s (%s)", a.Name, strings.Join(bits, ", "))
}

var (
	h3DialogueTagRe = regexp.MustCompile(`(?s)<d>.*?</d>`)
	h3BadTriggers   = []struct {
		pattern string
		reason  string
	}{
		{"eyes flush red", "容易导致眼球血红/阴间恐怖脸，建议改为「eyes glisten slightly」或克制凝视"},
		{"bloodshot", "容易导致眼球充血恐怖失真，建议删去"},
		{"unblinking", "H3 不眨眼提示词易产生死鱼眼/恐怖谷感，建议改为自然微眨"},
		{"jaw drops wide", "过度张大嘴易造成面部塌陷恐怖脸，建议改为微愣/目光停住"},
		{"wide shocked eyes", "过度瞪眼易出阴间微表情，建议改为克制错愕"},
		{"whip-pan", "极速甩镜在 H3 易整段糊屏或瞬移，建议改为平稳跟拍或直接切镜"},
		{"whip pan", "极速甩镜在 H3 易整段糊屏或瞬移，建议改为平稳跟拍或直接切镜"},
		{"rushing rapidly", "高速冲刺运动在 H3 易出现肢体撕裂/糊屏，建议改为稳步快走或慢推"},
		{"extreme eyeball zoom", "极限眼球拉近易畸变，建议改为普通近景/特写"},
		{"violently", "剧烈动作词易引发抽搐与穿模，建议改为克制、单一的自然动作"},
		{"turns around 180", "H3 I2V 对 180° 转身方向控制不稳，建议在转身前直接切镜"},
		{"spins around", "大幅旋转易穿模与掉脸，建议改为半身微转或切镜"},
		{"眼眶瞬间通红", "容易引发红眼恐怖脸，建议改为克制微表情"},
		{"瞳孔骤缩", "容易引发恐怖谷凝视，建议改为目光骤停"},
		{"面目狰狞", "容易导致面部畸变，建议改为克制紧绷神情"},
		{"急速甩镜", "极速甩镜在 H3 易整段糊屏，建议直接硬切或慢摇"},
		{"剧烈摇晃", "易引发画面撕裂，建议改为轻微自然手持"},
	}
)

// h3PromptWarnings 在 0 成本预览/存分镜时检测 MiniMax H3 实测高发雷区：
// 1) <d> 标签外含较多中文描述（H3 偶发会把中文动作说明直接念出声）；
// 2) 实测会导致阴间微表情、糊屏或转身崩坏的危险触发词。
func h3PromptWarnings(finalVP string, nativeAudio bool) []string {
	var warns []string
	// 1. 剥离 <d>...</d> 以及风格括号后的主体，统计残余汉字数量
	body := finalVP
	if idx := strings.Index(body, "integrated_multimodal_description:"); idx >= 0 {
		body = body[idx:]
	}
	stripped := h3DialogueTagRe.ReplaceAllString(body, "")
	cjk := 0
	for _, r := range stripped {
		if unicode.Is(unicode.Han, r) {
			cjk++
		}
	}
	if nativeAudio && cjk >= 12 {
		warns = append(warns, fmt.Sprintf("H3 视频提示词在 <d> 标签外含 %d 个汉字：MiniMax H3 偶发会把中文动作/运镜描述直接念出声；建议将 video_prompt 写成纯英文（或手写纯英文 integrated_multimodal_description:，仅 <d>[Chinese] 台词</d> 用中文）", cjk))
	}
	// 2. 扫描实测 H3 阴间脸 / 糊屏 / 崩坏触发词
	low := strings.ToLower(finalVP)
	for _, item := range h3BadTriggers {
		if strings.Contains(low, item.pattern) {
			warns = append(warns, fmt.Sprintf("H3 风险词「%s」：%s", item.pattern, item.reason))
		}
	}
	return warns
}

// shotVideoPromptFor 生成符合 MiniMax H3 官方规范的视频提示词（I2VA / FL2VA）
func shotVideoPromptFor(p *Project, s *Shot, seconds int, hasLastFrame bool) string {
	rawVP := strings.TrimSpace(s.VideoPrompt)
	align := h3AlignHeader(seconds, hasLastFrame)
	// 外部 Agent 或用户已手写完整官方格式：直接透传，只在缺首行对齐声明时补齐
	if strings.Contains(rawVP, "integrated_multimodal_description:") {
		if strings.HasPrefix(rawVP, "For the target video,") || strings.HasPrefix(rawVP, "How the reference pictures align") {
			return rawVP
		}
		return align + "\n\n" + rawVP
	}

	var desc []string
	styleHead := h3StylePrefix(p)
	if st := strings.TrimSpace(p.Style); st != "" {
		styleHead += " (" + st + ")"
	}
	if hasLastFrame {
		desc = append(desc, styleHead+", starting from the exact subject appearance, clothing, framing, and scene layout established by Picture 1.")
	} else {
		desc = append(desc, styleHead+", preserving the exact subject appearance, clothing, lighting, and scene layout shown in <Picture 1>.")
	}
	if cam := h3CameraPhrase(s.Camera); cam != "" {
		desc = append(desc, cam)
	}
	if rawVP != "" {
		desc = append(desc, rawVP)
	} else if act := strings.TrimSpace(s.Action); act != "" {
		desc = append(desc, act)
	}

	if d := strings.TrimSpace(s.Dialogue); d != "" {
		lines := parseDialogue(d)
		if p.AudioMode == "dub" {
			var talkers []string
			for _, pl := range lines {
				if pl.kind == "dialogue" && !contains(talkers, pl.speaker) {
					talkers = append(talkers, pl.speaker)
				}
			}
			if len(talkers) > 0 {
				desc = append(desc, fmt.Sprintf("%s speak on screen with natural mouth and facial movement matching the performance.", strings.Join(talkers, " and ")))
			}
		} else {
			assets := listAssets(p.ID)
			spkID := map[string]string{}
			nextID := 1
			for _, pl := range lines {
				txt := cleanH3Dialogue(pl.text)
				if txt == "" {
					continue
				}
				key := pl.speaker
				if pl.kind == "narration" {
					key = "__narrator__"
				}
				sid, existed := spkID[key]
				if !existed {
					sid = fmt.Sprintf("S%d", nextID)
					spkID[key] = sid
					nextID++
				}
				if pl.kind == "narration" {
					desc = append(desc, fmt.Sprintf("The narrator (%s) says in an off-screen voiceover: <d>[Chinese] %s</d> while on-screen characters' lips remain completely closed.", sid, txt))
					continue
				}
				who := pl.speaker
				if !existed {
					who = speakerHint(assets, pl.speaker)
				}
				if emo := h3EmotionEnglish(pl.emotion); emo != "" {
					desc = append(desc, fmt.Sprintf("%s (%s) speaks (%s): <d>[Chinese] %s</d>", who, sid, emo, txt))
				} else {
					desc = append(desc, fmt.Sprintf("%s (%s) says: <d>[Chinese] %s</d>", who, sid, txt))
				}
			}
		}
	}

	if hasLastFrame {
		desc = append(desc, "The motion progresses smoothly and settles cleanly into the pose, spacing, lighting, and final composition established by Picture 2 at the end of the shot.")
	}

	soundscape := "Natural environmental ambience and subtle physical foley synchronized with the on-screen motion."
	if p.AudioMode == "dub" {
		soundscape = "Clean room tone, environmental ambience, and physical action foley synchronized with the movement."
	}

	var b strings.Builder
	b.WriteString(align)
	b.WriteString("\n\nintegrated_multimodal_description: [Shot 1] ")
	b.WriteString(strings.Join(desc, " "))
	b.WriteString("\n\noverall_soundscape: ")
	b.WriteString(soundscape)
	b.WriteString("\n\nnon_diegetic_music: N/A")
	return b.String()
}
