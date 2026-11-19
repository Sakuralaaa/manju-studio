import { useEffect, useRef, useState } from 'react'
import { Clapperboard, Download, FileArchive, Captions, Play, Sparkles, Mic2, Music, Upload, Trash2, RotateCcw, FolderOpen, ImageIcon, FileText } from 'lucide-react'
import { api, upload, type Bgm, type Line, type Video } from '../../api'
import { useProject, StepHead, Cost } from '../Project'
import { useStore } from '../../store'
import { Empty, Segmented, Spinner } from '../../components/ui'
import { PlayBtn } from '../../components/audio'
import { fmtSec, fmtTime } from '../../labels'
import { arClass } from './media'
import { VoiceButton } from './Voice'

type Tab = 'mix' | 'voice' | 'music' | 'export'

export default function Final() {
  const { data } = useProject()
  const [tab, setTab] = useState<Tab>('mix')
  const clips = data.shots.map(s => {
    const done = s.videos.filter(v => v.status === 'done')
    return { shot: s, v: done.find(v => v.id === s.picked_video_id) || done[done.length - 1] }
  })
  const ready = clips.filter(c => c.v).length
  const total = clips.reduce((t, c) => t + (c.v?.seconds || 0), 0)
  return (
    <section className="stepview">
      <StepHead title="成片" note={`${ready} / ${data.shots.length} 段 · ${fmtSec(total)}`}>
        <Segmented block={false} value={tab} onChange={v => setTab(v as Tab)} options={[{ value: 'mix', label: '合成' }, { value: 'voice', label: '配音' }, { value: 'music', label: '配乐音效' }, { value: 'export', label: '导出' }]} />
      </StepHead>
      {tab === 'mix' && <Mix clips={clips} ready={ready} />}
      {tab === 'voice' && <Voices />}
      {tab === 'music' && <Music2 />}
      {tab === 'export' && <Export />}
      {data.shots.length === 0 && <div className="card card-empty"><Empty art="frame" title="还没有分镜" /></div>}
    </section>
  )
}

