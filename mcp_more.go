package main

// MCP 补充工具（0.2.9；0.2.10 起 listen_voice 支持 library_id、clone_voice 可顺手收进音色库）：单镜头改分镜、看任意图、听音色候选、建 / 改作品、删资产、从资产拿掉图、
// 用镜头原声（或上传音频）克隆角色音色。

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// VoiceRef 角色的原声参考样本（clone_voice 存下）：克隆音色用，以后也可做声音转换 / Ref2VA 的参考
type VoiceRef struct {
	Sample    string  `json:"sample"`
	Source    string  `json:"source"` // shot / upload / url
	ShotID    int64   `json:"shot_id,omitempty"`
	VideoID   int64   `json:"video_id,omitempty"`
	Start     float64 `json:"start,omitempty"`
	End       float64 `json:"end,omitempty"`
	Duration  float64 `json:"duration"`
	CreatedAt int64   `json:"created_at"`
}

func numF(desc string) map[string]any { return map[string]any{"type": "number", "description": desc} }

func enumS(desc string, vals ...string) map[string]any {
	return map[string]any{"type": "string", "enum": vals, "description": desc}
}

func projectProps() map[string]any {
	return map[string]any{
		"title": str("作品标题"), "idea": str("故事创意 / 一句话简介"), "style": str("画风，如「国漫厚涂，电影感光影」「日系赛璐璐」「写实真人」"),
		"kind":   enumS("作品类型：manju = 漫剧（动态漫画），live = 真人剧（写实）", "manju", "live"),
		"aspect": enumS("画幅", "9:16", "16:9", "1:1", "3:4", "4:3"), "episode_sec": num("单集目标时长（秒，10–600）"),
		"shot_target": num("每集目标镜头数（1–60）"), "episodes_plan": num("计划集数（1–200）"),
		"image_channel_id": num("默认生图渠道编号（list_image_channels）；0 = 用第一个"), "image_model": str("默认生图模型"),
		"text_channel_id": num("文本模型渠道编号（应用自己写剧本时用）"), "text_model": str("文本模型名"),
		"voice_channel_id": num("默认语音渠道编号（配音）"),
		"video_sec":        num("视频默认时长 5–10 秒"), "video_res": enumS("视频分辨率", "768p", "480p"), "video_turbo": boolp("视频用 Turbo 8 步"),
		"audio_mode":  enumS("声音：native = 用视频模型自带的声音（对白、环境声）；dub = 用配音（TTS）替换对白，原声按 orig_volume 压低", "native", "dub"),
		"orig_volume": numF("dub 模式下视频原声音量 0–1"), "takes": num("每个镜头默认抽卡次数 1–4"),
		"deliver": enumS("成片规格：source = 原分辨率；1080p = 放大到 1080p", "source", "1080p"), "post_upscale": boolp("视频后处理：超分"), "post_interp": boolp("视频后处理：补帧"),
		"voice_unify": boolp("生成后统一音色（默认关）：每条视频出来后在 Colab 上分离人声，用 Seed-VC 转成说话角色的音色母带再混回去，原版保留；镜头可用 set_voice_unify 单独开关"),
	}
}

var moreTools = []mcpTool{
	{"update_shot", "改一个镜头（只改给出的字段），镜头已有的关键帧候选、视频候选和选用都保留，不会重画。改了 keyframe_prompt 后用 generate_keyframe 重出；改了台词会同步台词表（没变的句子保留已有配音）。整集重写才用 save_shots。", obj(map[string]any{
		"shot_id": num("镜头编号"), "episode_id": num("集编号（与 code 一起用）"), "code": str("镜头序号，如 03"),
		"keyframe_prompt": str("关键帧中文生图提示词"), "video_prompt": str("图生视频中文描述"),
		"camera": str("景别 + 机位/运镜"), "picture": str("画面：人物动作、表情、环境变化"),
		"dialogue": map[string]any{"description": "台词：文本，或 [{speaker, text, emotion}]；传空字符串清掉", "oneOf": []any{map[string]any{"type": "string"}, arr("", obj(map[string]any{
			"speaker": str("角色名；旁白写「旁白」"), "text": str("台词"), "emotion": str("语气（可选）"),
		}, "speaker", "text"))}},
		"assets":     arr("出现的资产编号或名称（整体替换这个镜头的资产列表）", map[string]any{"type": "string"}),
		"duration":   num("时长 5–10 秒"),
		"transition": enumS("衔接", "cut", "continuous", "first_last"),
		"ref_ids":    map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "description": "这个镜头的手动参考图编号（构图、姿势、道具），最多 4 张；传空数组清掉"},
	})},
	{"view_image", "按图片编号看任意一张或几张图（画室、资产、关键帧、封面都行），返回 MCP 图片内容块（默认缩到长边 1536 像素的 JPEG；original=true 返回原图）。image_generate / generate_keyframe / generate_asset_image 返回的编号都能用。", obj(map[string]any{
		"image_id": num("图片编号"), "image_ids": map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "description": "多张图片编号，最多 8 张"},
		"original": boolp("返回原图"), "max_edge": num("缩放后的长边像素，256–4096，默认 1536"),
	})},
	{"listen_voice", "听音色：返回 MCP 音频内容块，并在文字里列出每段的文件名、时长、念的文字和 2 小时有效的免密钥下载地址（宿主不转音频块时下载到工作区再 read_file）。asset_id 时还列出这个角色存下的全部设计候选。给 sample（design_voice 的候选样本名，可多个）直接听；给 asset_id 听角色当前音色的样本和 clone_voice 存下的原声参考；给 project_id 听旁白音色样本；给 library_id 听音色库里的音色（样本、试听、原声参考）。没有本地样本的音色（Fish 音色 / 内置音色）请用 tts 合成一句试听。", obj(map[string]any{
		"sample": str("样本名"), "samples": arr("多个样本名", map[string]any{"type": "string"}),
		"asset_id": num("角色资产编号"), "library_id": num("音色库编号（list_voice_library）"), "project_id": num("作品编号（听旁白音色）"), "limit": num("最多几段，1–6，默认 4"),
	})},
	{"create_project", "新建作品（剧集），同时建好第 1 集。只有 title 必填，其余设置不填用默认（9:16、漫剧、原声）。", obj(projectProps(), "title")},
	{"update_project", "改作品设置：只改给出的字段。改画幅 / 画风不会重做已有的图和视频；audio_mode 从 native 改成 dub 后需要 generate_dub 生成配音。", obj(withKey(projectProps(), "project_id", num("作品编号")), "project_id")},
	{"delete_asset", "删除一个资产（角色 / 场景 / 道具）及它的全部图片，并从各镜头的资产列表里去掉它。不可恢复：必须传 confirm=true；不传时只返回会删掉什么。", obj(map[string]any{
		"asset_id": num("资产编号"), "project_id": num("作品编号（配合 code）"), "code": str("资产编号，如 C03"), "confirm": boolp("确认删除"),
	})},
	{"detach_image", "从资产里拿掉一张图（设定图 / 三视图 / 表情表 / 参考照 / 候选）并删除这张图；如果它是选用的，那个位置会变空。画室里的原图不受影响（attach_image 存进来的是副本）。必须传 confirm=true。", obj(map[string]any{
		"asset_id": num("资产编号"), "image_id": num("图片编号"), "confirm": boolp("确认拿掉并删除"),
	}, "asset_id", "image_id")},
	{"clone_voice", "用一段原声给角色克隆音色：从镜头选定视频里抽音频（可指定 start / end 秒截取一段，最长 60 秒，至少 3 秒，10 秒以上效果好），或给 upload_id（先 POST /v1/uploads 上传大文件）、audio_base64、audio_url。样本绑定为角色音色：语音渠道是 Fish 时在 Fish 建私有音色，否则走小米 MiMo 的 voiceclone（与设计音色样本同一条路）。样本同时存进角色的原声参考（voice_refs），以后可做声音转换参考。注意：视频原声里的背景音乐、环境声、别人的声音服务器不会去除，请用 start / end 挑一段只有这个角色说话、背景安静的片段，或自己处理干净后通过 POST /v1/uploads 拿 upload_id 传。", obj(map[string]any{
		"asset_id": num("角色资产编号"), "source_shot_id": num("从这个镜头的选定视频里抽音频"), "video_id": num("指定某个视频候选（可选，默认用镜头选定的）"),
		"upload_id": str("先通过 POST /v1/uploads 上传得到的临时文件编号（大音频推荐）"),
		"audio_base64": str("音频 base64 或 data URL（wav / mp3 / m4a 等）"), "audio_url": str("音频地址"),
		"start": numF("从第几秒开始（可选）"), "end": numF("到第几秒结束（可选）"),
		"channel_id":  num("语音渠道编号（可选）：不填时角色当前用 Fish 就用 Fish，否则用小米 MiMo"),
		"sample_text": str("这段音频里说的话（可选，Fish 建音色时用；不填且没截取时取这个镜头里该角色的台词）"),
		"description": str("音色描述（可选，记下来）"), "bind": boolp("设为角色音色，默认 true；false = 只存进原声参考"),
		"clean": boolp("自动裁首尾静音并做响度归一（loudnorm），默认 true；false = 保留原始音频"),
		"save_to_library": boolp("同时收进音色库（跨作品复用），默认 false"), "library_name": str("收进音色库时的名称（可选）"),
	}, "asset_id")},
}

