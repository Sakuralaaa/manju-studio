# 漫剧工坊

以 Colab 为算力中心的 AI 漫剧（竖屏短篇动态漫画剧）制作工坊。自托管、单个二进制、单管理员。

作品是**连载剧集**：设定集（世界观 / 人物小传 / 分集总纲）+ 多集，资产库跨集共享；每一集走
**设定 → 剧本 → 资产 → 分镜 → 关键帧 → 视频 → 成片** 七步，也可以**一键成集**。
剧本、资产提取、分镜、摘要用文本渠道（OpenAI 兼容对话接口）；角色设定图 / 定妆图 / 关键帧 / 封面用生图渠道；
镜头视频在你自己的 Colab 运行时上用 ComfyUI + MiniMax H3（官方 INT8 预量化权重）做图生视频；配音用语音渠道
（Fish Audio、小米 MiMo、OpenAI 兼容）；最后用 ffmpeg 混音、拼接成片，也能导出剪映草稿。

## 部署

```yaml
services:
  manju-studio:
    image: ghcr.io/sakuralaaa/manju-studio:latest
    restart: unless-stopped
    ports: ["8080:8080"]
    environment:
      ADMIN_PASSWORD: "你的密码"
      TZ: Asia/Shanghai
    volumes: ["./data:/data"]
```

数据目录 `/data`：`manju.db`（SQLite）、`images/`、`videos/`、`audio/`（配音、音色样本、配乐、音效）、`tmp/`。镜像自带 ffmpeg 和中文字体（烧录字幕用）。

| 环境变量 | 说明 | 默认 |
|---|---|---|
| `ADMIN_PASSWORD` | 管理员密码（必填，改密码后旧会话失效） | — |
| `PORT` / `LISTEN` | 监听端口 / 地址 | `8080` |
| `DATA_DIR` | 数据目录 | `/data` |

## 渠道

- **生图渠道**：接口地址 + 密钥 + 能力档案。新建时可用「OpenAI 官方 / 比例型 / 尺寸型 / 提示词比例型」快速填充（只是预填，类型由你自己选）。
  渠道不支持的参数（比例、质量等）自动写进提示词。参考图上限按档案截断（实测：比例型 5 张、尺寸型 20 张、提示词比例型 3 张），
  返回链接的图片立即下载落盘。
- **文本渠道**：OpenAI 兼容 `/v1/chat/completions`，测试连接后勾选模型，作品里可以选渠道和模型。
- **语音渠道**（配音）：
  - **Fish Audio**（主要）：`POST {base}/v1/tts`，请求头 `model`（`s2.1-pro` 默认，可选 `s2-pro` `s2.1-pro-free` `s3-pro` `drama-3` `s1`），
    角色音色 = 模型的 `reference_id`。应用内可浏览 / 搜索公开中文音色并试听（`GET /model?language=zh`），
    也能用上传或浏览器录音的 10–60 秒样本新建自己的音色（`POST /model` multipart：`visibility=private` `type=tts` `train_mode=fast`
    `voices` 多个文件 + 对应 `texts`），列出 / 改名 / 删除自己的音色（`self=true`、`PATCH` / `DELETE /model/{id}`）。
    语速走 `prosody.speed`；台词语气（如「压低声音」「愤怒」）映射成 S2 的 `[whispering]` `[angry]` 等标记（S1 用圆括号）。
  - **小米 MiMo**：`POST {base}/chat/completions`，请求头 `api-key`，要念的文字放 assistant 消息，语气指令放 user 消息，
    音频在 `choices[0].message.audio.data`（base64 wav）。内置音色用 `mimo-v2.5-tts`；**设计音色**用 `mimo-v2.5-tts-voicedesign`
    按文字描述出 2–3 个候选，留下的那段存成本地样本，之后用 `mimo-v2.5-tts-voiceclone`（`audio.voice = data:audio/wav;base64,…`）合成；
    克隆音色同样走 voiceclone。
  - **OpenAI 兼容**：`POST {base}/audio/speech`（`model` `voice` `speed` `instructions`）。
  - 没有原生语速的渠道在本地用 ffmpeg 变速；音调（半音）统一本地处理。
- 密钥只存在数据库里，界面只显示后四位。

## 算力（Colab）

