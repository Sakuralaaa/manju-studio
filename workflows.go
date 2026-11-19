package main

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

//go:embed colab/bootstrap.py
var bootstrapPy string

//go:embed colab/job.py
var jobPy string

// 视频工作流（ComfyUI API 格式），占位符：
//   {{PROMPT}} 提示词  {{IMAGE}} 首帧文件名（ComfyUI/input 下）  {{WIDTH}} {{HEIGHT}} 宽高
//   {{LENGTH}} 帧数（17k+5）  {{SEED}} 随机种子  {{PREFIX}} 输出文件名前缀
// 节点与参数取自 Comfy-Org/workflow_templates 的 video_minimax_h3_i2v（子图展开）。

const wfStandard = `{
  "1": {"class_type": "UNETLoader", "inputs": {"unet_name": "minimax_h3_fl2va_pruned_int8_convrot.safetensors", "weight_dtype": "default"}},
  "2": {"class_type": "CLIPLoader", "inputs": {"clip_name": "qwen3vl_32b_minimax_h3_nvfp4_awq.safetensors", "type": "minimax", "device": "default"}},
  "3": {"class_type": "VAELoader", "inputs": {"vae_name": "minimax_h3_video_vae_int8_convrot.safetensors"}},
  "4": {"class_type": "VAELoader", "inputs": {"vae_name": "minimax_h3_audio_vae_fp32.safetensors"}},
  "5": {"class_type": "LoadImage", "inputs": {"image": "{{IMAGE}}"}},
  "6": {"class_type": "MiniMaxH3ImageToVideo", "inputs": {"clip": ["2", 0], "vae": ["3", 0], "first_frame": ["5", 0], "prompt": "{{PROMPT}}", "width": "{{WIDTH}}", "height": "{{HEIGHT}}", "length": "{{LENGTH}}"}},
  "7": {"class_type": "RandomNoise", "inputs": {"noise_seed": "{{SEED}}"}},
  "8": {"class_type": "BasicGuider", "inputs": {"model": ["1", 0], "conditioning": ["6", 0]}},
  "9": {"class_type": "KSamplerSelect", "inputs": {"sampler_name": "res_multistep"}},
  "10": {"class_type": "BasicScheduler", "inputs": {"model": ["1", 0], "scheduler": "simple", "steps": 20, "denoise": 1}},
  "11": {"class_type": "SamplerCustomAdvanced", "inputs": {"noise": ["7", 0], "guider": ["8", 0], "sampler": ["9", 0], "sigmas": ["10", 0], "latent_image": ["6", 1]}},
  "12": {"class_type": "VAEDecode", "inputs": {"samples": ["11", 0], "vae": ["3", 0]}},
  "13": {"class_type": "VAEDecodeAudio", "inputs": {"samples": ["11", 0], "vae": ["4", 0]}},
  "14": {"class_type": "CreateVideo", "inputs": {"images": ["12", 0], "audio": ["13", 0], "fps": 24}},
  "15": {"class_type": "SaveVideo", "inputs": {"video": ["14", 0], "filename_prefix": "{{PREFIX}}", "format": "auto", "codec": "auto"}}
}`

