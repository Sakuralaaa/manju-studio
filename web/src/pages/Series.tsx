import { useCallback, useEffect, useState } from 'react'
import { ArrowLeft, Plus, Sparkles, Trash2, Clapperboard, Zap, Check, Film } from 'lucide-react'
import { api, type SeriesFull, type Project as P, type EpisodeFull } from '../api'
import { useStore } from '../store'
import { Empty, Modal, Segmented, Spinner, Stepper, useInterval } from '../components/ui'
import { epName, fmtCU, fmtSec, fmtTime, pipeStatusNames, stepNames } from '../labels'
import { PCtx, useRunner, useTaskToasts, Cost } from './Project'
import Assets from './project/Assets'

type Tab = 'episodes' | 'bible' | 'assets'

export default function Series({ id }: { id: number }) {
  const { route, go } = useStore()
  const [data, setData] = useState<SeriesFull | null>(null)
  const [err, setErr] = useState('')
  const toasts = useTaskToasts()
  const reload = useCallback(async () => {
    try { const d = await api<SeriesFull>(`/api/projects/${id}`); toasts(d.tasks); setData(d) } catch (e: any) { setErr(e.message) }
  }, [id])
  useEffect(() => { reload() }, [reload])
  const busy = !!data && (data.tasks.some(t => t.status === 'running') || data.episodes.some(e => e.pipeline && ['running', 'waiting', 'waiting_writer'].includes(e.pipeline.status)))
  useInterval(reload, busy ? 2500 : 15000)
  const run = useRunner(reload)
  if (err) return <div className="page"><div className="card card-empty">{err}</div></div>
  if (!data) return <div className="page"><div className="loading"><Spinner size={20} /></div></div>
  const tab = (['episodes', 'bible', 'assets'].includes(route.tab || '') ? route.tab : 'episodes') as Tab
  const p = data.project
  const patch = async (x: Partial<P>) => {
    setData(d => d ? { ...d, project: { ...d.project, ...x } } : d)
    await api(`/api/projects/${p.id}`, { method: 'PUT', body: x })
  }
  const task = (kind: string, ownerType: string, ownerId: number) => data.tasks.find(t => t.status === 'running' && t.kind === kind && t.owner_type === ownerType && t.owner_id === ownerId)
  const ctxData = { project: p, assets: data.assets, tasks: data.tasks, episode: null as any, episodes: [], shots: [], lines: [], steps: {} as any, colab: {} as any, pipeline: null } as unknown as EpisodeFull
  const done = data.episodes.filter(e => e.final_url).length
  return (
    <PCtx.Provider value={{ data: ctxData, reload, patch, patchEp: async () => {}, task, run, goStep: () => {}, library: true }}>
      <div className="page page-project">
        <div className="proj-head">
          <button className="icon-btn" onClick={() => go({ page: 'projects' })} aria-label="返回作品" title="返回作品"><ArrowLeft size={18} /></button>
          <h1 title={p.title}>{p.title}</h1>
          <span className="pill-tag">{data.episodes.length} 集</span>
          <span className="grow" />
          <button className="icon-btn hide-xs" onClick={() => confirm(`删除「${p.title}」的全部集数、资产、图片和视频？`) && api(`/api/projects/${p.id}`, { method: 'DELETE' }).then(() => go({ page: 'projects' }))} aria-label="删除作品" title="删除作品"><Trash2 size={16} /></button>
        </div>
        <div className="series-stats">
          <div><b>{data.episodes.length}</b><span>集数</span></div>
          <div><b>{done}</b><span>已成片</span></div>
          <div><b>{data.assets.length}</b><span>资产</span></div>
          <div><b>{fmtCU(data.ledger.cu)}</b><span>实际 CU</span></div>
          <div><b>{fmtCU(data.ledger.est_cu)}</b><span>估算 CU</span></div>
          <div><b>{data.ledger.images}</b><span>生图</span></div>
          <div><b>{data.ledger.tts}</b><span>配音</span></div>
        </div>
        <div className="series-tabs">
          <Segmented block={false} value={tab} onChange={v => go({ page: 'series', id: p.id, tab: v === 'episodes' ? undefined : v })}
            options={[{ value: 'episodes', label: '剧集' }, { value: 'bible', label: '设定集' }, { value: 'assets', label: '资产库' }]} />
        </div>
        {tab === 'episodes' && <Episodes data={data} run={run} />}
        {tab === 'bible' && <Bible data={data} patch={patch} run={run} task={task} />}
        {tab === 'assets' && <Assets />}
      </div>
    </PCtx.Provider>
  )
}