1. 在 Google Cloud 控制台建一个「桌面应用」类型的 OAuth 客户端，下载 JSON，在「算力 → 概览」上传。
2. 点「打开授权页」，同意访问 Colab（scope `https://www.googleapis.com/auth/colaboratory`，PKCE，`access_type=offline`）。
   浏览器会跳到打不开的 `http://localhost/?code=…`，把整条地址粘贴回来，点「完成授权」。之后自动刷新访问令牌。
3. 概览里能看到订阅档位和可用运行时规格。

调度：

- 排队的视频攒在**同一次 GPU 会话**里跑完，开机（约 5 分钟、约 1 CU）只付一次；
- 队列空闲超过 N 分钟自动关机，单次会话有最长时长，单段视频有超时（都可在「调度」里改）；
- 「有排队视频时自动开机」默认关闭，开机失败后不会自动重试；
- 每个花钱的按钮都显示估算：生图显示张数，视频显示 CU（G4 ≈ 8.9 CU/小时等费率为第三方估算，可改）；
- 服务启动时会清理上次遗留的本应用运行时（名字以 `manju-` 开头）。

- 「算力 → 账本」：每集实际用掉的视频 GPU 分钟 / CU（开机与空闲按会话里各集的视频数分摊）、估算 CU、生图张数、配音条数、对话次数；
  每次会话的开机时长和出视频时长。

视频环境（首次开机自动执行，可重复）：克隆 ComfyUI → 安装依赖 → 可选 SageAttention（失败自动回退）→
aria2 16 线程下载权重（`Comfy-Org/MiniMax-H3`：`minimax_h3_fl2va_pruned_int8_convrot` 约 21 GB、Qwen3-VL 文本编码器 NVFP4、视频/音频 VAE、Turbo 8 步 LoRA）→
启动 ComfyUI。模型文件列表、运行环境参数、标准 / Turbo 两套工作流（ComfyUI API 格式）都可以在「算力 → 视频模型」里改。

模型权重（「算力 → 调度 → 模型权重」，默认都关闭）：

- **下载镜像**：填一个 Hugging Face 镜像根地址（例如 `https://hf-mirror.com`），开机时把 `https://huggingface.co` 前缀替换掉。
- **云端硬盘缓存**：开启后授权多申请 `https://www.googleapis.com/auth/drive.file`（只能访问本应用自己建的文件），
  界面会提示需要重新授权一次（已有的授权只有 Colab 权限，应用不会自己去改）。之后：
  1. 开机时应用在云端硬盘里建「漫剧工坊权重缓存」文件夹，把访问令牌写到运行时的 `/content/mj_drive_token`（每 10 分钟刷新）；
  2. 第一次会话照常从 Hugging Face 下载，下载完在运行时里起一个后台线程，用可续传上传（64 MB 分块，每块前重读令牌）把权重传进文件夹，
     进度写在 `/content/mj_drive_status.json`，应用定时读回并记下每个文件的编号与大小；
  3. 之后的会话，大小一致的文件直接从 `https://www.googleapis.com/drive/v3/files/{id}?alt=media` 用 aria2 16 线程拉取，缺的再走 Hugging Face。

画质后期（每个作品在「设定 → 声音与画质」里选，**未在 GPU 上实测**）：

- 云端放大：在 CreateVideo 之前插入 `UpscaleModelLoader`（默认 `RealESRGAN_x2plus.pth`）→ `ImageUpscaleWithModel` → `ImageScale(lanczos)` 到 1080×1920；
- 云端补帧：插入 Fannovel16/ComfyUI-Frame-Interpolation 的 `RIFE VFI`（`rife47.pth`，2 倍到 48 帧/秒），开机时按需安装节点；
- 模型、节点仓库、倍数都在「算力 → 视频模型 → 画质后期」里改；
- 交付 1080p 时，成片在本地用 ffmpeg lanczos 统一缩放到 1080×1920（廉价兜底，云端没放大时也能交付 1080p）。

工作流占位符：`{{PROMPT}}` `{{IMAGE}}` `{{WIDTH}}` `{{HEIGHT}}` `{{LENGTH}}`（24fps，17k+5 帧）`{{SEED}}` `{{PREFIX}}`。
默认 768p（竖屏 768×1344），5–10 秒，H3 同时生成配音与音效。

运行时通信细节见 [docs/COLAB_NOTES.md](docs/COLAB_NOTES.md)；要点：运行时连接地址是 Jupyter Server，请求头 `X-Colab-Runtime-Proxy-Token`，
代码通过内核 WebSocket 执行，文件经 contents 接口上传、`/files/` 下载。