function Mix({ clips, ready }: { clips: { shot: any; v?: Video }[]; ready: number }) {
  const { data, task, run, patchEp } = useProject()
  const p = data.project, e = data.episode
  const [mode, setMode] = useState<'final' | 'seq'>(e.final_url ? 'final' : 'seq')
  const building = task('final', 'episode', e.id)
  const covering = task('cover', 'episode', e.id)
  const summing = task('summary', 'episode', e.id)
  useEffect(() => { if (e.final_url) setMode('final') }, [e.final_url])
  const dub = p.audio_mode === 'dub'
  const linesMissing = data.lines.filter(l => l.status !== 'done').length
  return (
    <>
      <div className="final-grid">
        <div className="card final-stage">
          <div className="final-tabs">
            <Segmented block={false} size="sm" value={mode} onChange={v => setMode(v as any)} options={[{ value: 'final', label: '成片' }, { value: 'seq', label: '按镜头预览' }]} />
          </div>
          <div className={`final-screen ${arClass(p.aspect)}`}>
            {mode === 'final' ? (e.final_url ? <video key={e.final_url} src={e.final_url} poster={e.cover_url || undefined} controls playsInline /> : <div className="frame-empty"><Clapperboard size={28} strokeWidth={1.5} /><span>还没有合成</span></div>)
              : <SeqPlayer clips={clips.filter(c => c.v).map(c => c.v!)} />}
          </div>
        </div>
        <div className="final-side">
          <div className="card scard">
            <h3 className="scard-title">合成</h3>
            <div className="field"><span className="field-label">字幕</span>
              <Segmented value={e.subtitles ? 'on' : 'off'} onChange={v => patchEp({ subtitles: v === 'on' })} options={[{ value: 'on', label: '烧录台词' }, { value: 'off', label: '不烧录' }]} />
            </div>
            <div className="mix-sum">
              <span>{dub ? `配音 ${data.lines.length - linesMissing} / ${data.lines.length} 句` : '视频原生声音'}</span>
              <span>{e.bgm_id ? '有配乐' : '无配乐'}</span>
              <span>{p.deliver === '1080p' ? '1080p' : '原始尺寸'}</span>
            </div>
            <button className="btn btn-primary btn-block" disabled={!!building || ready < data.shots.length || data.shots.length === 0}
              onClick={() => run(() => api(`/api/episodes/${e.id}/final`, { body: { subtitles: e.subtitles } }))}>
              {building ? <><Spinner />合成中</> : <><Clapperboard size={16} />{e.final_url ? '重新合成' : '合成成片'}</>}
            </button>
            {ready < data.shots.length && <p className="side-note">还差 {data.shots.length - ready} 段视频</p>}
            {dub && linesMissing > 0 && ready === data.shots.length && <p className="side-note">还有 {linesMissing} 句没配音</p>}
          </div>
          <div className="card scard">
            <h3 className="scard-title">封面</h3>
            <div className={`cover-box ${arClass(p.aspect)} ${covering ? 'is-busy' : ''}`}>
              {e.cover_url ? <img src={e.cover_url} alt="" /> : <div className="frame-empty"><ImageIcon size={22} strokeWidth={1.5} /></div>}
              {covering && <div className="frame-busy"><Spinner size={18} /></div>}
            </div>
            <button className="btn btn-secondary btn-block" disabled={!!covering} onClick={() => run(() => api(`/api/episodes/${e.id}/cover`, { body: {} }))}>
              <Sparkles size={16} />{e.cover_url ? '重新生成' : '生成封面'}<Cost>1 张</Cost>
            </button>
          </div>
          <div className="card scard">
            <h3 className="scard-title">本集摘要</h3>
            <p className="sum-text">{e.summary || <span className="muted">未写</span>}</p>
            {e.cliffhanger && <p className="sum-text"><b>卡点</b> {e.cliffhanger}</p>}
            <button className="btn btn-secondary btn-block" disabled={!!summing || !e.script} onClick={() => run(() => api(`/api/episodes/${e.id}/summary`, { method: 'POST' }))}>
              {summing ? <Spinner /> : <FileText size={16} />}{e.summary ? '重写摘要' : '写摘要'}<Cost>1 次对话</Cost>
            </button>
          </div>
        </div>
      </div>
      <div className="seq-strip">
        {clips.map(({ shot, v }) => {
          const kf = shot.images.find((i: any) => i.id === shot.picked_image_id)
          return (
            <div key={shot.id} className={`seq-item ${v ? '' : 'is-missing'}`}>
              <div className={`seq-thumb ${arClass(p.aspect)}`}>{kf ? <img src={kf.url} alt="" loading="lazy" /> : null}</div>
              <span>{shot.code}</span>
            </div>
          )
        })}
      </div>
    </>
  )
}