function Episodes({ data, run }: { data: SeriesFull; run: (fn: () => Promise<any>, ok?: string) => Promise<boolean> }) {
  const { go } = useStore()
  const [adding, setAdding] = useState(false)
  const p = data.project
  return (
    <>
      <div className="egrid">
        {data.episodes.map(e => {
          const steps = e.steps.done
          const next = steps.findIndex(x => !x)
          const pl = e.pipeline
          const plOn = pl && ['running', 'waiting', 'waiting_writer', 'confirm', 'failed'].includes(pl.status)
          return (
            <article key={e.id} className="card ecard" onClick={() => go({ page: 'project', id: p.id, ep: e.id })}>
              <div className={`ecover ar-${p.aspect.replace(':', '-')}`}>
                {e.thumb ? <img src={e.thumb} alt="" loading="lazy" /> : <div className="pcover-empty"><Clapperboard size={24} strokeWidth={1.5} /></div>}
                <span className="eno">{e.no}</span>
                {e.final_url && <span className="pbadge"><Film size={12} />成片</span>}
              </div>
              <div className="ebody">
                <h3 className="ptitle" title={epName(e.no, e.title)}>{epName(e.no, e.title)}</h3>
                <p className="pidea" data-clamp title={e.summary || e.idea}>{e.summary || e.idea || (e.no === 1 ? p.idea : '') || '还没有梗概'}</p>
                <div className="psteps">{steps.map((d, i) => <i key={i} className={d ? 'is-done' : i === next ? 'is-next' : ''} title={stepNames[i]} />)}</div>
                <div className="pmeta">
                  {plOn ? <span className={`ep-pipe is-${pl!.status}`}><Zap size={12} />{pipeStatusNames[pl!.status]}</span> : <span>{next < 0 ? '已完成' : `下一步 · ${stepNames[next]}`}</span>}
                  <span className="grow" />
                  <span>{e.duration ? fmtSec(e.duration) : fmtTime(e.updated_at)}</span>
                </div>
              </div>
            </article>
          )
        })}
        <button className="card ecard ecard-add" onClick={() => setAdding(true)}>
          <Plus size={22} /><span>新建第 {data.episodes.length + 1} 集</span>
        </button>
      </div>
      {adding && <NewEpisode data={data} onClose={() => setAdding(false)} run={run} />}
    </>
  )
}

function NewEpisode({ data, onClose }: { data: SeriesFull; onClose: () => void; run: any }) {
  const { go, toast } = useStore()
  const [idea, setIdea] = useState('')
  const [ai, setAi] = useState(true)
  const [busy, setBusy] = useState(false)
  const last = data.episodes[data.episodes.length - 1]
  const create = async () => {
    setBusy(true)
    try {
      const r = await api<{ episode: { id: number } }>(`/api/projects/${data.project.id}/episodes`, { body: { idea, ai_idea: ai && !idea.trim() } })
      go({ page: 'project', id: data.project.id, ep: r.episode.id, step: 1 })
    } catch (e: any) { toast(e.message, 'err'); setBusy(false) }
  }
  return (
    <Modal open onClose={onClose} title={`新建第 ${data.episodes.length + 1} 集`}
      footer={<><button className="btn btn-secondary" onClick={onClose}>取消</button><button className="btn btn-primary" disabled={busy} onClick={create}>{busy && <Spinner />}创建</button></>}>
      {last?.cliffhanger && <div className="prev-cliff"><span className="muted">上一集卡点</span><p>{last.cliffhanger}</p></div>}
      <label className="field"><span className="field-label">本集梗概</span>
        <textarea className="input" rows={4} value={idea} onChange={e => setIdea(e.target.value)} placeholder="留空则根据设定集、前情和上一集卡点自动拟定" />
      </label>
      {!idea.trim() && <div className="field"><span className="field-label">梗概</span>
        <Segmented value={ai ? 'ai' : 'none'} onChange={v => setAi(v === 'ai')} options={[{ value: 'ai', label: 'AI 拟定' }, { value: 'none', label: '稍后自己写' }]} />
      </div>}
    </Modal>
  )
}

