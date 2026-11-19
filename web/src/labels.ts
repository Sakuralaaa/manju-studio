import type { Caps } from './api'
export interface Params { size?: string; aspect?: string; resolution?: string; quality?: string; n?: number; format?: string; compression?: number; background?: string; moderation?: string }

export const capLabels: Record<keyof Caps, string> = {
  size: '尺寸', aspect: '比例', resolution: '分辨率', quality: '质量', n: '张数',
  format: '输出格式', compression: '压缩', background: '背景', moderation: '审核',
  response: '返回方式', refs: '参考图',
}

export const modeLabels: Record<string, string> = {
  native: '原生发送', prompt: '写进提示词', off: '不用', loop: '逐张请求',
  multipart: '表单上传', json: '内嵌数据',
}

const valueMap: Record<string, string> = {
  auto: '自动', low: '低', medium: '中', high: '高', xhigh: '超高', max: '极致', standard: '标准', hd: '高清',
  transparent: '透明', opaque: '不透明', url: '链接', b64_json: '内嵌数据',
  png: 'PNG', jpeg: 'JPEG', webp: 'WEBP', '1k': '1K', '2k': '2K', '4k': '4K', '1.5k': '1.5K',
}
export const moderationMap: Record<string, string> = { auto: '标准', low: '宽松' }

export function valueLabel(key: string, v: string): string {
  if (key === 'moderation') return moderationMap[v] || v
  if (key === 'size') return v === 'auto' ? '自动' : v.replace('x', '×')
  return valueMap[v] ?? v
}

export const paramOrder: (keyof Params)[] = ['aspect', 'size', 'resolution', 'quality', 'n', 'format', 'compression', 'background', 'moderation']
export const paramLabels: Record<string, string> = {
  aspect: '比例', size: '尺寸', resolution: '分辨率', quality: '质量', n: '张数',
  format: '格式', compression: '压缩', background: '背景', moderation: '审核',
}

export function describeParams(p: Params): { k: string; v: string }[] {
  const out: { k: string; v: string }[] = []
  for (const k of paramOrder) {
    const v = (p as any)[k]
    if (v === undefined || v === '' || v === 0) continue
    out.push({ k: paramLabels[k], v: k === 'n' || k === 'compression' ? String(v) + (k === 'compression' ? '%' : ' 张') : valueLabel(k, String(v)) })
  }
  return out
}

export const apiTypeLabels: Record<string, string> = { images: '生图接口', chat: '对话出图' }

export function fmtBytes(n: number) {
  if (n < 1024) return n + ' B'
  if (n < 1024 * 1024) return (n / 1024).toFixed(0) + ' KB'
  if (n < 1024 * 1024 * 1024) return (n / 1024 / 1024).toFixed(n < 10 * 1024 * 1024 ? 2 : 1) + ' MB'
  return (n / 1024 / 1024 / 1024).toFixed(2) + ' GB'
}

export function fmtDuration(ms: number) {
  if (!ms) return '—'
  if (ms < 1000) return ms + ' 毫秒'
  const s = ms / 1000
  if (s < 60) return s.toFixed(1) + ' 秒'
  return Math.floor(s / 60) + ' 分 ' + Math.round(s % 60) + ' 秒'
}

export function fmtTime(ms: number) {
  const d = new Date(ms)
  const now = new Date()
  const pad = (x: number) => String(x).padStart(2, '0')
  const hm = `${pad(d.getHours())}:${pad(d.getMinutes())}`
  if (d.toDateString() === now.toDateString()) return '今天 ' + hm
  const y = new Date(now); y.setDate(now.getDate() - 1)
  if (d.toDateString() === y.toDateString()) return '昨天 ' + hm
  if (d.getFullYear() === now.getFullYear()) return `${d.getMonth() + 1}月${d.getDate()}日 ${hm}`
  return `${d.getFullYear()}年${d.getMonth() + 1}月${d.getDate()}日 ${hm}`
}

export function ratioOf(w: number, h: number) {
  const g = (a: number, b: number): number => (b ? g(b, a % b) : a)
  const x = g(w, h) || 1
  return `${w / x}:${h / x}`
}

export function aspectNumber(r?: string, size?: string): number {
  if (r && r.includes(':')) {
    const [a, b] = r.split(':').map(Number)
    if (a > 0 && b > 0) return a / b
  }
  if (size && size.includes('x')) {
    const [a, b] = size.split('x').map(Number)
    if (a > 0 && b > 0) return a / b
  }
  return 1
}

export const stepNames = ['设定', '剧本', '资产', '分镜', '关键帧', '视频', '成片']
export const kindLabels: Record<string, string> = { character: '角色', scene: '场景', prop: '道具' }

export const phaseLabels: Record<string, string> = {
  off: '未开机', creating: '申请中', connecting: '连接中', booting: '准备环境', ready: '待命', running: '生成中', stopping: '关机中',
}

export function accName(acc: string) { return !acc || acc === 'NONE' ? 'CPU' : acc }

export function fmtCU(x: number) {
  if (!isFinite(x)) return '—'
  if (x < 0.1) return x.toFixed(2)
  if (x < 10) return x.toFixed(1)
  return Math.round(x).toString()
}

export function fmtClock(sec: number) {
  if (sec < 0) sec = 0
  const m = Math.floor(sec / 60), s = Math.floor(sec % 60)
  if (m >= 60) return `${Math.floor(m / 60)}:${String(m % 60).padStart(2, '0')}:${String(s).padStart(2, '0')}`
  return `${m}:${String(s).padStart(2, '0')}`
}

export function fmtSec(sec: number) {
  if (sec < 60) return `${Math.round(sec)} 秒`
  const m = Math.floor(sec / 60), s = Math.round(sec % 60)
  return s ? `${m} 分 ${s} 秒` : `${m} 分钟`
}

export const pipeStageNames: Record<string, string> = {
  idea: '梗概', script: '剧本', assets: '资产', storyboard: '分镜', keyframes: '关键帧', voice: '配音', videos: '视频', final: '成片', wrap: '收尾',
}
export const pipeStageOrder = ['idea', 'script', 'assets', 'storyboard', 'keyframes', 'voice', 'videos', 'final', 'wrap']
export const pipeStatusNames: Record<string, string> = {
  running: '进行中', confirm: '等你确认', waiting: '等待渲染', done: '已完成', failed: '已中断', canceled: '已停止',
}
export const chainNames: Record<string, string> = { off: '不衔接', ref: '参考上一镜', first: '首尾帧' }
export const voiceTypeNames: Record<string, string> = { fish: 'Fish Audio', mimo: '小米 MiMo', openai: 'OpenAI 兼容' }
export const voiceModeNames: Record<string, string> = { fish: '音色库', builtin: '内置音色', design: '设计音色', clone: '克隆音色', openai: '渠道音色' }
export const tagNames: Record<string, string> = { '': '设定图', sheet: '三视图', expr: '表情表' }

export function voiceLabel(v?: { mode?: string; title?: string; voice?: string }) {
  if (!v || !v.mode) return ''
  return v.title || v.voice || voiceModeNames[v.mode] || ''
}

export function epName(no: number, title?: string) { return title ? `第 ${no} 集 · ${title}` : `第 ${no} 集` }
