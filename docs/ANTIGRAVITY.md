# 用 Antigravity 当外部编剧

让 Google Antigravity 里的 Gemini 通过漫剧工坊的 MCP 服务器写设定集、梗概、剧本、分镜和本集摘要，并能看资产图、关键帧、视频首尾帧，听台词配音。应用本身不调用任何文本模型；出图、配音、视频、成片照常由漫剧工坊完成。

## 1. 连接方式

漫剧工坊的 MCP 端点是 `https://<你的域名>/mcp`，传输方式是 **Streamable HTTP**（JSON-RPC 2.0，POST 返回 `application/json`），用「接口」页新建的密钥做 Bearer 认证。

### 方式 A：远程连接（推荐）

在 Antigravity 的智能体面板点「…」→ **MCP Servers** → **Manage MCP Servers** → **View raw config**，编辑 `mcp_config.json`（全局 `~/.gemini/config/mcp_config.json`，或工作区 `.agents/mcp_config.json`）：

```json
{
  "mcpServers": {
    "manju": {
      "serverUrl": "https://manju.example.com/mcp",
      "headers": { "Authorization": "Bearer <密钥>" },
      "timeoutSeconds": 600
    }
  }
}
```

- `serverUrl`（也可写 `url`）+ `headers` 是 Antigravity 官方文档给出的远程服务器写法，远程服务器必须是 Streamable HTTP；旧的 HTTP+SSE 和 `httpUrl` 字段不支持。
- `timeoutSeconds`：默认每次工具调用 180 秒。写剧本、看图的工具都是秒级；`image_generate` 等出图工具可能更久，所以建议 600。
- 不要把带真实密钥的 `mcp_config.json` 提交进 git（工作区配置尤其要注意）。

### 方式 B：本地桥接（stdio）

如果你的 Antigravity 版本连不上远程服务器，或不想把密钥写进 `headers`，用仓库自带的桥接程序：它从 stdin 读 MCP 消息（每行一条 JSON-RPC），转发到漫剧工坊的 HTTP 端点，把响应写回 stdout。密钥只从环境变量读，不会打印。

```bash
go build -o manju-mcp-bridge ./cmd/manju-mcp-bridge
# 或：go install github.com/Sakuralaaa/manju-studio/cmd/manju-mcp-bridge@latest
```

```json
{
  "mcpServers": {
    "manju": {
      "command": "/path/to/manju-mcp-bridge",
      "env": { "MANJU_MCP_URL": "https://manju.example.com/mcp", "MANJU_API_KEY": "<密钥>" },
      "timeoutSeconds": 600
    }
  }
}
```

「接口」页的 **Antigravity** 卡片能一键复制这两种配置（只带 `<密钥>` 占位符，不会带出真实密钥）。

## 2. 规则文件

把 [`docs/antigravity/AGENTS.md`](antigravity/AGENTS.md) 存为 Antigravity **工作区根目录**下的 `AGENTS.md`（「接口」页可一键复制）。Antigravity 会自动加载工作区（及上层目录直到仓库根）的 `AGENTS.md` / `GEMINI.md`，内容始终生效、不需要 frontmatter。

也可以放进 `.agents/rules/manju.md`，但那里的文件**必须**以 frontmatter 开头，否则会被静默忽略：

```markdown
---
trigger: always_on
description: 漫剧工坊外部编剧规则
---
（AGENTS.md 的内容）
```

规则文件约定了：先调用 `get_writing_context`、看资产再写、全部用简体中文、保持角色造型一致、用 `save_*` 交回；漫剧质量要求（开场 3 秒钩子、结尾卡点、台词一句不超过 20 字）；未经用户同意绝不调用 `colab_start`。

## 3. 「外部编剧」模式

在作品某一集点「一键成集」→ 方式选 **外部编剧**（或让 Gemini 调用 `autopilot_start`，`mode=writer`）：

1. **梗概**：本集没有梗概（且不是用作品创意的第 1 集）时，状态变成 `waiting_writer`，等 `save_synopsis`；
2. **剧本**：等 `save_script`；
3. **资产与分镜**：等 `upsert_asset`（剧本里的角色、场景、关键道具）和 `save_shots`；
4. 交回分镜后自动继续：生成资产设定图 → 设计音色（用 `voice_description`，没有就按外貌拼）→ 关键帧 → 配音 → 视频排队（是否开机由算力设置决定）→ 成片 → 封面。本集摘要不再调用模型，留给 `save_episode_summary`。

每次 `save_*` 写入后，如果这一集正停在 `waiting_writer`，一键成集会自动继续；页面上显示「等外部编剧 · 在 Antigravity 中继续」。在应用里手动改好后，也可以点「已写好，继续」。

