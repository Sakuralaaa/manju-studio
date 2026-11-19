import { useState } from 'react'
import { ArrowDown, ArrowRight, ArrowUp, Pencil, Plus, Trash2, Wand2 } from 'lucide-react'
import { api, type Shot } from '../../api'
import { useProject, StepHead, Cost } from '../Project'
import { Empty, Modal, Spinner } from '../../components/ui'
import { fmtSec } from '../../labels'

export default function Storyboard() {
  const { data, task, run, goStep } = useProject()
  const p = data.project
  const [edit, setEdit] = useState<Shot | null>(null)
  const splitting = task('storyboard', 'project', p.id)
  const total = data.shots.reduce((s, x) => s + x.duration, 0)
  const split = () => {
    if (data.shots.length && !confirm('重新拆分会替换现有分镜（已生成的关键帧和视频不再关联），继续？')) return
    run(() => api(`/api/projects/${p.id}/shots/split`, { method: 'POST' }))
  }
  const move = (i: number, d: number) => {
    const ids = data.shots.map(s => s.id)
    const j = i + d
    if (j < 0 || j >= ids.length) return
    ;[ids[i], ids[j]] = [ids[j], ids[i]]
    run(() => api(`/api/projects/${p.id}/shots/order`, { body: ids }))
  }
  const byCode = Object.fromEntries(data.assets.map(a => [a.code, a]))
  return (
    <section className="stepview">
      <StepHead title="分镜" note={data.shots.length ? `${data.shots.length} 镜 · ${fmtSec(total)}` : undefined}>
        <button className="btn btn-secondary" disabled={!!splitting || !p.script.trim()} onClick={split}>
          {splitting ? <Spinner /> : <Wand2 size={16} />}{data.shots.length ? '重新拆分' : 'AI 拆分镜'}<Cost>1 次对话</Cost>
        </button>
        <button className="btn btn-secondary" onClick={() => run(() => api(`/api/projects/${p.id}/shots`, { body: {} }))}><Plus size={16} />添加镜头</button>
        <button className="btn btn-primary" disabled={!data.shots.length} onClick={() => goStep(5)}>下一步：关键帧<ArrowRight size={16} /></button>
      </StepHead>
      {data.shots.length === 0 && (
        <div className="card card-empty">
          {splitting ? <div className="loading"><Spinner size={18} /><span>正在拆分镜头</span></div> :
            <Empty art="book" title="还没有分镜" text="把剧本拆成 5–10 秒一个的镜头，每个镜头标好要用到的资产。" />}
        </div>
      )}
      <div className={`shots ${splitting ? 'is-dim' : ''}`}>
        {data.shots.map((s, i) => (
          <article key={s.id} className="card shot">
            <div className="shot-no">
              <b>{s.code}</b>
              <span>{s.duration} 秒</span>
            </div>
            <div className="shot-body">
              <div className="shot-cam">{s.camera || <span className="muted">未写镜头语言</span>}</div>
              <p className="shot-action">{s.action || <span className="muted">未写画面</span>}</p>
              {s.dialogue && <p className="shot-line">{s.dialogue}</p>}
            </div>
            <div className="shot-assets">
              {s.assets.map(c => (
                <span key={c} className="atag" title={byCode[c]?.name}>
                  {byCode[c]?.images.find(im => im.id === byCode[c]?.picked_image_id) ? <img src={byCode[c].images.find(im => im.id === byCode[c].picked_image_id)!.url} alt="" /> : <i />}
                  <b>{c}</b><span>{byCode[c]?.name || '已删除'}</span>
                </span>
              ))}
            </div>
            <div className="shot-actions">
              <button className="icon-btn sm" onClick={() => move(i, -1)} disabled={i === 0} aria-label="上移" title="上移"><ArrowUp size={14} /></button>
              <button className="icon-btn sm" onClick={() => move(i, 1)} disabled={i === data.shots.length - 1} aria-label="下移" title="下移"><ArrowDown size={14} /></button>
              <button className="icon-btn sm" onClick={() => setEdit(s)} aria-label="编辑" title="编辑"><Pencil size={14} /></button>
              <button className="icon-btn sm is-danger" onClick={() => confirm(`删除镜头 ${s.code}？`) && run(() => api(`/api/shots/${s.id}`, { method: 'DELETE' }))} aria-label="删除" title="删除"><Trash2 size={14} /></button>
            </div>
          </article>
        ))}
      </div>
      {edit && <ShotEditor shot={edit} onClose={() => setEdit(null)} />}
    </section>
  )
}

export function ShotEditor({ shot, onClose }: { shot: Shot; onClose: () => void }) {
  const { data, run } = useProject()
  const [s, setS] = useState<Shot>(shot)
  const toggle = (c: string) => setS({ ...s, assets: s.assets.includes(c) ? s.assets.filter(x => x !== c) : [...s.assets, c] })
  const save = async () => { if (await run(() => api(`/api/shots/${s.id}`, { method: 'PUT', body: s }), '已保存')) onClose() }
  return (
    <Modal open wide onClose={onClose} title={`镜头 ${shot.code}`}
      footer={<><button className="btn btn-secondary" onClick={onClose}>取消</button><button className="btn btn-primary" onClick={save}>保存</button></>}>
      <div className="form-grid">
        <label className="field"><span className="field-label">时长（秒）</span><input className="input" type="number" min={5} max={10} value={s.duration} onChange={e => setS({ ...s, duration: Number(e.target.value) })} /></label>
        <label className="field"><span className="field-label">镜头语言</span><input className="input" value={s.camera} onChange={e => setS({ ...s, camera: e.target.value })} placeholder="近景 · 缓慢推近" /></label>
        <label className="field span-2"><span className="field-label">画面</span><textarea className="input" rows={2} value={s.action} onChange={e => setS({ ...s, action: e.target.value })} /></label>
        <label className="field span-2"><span className="field-label">台词 / 旁白</span><textarea className="input" rows={2} value={s.dialogue} onChange={e => setS({ ...s, dialogue: e.target.value })} placeholder="角色名：台词" /></label>
      </div>
      <div className="field"><span className="field-label">出场资产</span>
        <div className="chips">
          {data.assets.map(a => <button key={a.code} type="button" className={`chip ${s.assets.includes(a.code) ? 'is-on' : ''}`} onClick={() => toggle(a.code)}><em>{a.code}</em>{a.name}</button>)}
          {data.assets.length === 0 && <span className="muted sm-text">还没有资产</span>}
        </div>
      </div>
      <label className="field"><span className="field-label">关键帧提示词</span><textarea className="input" rows={3} value={s.keyframe_prompt} onChange={e => setS({ ...s, keyframe_prompt: e.target.value })} /></label>
      <label className="field"><span className="field-label">视频描述</span><textarea className="input" rows={3} value={s.video_prompt} onChange={e => setS({ ...s, video_prompt: e.target.value })} placeholder="从关键帧开始的动作、运镜和声音" /></label>
    </Modal>
  )
}