function Bible({ data, patch, run, task }: { data: SeriesFull; patch: (x: Partial<P>) => Promise<void>; run: any; task: any }) {
  const p = data.project
  const [f, setF] = useState(p)
  const [note, setNote] = useState('')
  const busy = task('bible', 'project', p.id)
  useEffect(() => { setF(p) }, [p.world, p.bios, p.outline, p.idea])
  const save = (k: keyof P, v: any) => { if ((p as any)[k] !== v) patch({ [k]: v } as any) }
  const box = (k: 'world' | 'bios' | 'outline', title: string, rows: number, ph: string) => (
    <div className={`card scard bible-card ${busy ? 'is-busy' : ''}`}>
      <h3 className="scard-title">{title}</h3>
      <textarea className="input bible-text" rows={rows} value={(f as any)[k]} onChange={e => setF({ ...f, [k]: e.target.value })} onBlur={() => save(k, (f as any)[k])} placeholder={ph} />
    </div>
  )
  return (
    <div className="bible-grid">
      <div className="card scard bible-idea">
        <h3 className="scard-title">故事创意</h3>
        <textarea className="input" rows={3} value={f.idea} onChange={e => setF({ ...f, idea: e.target.value })} onBlur={() => save('idea', f.idea)} placeholder="一两句话：谁，想要什么，遇到了什么反转" />
        <div className="bible-bar">
          <div className="field field-row"><span className="field-label">计划集数</span><Stepper value={f.episodes_plan} min={1} max={200} onChange={v => { setF({ ...f, episodes_plan: v }); save('episodes_plan', v) }} /></div>
          <input className="input" value={note} onChange={e => setNote(e.target.value)} placeholder="额外要求（可选）" />
          <button className="btn btn-primary" disabled={!!busy || !f.idea.trim()} onClick={() => run(() => api(`/api/projects/${p.id}/bible/generate`, { body: { note } }))}>
            {busy ? <Spinner /> : <Sparkles size={16} />}{p.world || p.bios || p.outline ? 'AI 完善设定集' : 'AI 生成设定集'}<Cost>1 次对话</Cost>
          </button>
        </div>
      </div>
      {box('world', '世界观', 8, '时代、地点、规则、核心冲突')}
      {box('bios', '人物小传', 8, '姓名——年龄、外貌、服装、性格、动机、关系')}
      <div className="span-all">{box('outline', '分集总纲', 10, '第 1 集：……；卡点：……')}</div>
      {data.episodes.some(e => e.summary) && (
        <div className="card scard span-all">
          <h3 className="scard-title">前情摘要</h3>
          <div className="recap">
            {data.episodes.filter(e => e.summary).map(e => (
              <div key={e.id} className="recap-row"><span className="code">{e.no}</span><p>{e.summary}{e.cliffhanger && <em> 卡点：{e.cliffhanger}</em>}</p>{e.final_url && <Check size={14} className="accent-text" />}</div>
            ))}
          </div>
        </div>
      )}
      {!data.episodes.length && <Empty art="book" title="还没有集" />}
    </div>
  )
}
