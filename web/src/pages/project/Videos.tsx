import { useEffect, useState } from 'react'
import { ArrowRight, ListPlus, Play, Power, RotateCcw, X, Cpu } from 'lucide-react'
import { api, type ColabState, type Shot, type Video } from '../../api'
import { useProject, StepHead, Cost } from '../Project'
import { useStore } from '../../store'
import { Empty, Spinner, useInterval } from '../../components/ui'
import { accName, fmtClock, fmtCU, phaseLabels } from '../../labels'
import { arClass } from './media'

export function useColab(ms = 4000) {
  const [st, setSt] = useState<ColabState | null>(null)
  const load = () => api<ColabState>('/api/colab').then(setSt).catch(() => {})
  useEffect(() => { load() }, [])
  useInterval(load, ms)
  return { st, reload: load }
}

export function estimate(st: ColabState | null, n: number, turbo: boolean) {
  if (!st) return null
  const c = st.cfg, r = st.runner
  const gpuOn = r.phase !== 'off' && r.variant === 'VARIANT_GPU'
  const acc = gpuOn ? r.accelerator : c.accelerator
  const boot = gpuOn && r.booted ? 0 : gpuOn ? Math.max(0, c.boot_min - (Date.now() - r.started_at) / 60000) : c.boot_min
  const per = turbo ? c.clip_min_turbo : c.clip_min
  const mins = boot + n * per
  return { cu: (mins / 60) * (c.rates[acc] ?? 0), mins, acc }
}

export function CU({ st, n, turbo }: { st: ColabState | null; n: number; turbo: boolean }) {
  const e = estimate(st, n, turbo)
  if (!e) return null
  return <Cost>≈ {fmtCU(e.cu)} CU</Cost>
}

export function ComputeStrip({ st, reload }: { st: ColabState | null; reload: () => void }) {
  const { toast, go } = useStore()
  const [busy, setBusy] = useState(false)
  if (!st) return <div className="card cstrip"><Spinner /></div>
  const r = st.runner
  const on = r.phase !== 'off'
  const act = async (path: string, body?: any) => {
    setBusy(true)
    try { await api(path, { body: body ?? {} }); reload() } catch (e: any) { toast(e.message, 'err') } finally { setBusy(false) }
  }
  const elapsed = on && r.started_at ? (r.server_now - r.started_at) / 1000 : 0
  return (
    <div className={`card cstrip is-${r.phase}`}>
      <span className="cstrip-icon"><Cpu size={18} /></span>
      <div className="cstrip-main">
        <div className="cstrip-title">
          <b>{on ? `${accName(r.accelerator)} · ${phaseLabels[r.phase]}` : '运行时未开机'}</b>
          {r.phase === 'booting' && r.stage && <span className="muted">{r.stage}{r.stage_pct ? ` ${Math.round(r.stage_pct)}%` : ''}</span>}
        </div>
        <div className="cstrip-meta">
          {!r.authorized ? <span>还没有授权 Colab</span> : <>
            <span>排队 {r.queued}</span>
            {on && <span>已运行 {fmtClock(elapsed)}</span>}
            {on && <span>已用 ≈ {fmtCU(r.cu_used)} CU</span>}
            {r.phase === 'ready' && r.idle_left_sec > 0 && r.queued === 0 && <span>{fmtClock(r.idle_left_sec)} 后空闲关机</span>}
            {!on && r.message && <span className="danger-text" title={r.message}>上次开机失败</span>}
          </>}
        </div>
      </div>
      {!r.authorized ? <button className="btn btn-secondary btn-sm" onClick={() => go({ page: 'compute' })}>去授权</button> :
        on ? <button className="btn btn-secondary btn-sm" disabled={busy || r.phase === 'stopping'} onClick={() => confirm('关机后正在生成的镜头会退回队列，确定关机？') && act('/api/colab/stop')}><Power size={14} />关机</button> :
          <button className="btn btn-primary btn-sm" disabled={busy || r.queued === 0} onClick={() => act('/api/colab/start', { purpose: 'video' })}><Play size={14} />开机渲染<CU st={st} n={r.queued} turbo={false} /></button>}
    </div>
  )
}

