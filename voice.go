package main

// 配音：语音渠道（Fish Audio / 小米 MiMo / OpenAI 兼容），角色音色，逐句台词生成。
// Fish：POST {base}/v1/tts（请求头 model），音色 = reference_id；GET/POST/PATCH/DELETE {base}/model 管理音色模型。
// MiMo：POST {base}/chat/completions，assistant 消息是要念的文字，user 消息是语气指令；
//       内置音色 mimo-v2.5-tts；设计音色 mimo-v2.5-tts-voicedesign 出试听样本，保存后用 mimo-v2.5-tts-voiceclone 复用样本。
// OpenAI 兼容：POST {base}/audio/speech。

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

type VoiceSpec struct {
	ChannelID  int64   `json:"channel_id,omitempty"`
	Mode       string  `json:"mode,omitempty"` // fish / builtin / design / clone / openai
	Voice      string  `json:"voice,omitempty"`
	Title      string  `json:"title,omitempty"`
	Desc       string  `json:"desc,omitempty"`
	Sample     string  `json:"sample,omitempty"`
	SampleText string  `json:"sample_text,omitempty"`
	Cover      string  `json:"cover,omitempty"`
	Preview    string  `json:"preview,omitempty"`
	Speed      float64 `json:"speed,omitempty"`
	Pitch      float64 `json:"pitch,omitempty"`
	Style      string  `json:"style,omitempty"`
}

func (v VoiceSpec) empty() bool { return v.Mode == "" || (v.Voice == "" && v.Sample == "") }

func (v VoiceSpec) sig(text, emotion string) string {
	h := sha1.Sum([]byte(mustJSON(v) + "|" + text + "|" + emotion))
	return hex.EncodeToString(h[:8])
}

// cleanLineAudioFiles 删除匹配台词记录在磁盘上对应的配音文件（避免重写/删分镜留下孤儿音频）
func cleanLineAudioFiles(where string, args ...any) {
	rows, err := db.Query(`SELECT audio_file FROM lines `+where, args...)
	if err != nil {
		return
	}
	var files []string
	for rows.Next() {
		var f string
		if rows.Scan(&f) == nil && f != "" {
			files = append(files, f)
		}
	}
	rows.Close()
	for _, f := range files {
		_ = os.Remove(filepath.Join(dataDir, "audio", filepath.Base(f)))
	}
}

var ttsHTTP = &http.Client{Timeout: 3 * time.Minute}

const fishDefaultBase = "https://api.fish.audio"

func fishBase(ch *Channel) string {
	b := normBase(ch.BaseURL)
	if b == "" {
		b = fishDefaultBase
	}
	return strings.TrimSuffix(b, "/v1")
}

func fishModel(ch *Channel) string {
	if len(ch.Models) > 0 && ch.Models[0] != "" {
		return ch.Models[0]
	}
	return "s2.1-pro"
}

// Fish 的情绪标记：S2 用 [tag]，S1 用 (tag)
var fishEmotions = []struct{ words, tag string }{
	{"压低|小声|耳语|悄悄", "whispering"}, {"大喊|喊|吼|怒吼", "shouting"}, {"尖叫", "screaming"},
	{"愤怒|生气|怒", "angry"}, {"开心|高兴|欢快|喜", "happy"}, {"悲伤|难过|伤心|哽咽", "sad"}, {"哭", "sobbing"},
	{"惊讶|震惊|吃惊", "surprised"}, {"害怕|恐惧|颤抖", "scared"}, {"紧张|慌", "nervous"}, {"兴奋|激动", "excited"},
	{"冷笑|讽刺|嘲讽", "sarcastic"}, {"不屑|轻蔑", "disdainful"}, {"温柔|轻柔", "soft tone"}, {"平静|冷静|淡淡", "calm"},
	{"叹气|叹息", "sighing"}, {"笑", "chuckling"}, {"坚定|决绝", "determined"}, {"疲惫|虚弱", "panting"},
}

func fishTag(emotion, model string) string {
	e := strings.TrimSpace(emotion)
	if e == "" {
		return ""
	}
	for _, x := range fishEmotions {
		for _, w := range strings.Split(x.words, "|") {
			if strings.Contains(e, w) {
				if model == "s1" {
					return "(" + x.tag + ")"
				}
				return "[" + x.tag + "]"
			}
		}
	}
	return ""
}

func upstreamErr(resp *http.Response, raw []byte) error {
	msg := upstreamMsg(raw)
	var e struct {
		Message string `json:"message"`
		Detail  any    `json:"detail"`
	}
	if json.Unmarshal(raw, &e) == nil && e.Message != "" {
		msg = e.Message
	}
	return fmt.Errorf("语音渠道返回 %d：%s", resp.StatusCode, msg)
}

func setVoiceAuth(req *http.Request, ch *Channel) {
	if ch.APIKey == "" {
		return
	}
	req.Header.Set("Authorization", "Bearer "+ch.APIKey)
	if ch.APIType == "mimo" {
		req.Header.Set("api-key", ch.APIKey)
	}
}

