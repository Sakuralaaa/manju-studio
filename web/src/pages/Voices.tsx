import { useEffect, useState } from 'react'
import { AudioLines, Pencil, Search, Star, Trash2 } from 'lucide-react'
import { api, type LibVoice } from '../api'
import { useStore } from '../store'
import { Empty, Modal, Segmented, Spinner, TagInput } from '../components/ui'
import { PlayBtn } from '../components/audio'
import { PageHead } from './Projects'
import { fmtTime, voiceSourceNames } from '../labels'

// 音色库：跨作品收藏的音色。收藏入口在角色音色面板（设计候选、Fish 音色、当前音色）；用到角色上在音色面板「从音色库选」。
export default function Voices() {
  const { toast } = useStore()
  const [q, setQ] = useState('')
  const [fav, setFav] = useState(false)
  const [items, setItems] = useState<LibVoice[] | null>(null)
  const [edit, setEdit] = useState<LibVoice | null>(null)
  const [pv, setPv] = useState<Record<number, string>>({})
  const [pvBusy, setPvBusy] = useState(0)
  const load = async () => {
    try { const r = await api<{ items: LibVoice[] }>(`/api/voice/library?q=${encodeURIComponent(q)}${fav ? '&fav=1' : ''}`); setItems(r.items) } catch (e: any) { toast(e.message, 'err'); setItems([]) }
  }
  useEffect(() => { load() }, [fav])
  const star = async (l: LibVoice) => {
    try { await api(`/api/voice/library/${l.id}`, { method: 'PUT', body: { favorite: !l.favorite } }); load() } catch (e: any) { toast(e.message, 'err') }
  }
  const del = async (l: LibVoice) => {
    if (!confirm(`从音色库删除「${l.name}」？\n已经用到角色上的音色不受影响。`)) return
    try { await api(`/api/voice/library/${l.id}`, { method: 'DELETE' }); toast('已删除'); load() } catch (e: any) { toast(e.message, 'err') }
  }
  // 没有本地样本（Fish / 内置 / 渠道音色且没有试听）：合成一句试听
  const synth = async (l: LibVoice) => {
    if (pv[l.id]) { new Audio(pv[l.id]).play().catch(() => {}); return }
    setPvBusy(l.id)
    try {
      const r = await api<{ url: string }>('/api/voice/preview', { body: { voice: l.voice, text: l.voice.sample_text || '' } })
      setPv({ ...pv, [l.id]: r.url }); new Audio(r.url).play().catch(() => {})
    } catch (e: any) { toast(e.message, 'err') } finally { setPvBusy(0) }
  }
  return (
    <div className="page page-voices">
      <PageHead title="音色库" sub={items ? `${items.length} 个音色` : undefined} />
      <div className="toolbar">
        <Segmented block={false} size="sm" value={fav ? 'fav' : 'all'} onChange={x => setFav(x === 'fav')} options={[{ value: 'all', label: '全部' }, { value: 'fav', label: '星标' }]} />
        <div className="search"><Search size={14} /><input value={q} onChange={e => setQ(e.target.value)} onKeyDown={e => e.key === 'Enter' && load()} placeholder="搜索名称、描述、标签、性别、年龄" /></div>
      </div>
      {!items ? <div className="loading"><Spinner /></div> : items.length === 0 ? (
        <Empty art="stack" title={q || fav ? '没有找到' : '音色库还是空的'} text={q || fav ? undefined : '在角色的音色面板里，给设计候选、Fish 音色或已选的音色点星标「收藏到音色库」；之后任何作品的角色或旁白都能「从音色库选」。'} />
      ) : (
        <div className="libgrid">
          {items.map(l => {
            const src = l.sample_url || l.preview_url
            return (
              <article key={l.id} className="card libcard">
                <div className="libcard-head">
                  {src ? <PlayBtn src={src} size="md" /> : (
                    <button className="icon-btn" onClick={() => synth(l)} aria-label="合成试听" title="没有本地样本，合成一句试听">{pvBusy === l.id ? <Spinner size={13} /> : <AudioLines size={16} />}</button>
                  )}
                  <div className="libcard-title">
                    <b title={l.name}>{l.name}</b>
                    <span className="muted">{[voiceSourceNames[l.source] || l.source, l.gender, l.age].filter(Boolean).join(' · ')}</span>
                  </div>
                  <button className={`icon-btn sm ${l.favorite ? 'is-fav' : ''}`} onClick={() => star(l)} aria-label={l.favorite ? '取消星标' : '加星标'} title={l.favorite ? '取消星标' : '加星标'}><Star size={14} fill={l.favorite ? 'currentColor' : 'none'} /></button>
                  <button className="icon-btn sm" onClick={() => setEdit(l)} aria-label="编辑" title="改名 / 标签"><Pencil size={14} /></button>
                  <button className="icon-btn sm is-danger" onClick={() => del(l)} aria-label="删除" title="删除"><Trash2 size={14} /></button>
                </div>
                {l.description && <p className="libcard-desc" title={l.description}>{l.description}</p>}
                {l.tags.length > 0 && <div className="chips">{l.tags.map(t => <span key={t} className="tag-mini">{t}</span>)}</div>}
                {l.voice_refs.length > 0 && (
                  <div className="librefs">
                    <span className="muted">原声参考</span>
                    {l.voice_refs.map((r, i) => <span key={r.sample} className="libref"><PlayBtn src={l.ref_urls[i]} />{r.duration.toFixed(0)} 秒</span>)}
                  </div>
                )}
                <div className="libcard-foot muted">{l.origin ? `来自 ${l.origin} · ` : ''}{fmtTime(l.created_at)}</div>
              </article>
            )
          })}
        </div>
      )}
      {edit && <LibEdit item={edit} onClose={() => setEdit(null)} onSaved={() => { setEdit(null); load() }} />}
    </div>
  )
}