func withKey(m map[string]any, k string, v any) map[string]any {
	out := map[string]any{k: v}
	for a, b := range m {
		out[a] = b
	}
	return out
}

func callMoreTool(ctx context.Context, name string, args json.RawMessage, base string, audioOK bool) (any, error) {
	switch name {
	case "update_shot":
		return toolUpdateShot(ctx, args)
	case "view_image":
		return toolViewImage(ctx, args)
	case "listen_voice":
		return toolListenVoice(ctx, args, base, audioOK)
	case "design_voice":
		var a voiceToolArgs
		_ = json.Unmarshal(args, &a)
		v, err := toolDesignVoice(ctx, a, base)
		if err != nil {
			return nil, err
		}
		return withVoiceAudio(v, base, audioOK), nil
	case "create_project":
		return toolCreateProject(ctx, args)
	case "update_project":
		return toolUpdateProject(ctx, args)
	case "delete_asset":
		return toolDeleteAsset(ctx, args)
	case "detach_image":
		return toolDetachImage(ctx, args)
	case "clone_voice":
		return toolCloneVoice(ctx, args, base, audioOK)
	}
	return nil, errUnknownTool
}

// ---------- update_shot ----------

type shotUpdArgs struct {
	ShotID         int64           `json:"shot_id"`
	EpisodeID      int64           `json:"episode_id"`
	Code           string          `json:"code"`
	KeyframePrompt *string         `json:"keyframe_prompt"`
	VideoPrompt    *string         `json:"video_prompt"`
	Camera         *string         `json:"camera"`
	Picture        *string         `json:"picture"`
	Dialogue       json.RawMessage `json:"dialogue"`
	Assets         *[]string       `json:"assets"`
	Duration       *float64        `json:"duration"`
	Transition     *string         `json:"transition"`
	RefIDs         *[]int64        `json:"ref_ids"`
	RefImageIDs    *[]int64        `json:"ref_image_ids"`
}

// resolveShotAssets 资产编号 / 名称 → 去重排序后的编号（角色、场景、道具）
func resolveShotAssets(assets []*Asset, refs []string) ([]string, error) {
	byCode, byName := map[string]*Asset{}, map[string]*Asset{}
	var table []string
	for _, as := range assets {
		byCode[as.Code] = as
		byName[as.Name] = as
		table = append(table, as.Code+"="+as.Name)
	}
	codes := []string{}
	seen := map[string]bool{}
	for _, ref := range refs {
		ref = strings.TrimSpace(ref)
		if ref == "" {
			continue
		}
		as := byCode[strings.ToUpper(ref)]
		if as == nil {
			as = byName[ref]
		}
		if as == nil {
			return nil, fmt.Errorf("资产「%s」不存在。资产库：%s。先用 upsert_asset 建好再引用", ref, orDefault(strings.Join(table, "、"), "（空）"))
		}
		if !seen[as.Code] {
			seen[as.Code] = true
			codes = append(codes, as.Code)
		}
	}
	order := map[string]int{"C": 0, "S": 1, "P": 2}
	for x := 1; x < len(codes); x++ {
		for y := x; y > 0 && order[codes[y][:1]] < order[codes[y-1][:1]]; y-- {
			codes[y], codes[y-1] = codes[y-1], codes[y]
		}
	}
	return codes, nil
}