export default function Videos() {
  const { data, run, goStep } = useProject()
  const { st, reload } = useColab()
  const p = data.project
  const need = data.shots.filter(s => s.picked_image_id && !s.videos.some(v => v.status === 'done' || v.status === 'queued' || v.status === 'running')).length
  const queueAll = async (start: boolean) => {
    await run(() => api(`/api/projects/${p.id}/videos/queue`, { body: { start } }).then((r: any) => { if (r.start_error) throw new Error('已排队，开机失败：' + r.start_error) }), start ? '已排队并开机' : '已加入队列')
    reload()
  }
  const autoOn = st?.cfg.auto_start
  const off = !st || st.runner.phase === 'off'
  return (
    <section className="stepview">
      <StepHead title="视频" note={`${p.video_res} · ${p.video_turbo ? 'Turbo 8 步' : '标准 20 步'}`}>
        <button className="btn btn-secondary" disabled={need === 0} onClick={() => queueAll(false)}><ListPlus size={16} />全部排队<Cost>{need} 段</Cost></button>
        {off && !autoOn && <button className="btn btn-secondary" disabled={need === 0 || !st?.runner.authorized} onClick={() => queueAll(true)}><Play size={16} />排队并开机<CU st={st} n={need + (st?.runner.queued || 0)} turbo={p.video_turbo} /></button>}
        <button className="btn btn-primary" disabled={!data.steps.done[5]} onClick={() => goStep(7)}>下一步：成片<ArrowRight size={16} /></button>
      </StepHead>
      <ComputeStrip st={st} reload={reload} />
      {data.shots.length === 0 && <div className="card card-empty"><Empty art="frame" title="还没有分镜" /></div>}
      <div className={`kgrid kgrid-${p.aspect.replace(':', '-')}`}>
        {data.shots.map(s => <VideoCard key={s.id} s={s} st={st} />)}
      </div>
    </section>
  )
}

function VideoCard({ s, st }: { s: Shot; st: ColabState | null }) {
  const { data, run } = useProject()
  const p = data.project
  const kf = s.images.find(i => i.id === s.picked_image_id)
  const active = s.videos.find(v => v.status === 'running') || s.videos.find(v => v.status === 'queued')
  const done = s.videos.filter(v => v.status === 'done')
  const shown = done.find(v => v.id === s.picked_video_id) || done[done.length - 1]
  const lastFail = [...s.videos].reverse().find(v => v.status === 'failed')
  const failNewer = lastFail && (!shown || lastFail.id > shown.id) && !active
  return (
    <article className="card kcard vcard">
      <div className="kcard-head">
        <span className="code">{s.code}</span>
        <span className="kcard-cam" title={s.camera}>{s.camera || '镜头'}</span>
        <span className="grow" />
        <span className="muted sm-text">{Math.round(s.duration)} 秒</span>
      </div>
      <div className={`frame ${arClass(p.aspect)}`}>
        {shown && !active ? <video src={shown.url} poster={kf?.url} controls playsInline preload="none" /> :
          kf ? <img src={kf.url} alt="" /> : <div className="frame-empty"><span>缺关键帧</span></div>}
        {active && <VideoState v={active} />}
      </div>
      <div className="vstatus">
        {active ? (active.status === 'running' ? <span className="accent-text">{active.stage || '生成中'}</span> : <span>排队第 {active.position} 位</span>) :
          failNewer ? <span className="danger-text" title={lastFail!.error}>失败：{lastFail!.error}</span> :
            shown ? <span>{shown.seconds} 秒 · {shown.res}{shown.turbo ? ' · Turbo' : ''}</span> : <span className="muted">未生成</span>}
      </div>
      {done.length > 1 && (
        <div className="vers">
          {done.map((v, i) => <button key={v.id} className={`ver ${shown?.id === v.id ? 'is-on' : ''}`} onClick={() => run(() => api(`/api/videos/${v.id}/pick`, { method: 'POST' }))}>第 {i + 1} 版</button>)}
        </div>
      )}
      <div className="acard-foot">
        {active ? (
          <button className="btn btn-secondary btn-sm" onClick={() => run(() => api(`/api/videos/${active.id}/cancel`, { method: 'POST' }))}><X size={14} />取消</button>
        ) : (
          <button className="btn btn-secondary btn-sm" disabled={!kf} onClick={() => run(() => api(`/api/shots/${s.id}/video`, { body: {} }), '已加入队列')}>
            {shown ? <RotateCcw size={14} /> : <Play size={14} />}{shown ? '重新生成' : '生成'}<CU st={st} n={1} turbo={p.video_turbo} />
          </button>
        )}
      </div>
    </article>
  )
}

function VideoState({ v }: { v: Video }) {
  const pct = Math.round(v.progress * 100)
  return (
    <div className="vstate">
      {v.status === 'running' ? <>
        <div className="ring" style={{ ['--p' as any]: pct }}><span>{pct}%</span></div>
      </> : <><Spinner size={18} /><span>排队中</span></>}
    </div>
  )
}
