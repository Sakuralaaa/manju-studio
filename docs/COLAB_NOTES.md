# Colab 运行时：实测可用的协议（2026-11-19）

全部在 2026-11-19 13:08–13:12 UTC 用真实账号（订阅 SUBSCRIPTION_TIER_PRO）实测。
只创建过一台 CPU 运行时（VARIANT_CPU / NONE / SHAPE_STANDARD，id `manju-cpu-probe`），约 4 分钟后删除，
随后 `GET v1beta/runtimes` 返回 `{}`（空）。没有创建任何 GPU 运行时。

## 1. OAuth

- 桌面客户端 JSON（`installed.client_id / client_secret / token_uri`）。
- 授权地址：`https://accounts.google.com/o/oauth2/v2/auth`
  `?client_id=…&redirect_uri=http://localhost&response_type=code`
  `&scope=https://www.googleapis.com/auth/colaboratory&access_type=offline&prompt=consent`
  `&code_challenge=<S256>&code_challenge_method=S256&state=…`
- 用户授权后浏览器跳到 `http://localhost/?state=…&code=…&scope=…`（打不开没关系），把整段地址贴回应用。
- 换取：`POST https://oauth2.googleapis.com/token`（form）`grant_type=authorization_code, code, redirect_uri=http://localhost, client_id, client_secret, code_verifier` → `refresh_token`。
- 刷新：同一地址 `grant_type=refresh_token` → `access_token`（expires_in≈3599）。实测可用。

## 2. Colab API v1beta（`https://colaboratory.googleapis.com/`，`Authorization: Bearer <access_token>`）

| 调用 | 实测结果 |
|---|---|
| `GET v1beta/subscription` | `{"name":"subscription","tier":"SUBSCRIPTION_TIER_PRO"}` |
| `GET v1beta/runtimespecs` | 11 项，`eligible:true` 的有 CPU 标准/高内存、GPU A100/T4/G4/L4、TPU V5E1/V6E1；H100 没有 `eligible` 字段（=不可用） |
| `POST v1beta/runtimes?runtimeId=<id>` body `{"runtimeSpec":{variant,accelerator,shape}}` | 立即返回 Operation `{name:"operations/…", metadata:{@type:…CreateRuntimeMetadata, runtime:"runtimes/<id>"}}`，**没有 operations.get**（`GET v1beta/operations/…` 404），改为轮询 GetRuntime |
| `GET v1beta/runtimes/<id>` | CPU 机器约 10 秒内就带上 `connectionInfo{url, token(217 字符), expireTime(+1h)}` |
| `GET v1beta/runtimes` | 有机器时 `{"runtimes":[…]}`，空时 `{}` |
| `DELETE v1beta/runtimes/<id>` | `{}`，立即从列表消失 |

- runtimeId 规则：小写字母开头，字母数字连字符，1–63。
- 创建被拒时（额度/资格）返回 FAILED_PRECONDITION，细节在 Operation.error 的 ErrorInfo 里。
- 令牌 1 小时过期：快到 `expireTime` 时再调一次 GetRuntime 拿新 token。

## 3. 运行时 = Jupyter Server 2.20（Tornado），所有请求带头 `X-Colab-Runtime-Proxy-Token: <token>`

- `GET {url}/api` → `{"version":"2.20.0"}`
- **contents 根目录是 `/`**（`--FileContentsManager.root_dir=/`），内核工作目录是 `/content`。
  所以 `/content/x.bin` 对应 contents 路径 `content/x.bin`。
- 上传：`PUT {url}/api/contents/content/<路径>` body `{"type":"file","format":"base64","content":"…"}` → 201。
- 下载：`GET {url}/files/content/<路径>` 直接返回原始字节（6 MB 用时 0.3 秒）；
  `GET /api/contents/…?content=1&format=base64&type=file` 也行但要 base64 解码。
- **建内核用 sessions 接口**：`POST /api/sessions {"path":"manju.ipynb","type":"notebook","name":"manju.ipynb","kernel":{"name":"python3"}}` → 201，内核稳定。
  第一次直接 `POST /api/kernels` 后约 40 秒 Jupyter 服务整个重启了一次（期间 502，约 20 秒，之前的内核全部丢失）；
  这可能是分配后的首次重启而不是 kernels 接口本身的问题，但为稳妥：用 sessions 接口建内核，所有调用对 502/404 做重试，内核丢失时重新建会话。