func toolUpdateShot(ctx context.Context, raw json.RawMessage) (any, error) {
	var a shotUpdArgs
	if err := strictArgs(raw, &a); err != nil {
		return nil, err
	}
	s, err := findShot(a.ShotID, a.EpisodeID, a.Code)
	if err != nil {
		return nil, err
	}
	p, e, err := epCtx(s.EpisodeID)
	if err != nil {
		return nil, err
	}
	for _, x := range listShots(s.EpisodeID, true) {
		if x.ID == s.ID {
			s = x
		}
	}
	sets := map[string]any{}
	var changed, warn []string
	if a.KeyframePrompt != nil {
		kp := strings.TrimSpace(*a.KeyframePrompt)
		if runeLen(kp) < 8 {
			return nil, errors.New("keyframe_prompt 太短：写清构图、景别、人物位置与表情、光线")
		}
		if kp != s.KeyframePrompt {
			sets["keyframe_prompt"] = kp
			changed = append(changed, "keyframe_prompt")
			if s.PickedImageID > 0 {
				warn = append(warn, "已有的关键帧不会自动重画；要按新提示词出图请调用 generate_keyframe（旧候选会保留）")
			}
		}
	}
	if a.VideoPrompt != nil {
		vp := strings.TrimSpace(*a.VideoPrompt)
		if vp == "" {
			return nil, errors.New("video_prompt 不能为空")
		}
		if vp != s.VideoPrompt {
			sets["video_prompt"] = vp
			changed = append(changed, "video_prompt")
			if len(s.Videos) > 0 {
				warn = append(warn, "已有的视频不会自动重做；新描述只用于之后提交的视频")
			}
		}
	}
	if a.Camera != nil {
		if c := strings.TrimSpace(*a.Camera); c == "" {
			return nil, errors.New("camera 不能为空")
		} else if c != s.Camera {
			sets["camera"] = c
			changed = append(changed, "camera")
		}
	}
	if a.Picture != nil {
		if c := strings.TrimSpace(*a.Picture); c == "" {
			return nil, errors.New("picture 不能为空")
		} else if c != s.Action {
			sets["action"] = c
			changed = append(changed, "picture")
		}
	}
	dur := s.Duration
	if a.Duration != nil {
		d := math.Round(*a.Duration)
		if d < 5 || d > 10 {
			return nil, fmt.Errorf("duration=%v 不行：视频模型每段只能 5–10 秒", *a.Duration)
		}
		if d != s.Duration {
			sets["duration"] = d
			dur = d
			changed = append(changed, "duration")
			if len(s.Videos) > 0 {
				warn = append(warn, "已有视频的时长不会变；需要的话重新提交视频")
			}
		}
	}
	assets := listAssets(p.ID)
	dialogueChanged := false
	if len(a.Dialogue) > 0 && string(a.Dialogue) != "null" {
		dia, err := renderDialogue(a.Dialogue, s.Sort)
		if err != nil {
			return nil, err
		}
		if dia != s.Dialogue {
			sets["dialogue"] = dia
			dialogueChanged = true
			changed = append(changed, "dialogue")
		}
		chars := 0
		for _, pl := range parseDialogue(dia) {
			chars += runeLen(pl.text)
			if pl.kind == "dialogue" && matchSpeaker(assets, pl.speaker) == nil {
				warn = append(warn, fmt.Sprintf("说话人「%s」不在资产库，配音时找不到音色", pl.speaker))
			}
		}
		if float64(chars) > dur*5 {
			warn = append(warn, fmt.Sprintf("台词 %d 字，%v 秒可能念不完（约每秒 4–5 字）", chars, dur))
		}
	}
	if a.Assets != nil {
		codes, err := resolveShotAssets(assets, *a.Assets)
		if err != nil {
			return nil, err
		}
		if mustJSON(codes) != mustJSON(s.Assets) {
			sets["asset_codes"] = mustJSON(codes)
			changed = append(changed, "assets")
		}
	}
	if a.Transition != nil {
		chain, ok := transitionChain[strings.TrimSpace(*a.Transition)]
		if !ok {
			return nil, errors.New("transition 只能是 cut / continuous / first_last")
		}
		if chain != "off" && prevShot(s) == nil {
			return nil, errors.New("这是本集第一个镜头，没有上一镜头可以衔接，只能 cut")
		}
		if chain != s.Chain {
			sets["chain"] = chain
			changed = append(changed, "transition")
		}
	}
	refs := a.RefIDs
	if refs == nil {
		refs = a.RefImageIDs
	}
	if refs != nil {
		if err := setShotRefs(s, *refs); err != nil {
			return nil, err
		}
		changed = append(changed, "ref_ids")
	}
	for col, v := range sets {
		if _, err := db.Exec(`UPDATE shots SET `+col+`=? WHERE id=?`, v, s.ID); err != nil {
			return nil, err
		}
	}
	if dialogueChanged {
		syncLines(p, e)
	}
	if len(changed) > 0 {
		touchEpisode(e.ID)
	}
	s, _ = getShot(s.ID)
	refIDs := []int64{}
	for _, im := range s.RefImages {
		refIDs = append(refIDs, im.ID)
	}
	var cands, vids int
	_ = db.QueryRow(`SELECT COUNT(*) FROM images WHERE owner_type='shot' AND owner_id=?`, s.ID).Scan(&cands)
	_ = db.QueryRow(`SELECT COUNT(*) FROM videos WHERE shot_id=?`, s.ID).Scan(&vids)
	return map[string]any{"ok": true, "shot_id": s.ID, "code": s.Code, "changed": nonNil(changed), "warnings": nonNil(warn),
		"shot": map[string]any{"duration": s.Duration, "camera": s.Camera, "picture": s.Action, "dialogue": s.Dialogue, "assets": s.Assets,
			"transition": s.Chain, "keyframe_prompt": s.KeyframePrompt, "video_prompt": s.VideoPrompt, "ref_ids": refIDs},
		"kept": fmt.Sprintf("保留了 %d 张关键帧候选（选用 %d）、%d 个视频", cands, s.PickedImageID, vids)}, nil
}

// ---------- view_image ----------

type viewImageArgs struct {
	ImageID  int64   `json:"image_id"`
	ImageIDs []int64 `json:"image_ids"`
	Original bool    `json:"original"`
	MaxEdge  int     `json:"max_edge"`
}

func imageWhere(im *Image) string {
	switch im.OwnerType {
	case "asset":
		if as, err := getAsset(im.OwnerID); err == nil {
			t := map[string]string{"": "设定图", "sheet": "三视图", "expr": "表情表", "ref": "参考照"}[im.Tag]
			sel := ""
			if im.ID == as.PickedImageID || im.ID == as.SheetImageID || im.ID == as.ExprImageID {
				sel = "（选用）"
			}
			return fmt.Sprintf("资产 %s「%s」的%s%s", as.Code, as.Name, orDefault(t, "图"), sel)
		}
		return "资产图"
	case "shot":
		if s, err := getShot(im.OwnerID); err == nil {
			sel := ""
			if s.PickedImageID == im.ID {
				sel = "（选用）"
			}
			return fmt.Sprintf("镜头 #%d 的关键帧%s", s.ID, sel)
		}
		return "关键帧"
	case "cover":
		return "封面"
	case "studio", "ext":
		return "画室"
	}
	return im.OwnerType
}

