import { useState } from 'react'
import { ArrowRight, Pencil, Sparkles, Link2, Plus, X } from 'lucide-react'
import { api, type Asset, type Shot } from '../../api'
import { useProject, StepHead, Cost } from '../Project'
import { useStore } from '../../store'
import { Empty, Spinner } from '../../components/ui'
import { Picker, UploadBtn } from './media'
import { ImageLibrary, ModelSelect } from '../../components/imgtools'
import { ShotEditor } from './Storyboard'

export default function Keyframes() {
  const { data, task, run, goStep, patch } = useProject()
  const { channels } = useStore()
  const p = data.project, e = data.episode
  const [edit, setEdit] = useState<Shot | null>(null)
  const [lib, setLib] = useState<Shot | null>(null)
  const setRefs = (s: Shot, ids: number[]) => run(() => api(`/api/shots/${s.id}/refs`, { method: 'PUT', body: { ref_image_ids: ids } }))
  const ich = channels.find(c => c.id === p.image_channel_id && c.kind === 'image') || channels.find(c => c.kind === 'image')
  const refMax = ich && ich.caps.refs.mode !== 'off' ? (ich.caps.refs.max || 4) : 0
  const missing = data.shots.filter(s => s.images.length === 0 && !task('image', 'shot', s.id)).length
  const byCode = Object.fromEntries(data.assets.map(a => [a.code, a]))
  return (
    <section className="stepview">
      <StepHead title="关键帧" note={ich ? `参考图上限 ${refMax} 张` : undefined}>
        <ModelSelect size="md" value={{ channel_id: p.image_channel_id, model: p.image_model }} onChange={cm => patch({ image_channel_id: cm.channel_id, image_model: cm.model })} />
        <button className="btn btn-secondary" disabled={missing === 0} onClick={() => run(() => api(`/api/episodes/${e.id}/keyframes/generate-all`, { method: 'POST' }), `开始生成 ${missing} 张`)}>
          <Sparkles size={16} />全部生成<Cost>{missing} 张</Cost>
        </button>
        <button className="btn btn-primary" disabled={!data.steps.done[4]} onClick={() => goStep(6)}>下一步：视频<ArrowRight size={16} /></button>
      </StepHead>
      {data.shots.length === 0 && <div className="card card-empty"><Empty art="frame" title="还没有分镜" /></div>}
      <div className={`kgrid kgrid-${p.aspect.replace(':', '-')}`}>
        {data.shots.map(s => {
          const busy = !!task('image', 'shot', s.id)
          const refs = planRefs(s, byCode, refMax)
          return (
            <article key={s.id} className="card kcard">
              <div className="kcard-head">
                <span className="code">{s.code}</span>
                <span className="kcard-cam" title={s.camera}>{s.camera || '镜头'}</span>
                <span className="grow" />
                <button className="icon-btn sm" onClick={() => setEdit(s)} aria-label="编辑镜头" title="编辑镜头"><Pencil size={14} /></button>
              </div>
              <Picker images={s.images} picked={s.picked_image_id} aspect={p.aspect} busy={busy} empty={<span>未生成</span>} />
              <div className="krefs">
                {refs.map((r, i) => (
                  <span key={r.key} className={`kref ${i >= refMax ? 'is-cut' : ''} ${r.chain ? 'is-chain' : ''} ${r.manual ? 'is-manual' : ''}`} title={i >= refMax ? `${r.title}（超出渠道上限，这张不会发）` : r.title}>
                    {r.chain ? <i className="kref-ic"><Link2 size={12} /></i> : r.url && <img src={r.url} alt="" />}<b>{r.label}</b>
                    {r.manual && <button className="kref-x" aria-label="移除参考图" onClick={() => setRefs(s, (s.ref_images || []).filter(x => x.id !== r.manual).map(x => x.id))}><X size={11} /></button>}
                  </span>
                ))}
                {(s.ref_images || []).length < 4 && <button className="kref kref-add" onClick={() => setLib(s)} title="给这个镜头加参考图（构图、姿势、氛围、道具）"><Plus size={13} />参考</button>}
              </div>
              <div className="acard-foot">
                <button className="btn btn-secondary btn-sm" disabled={busy} onClick={() => run(() => api(`/api/shots/${s.id}/keyframe`, { body: { n: 1 } }))}>
                  {busy ? <Spinner size={13} /> : <Sparkles size={14} />}{s.images.length ? '再来一张' : '生成'}<Cost>1 张</Cost>
                </button>
                <span className="grow" />
                <UploadBtn path={`/api/shots/${s.id}/keyframe/upload`} label="" className="icon-btn sm" />
              </div>
            </article>
          )
        })}
      </div>
      {edit && <ShotEditor shot={edit} onClose={() => setEdit(null)} />}
      <ImageLibrary open={!!lib} multi max={4 - (lib?.ref_images || []).length} title={`${lib?.code || ''} 的参考图`} onClose={() => setLib(null)}
        onPick={ims => { if (lib) { setRefs(lib, [...(lib.ref_images || []).map(x => x.id), ...ims.map(x => x.id)]); setLib(null) } }} />
    </section>
  )
}

// 与服务端一致的参考图顺序：本镜头最重要的角色（有台词、全剧出场多）用三视图，
// 衔接时上一镜头最后一帧紧随其后，然后其余角色、场景、道具
function planRefs(s: Shot, byCode: Record<string, Asset>, limit: number) {
  const list = s.assets.map((c, i) => ({ a: byCode[c], i })).filter(x => x.a && x.a.picked_image_id)
  const score = (a: Asset, i: number) => a.appear - i * 0.01 + (s.dialogue.includes(a.name) ? 100 : 0) + (s.action.includes(a.name) || s.keyframe_prompt.includes(a.name) ? 10 : 0)
  const chars = list.filter(x => x.a.kind === 'character').sort((x, y) => score(y.a, y.i) - score(x.a, x.i))
  const others = list.filter(x => x.a.kind !== 'character').sort((x, y) => (y.a.kind === 'scene' ? 1000 : 0) + y.a.appear - (x.a.kind === 'scene' ? 1000 : 0) - x.a.appear)
  const out: { key: string; label: string; url?: string; title: string; chain?: boolean; manual?: number }[] = []
  const manual = () => (s.ref_images || []).forEach((im, k) => out.push({ key: `m${im.id}`, label: `参考${k + 1}`, url: im.url, title: '本镜头指定的参考图（构图、姿势、氛围），人物的脸仍按角色', manual: im.id }))
  if (chars.length === 0) manual()
  const img = (a: Asset, id: number) => a.images.find(im => im.id === id)?.url
  chars.forEach((x, k) => {
    const sheet = x.a.sheet_image_id && (k === 0 || limit >= list.length + 1)
    out.push({ key: x.a.code, label: x.a.code, url: img(x.a, sheet ? x.a.sheet_image_id : x.a.picked_image_id), title: `${x.a.name} · ${sheet ? '三视图' : '设定图'}` })
    if (k === 0 && s.chain === 'ref') out.push({ key: 'chain', label: '上一镜', chain: true, title: '上一镜头最后一帧' })
    if (k === 0) manual()
  })
  if (chars.length === 0 && s.chain === 'ref') out.push({ key: 'chain', label: '上一镜', chain: true, title: '上一镜头最后一帧' })
  others.forEach(x => out.push({ key: x.a.code, label: x.a.code, url: img(x.a, x.a.picked_image_id), title: x.a.name }))
  return out
}