- 执行：WebSocket `wss://{host}/api/kernels/<kernel_id>/channels?session_id=<随机hex>`，同样带 `X-Colab-Runtime-Proxy-Token` 头；
  **不要请求子协议**（`v1.kernel.websocket.jupyter.org` 会 404，Colab 不支持）；帧是 JSON 文本（旧版 Jupyter 消息格式，带 `channel` 字段）。
- 发送 `execute_request`（`channel:"shell"`，`header.session` 与 URL 的 session_id 一致，`version:"5.3"`）。
- 接收：按 `parent_header.msg_id` 过滤。实测顺序：`status busy` → `stream(stdout/stderr)`（逐行实时，tick 间隔 1 秒准确）→ `execute_result` → `execute_reply(channel=shell)` → `status idle`。
  **shell 与 iopub 之间顺序不保证**（有一次 `execute_reply` 早于 `status busy` 到达），所以要等到 *既收到 execute_reply 又收到 idle* 才算结束。
  异常：`error{ename, evalue, traceback}` + `execute_reply.status=="error"`。
- 运行时环境（CPU 标准）：Linux 6.6 x86_64，Python 3.13，2 核，12 GB 内存，`/` 有 226 GB，自带 ffmpeg、git，没有 aria2c、没有 nvidia-smi。
- 沙箱里的出站 WebSocket 不能走 HTTP 代理（websockets 库报代理认证错误），直连可用。Go 版本同样直连。

## 4. 保活

- 官方 VS Code 扩展每 5 分钟对有活动内核的服务器发保活；空闲 1 小时后视为闲置。
  本应用的做法：任务执行期间 WebSocket 一直连着并持续有输出；空闲超时与最长会话时长由应用自己控制，到点直接 DELETE 运行时。

## 5. 视频模型（未在 GPU 上实测，来源见下）

- 权重（Hugging Face `Comfy-Org/MiniMax-H3`，未加门槛，2026-11-19 用 HF API 列出的大小）：
  - `diffusion_models/minimax_h3_fl2va_pruned_int8_convrot.safetensors` 20.97 GB（官方 INT8 预量化，图生视频/文生视频）
  - `text_encoders/qwen3vl_32b_minimax_h3_nvfp4_awq.safetensors` 15.69 GB
  - `vae/minimax_h3_video_vae_int8_convrot.safetensors` 2.81 GB
  - `vae/minimax_h3_audio_vae_fp32.safetensors` 0.61 GB
  - `loras/minimax_h3_fl2v_turbo_8step_v1.0_comfyui_bf16.safetensors` 1.96 GB（Turbo 8 步）
- 工作流来自 `Comfy-Org/workflow_templates/templates/video_minimax_h3_i2v.json`（子图展开成 API 格式，见应用内「视频工作流」设置）：
  UNETLoader → (LoraLoaderModelOnly，Turbo 时) → BasicScheduler(simple, 步数 20 / Turbo 8) ；
  CLIPLoader(type=minimax) + VAELoader×2 → MiniMaxH3ImageToVideo(prompt, first_frame, width, height, length) →
  SamplerCustomAdvanced(RandomNoise, BasicGuider, KSamplerSelect=res_multistep) → VAEDecode + VAEDecodeAudio → CreateVideo(24fps) → SaveVideo。
  帧数：`n=max(5, round(秒*24)); length = n + (5 - n%17) % 17`（17k+5 网格）。768p：竖屏 768×1344、横屏 1344×768（32 的倍数）。
- 需要 ComfyUI ≥ 0.30.0（2026-08-03 合入原生 MiniMax H3 支持）。
- 来源：https://docs.comfy.org/tutorials/video/minimax/minimax-h3-native ，https://huggingface.co/Comfy-Org/MiniMax-H3 ，
  https://github.com/Comfy-Org/workflow_templates/blob/main/templates/video_minimax_h3_i2v.json

## 6. 应用内 Go 实现的真机验证（2026-11-19 13:28:45–13:29:08 UTC，CPU 运行时 `manju-768c11f0`，约 23 秒）