func toolViewImage(ctx context.Context, raw json.RawMessage) (any, error) {
	var a viewImageArgs
	if err := strictArgs(raw, &a); err != nil {
		return nil, err
	}
	ids := append([]int64{}, a.ImageIDs...)
	if a.ImageID > 0 {
		ids = append([]int64{a.ImageID}, ids...)
	}
	if len(ids) == 0 {
		return nil, errors.New("需要 image_id 或 image_ids")
	}
	if len(ids) > 8 {
		return nil, errors.New("一次最多看 8 张")
	}
	blocks := []any{}
	items := []map[string]any{}
	seen := map[int64]bool{}
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		blk, im, err := imageBlockByID(id, a.MaxEdge, a.Original)
		if err != nil {
			blocks = append(blocks, textBlock(fmt.Sprintf("图片 %d：%s", id, err.Error())))
			continue
		}
		where := imageWhere(im)
		label := fmt.Sprintf("图片编号 %d · %s（%d×%d）", im.ID, where, im.Width, im.Height)
		if im.Model != "" {
			label += " · " + im.Model
		}
		blocks = append(blocks, textBlock(label), blk)
		items = append(items, map[string]any{"image_id": im.ID, "where": where, "width": im.Width, "height": im.Height, "model": im.Model})
	}
	if len(items) == 0 {
		return nil, errors.New("没有找到这些图片")
	}
	return &mcpRich{Blocks: blocks, Structured: map[string]any{"images": items}}, nil
}

// ---------- 音频块：design_voice 候选 / listen_voice ----------

func audioFileBlock(name string) (map[string]any, int, error) {
	name = filepath.Base(name)
	if name == "." || name == "" || strings.Contains(name, "..") {
		return nil, 0, errors.New("样本名不对")
	}
	data, err := os.ReadFile(filepath.Join(dataDir, "audio", name))
	if err != nil {
		return nil, 0, fmt.Errorf("样本 %s 不存在", name)
	}
	mime := audioMimes[strings.ToLower(filepath.Ext(name))]
	if mime == "" {
		mime = "audio/mpeg"
	}
	return map[string]any{"type": "audio", "data": base64.StdEncoding.EncodeToString(data), "mimeType": mime}, len(data), nil
}

// withVoiceAudio 把 design_voice 的结果加上候选音频块（客户端协商的协议不支持音频时只给文字和地址）
func withVoiceAudio(v any, base string, audioOK bool) any {
	m, ok := v.(map[string]any)
	if !ok {
		return v
	}
	cands, _ := m["candidates"].([]map[string]any)
	b, _ := json.MarshalIndent(m, "", "  ")
	text, _ := m["sample_text"].(string)
	clips := []AudioClip{}
	for i, c := range cands {
		name, _ := c["sample"].(string)
		d, _ := c["duration"].(float64)
		clips = append(clips, makeClip(base, name, fmt.Sprintf("候选 %d", i+1), text, d))
	}
	blocks := []any{textBlock(string(b) + "\n\n" + clipsText(clips) + "\n\n听完用 set_voice（asset_id + sample，channel_id 可不填）选定；以后可用 listen_voice / get_audio_link 再听。")}
	if !audioOK {
		return &mcpRich{Blocks: blocks, Structured: m, Linked: true}
	}
	total := 0
	for i, c := range cands {
		name, _ := c["sample"].(string)
		blk, n, err := audioFileBlock(name)
		if err != nil || total+n > 10<<20 {
			continue
		}
		total += n
		blocks = append(blocks, textBlock(fmt.Sprintf("候选 %d · 样本 %s（%.1f 秒）", i+1, name, clips[i].Duration)), blk)
	}
	return &mcpRich{Blocks: blocks, Structured: m, Linked: true}
}

type listenVoiceArgs struct {
	Sample    string   `json:"sample"`
	Samples   []string `json:"samples"`
	AssetID   int64    `json:"asset_id"`
	ProjectID int64    `json:"project_id"`
	LibraryID int64    `json:"library_id"`
	Limit     int      `json:"limit"`
}

func toolListenVoice(ctx context.Context, raw json.RawMessage, base string, audioOK bool) (any, error) {
	var a listenVoiceArgs
	if err := strictArgs(raw, &a); err != nil {
		return nil, err
	}
	type item struct {
		name, label, text string
		dur               float64
	}
	var list []item
	head := ""
	for _, s := range append([]string{a.Sample}, a.Samples...) {
		if s = strings.TrimSpace(s); s != "" {
			list = append(list, item{name: s, label: "样本 " + s})
		}
	}
	info := map[string]any{}
	switch {
	case a.LibraryID > 0:
		l, err := getLibVoice(a.LibraryID)
		if err != nil {
			return nil, err
		}
		head = fmt.Sprintf("音色库 #%d「%s」（%s）", l.ID, l.Name, orDefault(l.Source, l.Mode))
		info["library"] = libBrief(l, base)
		info["voice"] = l.Voice
		if l.Sample != "" {
			list = append(list, item{name: l.Sample, label: "音色样本", text: l.Voice.SampleText})
		}
		if l.Preview != "" {
			list = append(list, item{name: l.Preview, label: "试听"})
		}
		for _, r := range l.Refs {
			if r.Sample != l.Sample {
				list = append(list, item{name: r.Sample, label: fmt.Sprintf("原声参考 %.1f 秒", r.Duration), dur: r.Duration})
			}
		}
		if l.Sample == "" && l.Preview == "" {
			head += "：没有本地样本，用 tts（voice = 上面的 voice 对象）合成一句试听"
		}
	case a.AssetID > 0:
		as, err := getAsset(a.AssetID)
		if err != nil || as.Kind != "character" {
			return nil, errors.New("角色不存在")
		}
		head = fmt.Sprintf("%s「%s」的音色", as.Code, as.Name)
		info["voice"] = as.Voice
		if as.Voice.Sample != "" {
			list = append(list, item{name: as.Voice.Sample, label: "当前音色样本（" + orDefault(as.Voice.Mode, "?") + "）", text: as.Voice.SampleText})
		} else if !as.Voice.empty() {
			head += "：当前音色没有本地样本（" + as.Voice.Mode + "），用 tts（asset_id）合成一句试听"
		} else {
			head += "：还没有音色"
		}
		for i := len(as.VoiceRefs) - 1; i >= 0; i-- {
			r := as.VoiceRefs[i]
			if r.Sample == as.Voice.Sample {
				continue
			}
			l := "原声参考"
			if r.ShotID > 0 {
				l += fmt.Sprintf("（镜头 #%d %.1f–%.1f 秒）", r.ShotID, r.Start, r.End)
			}
			list = append(list, item{name: r.Sample, label: l, dur: r.Duration})
		}
		info["voice_refs"] = as.VoiceRefs
		for i, c := range listVoiceCands(as.ProjectID, as.ID) {
			if c.Sample != as.Voice.Sample {
				list = append(list, item{name: c.Sample, label: fmt.Sprintf("设计候选 %d", i+1), text: c.Text, dur: c.Duration})
			}
		}
	case a.ProjectID > 0:
		p, err := getProject(a.ProjectID)
		if err != nil {
			return nil, errors.New("作品不存在")
		}
		head = "旁白音色"
		info["narrator_voice"] = p.NarratorVoice
		if p.NarratorVoice.Sample != "" {
			list = append(list, item{name: p.NarratorVoice.Sample, label: "旁白音色样本", text: p.NarratorVoice.SampleText})
		} else {
			head += "：没有本地样本，用 tts 合成一句试听"
		}
		for i, c := range listVoiceCands(p.ID, 0) {
			if c.Sample != p.NarratorVoice.Sample {
				list = append(list, item{name: c.Sample, label: fmt.Sprintf("旁白设计候选 %d", i+1), text: c.Text, dur: c.Duration})
			}
		}
	}
	if len(list) == 0 {
		if head != "" {
			return &mcpRich{Blocks: []any{textBlock(head)}, Structured: info}, nil
		}
		return nil, errors.New("需要 sample / samples、asset_id、project_id 或 library_id")
	}
	limit := clampInt(a.Limit, 1, 6)
	if a.Limit == 0 {
		limit = 4
	}
	clips := []AudioClip{}
	seenClip := map[string]bool{}
	for _, it := range list {
		if seenClip[it.name] {
			continue
		}
		seenClip[it.name] = true
		clips = append(clips, makeClip(base, it.name, it.label, it.text, it.dur))
	}
	info["clips"] = clips
	blocks := []any{}
	if head != "" {
		blocks = append(blocks, textBlock(head))
	}
	blocks = append(blocks, textBlock(clipsText(clips)))
	total, sent := 0, 0
	if !audioOK {
		info["sent"] = 0
		return &mcpRich{Blocks: blocks, Structured: info, Linked: true}, nil
	}
	for _, it := range list {
		if sent >= limit {
			break
		}
		blk, n, err := audioFileBlock(it.name)
		if err != nil {
			blocks = append(blocks, textBlock(it.label+"："+err.Error()))
			continue
		}
		if total+n > 10<<20 {
			break
		}
		total += n
		blocks = append(blocks, textBlock(it.label), blk)
		sent++
	}
	info["sent"] = sent
	if sent < len(clips) {
		blocks = append(blocks, textBlock(fmt.Sprintf("附上了 %d 段音频块；其余的按上面的下载地址听，或用 limit 加大（最多 6）。", sent)))
	}
	return &mcpRich{Blocks: blocks, Structured: info, Linked: true}, nil
}

