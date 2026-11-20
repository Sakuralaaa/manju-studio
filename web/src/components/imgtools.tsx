import { useEffect, useRef, useState } from 'react'
import { createPortal } from 'react-dom'
import { Check, Copy, Download, ImagePlus, Trash2, X } from 'lucide-react'
import { api, upload, type Asset, type Img, type RefLibItem, type StudioList } from '../api'
import { useStore } from '../store'
import { Modal, Segmented, Select, Spinner, copyText } from './ui'
import { kindLabels, tagNames } from '../labels'

// ---------- 渠道 · 模型 ----------
export interface ChModel { channel_id: number; model: string }

export function useImageChannels() {
  const { channels } = useStore()
  return channels.filter(c => c.kind === 'image')
}

export function refMaxOf(ch?: { caps: { refs: { mode: string; max?: number } } }) {
  if (!ch || ch.caps.refs.mode === 'off') return 0
  return ch.caps.refs.max || 4
}

/** 一个下拉同时选渠道和模型：「渠道 · 模型」 */
export function ModelSelect({ value, onChange, size = 'sm', className = '' }: { value: ChModel; onChange: (v: ChModel) => void; size?: 'sm' | 'md'; className?: string }) {
  const chs = useImageChannels()
  const ch = chs.find(c => c.id === value.channel_id) || chs[0]
  const model = ch && value.model && ch.models.includes(value.model) ? value.model : ch?.models[0] || ''
  const opts = chs.flatMap(c => (c.models.length ? c.models : ['']).map(m => ({ value: `${c.id}|${m}`, label: m ? `${c.name} · ${m}` : c.name, hint: refMaxOf(c) ? `参考图 ${refMaxOf(c)}` : '无参考图' })))
  return <Select size={size} className={`model-sel ${className}`} value={ch ? `${ch.id}|${model}` : ''} options={opts} placeholder="还没有生图渠道" disabled={!chs.length}
    onChange={v => { const [id, m] = v.split('|'); onChange({ channel_id: Number(id), model: m }) }} />
}

// ---------- 选图：画室 + 全部作品的资产 ----------
export function ImageLibrary({ open, onClose, onPick, multi = false, max = 8, title = '选择图片' }: {
  open: boolean; onClose: () => void; onPick: (imgs: Img[]) => void; multi?: boolean; max?: number; title?: string
}) {
  const [tab, setTab] = useState<'assets' | 'studio'>('assets')
  const [lib, setLib] = useState<RefLibItem[] | null>(null)
  const [studio, setStudio] = useState<Img[] | null>(null)
  const [sel, setSel] = useState<Img[]>([])
  const [proj, setProj] = useState<number>(0)
  useEffect(() => {
    if (!open) return
    setSel([])
    api<RefLibItem[]>('/api/reflib').then(x => { setLib(x); if (x[0]) setProj(p => p || x[0].project_id) }).catch(() => setLib([]))
    api<StudioList>('/api/studio').then(x => setStudio(x.images)).catch(() => setStudio([]))
  }, [open])
  const toggle = (im: Img) => {
    if (!multi) { onPick([im]); onClose(); return }
    setSel(s => s.some(x => x.id === im.id) ? s.filter(x => x.id !== im.id) : s.length >= max ? s : [...s, im])
  }
  const cur = lib?.find(x => x.project_id === proj)
  const Tile = ({ im, label }: { im: Img; label?: string }) => (
    <button className={`libtile ${sel.some(x => x.id === im.id) ? 'is-on' : ''}`} onClick={() => toggle(im)} title={label}>
      <img src={im.url} alt="" loading="lazy" />
      {label && <span className="libtile-label">{label}</span>}
      <i className="libtile-check"><Check size={12} /></i>
    </button>
  )
  return (
    <Modal open={open} onClose={onClose} title={title} wide className="libmodal"
      footer={multi ? <><span className="muted">已选 {sel.length}{max ? ` / ${max}` : ''}</span><span className="grow" /><button className="btn btn-secondary" onClick={onClose}>取消</button><button className="btn btn-primary" disabled={!sel.length} onClick={() => { onPick(sel); onClose() }}>确定</button></> : undefined}>
      <Segmented value={tab} onChange={v => setTab(v as any)} options={[{ value: 'assets', label: '作品资产' }, { value: 'studio', label: '画室' }]} />
      {tab === 'assets' && (
        !lib ? <div className="loading"><Spinner /></div> : lib.length === 0 ? <div className="muted lib-empty">还没有带图的资产</div> : <>
          {lib.length > 1 && <Select value={String(proj)} options={lib.map(x => ({ value: String(x.project_id), label: x.project || '未命名作品' }))} onChange={v => setProj(Number(v))} />}
          {cur?.assets.map(a => <AssetRow key={a.id} a={a} Tile={Tile} />)}
        </>
      )}
      {tab === 'studio' && (
        !studio ? <div className="loading"><Spinner /></div> : studio.length === 0 ? <div className="muted lib-empty">画室还是空的</div> :
          <div className="libgrid">{studio.map(im => <Tile key={im.id} im={im} />)}</div>
      )}
    </Modal>
  )
}

