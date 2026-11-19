# 漫剧工坊

以 Colab 为算力中心的 AI 漫剧（竖屏短篇动态漫画剧）制作工坊。自托管、单个二进制、单管理员。

一条龙流程：**设定 → 剧本 → 资产 → 分镜 → 关键帧 → 视频 → 成片**。
剧本、资产提取和分镜用文本渠道（OpenAI 兼容对话接口）；角色/场景/道具设定图和关键帧用生图渠道；
镜头视频在你自己的 Colab 运行时上用 ComfyUI + MiniMax H3（官方 INT8 预量化权重）做图生视频；最后用 ffmpeg 拼接成片。

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

数据目录 `/data`：`manju.db`（SQLite）、`images/`、`videos/`、`tmp/`。镜像自带 ffmpeg 和中文字体（烧录字幕用）。

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

视频环境（首次开机自动执行，可重复）：克隆 ComfyUI → 安装依赖 → 可选 SageAttention（失败自动回退）→
aria2 16 线程下载权重（`Comfy-Org/MiniMax-H3`：`minimax_h3_fl2va_pruned_int8_convrot` 约 21 GB、Qwen3-VL 文本编码器 NVFP4、视频/音频 VAE、Turbo 8 步 LoRA）→
启动 ComfyUI。模型文件列表、运行环境参数、标准 / Turbo 两套工作流（ComfyUI API 格式）都可以在「算力 → 视频模型」里改。

工作流占位符：`{{PROMPT}}` `{{IMAGE}}` `{{WIDTH}}` `{{HEIGHT}}` `{{LENGTH}}`（24fps，17k+5 帧）`{{SEED}}` `{{PREFIX}}`。
默认 768p（竖屏 768×1344），5–10 秒，H3 同时生成配音与音效。

运行时通信细节见 [docs/COLAB_NOTES.md](docs/COLAB_NOTES.md)；要点：运行时连接地址是 Jupyter Server，请求头 `X-Colab-Runtime-Proxy-Token`，
代码通过内核 WebSocket 执行，文件经 contents 接口上传、`/files/` 下载。

## 制作流程

| 步骤 | 做什么 |
|---|---|
| ① 设定 | 剧名、创意、风格、画幅（默认 9:16）、单集时长、目标镜头数、文本/生图渠道、视频分辨率与 Turbo |
| ② 剧本 | AI 按「钩子开场 → 节拍 → 反转 → 卡点」写单集剧本，可改写、可粘贴自己的剧本，自动保存 |
| ③ 资产 | 从剧本提取角色 C01…、场景 S01…、道具 P01…；逐个生成设定图，多张候选选一张，可重生成或上传 |
| ④ 分镜 | AI 拆成 5–10 秒的镜头：镜头语言、画面、台词、出场资产编号、关键帧提示词、视频描述；可编辑、排序、增删 |
| ⑤ 关键帧 | 用出场资产的选定图作参考图生成关键帧（角色 → 场景 → 道具，按渠道上限截断，超出的用文字描述补上） |
| ⑥ 视频 | 镜头排队到 Colab 图生视频；每个镜头显示排队位置 / 进度 / 结果，可重生成、取消、在多个版本间选择 |
| ⑦ 成片 | 按顺序预览；ffmpeg 统一分辨率与帧率后拼接，可选烧录台词字幕；下载成片、字幕、全部素材 zip |

## 对外接口（给其他智能体）

在「接口」页创建固定密钥（只显示一次，可吊销）。请求头 `Authorization: Bearer <密钥>`。

### MCP

可流式 HTTP 端点：`POST /mcp`（JSON-RPC 2.0，返回 `application/json`）。

```json
{ "mcpServers": { "manju": { "type": "http", "url": "https://你的域名/mcp", "headers": { "Authorization": "Bearer <密钥>" } } } }
```

| 工具 | 说明 |
|---|---|
| `colab_status` | 运行时状态、排队数、已用 CU 估算、订阅 |
| `colab_specs` | 可用运行时规格与每小时 CU |
| `colab_start` | 开机：`purpose=video`（需 GPU，自动准备视频环境）或 `manual`（仅开机，可选 CPU） |
| `colab_stop` | 关机 |
| `colab_run_python` | 在运行时内核里执行 Python，返回 stdout / stderr / 结果 / 错误 |
| `video_submit` | 提交图生视频：`image_id` / `image_url` / `image_base64` / `shot_id` + `prompt`、`seconds`、`turbo`、`resolution`、`aspect`、`seed` |
| `video_status` | 查询视频任务；完成后带下载地址 |
| `list_projects` | 作品与缺失步骤 |
| `list_shots` | 作品分镜、关键帧与视频地址 |
| `image_generate` | 用生图渠道出图 |

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
| POST | `/v1/images` | `{prompt, channel_id, model, aspect, n}` |
| GET | `/v1/images/{id}/file` | 下载图片 |

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

## 许可

MIT
