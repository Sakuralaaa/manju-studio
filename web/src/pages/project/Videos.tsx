import { useEffect, useRef, useState } from 'react'
import { ArrowRight, ListPlus, Play, Power, RotateCcw, X, Cpu, Columns2, Check, Link2, Wand2 } from 'lucide-react'
import { ShotUnify, TakeVoice } from './VoiceUnify'
import { api, type ColabState, type Shot, type Video } from '../../api'
import { useProject, StepHead, Cost } from '../Project'
import { useStore } from '../../store'
import { Empty, Modal, Segmented, Spinner, useInterval } from '../../components/ui'
import { accName, fmtClock, fmtCU, phaseLabels } from '../../labels'
import { arClass } from './media'

export function useColab(ms = 4000) {
  const [st, setSt] = useState<ColabState | null>(null)
  const load = () => api<ColabState>('/api/colab').then(setSt).catch(() => {})
  useEffect(() => { load() }, [])
  useInterval(load, ms)
  return { st, reload: load }
}

export function estimate(st: ColabState | null, n: number, turbo: boolean, post?: { upscale?: boolean; interp?: boolean }) {
  if (!st) return null
  const c = st.cfg, r = st.runner
  const gpuOn = r.phase !== 'off' && r.variant === 'VARIANT_GPU'
  const acc = gpuOn ? r.accelerator : c.accelerator
  const boot = gpuOn && r.booted ? 0 : gpuOn ? Math.max(0, c.boot_min - (Date.now() - r.started_at) / 60000) : c.boot_min
  const per = (turbo ? c.clip_min_turbo : c.clip_min) + (post?.upscale ? c.upscale_min || 0 : 0) + (post?.interp ? c.interp_min || 0 : 0)
  const mins = boot + n * per
  return { cu: (mins / 60) * (c.rates[acc] ?? 0), mins, acc }
}

export function CU({ st, n, turbo, post }: { st: ColabState | null; n: number; turbo: boolean; post?: { upscale?: boolean; interp?: boolean } }) {
  const e = estimate(st, n, turbo, post)
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
  const { data, run, goStep, patch } = useProject()
  const { st, reload } = useColab()
  const p = data.project, e = data.episode
  const post = { upscale: p.post_upscale, interp: p.post_interp }
  const shotsNeed = data.shots.filter(s => s.picked_image_id && !s.videos.some(v => v.status === 'done' || v.status === 'queued' || v.status === 'waiting_prev' || v.status === 'running')).length
  const need = shotsNeed * p.takes
  const queueAll = async (start: boolean) => {
    await run(() => api(`/api/episodes/${e.id}/videos/queue`, { body: { start } }).then((r: any) => { if (r.start_error) throw new Error('已排队，开机失败：' + r.start_error) }), start ? '已排队并开机' : '已加入队列')
    reload()
  }
  const autoOn = st?.cfg.auto_start
  const off = !st || st.runner.phase === 'off'
  return (
    <section className="stepview">
      <StepHead title="视频" note={`${p.video_res} · ${p.video_turbo ? 'Turbo 8 步' : '标准 20 步'}${p.post_upscale ? ' · 放大' : ''}${p.post_interp ? ' · 补帧' : ''}`}>
        <button className={`btn btn-secondary ${p.voice_unify ? 'is-on' : ''}`} onClick={() => patch({ voice_unify: !p.voice_unify })}
          title="生成后统一音色：每条视频出来后在 Colab 上分离人声，用 Seed-VC 转成说话角色的音色母带再混回去，原版保留"><Wand2 size={16} />统一音色<Cost>{p.voice_unify ? '开' : '关'}</Cost></button>
        <Segmented size="sm" block={false} value={String(p.takes)} onChange={v => patch({ takes: Number(v) })} options={[1, 2, 3, 4].map(n => ({ value: String(n), label: `每镜 ${n} 条` }))} />
        <button className="btn btn-secondary" disabled={need === 0} onClick={() => queueAll(false)}><ListPlus size={16} />全部排队<Cost>{need} 段</Cost></button>
        {off && !autoOn && <button className="btn btn-secondary" disabled={need === 0 || !st?.runner.authorized} onClick={() => queueAll(true)}><Play size={16} />排队并开机<CU st={st} n={need + (st?.runner.queued || 0)} turbo={p.video_turbo} post={post} /></button>}
        <button className="btn btn-primary" disabled={!data.steps.done[5]} onClick={() => goStep(7)}>下一步：成片<ArrowRight size={16} /></button>
      </StepHead>
      <ComputeStrip st={st} reload={reload} />
      {data.shots.length === 0 && <div className="card card-empty"><Empty art="frame" title="还没有分镜" /></div>}
      <div className={`kgrid kgrid-${p.aspect.replace(':', '-')}`}>
        {data.shots.map((s, i) => <VideoCard key={s.id} s={s} prev={data.shots[i - 1]} st={st} />)}
      </div>
    </section>
  )
}