function Voices() {
  const { data, task, run, patch } = useProject()
  const { go } = useStore()
  const p = data.project, e = data.episode
  const busy = task('lines', 'episode', e.id)
  const byShot = data.shots.map(s => ({ s, lines: data.lines.filter(l => l.shot_id === s.id) })).filter(x => x.lines.length > 0)
  const todo = data.lines.filter(l => l.status !== 'done').length
  const chars = data.assets.filter(a => a.kind === 'character')
  return (
    <div className="stack">
      {p.audio_mode !== 'dub' && (
        <div className="inline-warn"><span>当前用视频原生声音，配音不会混进成片</span><button className="btn btn-secondary btn-sm" onClick={() => patch({ audio_mode: 'dub' })}>改用配音覆盖</button></div>
      )}
      <div className="card scard">
        <div className="vcast">
          <div className="vcast-item"><span className="vcast-name">旁白</span><VoiceButton value={p.narrator_voice} name="旁白" onSave={v => patch({ narrator_voice: v })} icon={<Mic2 size={14} />} /></div>
          {chars.map(a => (
            <div key={a.id} className="vcast-item"><span className="vcast-name" title={a.name}>{a.name}</span>
              <VoiceButton value={a.voice || {}} name={a.name} assetId={a.id} onSave={v => run(() => api(`/api/assets/${a.id}`, { method: 'PUT', body: { name: a.name, description: a.description, prompt: a.prompt, voice: v } }), '音色已保存')} />
            </div>
          ))}
          <span className="grow" />
          <button className="btn btn-ghost btn-sm" onClick={() => go({ page: 'project', id: p.id, ep: e.id, step: 3 })}>去资产</button>
        </div>
      </div>
      <div className="line-head">
        <b>{data.lines.length} 句台词</b><span className="muted">{todo ? `${todo} 句待生成` : '全部就绪'}</span>
        <span className="grow" />
        {data.lines.length > 0 && todo === 0 && <button className="btn btn-ghost btn-sm" disabled={!!busy} onClick={() => confirm('全部重新生成？') && run(() => api(`/api/episodes/${e.id}/lines/generate`, { body: { force: true } }))}><RotateCcw size={14} />全部重做</button>}
        <button className="btn btn-primary btn-sm" disabled={!!busy || todo === 0} onClick={() => run(() => api(`/api/episodes/${e.id}/lines/generate`, { body: {} }))}>
          {busy ? <Spinner size={13} /> : <Mic2 size={14} />}生成配音<Cost>{todo} 句</Cost>
        </button>
      </div>
      {byShot.length === 0 && <div className="card card-empty"><Empty art="book" title="分镜里还没有台词" /></div>}
      {byShot.map(({ s, lines }) => (
        <div key={s.id} className="card lshot">
          <span className="code">{s.code}</span>
          <div className="lrows">{lines.map(l => <LineRow key={l.id} l={l} />)}</div>
        </div>
      ))}
    </div>
  )
}

function LineRow({ l }: { l: Line }) {
  const { task, run } = useProject()
  const [emo, setEmo] = useState(l.emotion)
  useEffect(() => setEmo(l.emotion), [l.emotion])
  const busy = !!task('line', 'line', l.id) || l.status === 'running'
  return (
    <div className={`lrow is-${l.status || 'none'}`}>
      <span className={`lspk ${l.kind === 'narration' ? 'is-nar' : ''}`} title={l.speaker}>{l.speaker}</span>
      <span className="ltext" title={l.text}>{l.text}</span>
      <input className="input lemo" value={emo} placeholder="语气" onChange={x => setEmo(x.target.value)} onBlur={() => emo !== l.emotion && run(() => api(`/api/lines/${l.id}`, { method: 'PUT', body: { emotion: emo } }))} />
      <span className="ldur">{l.status === 'failed' ? <span className="danger-text" title={l.error}>失败</span> : l.duration ? `${l.duration.toFixed(1)} 秒` : ''}</span>
      <PlayBtn src={l.audio_url || undefined} />
      <button className="icon-btn sm" disabled={busy} onClick={() => run(() => api(`/api/lines/${l.id}/generate`, { method: 'POST' }))} aria-label="重新生成" title="重新生成">{busy ? <Spinner size={13} /> : <RotateCcw size={14} />}</button>
    </div>
  )
}