## 剧集

- **作品 = 设定集 + 多集**。设定集里写世界观、人物小传、分集总纲，可以让 AI 根据创意生成或完善；资产库（角色 / 场景 / 道具、定妆图、音色）全剧共享。
- 写第 N 集剧本时，模型拿到：设定集 + 前几集的摘要 + 上一集的卡点（开场必须接住）。新建一集时可以让 AI 先拟定本集梗概。
- 每集合成后自动写摘要（80–150 字）和卡点，供后面的集使用；也可以手动改。
- 升级时，原来的单集作品自动变成该剧的第 1 集（剧本、分镜、视频、成片都挂到第 1 集）。

## 制作流程（每一集）

| 步骤 | 做什么 |
|---|---|
| ① 设定 | 本集标题、梗概（可 AI 拟定）；剧名、创意、风格、画幅、单集时长、目标镜头数、视频规格、每镜抽卡次数；人声方式（视频原生 / 配音覆盖）、旁白音色、交付画质、云端放大 / 补帧；文本 / 生图渠道 |
| ② 剧本 | AI 按「接住上集卡点 → 钩子 → 节拍 → 反转 → 卡点」写本集剧本，可改写、可粘贴，自动保存 |
| ③ 资产 | 从本集剧本补充提取资产（已有的不重复）；角色可做设定图、**定妆三视图**、**表情表**；每个角色设音色；「一键设计音色」给所有没音色的角色和旁白设计 |
| ④ 分镜 | AI 拆镜头；每个镜头可选与上一镜头的衔接：不衔接 / 最后一帧作参考 / 最后一帧作首帧（首尾帧生成） |
| ⑤ 关键帧 | 参考图按相关度挑选：本镜头最重要的角色（有台词、全剧出场多）用三视图，衔接时上一镜头最后一帧紧随其后，然后其余角色、场景、道具；超出渠道上限的改用文字描述；每个出场角色的固定造型（发型、服装、配饰）从外貌描述和人物小传里抽出来写进提示词。封面、表情表同样优先用定妆三视图作参考 |
| ⑥ 视频 | 每个镜头抽 1–4 条（CU 估算随之变化），候选条带里预览、选用，可两条并排同步播放对比；首尾帧镜头等上一镜头出视频后再排队 |
| ⑦ 成片 | **合成**：字幕、封面（生图模型渲染标题）、本集摘要；**配音**：逐句台词，可填语气、试听、单句重做；**配乐音效**：上传曲库，选曲、音量、淡入淡出、对白时自动压低，每个镜头可加音效；**导出**：成片、字幕、素材包、剪映草稿 |

配音覆盖模式下，视频描述只让 H3 生成口型和环境声（不要人声），H3 原声按音量压低，逐句配音按镜头排开（超长时先压缩句间隔，再最多加速 1.5 倍），旁白作画外音。

成片混音：逐镜头统一画面（可选 1080p lanczos、烧录字幕）并混入原声与音效 → 拼接 → 叠加配音轨 → 配乐循环铺满、淡入淡出，
`sidechaincompress` 以对白为触发自动压低配乐 → `alimiter`。

**剪映草稿**：`draft_content.json` + `draft_meta_info.json` + `materials/`，打成 zip。轨道：视频、配音、音效、配乐、字幕（文字轨）。
结构对照开源项目 pyJianYingDraft（剪映 5.9 草稿格式）；填写本机草稿目录时素材写绝对路径，留空时用剪映的草稿占位前缀。解压到剪映草稿目录即可打开（未在剪映客户端实测）。

**一键成集**：梗概 → 剧本 → 资产（提取、设定图、音色）→ 分镜 → 关键帧 → 配音 → 视频排队 → 成片 → 摘要与封面。
「全自动」一路做完；「关键处问我」在剧本、资产、分镜之后暂停等确认；「外部编剧」在梗概、剧本、分镜处停下（状态 `waiting_writer`），由 Antigravity 里的 Gemini 等外部智能体通过 MCP 写入后自动继续，应用不调用文本模型（见 [docs/ANTIGRAVITY.md](docs/ANTIGRAVITY.md)）。每一步先检查是否已完成，所以随时停下后可以继续；服务重启后自动接着跑。
视频只排队，是否开机由「算力 → 调度」里的「有排队视频时自动开机」决定（默认关闭）。

## 对外接口（给其他智能体）

在「接口」页创建固定密钥（只显示一次，可吊销）。请求头 `Authorization: Bearer <密钥>`。