function VideoCard({ s, prev, st }: { s: Shot; prev?: Shot; st: ColabState | null }) {
  const { data, run } = useProject()
  const p = data.project
  const [cmp, setCmp] = useState(false)
  const [view, setView] = useState<number | null>(null)
  const [ver, setVer] = useState<'original' | 'converted' | null>(null)
  const kf = s.images.find(i => i.id === s.picked_image_id)
  const actives = s.videos.filter(v => v.status === 'running' || v.status === 'queued' || v.status === 'waiting_prev')
  const active = actives.find(v => v.status === 'running') || actives[0]
  const done = s.videos.filter(v => v.status === 'done')
  const chosen = done.find(v => v.id === s.picked_video_id) || done[done.length - 1]
  const shown = done.find(v => v.id === view) || chosen
  const lastFail = [...s.videos].reverse().find(v => v.status === 'failed')
  const failNewer = lastFail && (!chosen || lastFail.id > chosen.id) && !active
  const waitPrev = s.chain === 'first' && !!prev && !prev.videos.some(v => v.status === 'done')
  const post = { upscale: p.post_upscale, interp: p.post_interp }
  const shownVer = shown ? (ver ?? (shown.vc_use && shown.vc_url ? 'converted' : 'original')) : 'original'
  const shownSrc = shown ? (shownVer === 'converted' && shown.vc_url ? shown.vc_url : shown.url) : ''
  return (
    <article className="card kcard vcard">
      <div className="kcard-head">
        <span className="code">{s.code}</span>
        <span className="kcard-cam" title={s.camera}>{s.camera || '镜头'}</span>
        <span className="grow" />
        {s.chain === 'first' && prev && <span className="chain-tag" title="上一镜头最后一帧作首帧"><Link2 size={11} />首尾帧</span>}
        <span className="muted sm-text">{Math.round(s.duration)} 秒</span>
      </div>
      <div className={`frame ${arClass(p.aspect)}`}>
        {shown && !active ? <video key={`${shown.id}-${shownVer}`} src={shownSrc} poster={kf?.url} controls playsInline preload="none" /> :
          kf ? <img src={kf.url} alt="" /> : <div className="frame-empty"><span>缺关键帧</span></div>}
        {active && <VideoState v={active} more={actives.length - 1} />}
      </div>
      <div className="vstatus">
        {active ? (active.status === 'running' ? <span className="accent-text">{active.stage || '生成中'}{actives.length > 1 ? ` · 还有 ${actives.length - 1} 条` : ''}</span> :
          active.status === 'waiting_prev' ? <span>等上一镜完成后开跑{actives.length > 1 ? ` · 共 ${actives.length} 条` : ''}</span> :
            <span>排队第 {active.position} 位{actives.length > 1 ? ` · 共 ${actives.length} 条` : ''}</span>) :
          failNewer ? <span className="danger-text" title={lastFail!.error}>失败：{lastFail!.error}</span> :
            waitPrev && !chosen ? <span className="muted">等上一镜头出视频</span> :
              shown ? <span>{shown.seconds} 秒 · {shown.res}{shown.turbo ? ' · Turbo' : ''}{shown.post?.upscale ? ' · 放大' : ''}{shown.post?.interp ? ' · 补帧' : ''}</span> : <span className="muted">未生成</span>}
      </div>
      {done.length > 1 && (
        <div className="takes">
          {done.map((v, i) => (
            <button key={v.id} className={`take ${shown?.id === v.id ? 'is-view' : ''} ${chosen?.id === v.id ? 'is-on' : ''}`} onClick={() => { setView(v.id); setVer(null) }} title={chosen?.id === v.id ? '已选用' : '预览'}>
              {kf && <img src={kf.url} alt="" />}<b>{i + 1}</b>{chosen?.id === v.id && <i><Check size={10} strokeWidth={3} /></i>}
            </button>
          ))}
          <span className="grow" />
          {shown && chosen && shown.id !== chosen.id && <button className="btn btn-ghost btn-sm" onClick={() => run(() => api(`/api/videos/${shown.id}/pick`, { method: 'POST' }), '已选用')}><Check size={14} />选用</button>}
          <button className="icon-btn sm" onClick={() => setCmp(true)} aria-label="对比" title="对比"><Columns2 size={14} /></button>
        </div>
      )}
      {shown && !active && <TakeVoice v={shown} st={st} onView={setVer} />}
      {!!s.dialogue.trim() && <ShotUnify s={s} />}
      <div className="acard-foot">
        {active ? (
          <button className="btn btn-secondary btn-sm" onClick={() => Promise.all(actives.map(v => api(`/api/videos/${v.id}/cancel`, { method: 'POST' }))).then(() => run(async () => {}))}><X size={14} />取消</button>
        ) : (
          <button className="btn btn-secondary btn-sm" disabled={!kf || waitPrev} onClick={() => run(() => api(`/api/shots/${s.id}/video`, { body: {} }), '已加入队列')}>
            {done.length ? <RotateCcw size={14} /> : <Play size={14} />}{done.length ? `再抽 ${p.takes} 条` : p.takes > 1 ? `生成 ${p.takes} 条` : '生成'}<CU st={st} n={p.takes} turbo={p.video_turbo} post={post} />
          </button>
        )}
      </div>
      {cmp && <Compare s={s} vids={done} chosen={chosen?.id} onClose={() => setCmp(false)} />}
    </article>
  )
}

