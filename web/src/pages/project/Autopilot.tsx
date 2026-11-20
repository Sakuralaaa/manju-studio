import { useState } from 'react'
import { Check, Pause, PenLine, Play, RotateCcw, Square, Zap } from 'lucide-react'
import { api, type Pipe } from '../../api'
import { useProject } from '../Project'
import { useStore } from '../../store'
import { Modal, Segmented, Spinner } from '../../components/ui'
import { fmtTime, pipeStageNames, pipeStageOrder, pipeStatusNames } from '../../labels'

export function AutopilotStart({ onClose }: { onClose: () => void }) {
  const { data, run } = useProject()
  const [mode, setMode] = useState<'auto' | 'ask' | 'writer'>('ask')
  const p = data.project, e = data.episode
  const dub = p.audio_mode === 'dub'
  const writer = mode === 'writer'
  const steps = writer
    ? ['梗概', '剧本', '资产与分镜', '资产图', '关键帧', ...(dub ? ['配音'] : []), '视频排队', '成片', '封面']
    : ['梗概', '剧本', '资产', '分镜', '关键帧', ...(dub ? ['配音'] : []), '视频排队', '成片', '摘要与封面']
  const asked = (s: string) => mode === 'ask' && ['剧本', '资产', '分镜'].includes(s)
  const byWriter = (s: string) => writer && ['梗概', '剧本', '资产与分镜'].includes(s)
  const start = async () => { if (await run(() => api(`/api/episodes/${e.id}/autopilot`, { body: { mode } }), '已开始')) onClose() }
  return (
    <Modal open onClose={onClose} title="一键成集"
      footer={<><button className="btn btn-secondary" onClick={onClose}>取消</button><button className="btn btn-primary" onClick={start}><Zap size={16} />开始</button></>}>
      <div className="field"><span className="field-label">方式</span>
        <Segmented value={mode} onChange={v => setMode(v as any)} options={[{ value: 'ask', label: '关键处问我' }, { value: 'auto', label: '全自动' }, { value: 'writer', label: '外部编剧' }]} />
      </div>
      <div className="ap-flow">
        {steps.map((s, i) => (
          <span key={s} className={`ap-chip ${asked(s) ? 'is-ask' : ''} ${byWriter(s) ? 'is-writer' : ''}`}>{i > 0 && <i />}{s}{asked(s) && <Pause size={11} />}{byWriter(s) && <PenLine size={11} />}</span>
        ))}
      </div>
      {writer && <p className="ap-note">梗概、剧本、资产外观和分镜由 Antigravity 里的 Gemini 通过 MCP 写入，每交回一步就自动继续；出图、配音、视频和成片照常进行，应用不调用文本模型。</p>}
    </Modal>
  )
}

export function AutopilotPanel({ pl }: { pl: Pipe }) {
  const { data, run, goStep } = useProject()
  const { go } = useStore()
  const e = data.episode
  const [log, setLog] = useState(false)
  const cur = pipeStageOrder.indexOf(pl.stage)
  const stages = pipeStageOrder.filter(s => s !== 'voice' || data.project.audio_mode === 'dub')
  const stepFor: Record<string, number> = { idea: 1, script: 2, assets: 3, storyboard: 4, keyframes: 5, voice: 7, videos: 6, final: 7, wrap: 7 }
  const active = pl.status === 'running' || pl.status === 'waiting'
  const waitW = pl.status === 'waiting_writer'
  return (
    <section className={`card ap-panel is-${pl.status}`}>
      <div className="ap-head">
        <span className="ap-icon">{active ? <Spinner size={16} /> : waitW ? <PenLine size={16} /> : pl.status === 'done' ? <Check size={16} strokeWidth={3} /> : <Zap size={16} />}</span>
        <div className="ap-title">
          <b>一键成集 · {pipeStatusNames[pl.status]}</b>
          <span className="muted" title={pl.message}>{pl.message || pipeStageNames[pl.stage] || '准备中'}</span>
        </div>
        <span className="grow" />
        <div className="row-actions">
          {(pl.status === 'confirm' || waitW) && <button className="btn btn-secondary btn-sm" onClick={() => goStep(stepFor[pl.stage] || 1)}>去查看</button>}
          {waitW && <button className="btn btn-secondary btn-sm hide-xs" onClick={() => run(() => api(`/api/episodes/${e.id}/autopilot/continue`, { method: 'POST' }), '继续')}><Play size={14} />已写好，继续</button>}
          {pl.status === 'confirm' && <button className="btn btn-primary btn-sm" onClick={() => run(() => api(`/api/episodes/${e.id}/autopilot/continue`, { method: 'POST' }), '继续')}><Play size={14} />确认继续</button>}
          {pl.status === 'failed' && <button className="btn btn-primary btn-sm" onClick={() => run(() => api(`/api/episodes/${e.id}/autopilot/continue`, { method: 'POST' }))}><RotateCcw size={14} />重试</button>}
          {(active || pl.status === 'confirm' || waitW) && <button className="btn btn-secondary btn-sm" onClick={() => confirm('停止一键成集？已完成的步骤会保留。') && run(() => api(`/api/episodes/${e.id}/autopilot/cancel`, { method: 'POST' }))}><Square size={13} />停止</button>}
          <button className="btn btn-ghost btn-sm hide-xs" onClick={() => setLog(!log)}>{log ? '收起' : '日志'}</button>
        </div>
      </div>
      {waitW && (
        <div className="ap-writer">
          <PenLine size={16} />
          <span><b>在 Antigravity 中继续</b> · 让 Gemini 先调用 get_writing_context，写好后用 save_* 交回，这里会自动往下走。</span>
          <button className="btn btn-ghost btn-sm hide-xs" onClick={() => go({ page: 'api' })}>连接说明</button>
        </div>
      )}
      <div className="ap-track">
        {stages.map(s => {
          const i = pipeStageOrder.indexOf(s)
          const st = pl.status === 'done' || i < cur ? 'is-done' : i === cur ? `is-cur is-${pl.status}` : ''
          return <span key={s} className={`ap-step ${st}`}><i />{pipeStageNames[s]}</span>
        })}
      </div>
      {log && <pre className="logbox">{pl.log.map(l => `${fmtTime(l.t)}  ${l.text}`).join('\n')}</pre>}
    </section>
  )
}