### MCP

可流式 HTTP（Streamable HTTP）端点：`POST /mcp`（JSON-RPC 2.0，返回 `application/json`；`GET` 返回 405，不提供服务端 SSE 流；校验 `Origin` 与 `MCP-Protocol-Version`）。

**Antigravity**：`mcp_config.json` 里写 `serverUrl` + `headers`（或用仓库里的 stdio 桥接 `cmd/manju-mcp-bridge`），工作区放规则文件 [docs/antigravity/AGENTS.md](docs/antigravity/AGENTS.md)。完整说明见 [docs/ANTIGRAVITY.md](docs/ANTIGRAVITY.md)；「接口」页可一键复制配置和规则文件。

```json
{ "mcpServers": { "manju": { "type": "http", "url": "https://你的域名/mcp", "headers": { "Authorization": "Bearer <密钥>" } } } }
```

| 工具 | 说明 |
|---|---|
| `get_writing_context` | 外部编剧先调用：设定集、前几集摘要与卡点、资产库（含角色固定造型）、本集状态、下一步 |
| `save_bible` | 写设定集：世界观、人物小传（文本或人物数组）、分集总纲 |
| `save_synopsis` | 写本集梗概 |
| `save_script` | 写剧本：结构化分场（画面 / 台词含说话人与语气 / 旁白）+ 卡点，或整份 Markdown |
| `save_shots` | 写（整体替换）分镜：时长、景别运镜、画面、台词、资产引用、衔接（cut / continuous / first_last）、关键帧与视频提示词 |
| `save_episode_summary` | 写本集摘要与卡点 |
| `upsert_asset` | 新建或更新角色 / 场景 / 道具；角色的固定造型（发型、五官、服装、配饰）会写进每张关键帧提示词，所以 `look` 只写静态外貌和固定服装；常带道具 / 习惯动作写 `props_note`（不注入提示词），写了手持物 / 姿势会在 `warnings` 提醒 |
| `update_shot` | 改单个镜头（`keyframe_prompt`、`video_prompt`、`camera`、`picture`、`dialogue`、`assets`、`duration`、`transition`、`ref_ids`，只改给出的字段），保留已有关键帧 / 视频候选 |
| `create_project` | 新建作品（同时建第 1 集）：标题、画风、作品类型、画幅、单集时长、默认生图渠道 / 模型、声音模式（native / dub）等 |
| `update_project` | 改作品设置（字段同上，只改给出的） |
| `delete_asset` | 删除资产及其图片，并从镜头资产列表里去掉；必须 `confirm=true`（不传只预览） |
| `detach_image` | 从资产里拿掉并删除一张图（`asset_id`、`image_id`）；必须 `confirm=true` |
| `view_asset_images` | 资产设定图 / 三视图 / 表情表，MCP 图片内容块（默认长边 ≤ 1536 的 JPEG，可要原图） |
| `view_shot` | 镜头信息 + 关键帧 + 选定视频首尾帧（图片内容块） |
| `view_image` | 按图片编号看任意图（画室、资产、关键帧、封面），`image_id` 或 `image_ids`（≤ 8），图片内容块 |
| `listen_dub` | 台词配音，MCP 音频内容块 |
| `listen_voice` | 听音色：`sample(s)`（design_voice 候选）、`asset_id`（角色当前音色样本 + 原声参考）或 `project_id`（旁白），音频内容块 |
| `clone_voice` | 用原声克隆角色音色：`source_shot_id`（镜头选定视频，可配 `video_id`）/ `audio_base64` / `audio_url`，可选 `start` / `end` 秒；Fish 渠道建私有音色，否则走 MiMo voiceclone；样本存进角色 `voice_refs`。视频原声里的配乐 / 环境声不会去除 |
| `get_video_link` | 视频 / 成片的限时下载地址与建议文件名（MCP 没有视频内容类型，下载到本地看） |
| `colab_status` | 运行时状态、排队数、已用 CU 估算、订阅 |
| `colab_specs` | 可用运行时规格与每小时 CU |
| `colab_start` | 开机：`purpose=video`（需 GPU，自动准备视频环境）或 `manual`（仅开机，可选 CPU） |
| `colab_stop` | 关机 |
| `colab_run_python` | 在运行时内核里执行 Python，返回 stdout / stderr / 结果 / 错误 |
| `video_submit` | 提交图生视频：`image_id` / `image_url` / `image_base64` / `shot_id`（可带 `takes` 抽卡次数）+ `prompt`、`seconds`、`turbo`、`resolution`、`aspect`、`seed` |
| `video_status` | 查询视频任务；完成后带下载地址 |
| `list_projects` | 作品（剧集）与每集缺失步骤 |
| `list_shots` | 作品全部集的分镜、关键帧、选定视频与抽卡候选 |
| `list_episodes` | 设定集与各集梗概、摘要、卡点、进度 |
| `create_episode` | 新建下一集，`ai_idea=true` 自动拟定梗概 |
| `get_episode` | 一集的剧本、摘要、步骤状态、分镜 |
| `autopilot_start` | 一键成集，`mode=auto` / `ask` / `writer`（外部编剧） |
| `autopilot_status` | 一键成集进度与日志 |
| `autopilot_continue` | 确认暂停点或从失败处重试 |
| `autopilot_cancel` | 停止一键成集 |
| `generate_dub` | 生成一集的全部配音 |
| `tts` | 用角色音色（`asset_id`）或指定音色合成一句话 |
| `design_voice` | 按描述设计音色候选，返回样本名、试听地址和音频内容块 |
| `set_voice` | 把候选设为角色 / 旁白音色（可选 `synth_channel_id` 指定配音渠道） |
| `image_generate` | 用生图渠道出图（放进画室，用 `view_image` 看） |
| `generate_keyframe` | 给镜头出关键帧；`extra_prompt`（只这一次的追加要求）、`ref_tags`（如 `["main"]` = 只用设定图锁脸，不带三视图、不注入固定造型） |