function Music2() {
  const { data, run, patchEp } = useProject()
  const { toast } = useStore()
  const e = data.episode
  const [lib, setLib] = useState<Bgm[] | null>(null)
  const [up, setUp] = useState(false)
  const file = useRef<HTMLInputElement>(null)
  const load = () => api<Bgm[]>('/api/bgm').then(setLib)
  useEffect(() => { load() }, [])
  const add = async (f: File) => {
    setUp(true)
    try { const b = await upload<Bgm>('/api/bgm', f); await load(); if (!e.bgm_id) patchEp({ bgm_id: b.id }) } catch (x: any) { toast(x.message, 'err') } finally { setUp(false) }
  }
  return (
    <div className="music-grid">
      <div className="card scard">
        <div className="scard-head"><h3 className="scard-title">配乐</h3><span className="grow" />
          <button className="btn btn-secondary btn-sm" disabled={up} onClick={() => file.current?.click()}>{up ? <Spinner size={13} /> : <Upload size={14} />}上传</button>
          <input ref={file} type="file" accept="audio/*" hidden onChange={x => { const f = x.target.files?.[0]; if (f) add(f); x.target.value = '' }} />
        </div>
        {!lib ? <div className="loading"><Spinner /></div> : (
          <div className="bgm-list">
            <button className={`bgm ${e.bgm_id === 0 ? 'is-on' : ''}`} onClick={() => patchEp({ bgm_id: 0 })}><span className="bgm-ic"><Music size={14} /></span><b>不用配乐</b></button>
            {lib.map(b => (
              <div key={b.id} className={`bgm ${e.bgm_id === b.id ? 'is-on' : ''}`} onClick={() => patchEp({ bgm_id: b.id })}>
                <span onClick={x => x.stopPropagation()}><PlayBtn src={b.url} /></span>
                <b title={b.name}>{b.name}</b><span className="muted">{fmtSec(b.duration)}</span>
                <span onClick={x => x.stopPropagation()}><button className="icon-btn sm is-danger" onClick={() => confirm(`从曲库删除「${b.name}」？`) && api(`/api/bgm/${b.id}`, { method: 'DELETE' }).then(load)} aria-label="删除" title="删除"><Trash2 size={13} /></button></span>
              </div>
            ))}
          </div>
        )}
        {e.bgm_id > 0 && (
          <div className="form-grid form-grid-4 bgm-tune">
            <label className="field"><span className="field-label">音量</span><Num v={Math.round(e.bgm_volume * 100)} unit="%" min={0} max={100} step={5} onSave={v => patchEp({ bgm_volume: v / 100 })} /></label>
            <label className="field"><span className="field-label">淡入</span><Num v={e.bgm_fade_in} unit="秒" min={0} max={15} step={0.5} onSave={v => patchEp({ bgm_fade_in: v })} /></label>
            <label className="field"><span className="field-label">淡出</span><Num v={e.bgm_fade_out} unit="秒" min={0} max={15} step={0.5} onSave={v => patchEp({ bgm_fade_out: v })} /></label>
            <div className="field"><span className="field-label">对白时压低</span><Segmented value={e.bgm_duck ? 'on' : 'off'} onChange={v => patchEp({ bgm_duck: v === 'on' })} options={[{ value: 'on', label: '开' }, { value: 'off', label: '关' }]} /></div>
          </div>
        )}
      </div>
      <div className="card scard">
        <h3 className="scard-title">镜头音效</h3>
        <div className="sfx-list">
          {data.shots.map(s => <SfxRow key={s.id} s={s} run={run} />)}
        </div>
      </div>
    </div>
  )
}

function Num({ v, unit, min, max, step, onSave }: { v: number; unit: string; min: number; max: number; step: number; onSave: (v: number) => void }) {
  const [x, setX] = useState(v)
  useEffect(() => setX(v), [v])
  return <div className="unit-input"><input className="input" type="number" min={min} max={max} step={step} value={x} onChange={e => setX(Number(e.target.value))} onBlur={() => x !== v && onSave(x)} /><span>{unit}</span></div>
}