// ---------- create_project / update_project ----------

type projectToolArgs struct {
	ProjectID int64 `json:"project_id"`
	projectIn
}

func checkProjectIn(in projectIn) error {
	oneOf := func(field string, v *string, vals ...string) error {
		if v != nil && !contains(vals, strings.TrimSpace(*v)) {
			return fmt.Errorf("%s 只能是 %s", field, strings.Join(vals, " / "))
		}
		return nil
	}
	for _, e := range []error{
		oneOf("aspect", in.Aspect, "9:16", "16:9", "1:1", "3:4", "4:3"),
		oneOf("audio_mode", in.AudioMode, "native", "dub"),
		oneOf("kind", in.Kind, "manju", "live"),
		oneOf("video_res", in.VideoRes, "768p", "480p"),
		oneOf("deliver", in.Deliver, "source", "1080p"),
	} {
		if e != nil {
			return e
		}
	}
	chk := func(field string, id *int64, kind string) error {
		if id == nil || *id == 0 {
			return nil
		}
		c, err := getChannel(*id)
		if err != nil || c.Kind != kind {
			return fmt.Errorf("%s=%d 不是%s渠道", field, *id, map[string]string{"image": "生图", "text": "文本", "voice": "语音"}[kind])
		}
		if field == "image_channel_id" && in.ImageModel != nil && *in.ImageModel != "" && len(c.Models) > 0 && !contains(c.Models, *in.ImageModel) {
			return fmt.Errorf("渠道「%s」没有模型 %s，可选：%s", c.Name, *in.ImageModel, strings.Join(c.Models, "、"))
		}
		return nil
	}
	for _, e := range []error{chk("image_channel_id", in.ImageChannelID, "image"), chk("text_channel_id", in.TextChannelID, "text"), chk("voice_channel_id", in.VoiceChannelID, "voice")} {
		if e != nil {
			return e
		}
	}
	if in.NarratorVoice != nil {
		return errors.New("旁白音色请用 design_voice / set_voice（project_id）设置")
	}
	return nil
}

func projectBrief(p *Project) map[string]any {
	return map[string]any{"project_id": p.ID, "title": p.Title, "idea": p.Idea, "style": p.Style, "kind": p.Kind, "aspect": p.Aspect,
		"episode_sec": p.EpisodeSec, "shot_target": p.ShotTarget, "episodes_plan": p.EpisodesPlan,
		"image_channel_id": p.ImageChannelID, "image_model": p.ImageModel, "text_channel_id": p.TextChannelID, "text_model": p.TextModel,
		"voice_channel_id": p.VoiceChannelID, "video_sec": p.VideoSec, "video_res": p.VideoRes, "video_turbo": p.VideoTurbo,
		"audio_mode": p.AudioMode, "orig_volume": p.OrigVolume, "takes": p.Takes, "deliver": p.Deliver, "post_upscale": p.PostUpscale, "post_interp": p.PostInterp, "voice_unify": p.VoiceUnify}
}

func toolCreateProject(ctx context.Context, raw json.RawMessage) (any, error) {
	var a projectToolArgs
	if err := strictArgs(raw, &a); err != nil {
		return nil, err
	}
	if a.Title == nil || strings.TrimSpace(*a.Title) == "" {
		return nil, errors.New("需要 title")
	}
	if err := checkProjectIn(a.projectIn); err != nil {
		return nil, err
	}
	res, err := db.Exec(`INSERT INTO projects(title,created_at,updated_at) VALUES(?,?,?)`, strings.TrimSpace(*a.Title), now(), now())
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	if err := applyProject(id, a.projectIn); err != nil {
		return nil, err
	}
	e, _ := insertEpisode(id, "")
	p, _ := getProject(id)
	out := map[string]any{"ok": true, "project": projectBrief(p),
		"next": "作品已建好。外部编剧：先 save_bible（world、bios、outline），再 upsert_asset 建角色，然后按集 save_synopsis / save_script / save_shots。"}
	if e != nil {
		out["episode_id"] = e.ID
	}
	return out, nil
}