用 `live_test.go`（构建标签 livetest，不进仓库）直接驱动应用代码：
- `runner.Start("NONE", …, "manual")` → CreateRuntime → 轮询 GetRuntime 拿到 connectionInfo → Ping → sessions 建内核：8 秒就绪；
- `execOnRuntime`：`21*2` 返回 `42`，stdout 正常；`raise ValueError('测试错误')` 返回 `ValueError: 测试错误`；
- `Jupyter.Upload` 上传脚本 / 首帧，`Jupyter.Download` 经 `/files/` 回传；
- 在运行时上启动一个**模拟 ComfyUI**（aiohttp 实现 `/prompt` `/ws` `/history` `/system_stats`，用 ffmpeg 把首帧做成 2 秒带声音的 mp4），
  跑通 `renderVideo`：上传首帧 → 填工作流 → `job.py` 提交 → 进度 `[mj] P` 逐条回传（进度 0.98）→ `[mj] OUT` → 下载 mp4（ffprobe：2.00 秒，有音轨），用时 4.5 秒；
- 再走一遍调度：`enqueueVideo` → `runner.tick()` → `runNext` → 视频状态 done；
- `runner.Stop` → DeleteRuntime → `GET v1beta/runtimes` 返回 0 台。

没有验证的：真 GPU 上的 ComfyUI 安装、权重下载、MiniMax H3 推理、SageAttention、G4 实际耗时与 CU。

## 7. 第二版新增（2026-11-19 15:00 UTC，CPU 运行时，约 30 秒，用完删除，`GET v1beta/runtimes` 返回 0 台）

同一个 `live_test.go` 增加了两项，在真实 CPU 运行时 + 模拟 ComfyUI 上通过：
- **首尾帧 + 画质后期注入**：`renderVideo` 上传首帧和尾帧，发到 ComfyUI 的工作流里 `MiniMaxH3ImageToVideo.last_frame` 接到新增的 `LoadImage`（文件确实在 `ComfyUI/input/`），
  `CreateVideo.images` 依次经过 `UpscaleModelLoader → ImageUpscaleWithModel → ImageScale → RIFE VFI`，`fps` 变成 48。
- **云端硬盘缓存的状态回传**：应用把访问令牌写进 `/content/mj_drive_token`，从 `/content/mj_drive_status.json` 读回文件编号并记下。

没有验证的（需要 GPU 或额外授权）：
- MiniMax H3 的 `last_frame` 输入在真实节点上的效果（节点定义里有这个可选输入，未实测）；
- `RealESRGAN_x2plus` 放大、`RIFE VFI` 补帧在 G4 上的耗时与显存（估算默认每段 1.5 / 1 分钟，可改）；ComfyUI-Frame-Interpolation 的安装（`install.py`，cupy 后端）；
- 云端硬盘的真实上传 / 下载（现有授权只有 Colab 权限，没有替用户重新授权）。方法：drive.file 范围；`POST drive/v3/files` 建文件夹；
  `POST upload/drive/v3/files?uploadType=resumable` 拿到会话地址后按 64 MB 分块 `PUT`（`Content-Range`，308 继续）；下载 `GET drive/v3/files/{id}?alt=media` 带 Bearer 令牌，aria2 支持分段。

## 加速（v0.2.3）

- **TE-Speed**（HELPMEEADICE/TE-Speed-MiniMaxH3-OSS，GPLv3，开机时从 GitHub 拉取，不随本项目分发）：块缓存，只加在标准（20 步）工作流上。soren-labs/minimax-h3-colab 在 G4 实测 20 步 8 秒：480p 182s→104s，768p 514s→288s（full=8 cache=12）。
- 插件自带的 patch_model.py 只认 2026 年 8 月初的 ComfyUI；新版 ComfyUI 把块循环搬进了 `_run_blocks`，所以用自带的 `colab/te_patch.py` 加同样的钩子（区间执行 + `("block_loop", 0)`）。已在 ComfyUI 主线（2026-11-20）上打补丁并用模拟块验证：20 步走 8 完整 + 12 缓存，输出与不加速一致。没装上时自动跳过该节点，按原速出片。
- Turbo（8 步蒸馏）不加 TE-Speed：步数太少，缓存窗口里几乎没有可省的步。
- **SageAttention**：G4（Python 3.12、torch 2.11.0+cu128、sm_120）优先装 soren-labs 预编译的 2.2.0 轮子并校验 sha256，其他情况装 PyPI 版本。
- **为什么不换 BF16**：soren-labs 方案本身用的也是 int8 主模型；BF16 精简版 40.2 GB，是 int8 的两倍，下载和云端缓存都翻倍，画质差异很小（int8 量化误差约 0.9%，第三方实测）。按成本优先，保持 int8。
- 未在 GPU 上实测：TE-Speed 与新版 ComfyUI 的实际提速、预编译 Sage 轮子与当前 Colab torch 版本是否匹配。