function SfxRow({ s, run }: { s: any; run: (fn: () => Promise<any>, ok?: string) => Promise<boolean> }) {
  const file = useRef<HTMLInputElement>(null)
  const [vol, setVol] = useState(Math.round(s.sfx_volume * 100))
  return (
    <div className="sfx-row">
      <span className="code">{s.code}</span>
      <span className="sfx-name" title={s.sfx_name || s.action}>{s.sfx_name || <span className="muted">{s.action || '无音效'}</span>}</span>
      {s.sfx_url && <div className="unit-input sm-unit"><input className="input" type="number" min={0} max={200} step={10} value={vol} onChange={e => setVol(Number(e.target.value))}
        onBlur={() => run(() => api(`/api/shots/${s.id}`, { method: 'PUT', body: { ...s, sfx_volume: vol / 100 } }))} /><span>%</span></div>}
      <PlayBtn src={s.sfx_url || undefined} />
      <button className="icon-btn sm" onClick={() => file.current?.click()} aria-label="上传音效" title="上传音效"><Upload size={14} /></button>
      {s.sfx_url && <button className="icon-btn sm is-danger" onClick={() => run(() => api(`/api/shots/${s.id}/sfx`, { method: 'DELETE' }))} aria-label="移除" title="移除"><Trash2 size={13} /></button>}
      <input ref={file} type="file" accept="audio/*" hidden onChange={e => { const f = e.target.files?.[0]; if (f) run(() => upload(`/api/shots/${s.id}/sfx`, f), '已添加音效'); e.target.value = '' }} />
    </div>
  )
}

function Export() {
  const { data } = useProject()
  const e = data.episode
  const [root, setRoot] = useState(() => localStorage.getItem('manju.jyroot') || '')
  return (
    <div className="music-grid">
      <div className="card scard">
        <h3 className="scard-title">下载</h3>
        {e.final_url ? <>
          <a className="dl-row" href={e.final_url + '?dl=1'} download><Download size={16} /><span>成片.mp4</span><span className="grow" /><span className="muted">{fmtTime(e.final_at)}</span></a>
          <a className="dl-row" href={e.final_url.replace(/\.mp4$/, '.srt') + '?dl=1'} download><Captions size={16} /><span>字幕.srt</span></a>
        </> : <div className="dl-row muted"><Clapperboard size={16} /><span>还没有合成成片</span></div>}
        {e.cover_url && <a className="dl-row" href={e.cover_url + '?dl=1'} download><ImageIcon size={16} /><span>封面</span></a>}
        <a className="dl-row" href={`/api/episodes/${e.id}/export`}><FileArchive size={16} /><span>全部素材.zip</span></a>
      </div>
      <div className="card scard">
        <h3 className="scard-title">剪映草稿</h3>
        <label className="field"><span className="field-label">本机草稿目录</span>
          <div className="search jy-root"><FolderOpen size={14} /><input value={root} onChange={x => { setRoot(x.target.value); localStorage.setItem('manju.jyroot', x.target.value) }} placeholder="可留空，例如 D:/JianyingPro Drafts" /></div>
        </label>
        <a className={`btn btn-primary btn-block ${data.steps.done[5] ? '' : 'is-disabled'}`} href={data.steps.done[5] ? `/api/episodes/${e.id}/capcut?root=${encodeURIComponent(root.trim())}` : undefined} aria-disabled={!data.steps.done[5]}>
          <Download size={16} />下载剪映草稿
        </a>
        {!data.steps.done[5] && <p className="side-note">还差视频</p>}
      </div>
    </div>
  )
}

function SeqPlayer({ clips }: { clips: Video[] }) {
  const [i, setI] = useState(0)
  const [playing, setPlaying] = useState(false)
  const ref = useRef<HTMLVideoElement>(null)
  useEffect(() => { if (playing) ref.current?.play().catch(() => {}) }, [i, playing])
  if (clips.length === 0) return <div className="frame-empty"><span>还没有视频</span></div>
  const cur = clips[Math.min(i, clips.length - 1)]
  return (
    <>
      <video ref={ref} key={cur.id} src={cur.url} controls playsInline onPlay={() => setPlaying(true)} onEnded={() => { if (i < clips.length - 1) setI(i + 1); else { setPlaying(false); setI(0) } }} />
      {!playing && <button className="seq-play" onClick={() => { setPlaying(true); ref.current?.play() }} aria-label="连续播放"><Play size={22} /></button>}
      <span className="seq-count">{i + 1} / {clips.length}</span>
    </>
  )
}