func toolUpdateProject(ctx context.Context, raw json.RawMessage) (any, error) {
	var a projectToolArgs
	if err := strictArgs(raw, &a); err != nil {
		return nil, err
	}
	old, err := getProject(a.ProjectID)
	if err != nil {
		return nil, errors.New("作品不存在，先用 list_projects 查作品编号")
	}
	if err := checkProjectIn(a.projectIn); err != nil {
		return nil, err
	}
	if err := applyProject(old.ID, a.projectIn); err != nil {
		return nil, err
	}
	p, _ := getProject(old.ID)
	var warn []string
	var media int
	_ = db.QueryRow(`SELECT COUNT(*) FROM images WHERE project_id=?`, p.ID).Scan(&media)
	if (p.Aspect != old.Aspect || p.Style != old.Style || p.Kind != old.Kind) && media > 0 {
		warn = append(warn, "画幅 / 画风 / 作品类型已改；已有的设定图、关键帧和视频不会自动重做")
	}
	if p.AudioMode == "dub" && old.AudioMode != "dub" {
		warn = append(warn, "已改为配音模式：用 generate_dub 生成各集配音，成片时对白用配音、视频原声按 orig_volume 压低")
	}
	return map[string]any{"ok": true, "project": projectBrief(p), "warnings": nonNil(warn)}, nil
}

// ---------- delete_asset / detach_image ----------

type delAssetArgs struct {
	AssetID   int64  `json:"asset_id"`
	ProjectID int64  `json:"project_id"`
	Code      string `json:"code"`
	Confirm   bool   `json:"confirm"`
}

// removeCode 从资产编号列表里去掉一个
func removeCode(codes []string, code string) ([]string, bool) {
	out := []string{}
	hit := false
	for _, c := range codes {
		if c == code {
			hit = true
			continue
		}
		out = append(out, c)
	}
	return out, hit
}

func toolDeleteAsset(ctx context.Context, raw json.RawMessage) (any, error) {
	var a delAssetArgs
	if err := strictArgs(raw, &a); err != nil {
		return nil, err
	}
	var as *Asset
	if a.AssetID > 0 {
		as, _ = getAsset(a.AssetID)
	} else if a.ProjectID > 0 && strings.TrimSpace(a.Code) != "" {
		for _, x := range listAssets(a.ProjectID) {
			if strings.EqualFold(x.Code, strings.TrimSpace(a.Code)) {
				as = x
			}
		}
	}
	if as == nil {
		return nil, errors.New("资产不存在：给 asset_id，或 project_id + code")
	}
	ims := listImages(`WHERE owner_type='asset' AND owner_id=?`, as.ID)
	type hit struct {
		s     *Shot
		codes []string
	}
	var hits []hit
	var where []string
	for _, e := range listEpisodes(as.ProjectID) {
		for _, s := range listShots(e.ID, false) {
			if rest, ok := removeCode(s.Assets, as.Code); ok {
				hits = append(hits, hit{s, rest})
				where = append(where, fmt.Sprintf("第%d集镜头%s", e.No, s.Code))
			}
		}
	}
	var derived []string
	for _, x := range listAssets(as.ProjectID) {
		if x.BaseID == as.ID {
			derived = append(derived, x.Code+" "+x.Name)
		}
	}
	plan := map[string]any{"asset_id": as.ID, "code": as.Code, "name": as.Name, "images": len(ims), "used_in_shots": nonNil(where), "same_person_variants": nonNil(derived)}
	if !a.Confirm {
		plan["ok"] = false
		plan["note"] = fmt.Sprintf("还没删除。会删掉「%s」和它的 %d 张图，并从 %d 个镜头的资产列表里去掉它；确认请再调用一次并传 confirm=true", as.Name, len(ims), len(hits))
		return plan, nil
	}
	for _, im := range ims {
		deleteImageRow(im)
	}
	for _, h := range hits {
		_, _ = db.Exec(`UPDATE shots SET asset_codes=? WHERE id=?`, mustJSON(h.codes), h.s.ID)
	}
	_, _ = db.Exec(`DELETE FROM assets WHERE id=?`, as.ID)
	_, _ = db.Exec(`DELETE FROM voice_cands WHERE asset_id=?`, as.ID)
	_, _ = db.Exec(`UPDATE assets SET base_id=0 WHERE base_id=?`, as.ID)
	touchProject(as.ProjectID)
	plan["ok"] = true
	plan["note"] = "已删除"
	return plan, nil
}

type detachArgs struct {
	AssetID int64 `json:"asset_id"`
	ImageID int64 `json:"image_id"`
	Confirm bool  `json:"confirm"`
}

func toolDetachImage(ctx context.Context, raw json.RawMessage) (any, error) {
	var a detachArgs
	if err := strictArgs(raw, &a); err != nil {
		return nil, err
	}
	as, err := getAsset(a.AssetID)
	if err != nil {
		return nil, errors.New("资产不存在")
	}
	im, err := getImage(a.ImageID)
	if err != nil {
		return nil, errors.New("图片不存在")
	}
	if im.OwnerType != "asset" || im.OwnerID != as.ID {
		return nil, fmt.Errorf("图片 %d 不属于资产 %s「%s」（用 view_asset_images 看它的图片编号）", im.ID, as.Code, as.Name)
	}
	slot := ""
	switch im.ID {
	case as.PickedImageID:
		slot = "设定图"
	case as.SheetImageID:
		slot = "三视图"
	case as.ExprImageID:
		slot = "表情表"
	}
	what := map[string]string{"": "设定图候选", "sheet": "三视图候选", "expr": "表情表候选", "ref": "参考照"}[im.Tag]
	if !a.Confirm {
		note := fmt.Sprintf("还没拿掉。图片 %d 是「%s」的%s", im.ID, as.Name, what)
		if slot != "" {
			note += "，正在作为选用的" + slot + "，拿掉后这个位置会变空"
		}
		return map[string]any{"ok": false, "note": note + "；确认请再调用一次并传 confirm=true"}, nil
	}
	_, _ = db.Exec(`UPDATE assets SET picked_image_id=0 WHERE picked_image_id=?`, im.ID)
	_, _ = db.Exec(`UPDATE assets SET sheet_image_id=0 WHERE sheet_image_id=?`, im.ID)
	_, _ = db.Exec(`UPDATE assets SET expr_image_id=0 WHERE expr_image_id=?`, im.ID)
	// 镜头的手动参考图里也去掉它
	rows, _ := db.Query(`SELECT id, ref_ids FROM shots WHERE project_id=? AND ref_ids LIKE ?`, as.ProjectID, fmt.Sprintf("%%%d%%", im.ID))
	type fix struct {
		id  int64
		ids []int64
	}
	var fixes []fix
	if rows != nil {
		for rows.Next() {
			var sid int64
			var js string
			var ids []int64
			if rows.Scan(&sid, &js) == nil && json.Unmarshal([]byte(js), &ids) == nil {
				out := []int64{}
				for _, x := range ids {
					if x != im.ID {
						out = append(out, x)
					}
				}
				if len(out) != len(ids) {
					fixes = append(fixes, fix{sid, out})
				}
			}
		}
		rows.Close()
	}
	for _, f := range fixes {
		_, _ = db.Exec(`UPDATE shots SET ref_ids=? WHERE id=?`, mustJSON(f.ids), f.id)
	}
	deleteImageRow(im)
	touchProject(as.ProjectID)
	out := map[string]any{"ok": true, "asset_id": as.ID, "image_id": im.ID, "removed": what}
	if slot != "" {
		out["note"] = slot + "位置现在是空的：用 pick_image 选另一张，或 generate_asset_image 重出"
	}
	return out, nil
}