function AssetRow({ a, Tile }: { a: Asset; Tile: (p: { im: Img; label?: string }) => JSX.Element }) {
  const ims = [...a.images].sort((x, y) => order(x, a) - order(y, a))
  return (
    <div className="librow">
      <div className="librow-head"><span className="code">{a.code}</span><b>{a.name}</b><span className="muted">{kindLabels[a.kind]}</span></div>
      <div className="libgrid">{ims.map(im => <Tile key={im.id} im={im} label={(isPicked(im, a) ? '✓ ' : '') + tagNames[im.tag || '']} />)}</div>
    </div>
  )
}
const isPicked = (im: Img, a: Asset) => im.id === a.picked_image_id || im.id === a.sheet_image_id || im.id === a.expr_image_id
const order = (im: Img, a: Asset) => (isPicked(im, a) ? 0 : 10) + ['', 'sheet', 'expr', 'ref'].indexOf(im.tag || '')

// ---------- 存进资产 ----------
export function AttachModal({ img, onClose, onDone }: { img: Img; onClose: () => void; onDone?: () => void }) {
  const { toast } = useStore()
  const [lib, setLib] = useState<{ id: number; title: string; assets: Asset[] }[] | null>(null)
  const [proj, setProj] = useState(0)
  const [asset, setAsset] = useState(0)
  const [tag, setTag] = useState<'' | 'sheet' | 'expr' | 'ref'>('ref')
  const [busy, setBusy] = useState(false)
  useEffect(() => {
    api<{ id: number; title: string }[]>('/api/projects').then(async ps => {
      const full = await Promise.all(ps.map(p => api<{ assets: Asset[] }>(`/api/projects/${p.id}`).then(d => ({ id: p.id, title: p.title, assets: d.assets })).catch(() => ({ id: p.id, title: p.title, assets: [] as Asset[] }))))
      setLib(full)
      if (full[0]) setProj(full[0].id)
    }).catch(() => setLib([]))
  }, [])
  const cur = lib?.find(x => x.id === proj)
  const a = cur?.assets.find(x => x.id === asset)
  const tags = a?.kind === 'character' ? (['ref', '', 'sheet', 'expr'] as const) : (['ref', ''] as const)
  const save = async () => {
    setBusy(true)
    try { await api(`/api/images/${img.id}/copy`, { body: { asset_id: asset, tag } }); toast(`已存到「${a?.name}」的${tagNames[tag]}`); onDone?.(); onClose() } catch (e: any) { toast(e.message, 'err') } finally { setBusy(false) }
  }
  return (
    <Modal open onClose={onClose} title="存到作品资产"
      footer={<><span className="grow" /><button className="btn btn-secondary" onClick={onClose}>取消</button><button className="btn btn-primary" disabled={!asset || busy} onClick={save}>{busy ? <Spinner /> : '保存'}</button></>}>
      {!lib ? <div className="loading"><Spinner /></div> : lib.length === 0 ? <div className="muted">还没有作品</div> : <>
        <div className="field"><span className="field-label">作品</span>
          <Select value={String(proj)} options={lib.map(x => ({ value: String(x.id), label: x.title || '未命名作品' }))} onChange={v => { setProj(Number(v)); setAsset(0) }} /></div>
        <div className="field"><span className="field-label">资产</span>
          <Select value={asset ? String(asset) : ''} placeholder={cur?.assets.length ? '选择角色、场景或道具' : '这个作品还没有资产'} disabled={!cur?.assets.length}
            options={(cur?.assets || []).map(x => ({ value: String(x.id), label: `${x.code} ${x.name}`, hint: kindLabels[x.kind] }))} onChange={v => { setAsset(Number(v)); setTag('ref') }} /></div>
        {a && <div className="field"><span className="field-label">存为</span>
          <Segmented value={tag} onChange={v => setTag(v as any)} options={tags.map(t => ({ value: t, label: tagNames[t] }))} />
          <span className="field-hint">{tag === 'ref' ? (a.kind === 'character' ? '参考图：以后给它出图时用来锁脸，不会替换现有的图' : '参考图：以后给它出图时参考外形和风格') : `直接作为${tagNames[tag]}并选用`}</span>
        </div>}
      </>}
    </Modal>
  )
}

