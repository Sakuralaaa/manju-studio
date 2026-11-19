import { useEffect, useState } from 'react'
import { ArrowRight } from 'lucide-react'
import { useProject, StepHead } from '../Project'
import { useStore } from '../../store'
import { Segmented, Select } from '../../components/ui'
import { styleChips } from '../Projects'

export default function Setup() {
  const { data, patch, goStep } = useProject()
  const { channels, go } = useStore()
  const p = data.project
  const [f, setF] = useState(p)
  useEffect(() => { setF(p) }, [p.id])
  const save = (k: keyof typeof p, v: any) => { if ((p as any)[k] !== v) patch({ [k]: v } as any) }
  const textChs = channels.filter(c => c.kind === 'text')
  const imgChs = channels.filter(c => c.kind === 'image')
  const tch = textChs.find(c => c.id === f.text_channel_id) || textChs[0]
  const ich = imgChs.find(c => c.id === f.image_channel_id) || imgChs[0]
  return (
    <section className="stepview">
      <StepHead title="设定">
        <button className="btn btn-primary" disabled={!f.idea.trim()} onClick={() => goStep(2)}>下一步：剧本<ArrowRight size={16} /></button>
      </StepHead>
      <div className="setup-grid">
        <div className="card scard">
          <h3 className="scard-title">故事</h3>
          <label className="field"><span className="field-label">剧名</span>
            <input className="input" value={f.title} onChange={e => setF({ ...f, title: e.target.value })} onBlur={() => f.title.trim() && save('title', f.title.trim())} />
          </label>
          <label className="field"><span className="field-label">故事创意</span>
            <textarea className="input" rows={5} value={f.idea} onChange={e => setF({ ...f, idea: e.target.value })} onBlur={() => save('idea', f.idea)} placeholder="一两句话：谁，想要什么，遇到了什么反转" />
          </label>
          <div className="field"><span className="field-label">画面风格</span>
            <input className="input" value={f.style} onChange={e => setF({ ...f, style: e.target.value })} onBlur={() => save('style', f.style)} placeholder="自由描述，或点下面的常用风格" />
            <div className="chips">{styleChips.map(s => <button key={s} type="button" className={`chip ${f.style === s ? 'is-on' : ''}`} onClick={() => { setF({ ...f, style: s }); save('style', s) }}>{s}</button>)}</div>
          </div>
        </div>
        <div className="card scard">
          <h3 className="scard-title">规格</h3>
          <div className="field"><span className="field-label">画幅</span>
            <Segmented value={f.aspect} onChange={v => { setF({ ...f, aspect: v }); save('aspect', v) }} options={[{ value: '9:16', label: '9:16', glyph: '9:16' }, { value: '16:9', label: '16:9', glyph: '16:9' }, { value: '1:1', label: '1:1', glyph: '1:1' }]} />
          </div>
          <div className="form-grid">
            <label className="field"><span className="field-label">单集时长（秒）</span>
              <input className="input" type="number" min={10} max={600} value={f.episode_sec} onChange={e => setF({ ...f, episode_sec: Number(e.target.value) })} onBlur={() => save('episode_sec', f.episode_sec)} />
            </label>
            <label className="field"><span className="field-label">目标镜头数</span>
              <input className="input" type="number" min={1} max={60} value={f.shot_target} onChange={e => setF({ ...f, shot_target: Number(e.target.value) })} onBlur={() => save('shot_target', f.shot_target)} />
            </label>
          </div>
          <div className="field"><span className="field-label">视频分辨率</span>
            <Segmented value={f.video_res} onChange={v => { setF({ ...f, video_res: v }); save('video_res', v) }} options={[{ value: '768p', label: '768p' }, { value: '480p', label: '480p 快速' }]} />
          </div>
          <div className="field"><span className="field-label">视频采样</span>
            <Segmented value={f.video_turbo ? 'turbo' : 'std'} onChange={v => { setF({ ...f, video_turbo: v === 'turbo' }); save('video_turbo', v === 'turbo') }} options={[{ value: 'std', label: '标准 20 步' }, { value: 'turbo', label: 'Turbo 8 步' }]} />
          </div>
        </div>
        <div className="card scard span-all">
          <h3 className="scard-title">渠道</h3>
          {textChs.length === 0 || imgChs.length === 0 ? (
            <div className="inline-warn">
              <span>{textChs.length === 0 && imgChs.length === 0 ? '还没有文本和生图渠道' : textChs.length === 0 ? '还没有文本渠道' : '还没有生图渠道'}</span>
              <button className="btn btn-secondary btn-sm" onClick={() => go({ page: 'channels' })}>去添加</button>
            </div>
          ) : null}
          <div className="form-grid form-grid-4">
            <div className="field"><span className="field-label">文本渠道</span>
              <Select value={String(tch?.id || '')} options={textChs.map(c => ({ value: String(c.id), label: c.name }))} placeholder="未添加" disabled={!textChs.length}
                onChange={v => { setF({ ...f, text_channel_id: Number(v), text_model: '' }); patch({ text_channel_id: Number(v), text_model: '' }) }} />
            </div>
            <div className="field"><span className="field-label">文本模型</span>
              <Select value={f.text_model && tch?.models.includes(f.text_model) ? f.text_model : tch?.models[0] || ''} options={(tch?.models || []).map(m => ({ value: m, label: m }))} placeholder="未选择" disabled={!tch}
                onChange={v => { setF({ ...f, text_model: v }); save('text_model', v) }} />
            </div>
            <div className="field"><span className="field-label">生图渠道</span>
              <Select value={String(ich?.id || '')} options={imgChs.map(c => ({ value: String(c.id), label: c.name }))} placeholder="未添加" disabled={!imgChs.length}
                onChange={v => { setF({ ...f, image_channel_id: Number(v), image_model: '' }); patch({ image_channel_id: Number(v), image_model: '' }) }} />
            </div>
            <div className="field"><span className="field-label">生图模型</span>
              <Select value={f.image_model && ich?.models.includes(f.image_model) ? f.image_model : ich?.models[0] || ''} options={(ich?.models || []).map(m => ({ value: m, label: m }))} placeholder="未选择" disabled={!ich}
                onChange={v => { setF({ ...f, image_model: v }); save('image_model', v) }} />
            </div>
          </div>
        </div>
      </div>
    </section>
  )
}
