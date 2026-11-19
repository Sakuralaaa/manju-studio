import { createContext, useCallback, useContext, useEffect, useRef, useState } from 'react'
import { ArrowLeft, Check, Download } from 'lucide-react'
import { api, type ProjectFull, type Project as P, type Task } from '../api'
import { useStore } from '../store'
import { Spinner, useInterval } from '../components/ui'
import { stepNames } from '../labels'
import Setup from './project/Setup'
import Script from './project/Script'
import Assets from './project/Assets'
import Storyboard from './project/Storyboard'
import Keyframes from './project/Keyframes'
import Videos from './project/Videos'
import Final from './project/Final'

interface Ctx {
  data: ProjectFull
  reload: () => Promise<void>
  patch: (p: Partial<P>) => Promise<void>
  task: (kind: string, ownerType: string, ownerId: number) => Task | undefined
  run: (fn: () => Promise<any>, ok?: string) => Promise<boolean>
  goStep: (n: number) => void
}
const PCtx = createContext<Ctx>(null as any)
export const useProject = () => useContext(PCtx)

export default function ProjectPage({ id }: { id: number }) {
  const { route, go, toast } = useStore()
  const [data, setData] = useState<ProjectFull | null>(null)
  const [err, setErr] = useState('')
  const seenFail = useRef(new Set<number>())
  const reload = useCallback(async () => {
    try {
      const d = await api<ProjectFull>(`/api/projects/${id}`)
      for (const t of d.tasks) {
        if (t.status === 'failed' && !seenFail.current.has(t.id)) {
          seenFail.current.add(t.id)
          if (Date.now() - t.created_at < 15 * 60_000) toast(t.error || '任务失败', 'err')
        }
      }
      setData(d)
    } catch (e: any) { setErr(e.message) }
  }, [id])
  useEffect(() => { reload() }, [reload])
  const busy = !!data && (data.tasks.some(t => t.status === 'running') || data.steps.queued > 0)
  useInterval(reload, busy ? 2000 : 15000)

  if (err) return <div className="page"><div className="card card-empty">{err}</div></div>
  if (!data) return <div className="page"><div className="loading"><Spinner size={20} /></div></div>

  const steps = data.steps.done
  const firstMissing = steps.findIndex(x => !x)
  const step = route.step || (firstMissing < 0 ? 7 : firstMissing + 1)
  const goStep = (n: number) => go({ page: 'project', id, step: n })
  const patch = async (p: Partial<P>) => {
    setData(d => d ? { ...d, project: { ...d.project, ...p } } : d)
    await api(`/api/projects/${id}`, { method: 'PUT', body: p })
  }
  const task = (kind: string, ownerType: string, ownerId: number) => data.tasks.find(t => t.status === 'running' && t.kind === kind && t.owner_type === ownerType && t.owner_id === ownerId)
  const run = async (fn: () => Promise<any>, ok?: string) => {
    try { await fn(); if (ok) toast(ok); await reload(); return true } catch (e: any) { toast(e.message, 'err'); return false }
  }
  const sub = stepStatus(data)
  const View = [Setup, Script, Assets, Storyboard, Keyframes, Videos, Final][step - 1]
  return (
    <PCtx.Provider value={{ data, reload, patch, task, run, goStep }}>
      <div className="page page-project">
        <div className="proj-head">
          <button className="icon-btn" onClick={() => go({ page: 'projects' })} aria-label="返回作品" title="返回作品"><ArrowLeft size={18} /></button>
          <h1 title={data.project.title}>{data.project.title}</h1>
          <span className="grow" />
          <a className="btn btn-secondary btn-sm" href={`/api/projects/${id}/export`}><Download size={14} />打包下载</a>
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
        <View />
      </div>
    </PCtx.Provider>
  )
}

function stepStatus(d: ProjectFull): string[] {
  const s = d.steps, p = d.project
  return [
    p.idea ? p.aspect : '缺创意',
    p.script ? `${[...p.script].length} 字` : '未写',
    s.assets ? (s.assets_ok === s.assets ? `${s.assets} 项` : `缺 ${s.assets - s.assets_ok} 张`) : '未提取',
    s.shots ? `${s.shots} 镜` : '未拆分',
    s.shots ? (s.keyframes === s.shots ? '齐全' : `缺 ${s.shots - s.keyframes} 张`) : '—',
    s.shots ? (s.videos === s.shots ? '齐全' : s.queued ? `排队 ${s.queued}` : `缺 ${s.shots - s.videos} 段`) : '—',
    p.final_url ? '已合成' : '未合成',
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
