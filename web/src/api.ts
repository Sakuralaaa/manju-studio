export type CapMode = 'native' | 'prompt' | 'loop' | 'off' | 'multipart' | 'json'
export interface Cap { mode: CapMode; values?: string[]; max?: number; default?: string }
export interface Caps {
  size: Cap; aspect: Cap; resolution: Cap; quality: Cap; n: Cap; format: Cap
  compression: Cap; background: Cap; moderation: Cap; response: Cap; refs: Cap
}
export interface Channel {
  id: number; kind: 'image' | 'text'; name: string; base_url: string; api_key?: string; key_tail: string
  api_type: 'images' | 'chat'; models: string[]; caps: Caps; sort: number; created_at: number
}
export interface Preset { key: string; name: string; base_url: string; api_type: 'images' | 'chat'; models: string[] | null; caps: Caps }

export interface Img {
  id: number; project_id: number; owner_type: string; owner_id: number; url: string; mime: string
  width: number; height: number; bytes: number; source: 'gen' | 'upload'; prompt: string; channel_id: number; model: string; created_at: number
}
export interface Video {
  id: number; project_id: number; shot_id: number; source: string
  status: 'queued' | 'running' | 'done' | 'failed' | 'canceled'; progress: number; stage: string
  prompt: string; image_url: string; seconds: number; res: string; aspect: string; turbo: boolean; seed: number
  url: string; bytes: number; error: string; created_at: number; started_at: number; finished_at: number; position?: number
}
export interface Project {
  id: number; title: string; idea: string; style: string; aspect: string; episode_sec: number; shot_target: number
  script: string; text_channel_id: number; text_model: string; image_channel_id: number; image_model: string
  video_sec: number; video_res: string; video_turbo: boolean; final_file: string; final_url: string; final_at: number
  created_at: number; updated_at: number
}
export interface ProjectCard extends Project { cover: string; shots: number; assets: number; keyframes: number; videos: number; steps: boolean[]; script_size: number }
export interface Asset { id: number; project_id: number; kind: 'character' | 'scene' | 'prop'; code: string; name: string; description: string; prompt: string; picked_image_id: number; sort: number; images: Img[] }
export interface Shot {
  id: number; project_id: number; sort: number; code: string; duration: number; camera: string; action: string; dialogue: string
  assets: string[]; keyframe_prompt: string; video_prompt: string; picked_image_id: number; picked_video_id: number; images: Img[]; videos: Video[]
}
export interface Task { id: number; project_id: number; kind: string; owner_type: string; owner_id: number; status: 'running' | 'done' | 'failed'; error: string; created_at: number }
export interface Steps { done: boolean[]; assets: number; assets_ok: number; shots: number; keyframes: number; videos: number; queued: number }
export interface RunnerView {
  phase: 'off' | 'creating' | 'connecting' | 'booting' | 'ready' | 'running' | 'stopping'
  runtime: string; accelerator: string; variant: string; purpose: string; booted: boolean
  started_at: number; ready_at: number; last_active: number; stage: string; stage_pct: number; current_job: number; message: string
  queued: number; running: number; cu_used: number; idle_left_sec: number; max_left_sec: number; authorized: boolean; server_now: number
}
export interface ProjectFull { project: Project; assets: Asset[]; shots: Shot[]; tasks: Task[]; steps: Steps; colab: RunnerView }

export interface RuntimeSpec { key: { variant: string; accelerator: string; shape: string }; eligible?: boolean }
export interface ColabCfg {
  accelerator: string; shape: string; idle_min: number; max_min: number; auto_start: boolean
  boot_min: number; clip_min: number; clip_min_turbo: number; job_timeout_min: number; rates: Record<string, number>
}
export interface SessionRow { id: number; accelerator: string; purpose: string; status: string; end_reason: string; jobs_done: number; started_at: number; ended_at: number; minutes: number; cu: number }
export interface ColabState {
  client: { id_tail: string; project: string } | null; authorized: boolean
  info: { tier: string; tier_label: string; specs: RuntimeSpec[]; fetched_at: number; error: string } | null
  cfg: ColabCfg; runner: RunnerView; sessions: SessionRow[]
}
export interface WorkflowItem { key: string; name: string; kind: 'workflow' | 'files' | 'boot'; text: string; custom: boolean }
export interface ApiKey { id: number; name: string; tail: string; created_at: number; last_used_at: number }

export class ApiError extends Error { status: number; constructor(m: string, s: number) { super(m); this.status = s } }

let onUnauthorized: () => void = () => {}
export function setUnauthorizedHandler(f: () => void) { onUnauthorized = f }

export async function api<T = any>(path: string, opts: { method?: string; body?: any; raw?: BodyInit } = {}): Promise<T> {
  const init: RequestInit = { method: opts.method || (opts.body !== undefined || opts.raw ? 'POST' : 'GET'), credentials: 'same-origin' }
  if (opts.raw) init.body = opts.raw
  else if (opts.body !== undefined) {
    init.body = JSON.stringify(opts.body)
    init.headers = { 'Content-Type': 'application/json' }
  }
  const res = await fetch(path, init)
  if (res.status === 401 && !path.startsWith('/api/login')) onUnauthorized()
  const text = await res.text()
  let data: any = null
  try { data = text ? JSON.parse(text) : null } catch { data = text }
  if (!res.ok) throw new ApiError((data && data.error) || `请求失败（${res.status}）`, res.status)
  return data as T
}

export function upload<T = any>(path: string, file: File) {
  const fd = new FormData()
  fd.append('file', file)
  return api<T>(path, { raw: fd })
}