const wfTurbo = `{
  "1": {"class_type": "UNETLoader", "inputs": {"unet_name": "minimax_h3_fl2va_pruned_int8_convrot.safetensors", "weight_dtype": "default"}},
  "16": {"class_type": "LoraLoaderModelOnly", "inputs": {"model": ["1", 0], "lora_name": "minimax_h3_fl2v_turbo_8step_v1.0_comfyui_bf16.safetensors", "strength_model": 1}},
  "2": {"class_type": "CLIPLoader", "inputs": {"clip_name": "qwen3vl_32b_minimax_h3_nvfp4_awq.safetensors", "type": "minimax", "device": "default"}},
  "3": {"class_type": "VAELoader", "inputs": {"vae_name": "minimax_h3_video_vae_int8_convrot.safetensors"}},
  "4": {"class_type": "VAELoader", "inputs": {"vae_name": "minimax_h3_audio_vae_fp32.safetensors"}},
  "5": {"class_type": "LoadImage", "inputs": {"image": "{{IMAGE}}"}},
  "6": {"class_type": "MiniMaxH3ImageToVideo", "inputs": {"clip": ["2", 0], "vae": ["3", 0], "first_frame": ["5", 0], "prompt": "{{PROMPT}}", "width": "{{WIDTH}}", "height": "{{HEIGHT}}", "length": "{{LENGTH}}"}},
  "7": {"class_type": "RandomNoise", "inputs": {"noise_seed": "{{SEED}}"}},
  "8": {"class_type": "BasicGuider", "inputs": {"model": ["16", 0], "conditioning": ["6", 0]}},
  "9": {"class_type": "KSamplerSelect", "inputs": {"sampler_name": "res_multistep"}},
  "10": {"class_type": "BasicScheduler", "inputs": {"model": ["16", 0], "scheduler": "simple", "steps": 8, "denoise": 1}},
  "11": {"class_type": "SamplerCustomAdvanced", "inputs": {"noise": ["7", 0], "guider": ["8", 0], "sampler": ["9", 0], "sigmas": ["10", 0], "latent_image": ["6", 1]}},
  "12": {"class_type": "VAEDecode", "inputs": {"samples": ["11", 0], "vae": ["3", 0]}},
  "13": {"class_type": "VAEDecodeAudio", "inputs": {"samples": ["11", 0], "vae": ["4", 0]}},
  "14": {"class_type": "CreateVideo", "inputs": {"images": ["12", 0], "audio": ["13", 0], "fps": 24}},
  "15": {"class_type": "SaveVideo", "inputs": {"video": ["14", 0], "filename_prefix": "{{PREFIX}}", "format": "auto", "codec": "auto"}}
}`

const hfBase = "https://huggingface.co/Comfy-Org/MiniMax-H3/resolve/main/"

// 模型文件：在运行时上多线程下载到 ComfyUI/models/<dir>/
const defaultFiles = `[
  {"url": "` + hfBase + `diffusion_models/minimax_h3_fl2va_pruned_int8_convrot.safetensors", "dir": "diffusion_models"},
  {"url": "` + hfBase + `text_encoders/qwen3vl_32b_minimax_h3_nvfp4_awq.safetensors", "dir": "text_encoders"},
  {"url": "` + hfBase + `vae/minimax_h3_video_vae_int8_convrot.safetensors", "dir": "vae"},
  {"url": "` + hfBase + `vae/minimax_h3_audio_vae_fp32.safetensors", "dir": "vae"},
  {"url": "` + hfBase + `loras/minimax_h3_fl2v_turbo_8step_v1.0_comfyui_bf16.safetensors", "dir": "loras"}
]`

type wfDef struct {
	Key     string `json:"key"`
	Name    string `json:"name"`
	Default string `json:"-"`
	Kind    string `json:"kind"` // workflow / files / boot
}

var workflowDefs = []wfDef{
	{Key: "standard", Name: "标准工作流", Default: wfStandard, Kind: "workflow"},
	{Key: "turbo", Name: "Turbo 工作流", Default: wfTurbo, Kind: "workflow"},
	{Key: "files", Name: "模型文件", Default: defaultFiles, Kind: "files"},
	{Key: "boot", Name: "运行环境", Default: `{"comfy_repo": "https://github.com/Comfy-Org/ComfyUI", "sage": true, "sage_pkg": "sageattention", "threads": 16, "extra_args": []}`, Kind: "boot"},
	{Key: "post", Name: "画质后期", Default: defaultPost, Kind: "boot"},
}

// 画质后期（在 Colab 的 ComfyUI 里做，未在 GPU 上实测）：
// 放大：核心节点 UpscaleModelLoader → ImageUpscaleWithModel → ImageScale(lanczos) 到交付尺寸；
// 补帧：Fannovel16/ComfyUI-Frame-Interpolation 的「RIFE VFI」节点，2 倍补到 48 帧/秒。
const defaultPost = `{
  "upscale_model": "RealESRGAN_x2plus.pth",
  "upscale_url": "https://github.com/xinntao/Real-ESRGAN/releases/download/v0.2.1/RealESRGAN_x2plus.pth",
  "interp_repo": "https://github.com/Fannovel16/ComfyUI-Frame-Interpolation",
  "interp_node": "RIFE VFI",
  "rife_ckpt": "rife47.pth",
  "interp_multiplier": 2
}`

func initWorkflows() {}

func workflowText(key string) string {
	for _, d := range workflowDefs {
		if d.Key == key {
			return getSetting("wf_"+key, d.Default)
		}
	}
	return ""
}

