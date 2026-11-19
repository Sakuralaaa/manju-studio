import { useRef, useState } from 'react'
import { Check, ImagePlus, Trash2, X } from 'lucide-react'
import { createPortal } from 'react-dom'
import { api, upload, type Img } from '../../api'
import { useProject } from '../Project'
import { Spinner } from '../../components/ui'

export function arClass(aspect: string) { return 'ar-' + aspect.replace(':', '-') }

// 选定图 + 候选条
export function Picker({ images, picked, aspect, busy, empty }: { images: Img[]; picked: number; aspect: string; busy: boolean; empty: React.ReactNode }) {
  const { run } = useProject()
  const [view, setView] = useState<Img | null>(null)
  const cur = images.find(i => i.id === picked) || images[images.length - 1]
  return (
    <>
      <div className={`frame ${arClass(aspect)} ${busy ? 'is-busy' : ''}`}>
        {cur ? <img src={cur.url} alt="" loading="lazy" onClick={() => setView(cur)} /> : <div className="frame-empty">{empty}</div>}
        {busy && <div className="frame-busy"><Spinner size={18} /><span>生成中</span></div>}
      </div>
      <div className="cands" data-count={images.length}>
        {images.map(im => (
          <button key={im.id} className={`cand ${im.id === picked ? 'is-on' : ''}`} onClick={() => im.id === picked ? setView(im) : run(() => api(`/api/images/${im.id}/pick`, { method: 'POST' }))} title={im.id === picked ? '查看' : '选用这张'}>
            <img src={im.url} alt="" loading="lazy" />
          </button>
        ))}
        {images.length === 0 && <span className="cands-empty">暂无候选</span>}
      </div>
      {view && <Viewer img={view} picked={view.id === picked} onClose={() => setView(null)} />}
    </>
  )
}

export function Viewer({ img, picked, onClose }: { img: Img; picked: boolean; onClose: () => void }) {
  const { run } = useProject()
  return createPortal(
    <div className="viewer" onMouseDown={e => e.target === e.currentTarget && onClose()}>
      <button className="viewer-close" onClick={onClose} aria-label="关闭"><X size={18} /></button>
      <img className="viewer-img" src={img.url} alt="" />
      <div className="viewer-bar">
        <span className="viewer-meta">{img.width}×{img.height} · {img.source === 'upload' ? '上传' : img.model || '生成'}</span>
        <span className="grow" />
        {!picked && <button className="btn btn-primary btn-sm" onClick={async () => { await run(() => api(`/api/images/${img.id}/pick`, { method: 'POST' }), '已选用'); onClose() }}><Check size={14} />选用</button>}
        <button className="btn btn-secondary btn-sm" onClick={async () => { if (confirm('删除这张图？')) { await run(() => api(`/api/images/${img.id}`, { method: 'DELETE' })); onClose() } }}><Trash2 size={14} />删除</button>
      </div>
    </div>, document.body)
}

export function UploadBtn({ path, label = '上传', className = 'btn btn-secondary btn-sm' }: { path: string; label?: string; className?: string }) {
  const { run } = useProject()
  const ref = useRef<HTMLInputElement>(null)
  return (
    <>
      <button className={className} onClick={() => ref.current?.click()} title="上传图片" aria-label="上传图片"><ImagePlus size={label ? 14 : 16} />{label}</button>
      <input ref={ref} type="file" accept="image/png,image/jpeg,image/webp" hidden onChange={e => {
        const f = e.target.files?.[0]
        if (f) run(() => upload(path, f), '已上传')
        e.target.value = ''
      }} />
    </>
  )
}
