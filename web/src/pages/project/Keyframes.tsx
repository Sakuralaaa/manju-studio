import { useState } from 'react'
import { ArrowRight, Pencil, Sparkles } from 'lucide-react'
import { api, type Shot } from '../../api'
import { useProject, StepHead, Cost } from '../Project'
import { useStore } from '../../store'
import { Empty, Spinner } from '../../components/ui'
import { Picker, UploadBtn } from './media'
import { ShotEditor } from './Storyboard'

export default function Keyframes() {
  const { data, task, run, goStep } = useProject()
  const { channels } = useStore()
  const p = data.project
  const [edit, setEdit] = useState<Shot | null>(null)
  const ich = channels.find(c => c.id === p.image_channel_id && c.kind === 'image') || channels.find(c => c.kind === 'image')
  const refMax = ich && ich.caps.refs.mode !== 'off' ? (ich.caps.refs.max || 4) : 0
  const missing = data.shots.filter(s => s.images.length === 0 && !task('image', 'shot', s.id)).length
  const byCode = Object.fromEntries(data.assets.map(a => [a.code, a]))
  return (
    <section className="stepview">
      <StepHead title="关键帧" note={ich ? `${ich.name} · 参考图上限 ${refMax} 张` : undefined}>
        <button className="btn btn-secondary" disabled={missing === 0} onClick={() => run(() => api(`/api/projects/${p.id}/keyframes/generate-all`, { method: 'POST' }), `开始生成 ${missing} 张`)}>
          <Sparkles size={16} />全部生成<Cost>{missing} 张</Cost>
        </button>
        <button className="btn btn-primary" disabled={!data.steps.done[4]} onClick={() => goStep(6)}>下一步：视频<ArrowRight size={16} /></button>
      </StepHead>
      {data.shots.length === 0 && <div className="card card-empty"><Empty art="frame" title="还没有分镜" text="先在「分镜」里拆好镜头。" /></div>}
      <div className={`kgrid kgrid-${p.aspect.replace(':', '-')}`}>
        {data.shots.map(s => {
          const busy = !!task('image', 'shot', s.id)
          // 参考图：角色 → 场景 → 道具，按渠道上限截断
          const order = ['character', 'scene', 'prop']
          const refs = s.assets.map(c => byCode[c]).filter(a => a && a.picked_image_id).sort((a, b) => order.indexOf(a.kind) - order.indexOf(b.kind))
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
                {refs.length === 0 && <span className="muted">无参考图</span>}
                {refs.map((a, i) => {
                  const im = a.images.find(x => x.id === a.picked_image_id)
                  return <span key={a.code} className={`kref ${i >= refMax ? 'is-cut' : ''}`} title={`${a.code} ${a.name}${i >= refMax ? '（超出上限，改用文字描述）' : ''}`}>{im && <img src={im.url} alt="" />}<b>{a.code}</b></span>
                })}
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
    </section>
  )
}