function LibEdit({ item, onClose, onSaved }: { item: LibVoice; onClose: () => void; onSaved: () => void }) {
  const { toast } = useStore()
  const [f, setF] = useState({ name: item.name, description: item.description, tags: item.tags, gender: item.gender, age: item.age })
  const [busy, setBusy] = useState(false)
  const save = async () => {
    setBusy(true)
    try { await api(`/api/voice/library/${item.id}`, { method: 'PUT', body: f }); toast('已保存'); onSaved() } catch (e: any) { toast(e.message, 'err') } finally { setBusy(false) }
  }
  return (
    <Modal open onClose={onClose} title="编辑音色"
      footer={<><button className="btn btn-secondary" onClick={onClose}>取消</button><button className="btn btn-primary" disabled={busy || !f.name.trim()} onClick={save}>{busy && <Spinner />}保存</button></>}>
      <label className="field"><span className="field-label">名称</span><input className="input" autoFocus value={f.name} onChange={e => setF({ ...f, name: e.target.value })} /></label>
      <label className="field"><span className="field-label">描述</span><textarea className="input" rows={3} value={f.description} onChange={e => setF({ ...f, description: e.target.value })} placeholder="音色质感、语速、适合什么角色" /></label>
      <div className="form-grid">
        <label className="field"><span className="field-label">性别</span><input className="input" value={f.gender} onChange={e => setF({ ...f, gender: e.target.value })} placeholder="男 / 女" /></label>
        <label className="field"><span className="field-label">年龄感</span><input className="input" value={f.age} onChange={e => setF({ ...f, age: e.target.value })} placeholder="少年、青年、中年" /></label>
      </div>
      <div className="field"><span className="field-label">标签</span><TagInput value={f.tags} onChange={tags => setF({ ...f, tags })} placeholder="输入后回车，如 清冷、反派" /></div>
    </Modal>
  )
}
