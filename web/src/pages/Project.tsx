import { createContext, useCallback, useContext, useEffect, useRef, useState } from 'react'
import { ArrowLeft, Check, Download, Zap } from 'lucide-react'
import { api, type EpisodeFull, type Project as P, type Episode as E, type Task } from '../api'
import { useStore } from '../store'
import { Select, Spinner, useInterval } from '../components/ui'
import { epName, stepNames } from '../labels'
import Setup from './project/Setup'
import Script from './project/Script'
import Assets from './project/Assets'
import Storyboard from './project/Storyboard'
import Keyframes from './project/Keyframes'
import Videos from './project/Videos'
import Final from './project/Final'
import { AutopilotStart, AutopilotPanel } from './project/Autopilot'

interface Ctx {
  data: EpisodeFull
  reload: () => Promise<void>
  patch: (p: Partial<P>) => Promise<void>
  patchEp: (p: Partial<E>) => Promise<void>
  task: (kind: string, ownerType: string, ownerId: number) => Task | undefined
  run: (fn: () => Promise<any>, ok?: string) => Promise<boolean>
  goStep: (n: number) => void
  library?: boolean
}
export const PCtx = createContext<Ctx>(null as any)
export const useProject = () => useContext(PCtx)

export function useRunner(reload: () => Promise<void>) {
  const { toast } = useStore()
  return async (fn: () => Promise<any>, ok?: string) => {
    try { await fn(); if (ok) toast(ok); await reload(); return true } catch (e: any) { toast(e.message, 'err'); return false }
  }
}

export function useTaskToasts() {
  const { toast } = useStore()
  const seen = useRef(new Set<number>())
  return (tasks: Task[]) => {
    for (const t of tasks) {
      if (t.status === 'failed' && !seen.current.has(t.id)) {
        seen.current.add(t.id)
        if (Date.now() - t.created_at < 15 * 60_000) toast(t.error || '任务失败', 'err')
      }
    }
  }
}

export default function ProjectPage({ id }: { id: number }) {
  const { route, go } = useStore()
  const [data, setData] = useState<EpisodeFull | null>(null)
  const [err, setErr] = useState('')
  const [auto, setAuto] = useState(false)
  const toasts = useTaskToasts()
  const reload = useCallback(async () => {
    try {
      const d = await api<EpisodeFull>(`/api/episodes/${id}`)
      toasts(d.tasks)
      setData(d)
    } catch (e: any) { setErr(e.message) }
  }, [id])
  useEffect(() => { reload() }, [reload])
  const pipeOn = !!data?.pipeline && ['running', 'waiting', 'waiting_writer'].includes(data.pipeline.status)
  const busy = !!data && (data.tasks.some(t => t.status === 'running') || data.steps.queued > 0 || pipeOn)
  useInterval(reload, busy ? 2000 : 15000)
  const run = useRunner(reload)

  if (err) return <div className="page"><div className="card card-empty">{err}</div></div>
  if (!data) return <div className="page"><div className="loading"><Spinner size={20} /></div></div>

  const p = data.project, e = data.episode
  const steps = data.steps.done
  const firstMissing = steps.findIndex(x => !x)
  const step = route.step || (firstMissing < 0 ? 7 : firstMissing + 1)
  const goStep = (n: number) => go({ page: 'project', id: p.id, ep: e.id, step: n })
  const patch = async (x: Partial<P>) => {
    setData(d => d ? { ...d, project: { ...d.project, ...x } } : d)
    await api(`/api/projects/${p.id}`, { method: 'PUT', body: x })
  }
  const patchEp = async (x: Partial<E>) => {
    setData(d => d ? { ...d, episode: { ...d.episode, ...x } } : d)
    await api(`/api/episodes/${e.id}`, { method: 'PUT', body: x })
  }
  const task = (kind: string, ownerType: string, ownerId: number) => data.tasks.find(t => t.status === 'running' && t.kind === kind && t.owner_type === ownerType && t.owner_id === ownerId)
  const sub = stepStatus(data)
  const View = [Setup, Script, Assets, Storyboard, Keyframes, Videos, Final][step - 1]
  const pl = data.pipeline
  const showPanel = !!pl && (pl.status !== 'done' || Date.now() - pl.updated_at < 10 * 60_000) && pl.status !== 'canceled'
  return (
    <PCtx.Provider value={{ data, reload, patch, patchEp, task, run, goStep }}>
      <div className="page page-project">
        <div className="proj-head">
          <button className="icon-btn" onClick={() => go({ page: 'series', id: p.id })} aria-label="返回剧集" title="返回剧集"><ArrowLeft size={18} /></button>
          <h1 title={p.title}>{p.title}</h1>
          <Select size="md" className="ep-sel" value={String(e.id)} onChange={v => go({ page: 'project', id: p.id, ep: Number(v), step })}
            options={data.episodes.map(x => ({ value: String(x.id), label: epName(x.no), hint: x.done ? '已成片' : undefined }))} />
          <span className="grow" />
          {!pipeOn && pl?.status !== 'confirm' && <button className="btn btn-primary btn-sm" onClick={() => setAuto(true)}><Zap size={14} />一键成集</button>}
          <a className="btn btn-secondary btn-sm hide-xs" href={`/api/episodes/${e.id}/export`}><Download size={14} />打包下载</a>
        </div>
        <nav className="stepbar" aria-label="制作流程">
          {stepNames.map((n, i) => (
            <button key={n} className={`step ${steps[i] ? 'is-done' : ''} ${step === i + 1 ? 'is-on' : ''}`} onClick={() => goStep(i + 1)}>
              <span className="step-no">{steps[i] ? <Check size={13} strokeWidth={3} /> : i + 1}</span>
              <span className="step-text">
                <span className="step-name">{n}</span>
                <span className="step-sub">{sub[i]}</span>
              </span>
            </button>
          ))}
        </nav>
        {showPanel && pl && <AutopilotPanel pl={pl} />}
        <View />
        {auto && <AutopilotStart onClose={() => setAuto(false)} />}
      </div>
    </PCtx.Provider>
  )
}

function stepStatus(d: EpisodeFull): string[] {
  const s = d.steps, p = d.project, e = d.episode
  return [
    e.idea || p.idea ? epName(e.no) : '缺梗概',
    e.script ? `${[...e.script].length} 字` : '未写',
    s.assets ? (s.assets_ok === s.assets ? `${s.assets} 项` : `缺 ${s.assets - s.assets_ok} 张`) : '未提取',
    s.shots ? `${s.shots} 镜` : '未拆分',
    s.shots ? (s.keyframes === s.shots ? '齐全' : `缺 ${s.shots - s.keyframes} 张`) : '—',
    s.shots ? (s.videos === s.shots ? '齐全' : s.queued ? `排队 ${s.queued}` : `缺 ${s.shots - s.videos} 段`) : '—',
    e.final_url ? '已合成' : '未合成',
  ]
}

export function StepHead({ title, children, note }: { title: string; children?: React.ReactNode; note?: React.ReactNode }) {
  return (
    <div className="step-head">
      <h2>{title}</h2>
      {note && <span className="step-note">{note}</span>}
      <span className="grow" />
      <div className="step-actions">{children}</div>
    </div>
  )
}

export function Cost({ children }: { children: React.ReactNode }) {
  return <span className="cost">{children}</span>
}
