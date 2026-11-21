import { useEffect, useState } from 'react'
import { ArrowRight, Sparkles, Mic2 } from 'lucide-react'
import { api } from '../../api'
import { useProject, StepHead, Cost } from '../Project'
import { useStore } from '../../store'
import { Segmented, Select, Spinner, Stepper } from '../../components/ui'
import { StylePicker, kindOptions, animeStyles, realStyles } from '../Projects'
import { LibSaveBtn, VoiceButton } from './Voice'

export default function Setup() {
  const { data, patch, patchEp, goStep, task, run } = useProject()
  const { channels, go } = useStore()
  const p = data.project, e = data.episode
  const [f, setF] = useState(p)
  const [ep, setEp] = useState(e)
  useEffect(() => { setF(p) }, [p.id])
  useEffect(() => { setEp(e) }, [e.id, e.idea])
  const save = (k: keyof typeof p, v: any) => { if ((p as any)[k] !== v) patch({ [k]: v } as any) }
  const saveEp = (k: keyof typeof e, v: any) => { if ((e as any)[k] !== v) patchEp({ [k]: v } as any) }
  const textChs = channels.filter(c => c.kind === 'text')
  const imgChs = channels.filter(c => c.kind === 'image')
  const tch = textChs.find(c => c.id === f.text_channel_id) || textChs[0]
  const ich = imgChs.find(c => c.id === f.image_channel_id) || imgChs[0]
  const ideaBusy = task('idea', 'episode', e.id)
  const prev = data.episodes.filter(x => x.no < e.no)
  return (
    <section className="stepview">
      <StepHead title="设定">
        <button className="btn btn-primary" disabled={!(ep.idea.trim() || f.idea.trim())} onClick={() => goStep(2)}>下一步：剧本<ArrowRight size={16} /></button>
      </StepHead>
      <div className="setup-grid">
        <div className="card scard">
          <h3 className="scard-title">本集</h3>
          <label className="field"><span className="field-label">本集标题</span>
            <input className="input" value={ep.title} onChange={x => setEp({ ...ep, title: x.target.value })} onBlur={() => saveEp('title', ep.title.trim())} placeholder="可留空，写完剧本后自动取剧本标题" />
          </label>
          <div className="field"><span className="field-label">本集梗概</span>
            <textarea className="input" rows={4} value={ep.idea} onChange={x => setEp({ ...ep, idea: x.target.value })} onBlur={() => saveEp('idea', ep.idea)}
              placeholder={prev.length ? '接上一集的卡点，本集发生什么、怎么反转、停在哪' : '一两句话：谁，想要什么，遇到了什么反转'} />
            <div className="row-actions">
              <button className="btn btn-secondary btn-sm" disabled={!!ideaBusy || !tch} onClick={() => run(() => api(`/api/episodes/${e.id}/idea`, { method: 'POST' }))}>
                {ideaBusy ? <Spinner size={13} /> : <Sparkles size={14} />}AI 拟定梗概<Cost>1 次对话</Cost>
              </button>
            </div>
          </div>
          <label className="field"><span className="field-label">剧名</span>
            <input className="input" value={f.title} onChange={x => setF({ ...f, title: x.target.value })} onBlur={() => f.title.trim() && save('title', f.title.trim())} />
          </label>
          <label className="field"><span className="field-label">整体创意</span>
            <textarea className="input" rows={3} value={f.idea} onChange={x => setF({ ...f, idea: x.target.value })} onBlur={() => save('idea', f.idea)} />
          </label>
          <div className="field"><span className="field-label">作品类型</span>
            <Segmented value={f.kind || 'manju'} options={kindOptions} onChange={v => {
              const st = v === 'live' && (!f.style || animeStyles.includes(f.style)) ? realStyles[0] : f.style
              setF({ ...f, kind: v, style: st }); patch({ kind: v, style: st } as any)
            }} />
            <span className="field-hint muted">漫剧也能选真人风画面，就是仿真人漫剧；真人剧的编剧和分镜按真人短剧来写。</span>
          </div>
          <div className="field" onBlur={() => save('style', f.style)}><span className="field-label">画面风格</span>
            <StylePicker value={f.style} onChange={v => setF({ ...f, style: v })} onPick={s => save('style', s)} />
          </div>
        </div>
        <div className="stack">
          <div className="card scard">
            <h3 className="scard-title">规格</h3>
            <div className="field"><span className="field-label">画幅</span>
              <Segmented value={f.aspect} onChange={v => { setF({ ...f, aspect: v }); save('aspect', v) }} options={[{ value: '9:16', label: '9:16', glyph: '9:16' }, { value: '16:9', label: '16:9', glyph: '16:9' }, { value: '1:1', label: '1:1', glyph: '1:1' }]} />
            </div>
            <div className="form-grid">
              <label className="field"><span className="field-label">单集时长（秒）</span>
                <input className="input" type="number" min={10} max={600} value={f.episode_sec} onChange={x => setF({ ...f, episode_sec: Number(x.target.value) })} onBlur={() => save('episode_sec', f.episode_sec)} />
              </label>
              <label className="field"><span className="field-label">目标镜头数</span>
                <input className="input" type="number" min={1} max={60} value={f.shot_target} onChange={x => setF({ ...f, shot_target: Number(x.target.value) })} onBlur={() => save('shot_target', f.shot_target)} />
              </label>
            </div>
            <div className="field"><span className="field-label">视频分辨率</span>
              <Segmented value={f.video_res} onChange={v => { setF({ ...f, video_res: v }); save('video_res', v) }} options={[{ value: '768p', label: '768p' }, { value: '480p', label: '480p 快速' }]} />
            </div>
            <div className="field"><span className="field-label">视频采样</span>
              <Segmented value={f.video_turbo ? 'turbo' : 'std'} onChange={v => { setF({ ...f, video_turbo: v === 'turbo' }); save('video_turbo', v === 'turbo') }} options={[{ value: 'std', label: '标准 20 步' }, { value: 'turbo', label: 'Turbo 8 步' }]} />
            </div>
            <div className="field field-row"><span className="field-label">每个镜头抽卡</span>
              <Stepper value={f.takes} min={1} max={4} onChange={v => { setF({ ...f, takes: v }); save('takes', v) }} />
            </div>
          </div>
          <div className="card scard">
            <h3 className="scard-title">声音与画质</h3>
            <div className="field"><span className="field-label">人声</span>
              <Segmented value={f.audio_mode} onChange={v => { setF({ ...f, audio_mode: v as any }); save('audio_mode', v) }} options={[{ value: 'native', label: '视频原生声音' }, { value: 'dub', label: '配音覆盖' }]} />
            </div>
            {f.audio_mode === 'dub' && <>
              <div className="field field-row"><span className="field-label">视频原声音量</span>
                <div className="unit-input sm-unit"><input className="input" type="number" min={0} max={100} step={5} value={Math.round(f.orig_volume * 100)}
                  onChange={x => setF({ ...f, orig_volume: Number(x.target.value) / 100 })} onBlur={() => save('orig_volume', f.orig_volume)} /><span>%</span></div>
              </div>
              <div className="field field-row"><span className="field-label">旁白音色</span>
                <VoiceButton value={p.narrator_voice} name="旁白" projectId={p.id} onApplied={() => run(async () => {}, '已换成音色库的音色')} onSave={v => patch({ narrator_voice: v })} icon={<Mic2 size={14} />} />
                {!!p.narrator_voice?.mode && <LibSaveBtn name={`${p.title}的旁白`} body={{ project_id: p.id }} />}
              </div>
            </>}
            <div className="field"><span className="field-label">交付画质</span>
              <Segmented value={f.deliver} onChange={v => { setF({ ...f, deliver: v as any }); save('deliver', v) }} options={[{ value: 'source', label: '原始尺寸' }, { value: '1080p', label: '1080p' }]} />
            </div>
            <div className="form-grid">
              <div className="field"><span className="field-label">云端放大</span>
                <Segmented value={f.post_upscale ? 'on' : 'off'} onChange={v => { setF({ ...f, post_upscale: v === 'on' }); save('post_upscale', v === 'on') }} options={[{ value: 'off', label: '关闭' }, { value: 'on', label: '开启' }]} />
              </div>
              <div className="field"><span className="field-label">云端补帧</span>
                <Segmented value={f.post_interp ? 'on' : 'off'} onChange={v => { setF({ ...f, post_interp: v === 'on' }); save('post_interp', v === 'on') }} options={[{ value: 'off', label: '关闭' }, { value: 'on', label: '48 帧' }]} />
              </div>
            </div>
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
