package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// ---------- Fish 按描述设计音色 ----------
// Fish 的 voice-design 接口是无状态的：返回几段候选试听音频。选中后用这段音频在 Fish 建一个私有音色模型，
// 之后按 reference_id 合成。模型编号记在样本旁边（同名 .fish 文件），同一段样本只建一次。

// designVoiceChannel 设计音色用的渠道：优先小米 MiMo，其次 Fish
func designVoiceChannel(id int64) (*Channel, error) {
	if ch, err := voiceChannel(id); err == nil && (ch.APIType == "mimo" || ch.APIType == "fish") {
		return ch, nil
	}
	if ch := firstVoiceChannel("mimo"); ch != nil {
		return ch, nil
	}
	if ch := firstVoiceChannel("fish"); ch != nil {
		return ch, nil
	}
	return nil, errors.New("设计音色需要一个小米 MiMo 或 Fish Audio 语音渠道")
}

// fishDesign 一次请求返回 n 个候选（Fish 按请求计费，不按候选数）
func fishDesign(ctx context.Context, ch *Channel, desc, text string, n int) ([]string, []float64, error) {
	if ch.APIKey == "" {
		return nil, nil, errors.New("Fish Audio 渠道还没有填密钥")
	}
	if r := []rune(text); len(r) > 150 {
		text = string(r[:150])
	}
	if r := []rune(desc); len(r) > 2000 {
		desc = string(r[:2000])
	}
	body := map[string]any{"instruction": desc, "reference_text": text, "language": "zh", "n": clampInt(n, 1, 4)}
	b, _ := json.Marshal(body)
	req, _ := http.NewRequestWithContext(ctx, "POST", fishBase(ch)+"/v1/voice-design", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("model", "voice-design-1")
	setVoiceAuth(req, ch)
	resp, err := (&http.Client{Timeout: 3 * time.Minute}).Do(req)
	if err != nil {
		return nil, nil, errors.New("连接 Fish Audio 失败：" + shortErr(err))
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if resp.StatusCode >= 300 {
		return nil, nil, upstreamErr(resp, raw)
	}
	var out struct {
		Candidates []struct {
			Audio string `json:"audio_base64"`
		} `json:"candidates"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || len(out.Candidates) == 0 {
		return nil, nil, errors.New("Fish 没有返回候选音色")
	}
	var names []string
	var durs []float64
	for _, c := range out.Candidates {
		data, err := base64.StdEncoding.DecodeString(c.Audio)
		if err != nil || len(data) == 0 {
			continue
		}
		name, dur, err := saveAudio("vd", data, "wav")
		if err != nil {
			continue
		}
		_ = os.WriteFile(filepath.Join(dataDir, "audio", name+".txt"), []byte(text), 0o644)
		names = append(names, name)
		durs = append(durs, dur)
	}
	if len(names) == 0 {
		return nil, nil, errors.New("Fish 返回的候选音频无法保存")
	}
	return names, durs, nil
}

// designCandidates 按渠道出 n 个候选样本
func designCandidates(ctx context.Context, ch *Channel, desc, text string, n int) ([]string, []float64, error) {
	if ch.APIType == "fish" {
		return fishDesign(ctx, ch, desc, text, n)
	}
	names := make([]string, n)
	durs := make([]float64, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			names[i], durs[i], errs[i] = designOnce(ctx, ch, desc, text)
		}(i)
	}
	wg.Wait()
	var on []string
	var od []float64
	for i := range names {
		if errs[i] == nil && names[i] != "" {
			on = append(on, names[i])
			od = append(od, durs[i])
		}
	}
	if len(on) == 0 {
		return nil, nil, errs[0]
	}
	return on, od, nil
}

var fishModelMu sync.Mutex

// fishModelFromSample 用一段样本建 Fish 私有音色，返回 reference_id；同一样本只建一次
func fishModelFromSample(ctx context.Context, ch *Channel, sample, title string) (string, error) {
	fishModelMu.Lock()
	defer fishModelMu.Unlock()
	idFile := filepath.Join(dataDir, "audio", sample+".fish")
	if b, err := os.ReadFile(idFile); err == nil && strings.TrimSpace(string(b)) != "" {
		return strings.TrimSpace(string(b)), nil
	}
	if ch.APIKey == "" {
		return "", errors.New("Fish Audio 渠道还没有填密钥")
	}
	data, err := os.ReadFile(filepath.Join(dataDir, "audio", sample))
	if err != nil {
		return "", errors.New("音色样本文件丢失")
	}
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("title", orDefault(strings.TrimSpace(title), "设计音色 "+sample))
	_ = mw.WriteField("type", "tts")
	_ = mw.WriteField("train_mode", "fast")
	_ = mw.WriteField("visibility", "private")
	part, _ := mw.CreateFormFile("voices", "sample.wav")
	_, _ = part.Write(data)
	if t, err := os.ReadFile(filepath.Join(dataDir, "audio", sample+".txt")); err == nil && strings.TrimSpace(string(t)) != "" {
		_ = mw.WriteField("texts", strings.TrimSpace(string(t)))
	}
	_ = mw.Close()
	req, _ := http.NewRequestWithContext(ctx, "POST", fishBase(ch)+"/model", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	setVoiceAuth(req, ch)
	resp, err := (&http.Client{Timeout: 3 * time.Minute}).Do(req)
	if err != nil {
		return "", errors.New("连接 Fish Audio 失败：" + shortErr(err))
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode >= 300 {
		return "", upstreamErr(resp, raw)
	}
	var m struct {
		ID string `json:"_id"`
	}
	if json.Unmarshal(raw, &m) != nil || m.ID == "" {
		return "", fmt.Errorf("Fish 建音色没有返回编号")
	}
	_ = os.WriteFile(idFile, []byte(m.ID), 0o644)
	return m.ID, nil
}

// designedVoiceSpec 把选中的设计样本变成能直接合成的音色
func designedVoiceSpec(ctx context.Context, ch *Channel, sample, title, desc, text string) (VoiceSpec, error) {
	v := VoiceSpec{ChannelID: ch.ID, Mode: "design", Title: title, Desc: desc, Sample: sample, SampleText: text, Speed: 1}
	if ch.APIType == "fish" {
		id, err := fishModelFromSample(ctx, ch, sample, title)
		if err != nil {
			return v, err
		}
		v.Mode = "fish"
		v.Voice = id
	}
	return v, nil
}