// ---------- 通用看图 ----------
export function ImgViewer({ img, onClose, actions }: { img: Img; onClose: () => void; actions?: React.ReactNode }) {
  const { toast } = useStore()
  useEffect(() => {
    const esc = (e: KeyboardEvent) => e.key === 'Escape' && onClose()
    window.addEventListener('keydown', esc)
    return () => window.removeEventListener('keydown', esc)
  }, [onClose])
  return createPortal(
    <div className="viewer" onMouseDown={e => e.target === e.currentTarget && onClose()}>
      <button className="viewer-close" onClick={onClose} aria-label="关闭"><X size={18} /></button>
      <img className="viewer-img" src={img.url} alt="" />
      {img.prompt && <div className="viewer-prompt" title={img.prompt}>{img.prompt}</div>}
      <div className="viewer-bar">
        <span className="viewer-meta">{img.width}×{img.height} · {img.source === 'upload' ? '上传' : img.model || '生成'}{img.owner_type === 'ext' ? ' · 智能体' : ''}</span>
        <span className="grow" />
        {img.prompt && <button className="icon-btn sm" title="复制提示词" aria-label="复制提示词" onClick={async () => { await copyText(img.prompt); toast('已复制提示词') }}><Copy size={15} /></button>}
        <a className="icon-btn sm" href={img.url} download title="下载" aria-label="下载"><Download size={15} /></a>
        {actions}
      </div>
    </div>, document.body)
}

// ---------- 上传后拿到图片 ----------
export function UploadPick({ path, onDone, label = '上传', className = 'btn btn-secondary btn-sm' }: { path: string; onDone: (im: Img) => void; label?: string; className?: string }) {
  const { toast } = useStore()
  const ref = useRef<HTMLInputElement>(null)
  const [busy, setBusy] = useState(false)
  return (
    <>
      <button className={className} disabled={busy} onClick={() => ref.current?.click()} title="上传图片">{busy ? <Spinner size={13} /> : <ImagePlus size={14} />}{label}</button>
      <input ref={ref} type="file" accept="image/png,image/jpeg,image/webp" hidden onChange={async e => {
        const f = e.target.files?.[0]
        e.target.value = ''
        if (!f) return
        setBusy(true)
        try { onDone(await upload<Img>(path, f)) } catch (er: any) { toast(er.message, 'err') } finally { setBusy(false) }
      }} />
    </>
  )
}

export function DelBtn({ onDel }: { onDel: () => void }) {
  return <button className="btn btn-ghost btn-sm is-danger" onClick={() => confirm('删除这张图？') && onDel()}><Trash2 size={14} />删除</button>
}