// ---------- clone_voice ----------

type cloneArgs struct {
	AssetID     int64    `json:"asset_id"`
	ShotID      int64    `json:"source_shot_id"`
	VideoID     int64    `json:"video_id"`
	UploadID    string   `json:"upload_id"`
	AudioB64    string   `json:"audio_base64"`
	AudioURL    string   `json:"audio_url"`
	Start       *float64 `json:"start"`
	End         *float64 `json:"end"`
	ChannelID   int64    `json:"channel_id"`
	SampleText  string   `json:"sample_text"`
	Description string   `json:"description"`
	Bind        *bool    `json:"bind"`
	Clean       *bool    `json:"clean"`
	SaveLib     bool     `json:"save_to_library"`
	LibName     string   `json:"library_name"`
}

const cloneMaxSec = 60.0

// clipArgs ffmpeg 截取参数：-ss start -t (end-start)，最长 60 秒
func clipArgs(start, end *float64) ([]string, float64, float64, error) {
	s, e := 0.0, 0.0
	if start != nil {
		s = *start
	}
	if end != nil {
		e = *end
	}
	if s < 0 || e < 0 {
		return nil, 0, 0, errors.New("start / end 不能是负数")
	}
	if end != nil && e <= s {
		return nil, 0, 0, errors.New("end 要大于 start")
	}
	d := cloneMaxSec
	if end != nil {
		d = math.Min(e-s, cloneMaxSec)
	}
	var args []string
	if s > 0 {
		args = append(args, "-ss", fmt.Sprintf("%.3f", s))
	}
	return args, s, d, nil
}

// extractVoiceWav 从媒体文件里截一段，转成 24k 单声道 wav；clean=true 时自动裁首尾静音并做响度归一
func extractVoiceWav(in string, start, end *float64, clean bool) ([]byte, float64, float64, error) {
	pre, s, d, err := clipArgs(start, end)
	if err != nil {
		return nil, 0, 0, err
	}
	tmp, err := os.MkdirTemp(filepath.Join(dataDir, "tmp"), "clone-")
	if err != nil {
		return nil, 0, 0, err
	}
	defer os.RemoveAll(tmp)
	out := filepath.Join(tmp, "out.wav")
	runExtract := func(af string) ([]byte, float64, error) {
		args := append([]string{"-y", "-v", "error"}, pre...)
		args = append(args, "-i", in, "-t", fmt.Sprintf("%.3f", d), "-vn")
		if af != "" {
			args = append(args, "-af", af)
		}
		args = append(args, "-ac", "1", "-ar", "24000", "-c:a", "pcm_s16le", out)
		if b, err := exec.Command("ffmpeg", args...).CombinedOutput(); err != nil {
			return nil, 0, fmt.Errorf("抽取音频失败（视频可能没有声音）：%s", lastLines(string(b), 1))
		}
		ci, _ := probe(out)
		data, err := os.ReadFile(out)
		if err != nil || len(data) < 1000 {
			return nil, 0, errors.New("抽出来的音频是空的（视频可能没有声音，或 start 超出了时长）")
		}
		return data, ci.dur, nil
	}
	if clean {
		const trimNorm = "silenceremove=start_periods=1:start_threshold=-40dB,areverse,silenceremove=start_periods=1:start_threshold=-40dB,areverse,loudnorm"
		data, dur, err := runExtract(trimNorm)
		if err == nil && dur >= 1.5 {
			return data, dur, s, nil
		}
		// 极轻音量被静音检测误裁太短时，退回仅响度归一
		data, dur, err = runExtract("loudnorm")
		if err == nil {
			return data, dur, s, nil
		}
	}
	data, dur, err := runExtract("")
	if err != nil {
		return nil, 0, 0, err
	}
	return data, dur, s, nil
}