## 生成后统一音色（0.2.11，`colab/vc.py`）

在视频运行时（和 ComfyUI 同一台机器）上执行，脚本可重复执行：

1. **安装（后台、只装一次）**：调度循环发现有要统一音色的视频在排队 / 生成时，先执行 `mode=setup_bg`，用 `nohup` 起一个安装脚本，不占用视频渲染：
   `git clone --depth 1 https://github.com/Plachtaa/seed-vc /content/seed-vc` →
   依赖（accelerate、librosa、huggingface-hub≥0.28.1、munch、einops、descript-audio-codec、pydub、resemblyzer、transformers==4.46.3、soundfile、hydra-core、pyyaml、python-dotenv、demucs）用 `pip install --target /content/vclib`，
   `--only-binary=scipy,numpy` 并用 `-c` 锁成机器上已有的 scipy / numpy / torch / torchaudio 版本（scipy 从源码编译会失败）→
   删掉 vclib 里的 torch* / torchaudio* / numpy* / nvidia* / triton* / **旧的 `typing` 补丁包**（`typing.py`、`typing-*`）/ dataclasses / enum34 / bin / functorch（会顶掉系统里的新版本）→
   给 `seed-vc/modules/bigvgan/bigvgan.py` 的 `_from_pretrained` 补默认值 `proxies: Optional[Dict] = None`、`resume_download: bool = False`（新版 huggingface_hub 不再传这两个参数）。
   完成标记 `/content/vclib/.mj_ok`，失败标记 `/content/mj_vc_setup.fail`（日志 `/content/mj_vc_setup.log`）。
2. **每条视频**（`mode=run`）：上传原版视频和母带到 `/content/mj_vc/in/` → 等安装完成 → ffmpeg 抽音轨（44.1k 立体声）→ `python -m demucs --two-stems vocals -n htdemucs` 分出 vocals / no_vocals →
   母带（按内容缓存）：可选先 demucs 去配乐 → 去首尾静音 + loudnorm → 单声道 44.1k、最长 25 秒 →
   `inference.py --source vocals --target 母带 --diffusion-steps 30 --length-adjust 1.0 --inference-cfg-rate 0.7 --f0-condition False --fp16 True`（f0 开时加 `--auto-f0-adjust`、`--semi-tone-shift`）→
   混音 `[1:a]aresample=44100,pan=stereo|c0=c0|c1=c0[a1];[2:a][a1]amix=inputs=2:duration=first:normalize=0`，视频流直接复制 → 应用下载为 `v<id>-vc-*.mp4`。
   显存不够（CUDA OOM）时调用 ComfyUI `/free` 卸载模型后重试一次（下一条视频要重新加载模型）。
3. **权重缓存**：`PYTHONPATH=/content/vclib:/content/seed-vc`、`HF_HOME=/content/mj_vc/hf`、`TORCH_HOME=/content/mj_vc/torch`，Seed-VC 自己的权重在 `seed-vc/checkpoints`。
   开了云端硬盘缓存时，第一次成功后把这三个目录打成 `mj_vc_weights.tar`（f0 模式 `mj_vc_weights_f0.tar`）后台传进同一个缓存文件夹，状态写 `/content/mj_vc_drive_status.json`，应用每分钟取一次记进缓存状态；之后的机器第一次用时先从云端硬盘拉这个包解开。
- 来源：bench/vc_setup.sh、vc_gpu.sh、vc_run.sh、vc_cpu.sh（2026-11-21 在 A100 / CPU 运行时上跑通的手动流程）。应用内的这套代码只在沙盒里用假的 demucs / Seed-VC 走通过流程和 ffmpeg 混音，**没有在真 GPU 上跑过**。