func handleWorkflowList(w http.ResponseWriter, r *http.Request) {
	out := []map[string]any{}
	for _, d := range workflowDefs {
		v := workflowText(d.Key)
		out = append(out, map[string]any{"key": d.Key, "name": d.Name, "kind": d.Kind, "text": v, "custom": v != d.Default})
	}
	writeJSON(w, out)
}

func handleWorkflowSave(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	var in struct {
		Text string `json:"text"`
	}
	if err := readJSON(r, &in); err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	var def *wfDef
	for i := range workflowDefs {
		if workflowDefs[i].Key == key {
			def = &workflowDefs[i]
		}
	}
	if def == nil {
		httpErr(w, 404, "不存在")
		return
	}
	if err := validateWorkflow(def.Kind, in.Text); err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	_ = setSetting("wf_"+key, in.Text)
	writeJSON(w, map[string]any{"ok": true})
}

func handleWorkflowReset(w http.ResponseWriter, r *http.Request) {
	delSetting("wf_" + r.PathValue("key"))
	writeJSON(w, map[string]any{"ok": true})
}

func validateWorkflow(kind, text string) error {
	switch kind {
	case "workflow":
		var m map[string]struct {
			ClassType string         `json:"class_type"`
			Inputs    map[string]any `json:"inputs"`
		}
		if err := json.Unmarshal([]byte(text), &m); err != nil {
			return errors.New("不是有效的 JSON：" + err.Error())
		}
		if len(m) == 0 {
			return errors.New("工作流是空的")
		}
		for id, n := range m {
			if n.ClassType == "" {
				return fmt.Errorf("节点 %s 缺少 class_type（需要 API 格式的工作流）", id)
			}
		}
		hasCreate := false
		for _, n := range m {
			if n.ClassType == "CreateVideo" {
				hasCreate = true
			}
		}
		_ = hasCreate
		for _, ph := range []string{"{{IMAGE}}", "{{PROMPT}}"} {
			if !strings.Contains(text, ph) {
				return fmt.Errorf("工作流里没有 %s 占位符", ph)
			}
		}
	case "files":
		var fs []struct {
			URL string `json:"url"`
			Dir string `json:"dir"`
		}
		if err := json.Unmarshal([]byte(text), &fs); err != nil {
			return errors.New("不是有效的 JSON：" + err.Error())
		}
		for _, f := range fs {
			if !strings.HasPrefix(f.URL, "http") || f.Dir == "" {
				return errors.New("每一项都要有 url 和 dir")
			}
		}
	case "boot":
		var m map[string]any
		if err := json.Unmarshal([]byte(text), &m); err != nil {
			return errors.New("不是有效的 JSON：" + err.Error())
		}
	}
	return nil
}

// fillWorkflow 用实际参数替换占位符；整值占位符替换成对应类型
func fillWorkflow(text string, vals map[string]any) (map[string]any, error) {
	var wf map[string]any
	if err := json.Unmarshal([]byte(text), &wf); err != nil {
		return nil, err
	}
	var walk func(v any) any
	walk = func(v any) any {
		switch x := v.(type) {
		case string:
			if strings.HasPrefix(x, "{{") && strings.HasSuffix(x, "}}") {
				if val, ok := vals[strings.Trim(x, "{}")]; ok {
					return val
				}
			}
			for k, val := range vals {
				x = strings.ReplaceAll(x, "{{"+k+"}}", fmt.Sprint(val))
			}
			return x
		case map[string]any:
			for k, vv := range x {
				x[k] = walk(vv)
			}
			return x
		case []any:
			for i, vv := range x {
				x[i] = walk(vv)
			}
			return x
		}
		return v
	}
	walk(wf)
	return wf, nil
}

// videoDims 768p：短边 768，长边不超过 1344，32 的倍数
func videoDims(aspect, res string) (int, int) {
	short := 768
	if res == "480p" {
		short = 480
	}
	r := ratioValue(aspect)
	if r <= 0 {
		r = 9.0 / 16
	}
	round32 := func(x float64) int { return int(x/32+0.5) * 32 }
	if r >= 1 {
		long := round32(float64(short) * r)
		if long > short*7/4 {
			long = round32(float64(short) * 7 / 4)
		}
		return long, short
	}
	long := round32(float64(short) / r)
	if long > short*7/4 {
		long = round32(float64(short) * 7 / 4)
	}
	return short, long
}

// framesFor 24fps，17k+5 网格
func framesFor(sec int) int {
	n := sec * 24
	if n < 5 {
		n = 5
	}
	return n + ((5-n%17)%17+17)%17
}