function Compare({ s, vids, chosen, onClose }: { s: Shot; vids: Video[]; chosen?: number; onClose: () => void }) {
  const { data, run } = useProject()
  const [pair, setPair] = useState<number[]>(() => vids.slice(0, 2).map(v => v.id))
  const refs = [useRef<HTMLVideoElement>(null), useRef<HTMLVideoElement>(null)]
  const playAll = () => refs.forEach(r => { if (r.current) { r.current.currentTime = 0; r.current.play() } })
  return (
    <Modal open wide onClose={onClose} title={`镜头 ${s.code} · 对比`} className="cmp-modal"
      footer={<><button className="btn btn-secondary" onClick={playAll}><Play size={16} />同时播放</button><span className="grow" /><button className="btn btn-primary" onClick={onClose}>完成</button></>}>
      <div className="cmp-grid">
        {[0, 1].map(k => {
          const v = vids.find(x => x.id === pair[k])
          return (
            <div key={k} className="cmp-col">
              <Segmented size="sm" value={String(pair[k])} onChange={x => setPair(pair.map((y, j) => j === k ? Number(x) : y))} options={vids.map((x, i) => ({ value: String(x.id), label: `第 ${i + 1} 条` }))} />
              <div className={`frame ${arClass(data.project.aspect)}`}>{v && <video ref={refs[k]} src={v.vc_use && v.vc_url ? v.vc_url : v.url} controls playsInline loop />}</div>
              {v && (chosen === v.id ? <span className="pill-tag is-ok"><Check size={12} />已选用</span> :
                <button className="btn btn-secondary btn-sm" onClick={() => run(() => api(`/api/videos/${v.id}/pick`, { method: 'POST' }), '已选用')}><Check size={14} />选用这条</button>)}
            </div>
          )
        })}
      </div>
    </Modal>
  )
}

function VideoState({ v, more }: { v: Video; more: number }) {
  const pct = Math.round(v.progress * 100)
  return (
    <div className="vstate">
      {v.status === 'running' ? <>
        <div className="ring" style={{ ['--p' as any]: pct }}><span>{pct}%</span></div>
      </> : <><Spinner size={18} /><span>{v.status === 'waiting_prev' ? '等上一镜' : '排队中'}{more > 0 ? ` · ${more + 1} 条` : ''}</span></>}
    </div>
  )
}
