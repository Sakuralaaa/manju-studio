package main

// MiniMax H3 官方视频提示词格式（对照 MiniMaxAI/MiniMax-H3 VIDEO_PROMPT_WRITING_GUIDE_base_en.md）：
//   Part 1: 图片对齐首行（I2VA / FL2VA），空一行；
//   Part 2: integrated_multimodal_description / overall_soundscape / non_diegetic_music。
// 如果镜头 video_prompt 本身已按官方格式写好（含 integrated_multimodal_description:），原样透传（仅在缺对齐首行时补上）。
// 注意：H3 的 BasicGuider cfg=1 无 negative 分支，禁止出现「不要人声/不要字幕/不要配乐」等否定词清单，改为显式填写音频字段。

import (
	"fmt"
	"strings"
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

func speakerHint(assets []*Asset, name string) string {
	a := matchSpeaker(assets, name)
	if a == nil {
		return name
	}
	var bits []string
	if strings.TrimSpace(a.Look.Age) != "" {
		bits = append(bits, strings.TrimSpace(a.Look.Age))
	}
	if strings.TrimSpace(a.Look.Gender) != "" {
		bits = append(bits, strings.TrimSpace(a.Look.Gender))
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
				if emo := strings.TrimSpace(pl.emotion); emo != "" {
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