## 4. 工具

### 写

| 工具 | 作用 |
| --- | --- |
| `save_bible` | 世界观、人物小传（文本或 `[{name, age, look, outfit, personality, motivation, relations, speech}]`）、分集总纲（文本或 `[{no, plot, cliffhanger}]`） |
| `save_synopsis` | 本集梗概（15–800 字）与可选标题 |
| `save_script` | 剧本：结构化 `scenes[{location, time, beats[{type: action/line/narration, text, speaker, emotion}]}]` + `cliffhanger`，或整份 `markdown`。写入格式与应用模型路径一致（`# 标题` / `## 第 N 场 · 地点 · 时间` / `【画面】` / `角色名：（语气）台词` / `（旁白）` / `【卡点】`） |
| `save_shots` | 整体替换分镜：`duration`（5–10 秒）、`camera`、`picture`、`dialogue`（文本或 `[{speaker, text, emotion}]`）、`assets`（编号或名称，必须已存在）、`transition`（`cut` / `continuous` / `first_last`）、`keyframe_prompt`、`video_prompt`。已有关键帧或视频时需 `overwrite=true` |
| `save_episode_summary` | 本集摘要（30–400 字）+ 卡点 |
| `upsert_asset` | 新建或更新角色 / 场景 / 道具；角色用 `look`（`hair`、`face`、`body`、`outfit`、`accessories`…）写固定造型，会被 `consistency.go` 原样写进关键帧和封面提示词；可带 `voice_description` |

所有写入工具都会校验输入，错误用中文说明怎么改；有 `warnings` 时（如台词超过 20 字、说话人不在资产库）也会返回。

### 读

| 工具 | 返回 |
| --- | --- |
| `get_writing_context` | 一次拿全：作品设定、设定集、前几集摘要与卡点、资产库（含 `fixed_look`）、本集梗概 / 剧本 / 分镜 / 缺失步骤 / 一键成集状态、`next_action` |
| `view_asset_images` | 设定图 / 定妆三视图 / 表情表：`{"type":"image","data":<base64>,"mimeType":"image/jpeg"}`，默认缩到长边 ≤ 1536 像素（`max_edge` 可调 256–4096，`original=true` 原图，≤ 12 MB） |
| `view_shot` | 镜头文字 + 关键帧 + 选定视频的第一帧和最后一帧（图片块） |
| `listen_dub` | 台词配音：`{"type":"audio","data":<base64>,"mimeType":"audio/mpeg"}`（MCP 2025-03-26 起支持音频块；客户端协商 2024-11-05 时改给音频地址），每次最多 8 段、合计 ≤ 10 MB |
| `get_video_link` | 视频的限时下载地址（2 小时，不需要密钥）+ 建议文件名。**MCP 没有视频内容类型**，请让智能体用 `curl -fL -o <filename> "<url>"` 下载到工作区再看 |
| `get_audio_link` | 音频的限时下载地址（2 小时，不需要密钥）+ 文件名、时长、念的文字：`sample(s)`、`asset_id`（`which` = `sample` / `refs` / `master` / `candidates`，默认全部）、`project_id`（旁白）、`library_id`、`line_id` |
| `export_voice_candidates` | `project_id`（可选 `asset_ids`）：把各角色的设计候选、当前音色、原声参考、母带（和旁白）打成一个 zip，返回一个限时下载地址；包内文件名全是 ASCII（`C01_asset10_cand1_vd-xxx.wav`），附 `manifest.txt` / `manifest.json`（文件 → 角色、候选序号、原始样本名、时长、文字、是否当前绑定） |

0.2.15：**全流程 Agent 实操卡点全打通**。
- **镜头关键帧直挂/直传**：`attach_image` 与 `upload_image` 支持传 `shot_id`，画室图或外部精修图可直接设为镜头关键帧并选用。
- **多抽卡 + 首尾帧防抢跑**：`takes >= 2` 时，上一镜仍有 `queued`/`running` 抽卡未跑完时暂不自动解锁 `waiting_prev`（显式调用 `pick_video` 或全部抽卡跑完后解锁），避免拿未审的第 1 条抽卡抢跑浪费 GPU。
- **语音渠道与 Prompt 预览**：新增 `list_voice_channels`（不再盲猜语音渠道编号）；新增 0 成本只读工具 `preview_shot_prompts`（出图/出视频前提前检查最终关键帧 Prompt、参考图截断情况与 H3 官方视频 Prompt）。
- **图片免密钥下载链接**：`/v1/dl/` 新增图片签名类型 `i`，`view_image`、`view_asset_images`、`view_shot` 文字里均附带 2 小时免密钥下载链接（宿主不转图片块时也能下载查看）。
- **批量资产图、队列查询、单句配音精修与长剧集省上下文**：新增 `generate_missing_assets`、`list_video_queue`、`list_lines`、`update_line`、`generate_line`；`list_shots` 支持 `episode_id` 过滤与 `brief=true` 精简模式；顺手补齐网页排镜自动清理首尾帧、原生字幕极端多句保护与上传大小限制。