### REST

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/v1/colab` | 运行时状态 |
| GET | `/v1/colab/specs` | 可用规格 |
| POST | `/v1/colab/start` | `{accelerator, shape, purpose}` |
| POST | `/v1/colab/stop` | 关机 |
| POST | `/v1/colab/exec` | `{code, timeout_sec}` |
| POST | `/v1/videos` | 提交视频（字段同 `video_submit`） |
| GET | `/v1/videos/{id}` | 视频状态 |
| GET | `/v1/videos/{id}/file` | 下载视频 |
| GET | `/v1/projects` | 作品列表 |
| GET | `/v1/projects/{id}/shots` | 分镜 |
| GET | `/v1/projects/{id}/episodes` | 设定集与各集 |
| POST | `/v1/projects/{id}/episodes` | `{idea, ai_idea}` 新建一集 |
| GET | `/v1/episodes/{id}` | 一集详情 |
| GET | `/v1/episodes/{id}/final` | 下载成片 |
| POST | `/v1/episodes/{id}/autopilot` | `{mode}` 一键成集 |
| GET | `/v1/episodes/{id}/autopilot` | 进度 |
| POST | `/v1/episodes/{id}/autopilot/continue` | 确认 / 重试 |
| POST | `/v1/episodes/{id}/autopilot/cancel` | 停止 |
| POST | `/v1/episodes/{id}/lines` | `{force}` 生成配音 |
| POST | `/v1/tts` | `{text, emotion, asset_id \| voice}` → `{url, duration}` |
| GET | `/v1/audio/{name}` | 下载音频 |
| POST | `/v1/images` | `{prompt, channel_id, model, aspect, n}` |
| GET | `/v1/images/{id}/file` | 下载图片 |
| GET | `/v1/dl/{token}` | `get_video_link` 给出的限时下载地址（令牌即授权，2 小时有效） |

```bash
curl -X POST https://你的域名/v1/videos \
  -H "Authorization: Bearer <密钥>" -H "Content-Type: application/json" \
  -d '{"image_url":"https://…/frame.png","prompt":"她缓缓回头，风吹起发梢","seconds":5}'
```

## 开发

```bash
cd web && npm ci && npm run build && cd ..
go build -o manju-studio . && ADMIN_PASSWORD=dev DATA_DIR=./data ./manju-studio
```

Go 1.26 + SQLite（modernc，无 CGO）+ React / Vite / TypeScript（手写样式），前端嵌入单个二进制。
推送 `v*` 标签时 GitHub Actions 构建 amd64 + arm64 镜像到 `ghcr.io/sakuralaaa/manju-studio`（`latest` / `x.y.z` / `x.y` / `sha`）。
推送到 `ci-check` 分支（或手动触发）时另一个工作流只构建 amd64 镜像做检查，不推送。

## 许可

MIT