// synthesize 合成一句话，返回音频字节与扩展名
func synthesize(ctx context.Context, ch *Channel, v VoiceSpec, text, emotion string) ([]byte, string, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, "", errors.New("没有要念的文字")
	}
	switch ch.APIType {
	case "fish":
		if v.Voice == "" && v.Sample != "" {
			// 设计出来的样本：第一次用时在 Fish 建私有音色
			id, err := fishModelFromSample(ctx, ch, v.Sample, v.Title)
			if err != nil {
				return nil, "", err
			}
			v.Voice = id
		}
		if v.Voice == "" {
			return nil, "", errors.New("还没有选 Fish 音色")
		}
		model := fishModel(ch)
		body := map[string]any{"text": strings.TrimSpace(fishTag(emotion, model) + " " + text), "reference_id": v.Voice, "format": "mp3", "mp3_bitrate": 128, "normalize": true, "latency": "normal"}
		if v.Speed > 0 && math.Abs(v.Speed-1) > 0.01 {
			body["prosody"] = map[string]any{"speed": clampF(v.Speed, 0.5, 2), "volume": 0}
		}
		b, _ := json.Marshal(body)
		req, _ := http.NewRequestWithContext(ctx, "POST", fishBase(ch)+"/v1/tts", bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("model", model)
		setVoiceAuth(req, ch)
		return doAudio(req, "mp3")
	case "mimo":
		model := "mimo-v2.5-tts"
		audio := map[string]any{"format": "wav"}
		switch v.Mode {
		case "builtin":
			audio["voice"] = orDefault(v.Voice, "mimo_default")
		case "design", "clone":
			if v.Sample == "" {
				return nil, "", errors.New("音色样本不存在")
			}
			data, err := os.ReadFile(filepath.Join(dataDir, "audio", v.Sample))
			if err != nil {
				return nil, "", errors.New("音色样本文件丢失")
			}
			if len(data) > 10<<20 {
				return nil, "", errors.New("音色样本超过 10 MB")
			}
			mime := "audio/wav"
			if strings.HasSuffix(v.Sample, ".mp3") {
				mime = "audio/mpeg"
			}
			model = "mimo-v2.5-tts-voiceclone"
			audio["voice"] = "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data)
		default:
			return nil, "", errors.New("这个音色不属于该语音渠道")
		}
		var instr []string
		if s := strings.TrimSpace(v.Style); s != "" {
			instr = append(instr, s)
		}
		if e := strings.TrimSpace(emotion); e != "" {
			instr = append(instr, "这句话的语气："+e)
		}
		msgs := []any{}
		if len(instr) > 0 {
			msgs = append(msgs, map[string]any{"role": "user", "content": strings.Join(instr, "。")})
		}
		msgs = append(msgs, map[string]any{"role": "assistant", "content": text})
		data, err := mimoCall(ctx, ch, model, msgs, audio)
		return data, "wav", err
	case "openai":
		model := "tts-1"
		if len(ch.Models) > 0 {
			model = ch.Models[0]
		}
		body := map[string]any{"model": model, "input": text, "voice": orDefault(v.Voice, "alloy"), "response_format": "mp3"}
		if v.Speed > 0 && math.Abs(v.Speed-1) > 0.01 {
			body["speed"] = clampF(v.Speed, 0.25, 4)
		}
		if e := strings.TrimSpace(emotion); e != "" {
			body["instructions"] = "用这种语气说：" + e
		}
		b, _ := json.Marshal(body)
		req, _ := http.NewRequestWithContext(ctx, "POST", apiURL(ch.BaseURL, "/audio/speech"), bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
		setVoiceAuth(req, ch)
		return doAudio(req, "mp3")
	}
	return nil, "", errors.New("不支持的语音渠道类型")
}

func doAudio(req *http.Request, ext string) ([]byte, string, error) {
	var resp *http.Response
	var err error
	for i := 0; i < 2; i++ {
		resp, err = ttsHTTP.Do(req)
		if err == nil && resp.StatusCode < 500 && resp.StatusCode != 429 {
			break
		}
		if err == nil {
			resp.Body.Close()
		}
		if req.Context().Err() != nil || req.GetBody == nil {
			break
		}
		time.Sleep(2 * time.Second)
		req.Body, _ = req.GetBody()
	}
	if err != nil {
		return nil, "", errors.New("连接语音渠道失败：" + shortErr(err))
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if resp.StatusCode >= 300 {
		return nil, "", upstreamErr(resp, raw)
	}
	if strings.Contains(resp.Header.Get("Content-Type"), "json") {
		return nil, "", errors.New("语音渠道没有返回音频：" + upstreamMsg(raw))
	}
	if len(raw) < 100 {
		return nil, "", errors.New("语音渠道返回的音频是空的")
	}
	return raw, ext, nil
}

func mimoCall(ctx context.Context, ch *Channel, model string, msgs []any, audio map[string]any) ([]byte, error) {
	body := map[string]any{"model": model, "messages": msgs, "audio": audio}
	b, _ := json.Marshal(body)
	req, _ := http.NewRequestWithContext(ctx, "POST", apiURL(orDefault(ch.BaseURL, "https://api.xiaomimimo.com/v1"), "/chat/completions"), bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	setVoiceAuth(req, ch)
	resp, err := ttsHTTP.Do(req)
	if err != nil {
		return nil, errors.New("连接语音渠道失败：" + shortErr(err))
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if resp.StatusCode >= 300 {
		return nil, upstreamErr(resp, raw)
	}
	var out struct {
		Choices []struct {
			Message struct {
				Audio struct {
					Data string `json:"data"`
				} `json:"audio"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || len(out.Choices) == 0 || out.Choices[0].Message.Audio.Data == "" {
		return nil, errors.New("语音渠道没有返回音频：" + upstreamMsg(raw))
	}
	data, err := base64.StdEncoding.DecodeString(out.Choices[0].Message.Audio.Data)
	if err != nil {
		return nil, errors.New("音频解码失败")
	}
	return data, nil
}

// ---------- 本地后处理：语速 / 音调 ----------

func atempoChain(f float64) string {
	var parts []string
	for f > 2 {
		parts = append(parts, "atempo=2")
		f /= 2
	}
	for f < 0.5 {
		parts = append(parts, "atempo=0.5")
		f /= 0.5
	}
	parts = append(parts, fmt.Sprintf("atempo=%.4f", f))
	return strings.Join(parts, ",")
}

// postProcess 没有原生语速参数的渠道在本地变速；音调统一在本地处理。输出 mp3。
func postProcess(data []byte, ext string, nativeSpeed bool, v VoiceSpec) ([]byte, string, error) {
	var filters []string
	if v.Pitch != 0 {
		f := math.Pow(2, clampF(v.Pitch, -12, 12)/12)
		filters = append(filters, fmt.Sprintf("aresample=44100,asetrate=%.0f,aresample=44100,%s", 44100*f, atempoChain(1/f)))
	}
	if !nativeSpeed && v.Speed > 0 && math.Abs(v.Speed-1) > 0.01 {
		filters = append(filters, atempoChain(clampF(v.Speed, 0.5, 2)))
	}
	if len(filters) == 0 && ext == "mp3" {
		return data, ext, nil
	}
	tmp, err := os.MkdirTemp(filepath.Join(dataDir, "tmp"), "tts-")
	if err != nil {
		return nil, "", err
	}
	defer os.RemoveAll(tmp)
	in := filepath.Join(tmp, "in."+ext)
	out := filepath.Join(tmp, "out.mp3")
	_ = os.WriteFile(in, data, 0o644)
	args := []string{"-y", "-v", "error", "-i", in}
	if len(filters) > 0 {
		args = append(args, "-af", strings.Join(filters, ","))
	}
	args = append(args, "-ac", "1", "-ar", "44100", "-b:a", "160k", out)
	if b, err := exec.Command("ffmpeg", args...).CombinedOutput(); err != nil {
		return nil, "", fmt.Errorf("音频处理失败：%s", lastLines(string(b), 2))
	}
	d, err := os.ReadFile(out)
	return d, "mp3", err
}

func saveAudio(prefix string, data []byte, ext string) (string, float64, error) {
	name := fmt.Sprintf("%s-%d-%s.%s", prefix, time.Now().Unix(), randHex(4), ext)
	p := filepath.Join(dataDir, "audio", name)
	if err := os.WriteFile(p, data, 0o644); err != nil {
		return "", 0, err
	}
	ci, _ := probe(p)
	return name, ci.dur, nil
}

// speakTo 合成 + 后处理 + 落盘
func speakTo(ctx context.Context, v VoiceSpec, text, emotion, prefix string) (string, float64, error) {
	ch, err := voiceChannel(v.ChannelID)
	if err != nil {
		return "", 0, err
	}
	data, ext, err := synthesize(ctx, ch, v, text, emotion)
	if err != nil {
		return "", 0, err
	}
	data, ext, err = postProcess(data, ext, ch.APIType != "mimo", v)
	if err != nil {
		return "", 0, err
	}
	return saveAudio(prefix, data, ext)
}

func voiceChannel(id int64) (*Channel, error) {
	if id > 0 {
		if ch, err := getChannel(id); err == nil && ch.Kind == "voice" {
			return ch, nil
		}
	}
	return nil, errors.New("音色所在的语音渠道不存在，请重新选择音色")
}

func firstVoiceChannel(apiType string) *Channel {
	cs, _ := listChannels()
	for _, c := range cs {
		if c.Kind == "voice" && (apiType == "" || c.APIType == apiType) {
			return c
		}
	}
	return nil
}

// ---------- 台词 ----------

type Line struct {
	ID        int64   `json:"id"`
	ProjectID int64   `json:"project_id"`
	EpisodeID int64   `json:"episode_id"`
	ShotID    int64   `json:"shot_id"`
	Sort      int     `json:"sort"`
	Speaker   string  `json:"speaker"`
	AssetID   int64   `json:"asset_id"`
	Kind      string  `json:"kind"`
	Text      string  `json:"text"`
	Emotion   string  `json:"emotion"`
	AudioFile string  `json:"-"`
	AudioURL  string  `json:"audio_url"`
	Duration  float64 `json:"duration"`
	Status    string  `json:"status"`
	Error     string  `json:"error"`
	Sig       string  `json:"-"`
	Stale     bool    `json:"stale"`
}

const lineCols = `id,project_id,episode_id,shot_id,sort,speaker,asset_id,kind,text,emotion,audio_file,duration,status,error,sig`

func listLines(where string, args ...any) []*Line {
	rows, err := db.Query(`SELECT `+lineCols+` FROM lines `+where, args...)
	if err != nil {
		return []*Line{}
	}
	defer rows.Close()
	out := []*Line{}
	for rows.Next() {
		var l Line
		if rows.Scan(&l.ID, &l.ProjectID, &l.EpisodeID, &l.ShotID, &l.Sort, &l.Speaker, &l.AssetID, &l.Kind, &l.Text, &l.Emotion, &l.AudioFile, &l.Duration, &l.Status, &l.Error, &l.Sig) == nil {
			if l.AudioFile != "" {
				l.AudioURL = "/files/audio/" + l.AudioFile
			}
			out = append(out, &l)
		}
	}
	return out
}

func getLine(id int64) (*Line, error) {
	ls := listLines(`WHERE id=?`, id)
	if len(ls) == 0 {
		return nil, errors.New("台词不存在")
	}
	return ls[0], nil
}

var emoRe = regexp.MustCompile(`^[（(]([^）)]{1,16})[）)]\s*`)

type parsedLine struct{ speaker, kind, text, emotion string }

// parseDialogue 「角色名：（语气）台词」→ 逐句；旁白单独标出
func parseDialogue(d string) []parsedLine {
	var out []parsedLine
	for _, ln := range strings.Split(d, "\n") {
		ln = strings.TrimSpace(ln)
		if ln == "" {
			continue
		}
		pl := parsedLine{kind: "dialogue"}
		if m := speakerRe.FindString(ln); m != "" {
			sp := strings.TrimSpace(strings.TrimRight(strings.TrimSpace(m), "：:"))
			sp = strings.Trim(sp, "（()）")
			pl.speaker = sp
			ln = strings.TrimSpace(ln[len(m):])
		} else if strings.HasPrefix(ln, "（旁白）") || strings.HasPrefix(ln, "(旁白)") {
			pl.speaker = "旁白"
			ln = strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(ln, "（旁白）"), "(旁白)"))
		} else {
			pl.speaker = "旁白"
		}
		if strings.Contains(pl.speaker, "旁白") || pl.speaker == "画外音" {
			pl.kind, pl.speaker = "narration", "旁白"
		}
		if m := emoRe.FindStringSubmatch(ln); m != nil {
			pl.emotion = m[1]
			ln = strings.TrimSpace(ln[len(m[0]):])
		}
		pl.text = strings.Trim(ln, "“”\"「」 ")
		if pl.text != "" {
			out = append(out, pl)
		}
	}
	return out
}

func matchSpeaker(assets []*Asset, name string) *Asset {
	for _, a := range assets {
		if a.Kind == "character" && a.Name == name {
			return a
		}
	}
	for _, a := range assets {
		if a.Kind == "character" && name != "" && (strings.Contains(a.Name, name) || strings.Contains(name, a.Name)) {
			return a
		}
	}
	return nil
}

// syncLines 按分镜台词重建逐句列表，文字没变的句子保留已生成的音频
func syncLines(p *Project, e *Episode) {
	assets := listAssets(p.ID)
	old := listLines(`WHERE episode_id=?`, e.ID)
	keep := map[int64]bool{}
	find := func(shot int64, pl parsedLine) *Line {
		for _, l := range old {
			if !keep[l.ID] && l.ShotID == shot && l.Text == pl.text && l.Speaker == pl.speaker {
				return l
			}
		}
		return nil
	}
	for _, s := range listShots(e.ID, false) {
		for i, pl := range parseDialogue(s.Dialogue) {
			var aid int64
			if pl.kind == "dialogue" {
				if a := matchSpeaker(assets, pl.speaker); a != nil {
					aid = a.ID
				}
			}
			if l := find(s.ID, pl); l != nil {
				keep[l.ID] = true
				_, _ = db.Exec(`UPDATE lines SET sort=?, asset_id=?, kind=? WHERE id=?`, i, aid, pl.kind, l.ID)
				continue
			}
			_, _ = db.Exec(`INSERT INTO lines(project_id,episode_id,shot_id,sort,speaker,asset_id,kind,text,emotion,created_at) VALUES(?,?,?,?,?,?,?,?,?,?)`,
				p.ID, e.ID, s.ID, i, pl.speaker, aid, pl.kind, pl.text, pl.emotion, now())
		}
	}
	for _, l := range old {
		if !keep[l.ID] {
			if l.AudioFile != "" {
				_ = os.Remove(filepath.Join(dataDir, "audio", l.AudioFile))
			}
			_, _ = db.Exec(`DELETE FROM lines WHERE id=?`, l.ID)
		}
	}
}

func lineVoice(p *Project, l *Line) (VoiceSpec, error) {
	if l.Kind == "narration" {
		if p.NarratorVoice.empty() {
			return VoiceSpec{}, errors.New("还没有设置旁白音色")
		}
		return p.NarratorVoice, nil
	}
	if l.AssetID == 0 {
		return VoiceSpec{}, fmt.Errorf("「%s」不在资产库里，没法确定音色", l.Speaker)
	}
	a, err := getAsset(l.AssetID)
	if err != nil {
		return VoiceSpec{}, fmt.Errorf("「%s」已从资产库删除", l.Speaker)
	}
	if a.Voice.empty() {
		return VoiceSpec{}, fmt.Errorf("角色「%s」还没有设置音色", a.Name)
	}
	return a.Voice, nil
}

func genLine(ctx context.Context, p *Project, l *Line) error {
	v, err := lineVoice(p, l)
	if err != nil {
		_, _ = db.Exec(`UPDATE lines SET status='failed', error=? WHERE id=?`, err.Error(), l.ID)
		return err
	}
	_, _ = db.Exec(`UPDATE lines SET status='running', error='' WHERE id=?`, l.ID)
	name, dur, err := speakTo(ctx, v, l.Text, l.Emotion, fmt.Sprintf("l%d", l.ID))
	ledgerAdd(p.ID, l.EpisodeID, "tts", 1, 0, 0, 0, "")
	if err != nil {
		_, _ = db.Exec(`UPDATE lines SET status='failed', error=? WHERE id=?`, err.Error(), l.ID)
		return err
	}
	if l.AudioFile != "" {
		_ = os.Remove(filepath.Join(dataDir, "audio", l.AudioFile))
	}
	_, _ = db.Exec(`UPDATE lines SET status='done', error='', audio_file=?, duration=?, sig=? WHERE id=?`, name, dur, v.sig(l.Text, l.Emotion), l.ID)
	return nil
}

// linesTask 生成本集还没有（或音色/文字已变）的配音
func linesTask(p *Project, e *Episode, force bool) (int64, error) {
	syncLines(p, e)
	ls := listLines(`WHERE episode_id=? ORDER BY shot_id, sort`, e.ID)
	var todo []*Line
	for _, l := range ls {
		v, err := lineVoice(p, l)
		if force || l.AudioFile == "" || (err == nil && l.Sig != v.sig(l.Text, l.Emotion)) {
			todo = append(todo, l)
		}
	}
	if len(ls) == 0 {
		return 0, errors.New("这一集的分镜里没有台词")
	}
	if len(todo) == 0 {
		return 0, errors.New("配音都是最新的")
	}
	return startTask(p.ID, e.ID, "lines", "episode", e.ID, func(ctx context.Context) error {
		sem := make(chan struct{}, 3)
		var mu sync.Mutex
		var firstErr error
		fails := 0
		var wg sync.WaitGroup
		for _, l := range todo {
			wg.Add(1)
			sem <- struct{}{}
			go func(l *Line) {
				defer wg.Done()
				defer func() { <-sem }()
				if err := genLine(ctx, p, l); err != nil {
					mu.Lock()
					fails++
					if firstErr == nil {
						firstErr = err
					}
					mu.Unlock()
				}
			}(l)
		}
		wg.Wait()
		if fails > 0 {
			return fmt.Errorf("%d 句配音失败：%s", fails, firstErr.Error())
		}
		return nil
	})
}

func handleLinesGenerate(w http.ResponseWriter, r *http.Request) {
	p, e, err := epCtx(pathID(r))
	if err != nil {
		httpErr(w, 404, err.Error())
		return
	}
	var in struct {
		Force bool `json:"force"`
	}
	_ = readJSON(r, &in)
	tid, err := linesTask(p, e, in.Force)
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	writeJSON(w, map[string]any{"task_id": tid})
}

func handleLineGenerate(w http.ResponseWriter, r *http.Request) {
	l, err := getLine(pathID(r))
	if err != nil {
		httpErr(w, 404, err.Error())
		return
	}
	p, _ := getProject(l.ProjectID)
	tid, err := startTask(p.ID, l.EpisodeID, "line", "line", l.ID, func(ctx context.Context) error { return genLine(ctx, p, l) })
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	writeJSON(w, map[string]any{"task_id": tid})
}

func handleLineUpdate(w http.ResponseWriter, r *http.Request) {
	l, err := getLine(pathID(r))
	if err != nil {
		httpErr(w, 404, err.Error())
		return
	}
	var in struct {
		Emotion *string `json:"emotion"`
		Text    *string `json:"text"`
	}
	_ = readJSON(r, &in)
	if in.Emotion != nil {
		_, _ = db.Exec(`UPDATE lines SET emotion=? WHERE id=?`, strings.TrimSpace(*in.Emotion), l.ID)
	}
	if in.Text != nil && strings.TrimSpace(*in.Text) != "" {
		_, _ = db.Exec(`UPDATE lines SET text=? WHERE id=?`, strings.TrimSpace(*in.Text), l.ID)
	}
	writeJSON(w, map[string]any{"ok": true})
}

// ---------- 音色：试听 / 设计 / 克隆 ----------

func handleVoicePreview(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Voice   VoiceSpec `json:"voice"`
		Text    string    `json:"text"`
		Emotion string    `json:"emotion"`
	}
	if err := readJSON(r, &in); err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	if strings.TrimSpace(in.Text) == "" {
		in.Text = "你好，这是我的声音。接下来的故事，由我来讲给你听。"
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Minute)
	defer cancel()
	name, dur, err := speakTo(ctx, in.Voice, in.Text, in.Emotion, "pv")
	ledgerAdd(0, 0, "tts", 1, 0, 0, 0, "")
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	writeJSON(w, map[string]any{"url": "/files/audio/" + name, "duration": dur})
}

const voiceDescSystem = `你是配音导演。根据角色资料，为语音合成模型写一段音色描述，用来设计这个角色的声音。
要求：1–3 句中文，写清性别、年龄感、音色质感（如清亮、低沉、沙哑、甜美、有磁性）、说话速度与节奏、性格带来的语气、口音（没有就写普通话），不要写后期效果（混响、回声等），不要出现角色名字。
同时写一句这个角色会说的台词（15–30 字，符合其性格，用来试听）。
只输出 JSON：{"desc":"","sample_text":""}`

func draftVoiceDesc(ctx context.Context, p *Project, a *Asset) (string, string, error) {
	sample := ""
	rows, _ := db.Query(`SELECT text FROM lines WHERE asset_id=? ORDER BY length(text) DESC LIMIT 1`, a.ID)
	if rows != nil {
		if rows.Next() {
			_ = rows.Scan(&sample)
		}
		rows.Close()
	}
	if d := strings.TrimSpace(a.Voice.Desc); d != "" && a.Voice.Mode == "" {
		// 外部编剧写好的音色描述（upsert_asset 的 voice_description）
		return d, orDefault(strings.TrimSpace(a.Voice.SampleText), orDefault(sample, "你好，很高兴认识你。")), nil
	}
	ch, model, err := textChannelFor(p)
	if err != nil {
		// 没有文本渠道：用外貌描述拼一句
		d := strings.TrimSpace(a.Description)
		if d == "" {
			return "", "", err
		}
		return "根据这个人物的形象配音：" + d + "。普通话，语速适中。", orDefault(sample, "你好，很高兴认识你。"), nil
	}
	user := fmt.Sprintf("角色名：%s\n外貌：%s\n人物小传（节选）：%s\n", a.Name, a.Description, excerptFor(p.Bios, a.Name))
	if sample != "" {
		user += "剧中台词示例：" + sample + "\n"
	}
	text, err := callChatL(ctx, p.ID, 0, ch, model, voiceDescSystem, user, 0.6)
	if err != nil {
		return "", "", err
	}
	var out struct {
		Desc   string `json:"desc"`
		Sample string `json:"sample_text"`
	}
	if err := extractJSON(text, &out); err != nil {
		return strings.TrimSpace(text), orDefault(sample, "你好，很高兴认识你。"), nil
	}
	if sample != "" {
		out.Sample = sample
	}
	return strings.TrimSpace(out.Desc), orDefault(strings.TrimSpace(out.Sample), "你好，很高兴认识你。"), nil
}

// excerptFor 从人物小传里截出提到某人的段落
func excerptFor(bios, name string) string {
	for _, para := range strings.Split(bios, "\n") {
		if strings.Contains(para, name) {
			if r := []rune(para); len(r) > 300 {
				return string(r[:300])
			}
			return para
		}
	}
	return "（无）"
}

func handleVoiceDescribe(w http.ResponseWriter, r *http.Request) {
	a, err := getAsset(pathID(r))
	if err != nil {
		httpErr(w, 404, "资产不存在")
		return
	}
	p, _ := getProject(a.ProjectID)
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Minute)
	defer cancel()
	desc, sample, err := draftVoiceDesc(ctx, p, a)
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	writeJSON(w, map[string]any{"desc": desc, "sample_text": sample})
}

func designOnce(ctx context.Context, ch *Channel, desc, text string) (string, float64, error) {
	msgs := []any{map[string]any{"role": "user", "content": desc}, map[string]any{"role": "assistant", "content": text}}
	data, err := mimoCall(ctx, ch, "mimo-v2.5-tts-voicedesign", msgs, map[string]any{"format": "wav"})
	if err != nil {
		return "", 0, err
	}
	name, dur, err := saveAudio("vd", data, "wav")
	if err == nil && strings.TrimSpace(text) != "" {
		_ = os.WriteFile(filepath.Join(dataDir, "audio", name+".txt"), []byte(strings.TrimSpace(text)), 0o644)
	}
	return name, dur, err
}

// handleVoiceDesign 用音色描述生成 2–3 个候选试听样本
func handleVoiceDesign(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ChannelID int64  `json:"channel_id"`
		Desc      string `json:"desc"`
		Text      string `json:"text"`
		N         int    `json:"n"`
		ProjectID int64  `json:"project_id"`
		AssetID   int64  `json:"asset_id"`
	}
	if err := readJSON(r, &in); err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	if strings.TrimSpace(in.Desc) == "" {
		httpErr(w, 400, "先写音色描述")
		return
	}
	ch, err := designVoiceChannel(in.ChannelID)
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	n := clampInt(in.N, 1, 3)
	text := orDefault(strings.TrimSpace(in.Text), "你好，很高兴认识你。接下来的故事，由我来讲给你听。")
	ctx, cancel := context.WithTimeout(r.Context(), 4*time.Minute)
	defer cancel()
	type cand struct {
		Sample   string  `json:"sample"`
		URL      string  `json:"url"`
		Duration float64 `json:"duration"`
	}
	names, durs, err := designCandidates(ctx, ch, strings.TrimSpace(in.Desc), text, n)
	reqs := n
	if ch.APIType == "fish" {
		reqs = 1
	}
	ledgerAdd(in.ProjectID, 0, "tts", reqs, 0, 0, 0, "")
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	saveVoiceCands(in.ProjectID, in.AssetID, ch, strings.TrimSpace(in.Desc), text, names, durs, "web")
	res := []*cand{}
	for i, nm := range names {
		res = append(res, &cand{Sample: nm, URL: "/files/audio/" + nm, Duration: durs[i]})
	}
	writeJSON(w, map[string]any{"channel_id": ch.ID, "candidates": res, "text": text})
}

// toWav 把上传或录制的样本（webm / m4a / mp3 / wav …）统一转成 24 kHz 单声道 wav，最长 60 秒
func toWav(data []byte) ([]byte, float64, error) {
	tmp, err := os.MkdirTemp(filepath.Join(dataDir, "tmp"), "smp-")
	if err != nil {
		return nil, 0, err
	}
	defer os.RemoveAll(tmp)
	in := filepath.Join(tmp, "in")
	out := filepath.Join(tmp, "out.wav")
	_ = os.WriteFile(in, data, 0o644)
	if b, err := exec.Command("ffmpeg", "-y", "-v", "error", "-i", in, "-t", "60", "-ac", "1", "-ar", "24000", "-c:a", "pcm_s16le", out).CombinedOutput(); err != nil {
		return nil, 0, fmt.Errorf("音频格式无法识别：%s", lastLines(string(b), 1))
	}
	ci, _ := probe(out)
	d, err := os.ReadFile(out)
	return d, ci.dur, err
}

func readAudioUpload(r *http.Request, field string) ([][]byte, error) {
	if err := r.ParseMultipartForm(80 << 20); err != nil {
		return nil, errors.New("上传失败")
	}
	var out [][]byte
	for _, fh := range r.MultipartForm.File[field] {
		f, err := fh.Open()
		if err != nil {
			continue
		}
		b, _ := io.ReadAll(io.LimitReader(f, 40<<20))
		f.Close()
		if len(b) > 0 {
			out = append(out, b)
		}
	}
	if len(out) == 0 {
		return nil, errors.New("没有收到音频")
	}
	return out, nil
}

// handleVoiceSample 保存一段克隆样本（上传或录音），返回本地文件名
func handleVoiceSample(w http.ResponseWriter, r *http.Request) {
	files, err := readAudioUpload(r, "file")
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	wav, dur, err := toWav(files[0])
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	if dur < 3 {
		httpErr(w, 400, "样本太短，至少录 3 秒，10 秒以上效果更好")
		return
	}
	name, dur, err := saveAudio("vc", wav, "wav")
	if err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	writeJSON(w, map[string]any{"sample": name, "url": "/files/audio/" + name, "duration": dur})
}

// ---------- Fish 音色库 ----------

type fishItem struct {
	ID         string   `json:"id"`
	Title      string   `json:"title"`
	Desc       string   `json:"desc"`
	Tags       []string `json:"tags"`
	Cover      string   `json:"cover"`
	Sample     string   `json:"sample"`
	SampleText string   `json:"sample_text"`
	Likes      int      `json:"likes"`
	Uses       int      `json:"uses"`
	Mine       bool     `json:"mine"`
	State      string   `json:"state"`
}

func fishCover(c string) string {
	if c == "" {
		return ""
	}
	if strings.HasPrefix(c, "http") {
		return c
	}
	return "https://public-platform.r2.fish.audio/" + strings.TrimPrefix(c, "/")
}

func fishChannelFor(id int64) (*Channel, error) {
	if ch, err := voiceChannel(id); err == nil && ch.APIType == "fish" {
		return ch, nil
	}
	if ch := firstVoiceChannel("fish"); ch != nil {
		return ch, nil
	}
	return nil, errors.New("还没有 Fish Audio 语音渠道")
}

func handleFishModels(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	ch, err := fishChannelFor(atoi64(q.Get("channel_id")))
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	mine := q.Get("self") == "1"
	v := url.Values{"page_size": {"12"}, "page_number": {orDefault(q.Get("page"), "1")}}
	if mine {
		v.Set("self", "true")
	} else {
		v.Set("language", "zh")
		v.Set("sort_by", orDefault(q.Get("sort"), "task_count"))
	}
	if s := strings.TrimSpace(q.Get("q")); s != "" {
		v.Set("title", s)
	}
	if t := strings.TrimSpace(q.Get("tag")); t != "" {
		v.Set("tag", t)
	}
	req, _ := http.NewRequestWithContext(r.Context(), "GET", fishBase(ch)+"/model?"+v.Encode(), nil)
	setVoiceAuth(req, ch)
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		httpErr(w, 502, "连接 Fish Audio 失败："+shortErr(err))
		return
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode >= 300 {
		httpErr(w, 400, upstreamErr(resp, raw).Error())
		return
	}
	var out struct {
		Total   int  `json:"total"`
		HasMore bool `json:"has_more"`
		Items   []struct {
			ID      string   `json:"_id"`
			Title   string   `json:"title"`
			Desc    string   `json:"description"`
			Tags    []string `json:"tags"`
			Cover   string   `json:"cover_image"`
			State   string   `json:"state"`
			Likes   int      `json:"like_count"`
			Uses    int      `json:"task_count"`
			Samples []struct {
				Text  string `json:"text"`
				Audio string `json:"audio"`
			} `json:"samples"`
		} `json:"items"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		httpErr(w, 502, "Fish Audio 返回格式不对")
		return
	}
	items := []fishItem{}
	for _, it := range out.Items {
		fi := fishItem{ID: it.ID, Title: it.Title, Desc: it.Desc, Tags: it.Tags, Cover: fishCover(it.Cover), Likes: it.Likes, Uses: it.Uses, Mine: mine, State: it.State}
		if fi.Tags == nil {
			fi.Tags = []string{}
		}
		if len(it.Samples) > 0 {
			fi.Sample, fi.SampleText = it.Samples[0].Audio, it.Samples[0].Text
		}
		items = append(items, fi)
	}
	writeJSON(w, map[string]any{"channel_id": ch.ID, "items": items, "total": out.Total, "has_more": out.HasMore || len(items) == 12})
}

// handleFishCreate 在 Fish 上新建私有音色：title + 1–20 段样本（可带对应文字）
func handleFishCreate(w http.ResponseWriter, r *http.Request) {
	files, err := readAudioUpload(r, "voices")
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	ch, err := fishChannelFor(atoi64(r.FormValue("channel_id")))
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	if ch.APIKey == "" {
		httpErr(w, 400, "Fish Audio 渠道还没有填密钥")
		return
	}
	title := strings.TrimSpace(r.FormValue("title"))
	if title == "" {
		httpErr(w, 400, "给音色起个名字")
		return
	}
	texts := r.MultipartForm.Value["texts"]
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("title", title)
	_ = mw.WriteField("type", "tts")
	_ = mw.WriteField("train_mode", "fast")
	_ = mw.WriteField("visibility", "private")
	_ = mw.WriteField("enhance_audio_quality", "true")
	if d := strings.TrimSpace(r.FormValue("description")); d != "" {
		_ = mw.WriteField("description", d)
	}
	total := 0.0
	for i, f := range files {
		wav, dur, err := toWav(f)
		if err != nil {
			httpErr(w, 400, err.Error())
			return
		}
		total += dur
		part, _ := mw.CreateFormFile("voices", fmt.Sprintf("sample%d.wav", i+1))
		_, _ = part.Write(wav)
	}
	if total < 8 {
		httpErr(w, 400, "样本总长太短，建议 10–60 秒")
		return
	}
	hasText := false
	for _, t := range texts {
		if strings.TrimSpace(t) != "" {
			hasText = true
		}
	}
	if hasText {
		for i := range files {
			t := ""
			if i < len(texts) {
				t = strings.TrimSpace(texts[i])
			}
			_ = mw.WriteField("texts", t)
		}
	}
	_ = mw.Close()
	req, _ := http.NewRequestWithContext(r.Context(), "POST", fishBase(ch)+"/model", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	setVoiceAuth(req, ch)
	resp, err := (&http.Client{Timeout: 3 * time.Minute}).Do(req)
	if err != nil {
		httpErr(w, 502, "连接 Fish Audio 失败："+shortErr(err))
		return
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode >= 300 {
		httpErr(w, 400, upstreamErr(resp, raw).Error())
		return
	}
	var m struct {
		ID    string `json:"_id"`
		Title string `json:"title"`
		State string `json:"state"`
	}
	_ = json.Unmarshal(raw, &m)
	if m.ID == "" {
		httpErr(w, 502, "Fish Audio 没有返回音色编号")
		return
	}
	writeJSON(w, map[string]any{"id": m.ID, "title": orDefault(m.Title, title), "state": m.State, "channel_id": ch.ID})
}

func handleFishUpdate(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ChannelID int64  `json:"channel_id"`
		Title     string `json:"title"`
	}
	_ = readJSON(r, &in)
	ch, err := fishChannelFor(in.ChannelID)
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	if strings.TrimSpace(in.Title) == "" {
		httpErr(w, 400, "名称不能为空")
		return
	}
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("title", strings.TrimSpace(in.Title))
	_ = mw.Close()
	req, _ := http.NewRequestWithContext(r.Context(), "PATCH", fishBase(ch)+"/model/"+url.PathEscape(r.PathValue("mid")), &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	setVoiceAuth(req, ch)
	fishSimple(w, req)
}

func handleFishDelete(w http.ResponseWriter, r *http.Request) {
	ch, err := fishChannelFor(atoi64(r.URL.Query().Get("channel_id")))
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	req, _ := http.NewRequestWithContext(r.Context(), "DELETE", fishBase(ch)+"/model/"+url.PathEscape(r.PathValue("mid")), nil)
	setVoiceAuth(req, ch)
	fishSimple(w, req)
}

func fishSimple(w http.ResponseWriter, req *http.Request) {
	resp, err := (&http.Client{Timeout: 60 * time.Second}).Do(req)
	if err != nil {
		httpErr(w, 502, "连接 Fish Audio 失败："+shortErr(err))
		return
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 300 {
		httpErr(w, 400, upstreamErr(resp, raw).Error())
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

// ---------- 一键为全部角色设计音色 ----------

func designAllTask(p *Project) (int64, error) {
	ch, err := designVoiceChannel(0)
	if err != nil {
		return 0, err
	}
	var todo []*Asset
	for _, a := range listAssets(p.ID) {
		if a.Kind == "character" && a.Voice.empty() {
			todo = append(todo, a)
		}
	}
	needNarr := p.NarratorVoice.empty()
	if len(todo) == 0 && !needNarr {
		return 0, errors.New("所有角色都已经有音色")
	}
	return startTask(p.ID, 0, "voices", "project", p.ID, func(ctx context.Context) error {
		var fails []string
		for _, a := range todo {
			desc, text, err := draftVoiceDesc(ctx, p, a)
			if err == nil {
				var names []string
				var durs []float64
				names, durs, err = designCandidates(ctx, ch, desc, text, 1)
				ledgerAdd(p.ID, 0, "tts", 1, 0, 0, 0, "")
				if err == nil {
					saveVoiceCands(p.ID, a.ID, ch, desc, text, names, durs, "auto")
					var v VoiceSpec
					v, err = designedVoiceSpec(ctx, ch, names[0], a.Name+"的声音", desc, text)
					if err == nil {
						_, err = db.Exec(`UPDATE assets SET voice=? WHERE id=?`, mustJSON(v), a.ID)
					}
				}
			}
			if err != nil {
				fails = append(fails, a.Name+"："+err.Error())
			}
		}
		if needNarr {
			desc := "成熟稳重的中年男声旁白，普通话标准，音色低沉有磁性，语速平稳，带一点悬疑感，适合讲故事。"
			nt := "那一夜之后，一切都变了。没有人知道，真正的秘密才刚刚开始。"
			names, durs, err := designCandidates(ctx, ch, desc, nt, 1)
			var v VoiceSpec
			if err == nil {
				saveVoiceCands(p.ID, 0, ch, desc, nt, names, durs, "auto")
				v, err = designedVoiceSpec(ctx, ch, names[0], "旁白", desc, nt)
			}
			if err == nil {
				_, _ = db.Exec(`UPDATE projects SET narrator_voice=? WHERE id=?`, mustJSON(v), p.ID)
			} else {
				fails = append(fails, "旁白："+err.Error())
			}
		}
		if len(fails) > 0 {
			return errors.New(strings.Join(fails, "；"))
		}
		return nil
	})
}

func handleVoicesDesignAll(w http.ResponseWriter, r *http.Request) {
	p, err := getProject(pathID(r))
	if err != nil {
		httpErr(w, 404, "作品不存在")
		return
	}
	tid, err := designAllTask(p)
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	writeJSON(w, map[string]any{"task_id": tid})
}

// MiMo 内置音色
var mimoBuiltin = []map[string]string{
	{"id": "冰糖", "label": "冰糖", "hint": "女声"}, {"id": "茉莉", "label": "茉莉", "hint": "女声"},
	{"id": "苏打", "label": "苏打", "hint": "男声"}, {"id": "白桦", "label": "白桦", "hint": "男声"},
	{"id": "mimo_default", "label": "默认", "hint": "女声"},
}

func handleVoiceMeta(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{
		"mimo_builtin": mimoBuiltin,
		"fish_models":  []string{"s2.1-pro", "s2-pro", "s2.1-pro-free", "s3-pro", "drama-3", "s1"},
		"mimo_models":  []string{"mimo-v2.5-tts"},
	})
}

var _ = strconv.Itoa