0.2.14：**0.2.13 细节修复**：空闲暂缓关机期间云盘状态轮询节流为每 30 秒且关机前跳过已回收运行时；网页端完整适配 `waiting_prev` 显示与取消；`upsert_asset` 局部更新 `look` 时自动保留未传字段与原 `description`；删镜 / 排镜 / 改衔接后自动清理失效 `waiting_prev` 并即时解锁满足条件的镜头；配饰写「无 / 无配饰」不再拼成「戴无」；Colab 状态文件改原子替换写入。

0.2.13：**Agent 全链路闭环、角色一致性、H3 官方提示词与稳定性优化**。
- **上传与异步生图**：新增 `POST /v1/uploads`（直传大图/音频拿 `upload_id`，免 base64，`upload_image`、`clone_voice`、`video_submit`、`upload_bgm` 均支持）；`image_generate` / `generate_keyframe` 新增 `wait=false` 异步返回 `task_id`，新增 `get_task_status` 与 `generate_missing_keyframes` 批量补关键帧。
- **角色外观一致**：角色 `look` 结构化存入 `assets.look_json`（不与简介重复拼接），外观抽取按小句切分、按词匹配，避免性格/心理描写混入外观或 320 字截断；禁用项放 `look.avoid` 单独约束，不把否定词拼进正面描述。
- **审图暂停点**：`autopilot_start`（及 `/api/episodes/{id}/autopilot`）新增 `pause_at`（`script` / `assets` / `storyboard` / `keyframes`），`writer` / `auto` / `ask` 全模式通用，到点暂停审图后调用 `autopilot_continue` 继续。
- **MiniMax H3 官方视频提示词格式**：自动生成的视频提示词按 `MiniMax-H3` 官方结构（`I2VA`/`FL2VA`、`integrated_multimodal_description`、`[Shot 1]`、`(S1)`/`<d>[Chinese] ...</d>`、`overall_soundscape`、`non_diegetic_music`）拼装；手写该格式时原样透传。
- **后半段与单镜 MCP 工具**：新增 `pick_video`、`cancel_video`、`queue_episode_videos`、`build_final`、`generate_cover`、`list_bgm`、`upload_bgm`、`set_episode_audio`、`set_shot_sfx`、`export_capcut`、`add_shot`、`delete_shot`、`reorder_shots`；`list_shots`/`get_episode` 返回完整 `take_details`；删镜或覆盖分镜前自动清理孤儿配音文件。
- **首尾帧衔接、审片、字幕与变声**：`first_last` 镜头整集排队时先入 `waiting_prev` 等上一镜出片/选片后自动补首帧开跑；`view_shot` 新增 `take_id` 与 `sample_frames`（2–9 帧抽帧拼图）；原生台词字幕按字数分时长、按画幅定字号；`convert_take_voice` 与 `set_voice_conv_override` 支持单次/按角色覆盖升降调、f0、步数、CFG；`clone_voice` 默认裁静音+响度归一（`clean=false` 可关）。
- **云盘同步与测试**：Colab 关机前先同步权重上传状态，后台上传中按上限延长空闲关机；单元测试 `studio_test.go` 入库。

0.2.12：**宿主不转音频块时也能听**。Antigravity 实测不会把 MCP 音频内容块交给 Gemini（模型只看到「样本 vd-xxx.wav」这样的文字），而 `/v1/audio/` 要密钥、模型下载不了。现在所有返回音频的工具（`design_voice`、`listen_voice`、`clone_voice`、`listen_dub`，以及结果里提到音频的 `set_voice`、`tts`、`list_voice_library`、`save_voice_to_library`、`apply_library_voice`、`set_voice_master`、`convert_take_voice` 等）都在**文字**里写明每段的文件名、时长、念的文字和 2 小时有效的免密钥下载地址（`/v1/dl/`，与 `get_video_link` 同一套签名），智能体 `curl` / `Invoke-WebRequest` 下到工作区再 `read_file`。新增 `get_audio_link`、`export_voice_candidates`、`add_voice_candidates`（把 0.2.12 以前设计的样本补登为某角色的候选）。`design_voice` 的候选按角色存下来，网页音色面板「设计音色」页能看到、试听、选用（标注「外部智能体」）；`set_voice` 可以绑定任何一次设计出来的候选，`channel_id` 不填时用当初设计它的渠道。

