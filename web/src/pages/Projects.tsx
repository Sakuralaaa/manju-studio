import { useEffect, useState, type ReactNode } from 'react'
import { Plus, Clapperboard, Film } from 'lucide-react'
import { api, type ProjectCard } from '../api'
import { useStore } from '../store'
import { Empty, Modal, Segmented, Spinner } from '../components/ui'
import { fmtTime, stepNames } from '../labels'

export function PageHead({ title, sub, children }: { title: ReactNode; sub?: ReactNode; children?: ReactNode }) {
  return (
    <div className="page-head">
      <h1>{title}</h1>
      {sub && <span className="page-sub">{sub}</span>}
      <span className="grow" />
      {children}
    </div>
  )
}

export const styleChips = ['日系赛璐璐', '国漫厚涂', '韩漫精致', '3D 卡通', '水墨国风', '美式漫画', '赛博朋克', '暗黑哥特']

export default function Projects() {
  const { go } = useStore()
  const [list, setList] = useState<ProjectCard[] | null>(null)
  const [creating, setCreating] = useState(false)
  useEffect(() => { api<ProjectCard[]>('/api/projects').then(setList).catch(() => setList([])) }, [])
  return (
    <div className="page">
      <PageHead title="作品" sub={list ? `${list.length} 部` : undefined}>
        <button className="btn btn-primary" onClick={() => setCreating(true)}><Plus size={16} />新建作品</button>
      </PageHead>
      {!list && <div className="loading"><Spinner size={20} /></div>}
      {list && list.length === 0 && (
        <div className="card card-empty"><Empty art="frame" title="还没有作品" text="从一个故事创意开始。" action={<button className="btn btn-primary" onClick={() => setCreating(true)}><Plus size={16} />新建作品</button>} /></div>
      )}
      {list && list.length > 0 && (
        <div className="pgrid">
          {list.map(p => {
            const done = p.steps.filter(Boolean).length
            const next = p.steps.findIndex(x => !x)
            return (
              <article key={p.id} className="pcard2" onClick={() => go({ page: 'project', id: p.id })}>
                <div className="pcover">
                  {p.cover ? <img src={p.cover} alt="" loading="lazy" /> : <div className="pcover-empty"><Clapperboard size={28} strokeWidth={1.5} /></div>}
                  {p.final_url && <span className="pbadge"><Film size={12} />成片</span>}
                </div>
                <div className="pbody">
                  <h3 className="ptitle" title={p.title}>{p.title}</h3>
                  <p className="pidea" data-clamp>{p.idea || '还没有写创意'}</p>
                  <div className="psteps" aria-label={`完成 ${done} / 7`}>
                    {p.steps.map((d, i) => <i key={i} className={d ? 'is-done' : i === next ? 'is-next' : ''} title={stepNames[i]} />)}
                  </div>
                  <div className="pmeta">
                    <span>{next < 0 ? '已完成' : `下一步 · ${stepNames[next]}`}</span>
                    <span className="grow" />
                    <span>{fmtTime(p.updated_at)}</span>
                  </div>
                </div>
              </article>
            )
          })}
        </div>
      )}
      {creating && <CreateProject onClose={() => setCreating(false)} />}
    </div>
  )
}

function CreateProject({ onClose }: { onClose: () => void }) {
  const { go, toast } = useStore()
  const [title, setTitle] = useState('')
  const [idea, setIdea] = useState('')
  const [style, setStyle] = useState('')
  const [aspect, setAspect] = useState('9:16')
  const [busy, setBusy] = useState(false)
  const create = async () => {
    setBusy(true)
    try {
      const p = await api<{ id: number }>('/api/projects', { body: { title, idea, style, aspect } })
      go({ page: 'project', id: p.id, step: idea.trim() ? 2 : 1 })
    } catch (e: any) { toast(e.message, 'err'); setBusy(false) }
  }
  return (
    <Modal open onClose={onClose} title="新建作品"
      footer={<><button className="btn btn-secondary" onClick={onClose}>取消</button><button className="btn btn-primary" disabled={busy || !title.trim()} onClick={create}>{busy && <Spinner />}创建</button></>}>
      <label className="field"><span className="field-label">剧名</span><input className="input" autoFocus value={title} onChange={e => setTitle(e.target.value)} placeholder="例如：重生之我在天台等你" /></label>
      <label className="field"><span className="field-label">故事创意</span><textarea className="input" rows={4} value={idea} onChange={e => setIdea(e.target.value)} placeholder="一两句话：谁，想要什么，遇到了什么反转" /></label>
      <div className="field"><span className="field-label">画面风格</span>
        <input className="input" value={style} onChange={e => setStyle(e.target.value)} placeholder="自由描述，或点下面的常用风格" />
        <div className="chips">{styleChips.map(s => <button key={s} type="button" className={`chip ${style === s ? 'is-on' : ''}`} onClick={() => setStyle(s)}>{s}</button>)}</div>
      </div>
      <div className="field"><span className="field-label">画幅</span>
        <Segmented value={aspect} onChange={setAspect} options={[{ value: '9:16', label: '竖屏 9:16', glyph: '9:16' }, { value: '16:9', label: '横屏 16:9', glyph: '16:9' }, { value: '1:1', label: '方形 1:1', glyph: '1:1' }]} />
      </div>
    </Modal>
  )
}
