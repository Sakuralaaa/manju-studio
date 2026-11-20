import { useCallback, useEffect, useState } from 'react'
import { FolderInput, Images, Plus, RotateCcw, Sparkles, Trash2, X } from 'lucide-react'
import { api, type Img, type StudioList, type Task } from '../api'
import { useStore } from '../store'
import { Empty, Segmented, Spinner, Stepper, useInterval } from '../components/ui'
import { AttachModal, ImageLibrary, ImgViewer, ModelSelect, UploadPick, refMaxOf, useImageChannels, type ChModel } from '../components/imgtools'
import { PageHead } from './Projects'
import { useTaskToasts } from './Project'

const aspects = ['9:16', '3:4', '1:1', '4:3', '16:9']
const DRAFT = 'studio-draft'

interface Draft { prompt: string; aspect: string; n: number; cm: ChModel; realistic: boolean; refs: Img[] }

function loadDraft(): Draft {
  try { const d = JSON.parse(localStorage.getItem(DRAFT) || ''); if (d && typeof d.prompt === 'string') return { refs: [], ...d } } catch { /* 无草稿 */ }
  return { prompt: '', aspect: '3:4', n: 1, cm: { channel_id: 0, model: '' }, realistic: false, refs: [] }
}

export default function Studio() {
  const { toast } = useStore()
  const chs = useImageChannels()
  const [d, setD] = useState<Draft>(loadDraft)
  const [list, setList] = useState<Img[] | null>(null)
  const [tasks, setTasks] = useState<Task[]>([])
  const [more, setMore] = useState(false)
  const [view, setView] = useState<Img | null>(null)
  const [attach, setAttach] = useState<Img | null>(null)
  const [lib, setLib] = useState(false)
  const [sending, setSending] = useState(false)
  const toasts = useTaskToasts()
  useEffect(() => { localStorage.setItem(DRAFT, JSON.stringify(d)) }, [d])

  const load = useCallback(async () => {
    try {
      const x = await api<StudioList>('/api/studio')
      toasts(x.tasks)
      setTasks(x.tasks.filter(t => t.status === 'running'))
      setList(cur => {
        // 保留已加载的更早的图
        if (!cur) return x.images
        const ids = new Set(x.images.map(i => i.id))
        const minNew = x.images.length ? x.images[x.images.length - 1].id : Infinity
        return [...x.images, ...cur.filter(i => !ids.has(i.id) && i.id < minNew)]
      })
      setMore(m => m || x.more)
    } catch (e: any) { toast(e.message, 'err') }
  }, [])
  useEffect(() => { load() }, [load])
  useInterval(load, tasks.length ? 2500 : 20000)

  const ch = chs.find(c => c.id === d.cm.channel_id) || chs[0]
  const refMax = refMaxOf(ch)
  const over = d.refs.length > refMax
  const set = (x: Partial<Draft>) => setD(v => ({ ...v, ...x }))
  const addRefs = (ims: Img[]) => set({ refs: [...d.refs, ...ims.filter(i => !d.refs.some(r => r.id === i.id))].slice(0, 8) })

  const generate = async () => {
    setSending(true)
    try {
      await api('/api/studio/generate', { body: { prompt: d.prompt, aspect: d.aspect, n: d.n, channel_id: ch?.id || 0, model: d.cm.model, realistic: d.realistic, ref_image_ids: d.refs.map(r => r.id) } })
      await load()
    } catch (e: any) { toast(e.message, 'err') } finally { setSending(false) }
  }
  const loadMore = async () => {
    if (!list?.length) return
    const x = await api<StudioList>(`/api/studio?before=${list[list.length - 1].id}`)
    setList([...list, ...x.images]); setMore(x.more)
  }
  const del = async (im: Img) => {
    await api(`/api/images/${im.id}`, { method: 'DELETE' })
    setList(l => l?.filter(x => x.id !== im.id) || l)
    set({ refs: d.refs.filter(r => r.id !== im.id) })
    setView(null)
  }
  const reuse = (im: Img) => { set({ prompt: im.prompt }); setView(null); window.scrollTo({ top: 0, behavior: 'smooth' }) }

  const pending = tasks.length
  return (
    <div className="page page-studio">
      <PageHead title="画室" sub="不挂作品的自由出图；智能体生成的图也在这里" />
      <div className="card studio-compose">
        <textarea className="input studio-prompt" rows={4} value={d.prompt} placeholder="描述你要的画面。带参考图时写清每张图参考什么，比如：参考图1是同一个人，脸型五官保持一致，改成 2006 年的造型……"
          onChange={e => set({ prompt: e.target.value })} onKeyDown={e => { if ((e.metaKey || e.ctrlKey) && e.key === 'Enter' && d.prompt.trim()) generate() }} />
        <div className="studio-refs">
          {d.refs.map((r, i) => (
            <span key={r.id} className={`studio-ref ${i >= refMax ? 'is-cut' : ''}`} title={`参考图${i + 1}`}>
              <img src={r.url} alt="" /><b>{i + 1}</b>
              <button onClick={() => set({ refs: d.refs.filter(x => x.id !== r.id) })} aria-label="移除"><X size={12} /></button>
            </span>
          ))}
          <button className="btn btn-ghost btn-sm" onClick={() => setLib(true)}><Images size={14} />选参考图</button>
          <UploadPick path="/api/studio/upload" label="上传参考图" className="btn btn-ghost btn-sm" onDone={im => { addRefs([im]); load() }} />
          <span className="muted studio-refnote">{refMax ? `这个模型最多 ${refMax} 张参考图` : '这个渠道不支持参考图'}</span>
        </div>
        <div className="studio-opts">
          <Segmented block={false} value={d.aspect} onChange={v => set({ aspect: v })} options={aspects.map(a => ({ value: a, label: a }))} />
          <Stepper value={d.n} min={1} max={4} onChange={n => set({ n })} />
          <ModelSelect size="md" value={{ channel_id: ch?.id || 0, model: d.cm.model }} onChange={cm => set({ cm })} />
          <label className="chk"><input type="checkbox" checked={d.realistic} onChange={e => set({ realistic: e.target.checked })} />真人写实</label>
          <span className="grow" />
          <button className="btn btn-primary" disabled={!d.prompt.trim() || sending || !ch || over} onClick={generate} title={over ? '参考图超过上限' : 'Ctrl / ⌘ + 回车'}>
            {sending ? <Spinner /> : <Sparkles size={16} />}生成 {d.n} 张
          </button>
        </div>
      </div>

      {list === null ? <div className="loading"><Spinner size={20} /></div> : list.length === 0 && !pending ? (
        <div className="card card-empty"><Empty art="frame" title="画室还是空的" text="在上面写提示词生成，或者让智能体用 image_generate 出图" /></div>
      ) : (
        <div className="studio-grid">
          {Array.from({ length: pending }).map((_, i) => <div key={'p' + i} className="studio-tile is-busy"><Spinner size={18} /><span>生成中</span></div>)}
          {list.map(im => (
            <button key={im.id} className="studio-tile" onClick={() => setView(im)} title={im.prompt}>
              <img src={im.url} alt="" loading="lazy" />
              {im.owner_type === 'ext' && <span className="studio-badge">智能体</span>}
              {im.source === 'upload' && <span className="studio-badge">上传</span>}
            </button>
          ))}
        </div>
      )}
      {more && <div className="studio-more"><button className="btn btn-ghost" onClick={loadMore}>加载更早的图</button></div>}

      {view && <ImgViewer img={view} onClose={() => setView(null)} actions={<>
        <button className="btn btn-secondary btn-sm" onClick={() => { addRefs([view]); setView(null); toast('已加为参考图') }}><Plus size={14} />参考图</button>
        {view.prompt && <button className="btn btn-secondary btn-sm" onClick={() => reuse(view)}><RotateCcw size={14} />用这条提示词</button>}
        <button className="btn btn-secondary btn-sm" onClick={() => setAttach(view)}><FolderInput size={14} />存到资产</button>
        <button className="icon-btn sm is-danger" title="删除" aria-label="删除" onClick={() => confirm('删除这张图？') && del(view)}><Trash2 size={15} /></button>
      </>} />}
      {attach && <AttachModal img={attach} onClose={() => setAttach(null)} />}
      <ImageLibrary open={lib} onClose={() => setLib(false)} multi max={Math.max(1, 8 - d.refs.length)} title="选参考图" onPick={addRefs} />
    </div>
  )
}
