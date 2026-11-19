package main

// 视频任务的附加处理：首尾帧（MiniMaxH3ImageToVideo.last_frame）、画质后期节点注入、开机配置（镜像、云端硬盘权重缓存、后期组件）。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

type postCfg struct {
	UpscaleModel string `json:"upscale_model"`
	UpscaleURL   string `json:"upscale_url"`
	InterpRepo   string `json:"interp_repo"`
	InterpNode   string `json:"interp_node"`
	RifeCkpt     string `json:"rife_ckpt"`
	Multiplier   int    `json:"interp_multiplier"`
}

func loadPostCfg() postCfg {
	var c postCfg
	_ = json.Unmarshal([]byte(defaultPost), &c)
	_ = json.Unmarshal([]byte(workflowText("post")), &c)
	if c.Multiplier < 2 {
		c.Multiplier = 2
	}
	if c.InterpNode == "" {
		c.InterpNode = "RIFE VFI"
	}
	return c
}

func findNode(wf map[string]any, class string) (string, map[string]any) {
	for id, n := range wf {
		if m, ok := n.(map[string]any); ok && m["class_type"] == class {
			return id, m
		}
	}
	return "", nil
}

func nodeInputs(n map[string]any) map[string]any {
	in, _ := n["inputs"].(map[string]any)
	if in == nil {
		in = map[string]any{}
		n["inputs"] = in
	}
	return in
}

// injectLastFrame 加一个 LoadImage 接到 MiniMaxH3ImageToVideo 的 last_frame（首尾帧生成）
func injectLastFrame(wf map[string]any, file string) error {
	_, h3 := findNode(wf, "MiniMaxH3ImageToVideo")
	if h3 == nil {
		return errors.New("工作流里没有 MiniMaxH3ImageToVideo 节点，无法使用首尾帧")
	}
	wf["900"] = map[string]any{"class_type": "LoadImage", "inputs": map[string]any{"image": file}}
	nodeInputs(h3)["last_frame"] = []any{"900", 0}
	return nil
}

// injectPost 在 CreateVideo 之前插入放大 / 补帧节点
func injectPost(wf map[string]any, p VideoPost, w, h int) error {
	_, cv := findNode(wf, "CreateVideo")
	if cv == nil {
		return errors.New("工作流里没有 CreateVideo 节点，无法做画质后期")
	}
	in := nodeInputs(cv)
	src, ok := in["images"].([]any)
	if !ok {
		return errors.New("CreateVideo 的画面输入不是节点连线")
	}
	c := loadPostCfg()
	if p.Upscale {
		wf["901"] = map[string]any{"class_type": "UpscaleModelLoader", "inputs": map[string]any{"model_name": c.UpscaleModel}}
		wf["902"] = map[string]any{"class_type": "ImageUpscaleWithModel", "inputs": map[string]any{"upscale_model": []any{"901", 0}, "image": src}}
		wf["903"] = map[string]any{"class_type": "ImageScale", "inputs": map[string]any{"image": []any{"902", 0}, "upscale_method": "lanczos", "width": w, "height": h, "crop": "disabled"}}
		src = []any{"903", 0}
	}
	if p.Interp {
		wf["904"] = map[string]any{"class_type": c.InterpNode, "inputs": map[string]any{"ckpt_name": c.RifeCkpt, "frames": src, "clear_cache_after_n_frames": 10,
			"multiplier": c.Multiplier, "fast_mode": true, "ensemble": true, "scale_factor": 1.0}}
		src = []any{"904", 0}
		fps := 24.0
		if f, ok := in["fps"].(float64); ok && f > 0 {
			fps = f
		}
		in["fps"] = fps * float64(c.Multiplier)
	}
	in["images"] = src
	return nil
}

func deliverDimsFor(aspect string) (int, int) {
	return deliverDims(&Project{Aspect: aspect, Deliver: "1080p"})
}

// neededPost 队列里的视频是否需要放大 / 补帧
func (r *Runner) neededPost() VideoPost {
	var need VideoPost
	for _, v := range listVideos(`WHERE status='queued'`) {
		need.Upscale = need.Upscale || v.Post.Upscale
		need.Interp = need.Interp || v.Post.Interp
	}
	return need
}

// ensurePost 任务需要的后期组件还没装：重跑一遍准备环境（幂等，装了新节点会重启 ComfyUI）
func (r *Runner) ensurePost(ctx context.Context, need VideoPost) error {
	r.mu.Lock()
	have := r.postReady
	r.mu.Unlock()
	if (!need.Upscale || have.Upscale) && (!need.Interp || have.Interp) {
		return nil
	}
	r.logf("准备画质后期组件")
	return r.bootstrapWith(ctx, VideoPost{Upscale: need.Upscale || have.Upscale, Interp: need.Interp || have.Interp})
}

// bootConfig 组装开机配置：模型文件（可走镜像）、后期组件、云端硬盘缓存
func bootConfig(ctx context.Context, jup *Jupyter, boot map[string]any, post VideoPost) map[string]any {
	cfg := loadColabCfg()
	var files []map[string]any
	_ = json.Unmarshal([]byte(workflowText("files")), &files)
	if cfg.Mirror != "" {
		for _, f := range files {
			if u, ok := f["url"].(string); ok && strings.HasPrefix(u, "https://huggingface.co/") {
				f["url"] = cfg.Mirror + strings.TrimPrefix(u, "https://huggingface.co")
			}
		}
	}
	boot["files"] = files
	pc := loadPostCfg()
	boot["post"] = map[string]any{"upscale": post.Upscale, "interp": post.Interp, "upscale_model": pc.UpscaleModel, "upscale_url": pc.UpscaleURL, "interp_repo": pc.InterpRepo}
	boot["drive"] = map[string]any{"enabled": false}
	if cfg.DriveCache {
		if d, err := driveBootCfg(ctx, jup); err == nil {
			boot["drive"] = d
		} else {
			runner.logf("云端硬盘缓存不可用：%s", err.Error())
		}
	}
	return boot
}

var _ = fmt.Sprint