func fetchAudioURL(ctx context.Context, u string) ([]byte, error) {
	if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
		return nil, errors.New("audio_url 要是 http(s) 地址")
	}
	c, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(c, "GET", u, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, errors.New("下载音频失败：" + shortErr(err))
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("下载音频失败：HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 40<<20+1))
	if err != nil {
		return nil, errors.New("下载音频失败")
	}
	if len(data) > 40<<20 {
		return nil, errors.New("音频超过 40 MB")
	}
	return data, nil
}

// cloneChannel 克隆用哪个语音渠道：指定的 > 角色当前用的 Fish > 小米 MiMo > Fish
func cloneChannel(id int64, cur VoiceSpec) (*Channel, error) {
	if id > 0 {
		ch, err := voiceChannel(id)
		if err != nil || (ch.APIType != "mimo" && ch.APIType != "fish") {
			return nil, errors.New("克隆音色的渠道需要是小米 MiMo 或 Fish Audio")
		}
		return ch, nil
	}
	if cur.ChannelID > 0 {
		if ch, err := voiceChannel(cur.ChannelID); err == nil && ch.APIType == "fish" {
			return ch, nil
		}
	}
	if ch := firstVoiceChannel("mimo"); ch != nil {
		return ch, nil
	}
	if ch := firstVoiceChannel("fish"); ch != nil {
		return ch, nil
	}
	return nil, errors.New("克隆音色需要一个小米 MiMo 或 Fish Audio 语音渠道")
}

func toolCloneVoice(ctx context.Context, raw json.RawMessage, base string, audioOK bool) (any, error) {
	var a cloneArgs
	if err := strictArgs(raw, &a); err != nil {
		return nil, err
	}
	as, err := getAsset(a.AssetID)
	if err != nil || as.Kind != "character" {
		return nil, errors.New("角色不存在")
	}
	srcN := 0
	for _, x := range []bool{a.ShotID > 0 || a.VideoID > 0, strings.TrimSpace(a.UploadID) != "", strings.TrimSpace(a.AudioB64) != "", strings.TrimSpace(a.AudioURL) != ""} {
		if x {
			srcN++
		}
	}
	if srcN != 1 {
		return nil, errors.New("音频来源四选一：source_shot_id（可配 video_id）、upload_id、audio_base64、audio_url")
	}
	ref := VoiceRef{CreatedAt: now()}
	var inPath string
	text := strings.TrimSpace(a.SampleText)
	switch {
	case strings.TrimSpace(a.UploadID) != "":
		p, _, err := resolveUpload(a.UploadID)
		if err != nil {
			return nil, err
		}
		inPath = p
		ref.Source = "upload"
	case a.ShotID > 0 || a.VideoID > 0:
		var v *Video
		if a.VideoID > 0 {
			v, err = getVideo(a.VideoID)
			if err != nil || v.Status != "done" || v.File == "" {
				return nil, errors.New("这个视频不存在或还没做好")
			}
			if a.ShotID > 0 && v.ShotID != a.ShotID {
				return nil, errors.New("video_id 不属于这个镜头")
			}
		} else {
			s, err := getShot(a.ShotID)
			if err != nil {
				return nil, errors.New("镜头不存在")
			}
			for _, x := range listShots(s.EpisodeID, true) {
				if x.ID == s.ID {
					s = x
				}
			}
			if v = shotVideoFor(s); v == nil {
				return nil, errors.New("这个镜头还没有做好的视频")
			}
		}
		if v.ProjectID != as.ProjectID {
			return nil, errors.New("这个视频不在角色所在的作品里")
		}
		inPath = filepath.Join(dataDir, "videos", v.File)
		ref.Source, ref.ShotID, ref.VideoID = "shot", v.ShotID, v.ID
		if text == "" && a.Start == nil && a.End == nil {
			var said []string
			for _, l := range listLines(`WHERE shot_id=? ORDER BY sort, id`, v.ShotID) {
				if l.Speaker == as.Name || l.AssetID == as.ID {
					said = append(said, l.Text)
				}
			}
			text = strings.Join(said, "")
		}
	default:
		var data []byte
		if strings.TrimSpace(a.AudioB64) != "" {
			s := strings.TrimSpace(a.AudioB64)
			if i := strings.Index(s, ","); strings.HasPrefix(s, "data:") && i > 0 {
				s = s[i+1:]
			}
			data, err = base64.StdEncoding.DecodeString(strings.Join(strings.Fields(s), ""))
			if err != nil {
				return nil, errors.New("audio_base64 不是有效的 base64")
			}
			ref.Source = "upload"
		} else {
			data, err = fetchAudioURL(ctx, strings.TrimSpace(a.AudioURL))
			if err != nil {
				return nil, err
			}
			ref.Source = "url"
		}
		tmp, err := os.MkdirTemp(filepath.Join(dataDir, "tmp"), "clonein-")
		if err != nil {
			return nil, err
		}
		defer os.RemoveAll(tmp)
		inPath = filepath.Join(tmp, "in")
		if err := os.WriteFile(inPath, data, 0o644); err != nil {
			return nil, err
		}
	}
	clean := a.Clean == nil || *a.Clean
	wav, dur, start, err := extractVoiceWav(inPath, a.Start, a.End, clean)
	if err != nil {
		return nil, err
	}
	if dur < 3 {
		return nil, fmt.Errorf("这段音频只有 %.1f 秒，至少要 3 秒（10 秒以上效果更好）", dur)
	}
	ch, err := cloneChannel(a.ChannelID, as.Voice)
	if err != nil {
		return nil, err
	}
	name, dur, err := saveAudio("vc", wav, "wav")
	if err != nil {
		return nil, err
	}
	if text != "" {
		_ = os.WriteFile(filepath.Join(dataDir, "audio", name+".txt"), []byte(text), 0o644)
	}
	ref.Sample, ref.Duration, ref.Start = name, dur, start
	if a.End != nil {
		ref.End = start + dur
	}
	var warn []string
	if dur < 8 {
		warn = append(warn, fmt.Sprintf("样本 %.1f 秒，偏短；10 秒以上的干净对白效果更好", dur))
	}
	if ref.Source == "shot" {
		warn = append(warn, "视频原声里的背景音乐 / 环境声 / 其他人的声音没有去除；听一下样本，不干净就换一段（start / end）或处理后用 audio_base64 传")
	}
	title := as.Name + "的声音（克隆）"
	spec := VoiceSpec{ChannelID: ch.ID, Mode: "clone", Title: title, Desc: orDefault(strings.TrimSpace(a.Description), as.Voice.Desc), Sample: name, SampleText: text, Speed: 1}
	if as.Voice.Speed > 0 {
		spec.Speed = as.Voice.Speed
	}
	spec.Style = as.Voice.Style
	bind := a.Bind == nil || *a.Bind
	if bind && ch.APIType == "fish" {
		c, cancel := context.WithTimeout(ctx, 4*time.Minute)
		defer cancel()
		id, err := fishModelFromSample(c, ch, name, title)
		if err != nil {
			return nil, fmt.Errorf("样本已存为 %s，但在 Fish 建音色失败：%s", name, err.Error())
		}
		spec.Voice = id
	}
	refs := append(as.VoiceRefs, ref)
	if len(refs) > 12 {
		refs = refs[len(refs)-12:]
	}
	_, _ = db.Exec(`UPDATE assets SET voice_refs=? WHERE id=?`, mustJSON(refs), as.ID)
	if bind {
		_, _ = db.Exec(`UPDATE assets SET voice=? WHERE id=?`, mustJSON(spec), as.ID)
	}
	touchProject(as.ProjectID)
	out := map[string]any{"ok": true, "asset_id": as.ID, "name": as.Name, "sample": name, "url": base + "/v1/audio/" + name, "duration": dur,
		"channel_id": ch.ID, "channel": ch.APIType, "bound": bind, "voice_ref": ref, "warnings": nonNil(warn)}
	if bind {
		out["voice"] = spec
		out["next"] = "已设为角色音色：之后配音都用它。用 tts（asset_id）念一句试听，或 generate_dub 重做配音（force=true）。"
	} else {
		out["next"] = "只存进了原声参考，没改角色音色；要用它请再调用并 bind=true。"
	}
	if a.SaveLib {
		c, cancel := context.WithTimeout(ctx, 2*time.Minute)
		l, exists, err := saveLibVoice(c, libSaveIn{Voice: &spec, AssetID: as.ID, Name: strings.TrimSpace(a.LibName), Source: "clone"}, []VoiceRef{ref})
		cancel()
		if err != nil {
			warn = append(warn, "收进音色库失败："+err.Error())
			out["warnings"] = nonNil(warn)
		} else {
			out["library_id"], out["library_exists"] = l.ID, exists
		}
	}
	out["download_url"] = audioDLURL(base, name)
	b, _ := json.MarshalIndent(out, "", "  ")
	blocks := []any{textBlock(string(b) + "\n\n" + clipsText([]AudioClip{makeClip(base, name, "截取的样本", text, dur)}))}
	if audioOK {
		if blk, _, err := audioFileBlock(name); err == nil {
			blocks = append(blocks, textBlock("截取的样本"), blk)
		}
	}
	return &mcpRich{Blocks: blocks, Structured: out, Linked: true}, nil
}