0.2.11 新增：生成后统一音色——`convert_take_voice`（把一条视频的人声转成说话角色的音色母带，原版保留）、`pick_take_version`（原版 / 转换版）、`set_voice_unify`（作品默认 / 镜头覆盖）、`set_voice_master`（看 / 指定母带）；`update_project` / `create_project` 新增 `voice_unify`；`get_video_link` 新增 `version`；`list_shots` / `get_episode` 的镜头带 `voice_unify`（是否开启、说话角色、母带、跳过原因）和 `take_voice`（每条视频的统一音色状态）。

0.2.10 新增：音色库——`list_voice_library`、`save_voice_to_library`、`apply_library_voice`、`delete_library_voice`（需 `confirm=true`）；`listen_voice` 支持 `library_id`；`clone_voice` 新增 `save_to_library`。

0.2.9 新增：`update_shot`（改单个镜头、保留已有关键帧 / 视频）、`view_image`（按编号看任意图）、`listen_voice`（听音色样本）、`clone_voice`（用镜头原声克隆音色）、`create_project` / `update_project`、`delete_asset` / `detach_image`（需 `confirm=true`）；`design_voice` 结果带音频内容块；`generate_keyframe` 新增 `extra_prompt`、`ref_tags`；`upsert_asset` 新增 `props_note`。

原有工具（`list_projects`、`list_episodes`、`create_episode`、`get_episode`、`autopilot_*`、`generate_dub`、`tts`、`design_voice`、`set_voice`、`image_generate`、`colab_*`、`video_*`）不变。

## 5. Streamable HTTP 实现说明

- `POST /mcp`：单条或批量 JSON-RPC；有响应时返回 `application/json`，只有通知 / 响应时返回 `202`、无正文。
- `GET /mcp`：`405`（不提供服务端主动推送的 SSE 流，规范允许）；`DELETE /mcp`：`204`。
- 协议版本：支持 `2025-11-25`、`2025-06-18`、`2025-03-26`、`2024-11-05`；`initialize` 时客户端要的版本受支持就照用，否则回 `2025-06-18`；请求头 `Mcp-Protocol-Version` 不受支持时回 `400`。
- 带 `Origin` 头且与 Host 不同源的请求回 `403`（防 DNS 重绑定）；无状态，不发 `Mcp-Session-Id`。
- 认证：`Authorization: Bearer <密钥>`（或 `X-Api-Key`）。

## 6. 资料来源与验证情况

- Antigravity MCP 文档（`mcp_config.json` 结构、`serverUrl`/`url`、`headers`、`timeoutSeconds`、远程只支持 Streamable HTTP、配置文件位置）：https://antigravity.google/docs/mcp
- Antigravity 规则文档（`AGENTS.md` / `GEMINI.md` / `.agents/rules/*.md`、frontmatter `trigger` 必填）：https://antigravity.google/docs/rules
- MCP 规范 Streamable HTTP（POST/202/GET 405、Origin 校验、`MCP-Protocol-Version` 不支持时 400）：https://modelcontextprotocol.io/specification/2025-06-18/basic/transports
- 音频内容块自 2025-03-26 起加入：https://modelcontextprotocol.io/specification/2025-03-26/changelog ；2025-11-25 要求非法 Origin 回 403、参数校验错误按工具执行错误（`isError`）返回：https://modelcontextprotocol.io/specification/2025-11-25/changelog

**已验证**（本地，模拟上游，`livetest/writer_test.py`，95 项）：只用 MCP JSON-RPC 模拟编剧，从空设定集一路写到资产图、音色、关键帧、配音完成、视频排队；全程没有调用文本模型；图片块 / 音频块是合法 base64，mimeType 与文件头一致；限时下载链接可下载、篡改即 403；桥接程序（initialize、tools/list、图片工具、错误密钥）。

**未验证**：没有在真实的 Antigravity 客户端里连过（沙盒里没有 Antigravity）；Antigravity 是否会把 MCP 返回的图片块交给 Gemini 看，官方文档没有写明；音频块据用户的智能体反馈**不会**交给模型（0.2.12 起每段音频都在文字里给下载地址，见上）。如果图片块也只转文字，`view_shot` / `view_asset_images` 仍会返回文字说明，可以改用 `get_video_link` 或图片地址让智能体下载。
