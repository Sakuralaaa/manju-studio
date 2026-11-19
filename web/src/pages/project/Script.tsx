import { useEffect, useRef, useState } from 'react'
import { ArrowRight, Sparkles } from 'lucide-react'
import { api } from '../../api'
import { useProject, StepHead, Cost } from '../Project'
import { useStore } from '../../store'
import { Spinner } from '../../components/ui'

export default function Script() {
  const { data, patch, task, run, goStep } = useProject()
  const { channels } = useStore()
  const p = data.project
  const [text, setText] = useState(p.script)
  const [note, setNote] = useState('')
  const [saved, setSaved] = useState(true)
  const busy = task('script', 'project', p.id)
  const last = useRef(p.script)
  // 生成完成后同步服务端内容
  useEffect(() => { if (p.script !== last.current) { setText(p.script); last.current = p.script; setSaved(true) } }, [p.script])
  useEffect(() => {
    if (saved) return
    const t = setTimeout(() => { last.current = text; patch({ script: text }).then(() => setSaved(true)) }, 800)
    return () => clearTimeout(t)
  }, [text, saved])
  const tch = channels.find(c => c.id === p.text_channel_id && c.kind === 'text') || channels.find(c => c.kind === 'text')
  const model = p.text_model && tch?.models.includes(p.text_model) ? p.text_model : tch?.models[0]
  const gen = () => run(() => api(`/api/projects/${p.id}/script/generate`, { body: { note } }))
  const chars = [...text].length
  return (
    <section className="stepview">
      <StepHead title="剧本" note={tch ? `${tch.name} · ${model || '未选模型'}` : undefined}>
        <button className="btn btn-secondary" disabled={!!busy || !p.idea || !tch} onClick={gen}>
          {busy ? <Spinner /> : <Sparkles size={16} />}{text.trim() ? (note.trim() ? '按意见改写' : '重新生成') : 'AI 写剧本'}<Cost>1 次对话</Cost>
        </button>
        <button className="btn btn-primary" disabled={!text.trim()} onClick={() => goStep(3)}>下一步：资产<ArrowRight size={16} /></button>
      </StepHead>
      <div className="card script-card">
        <div className="script-bar">
          <input className="input" value={note} onChange={e => setNote(e.target.value)} placeholder="修改意见（可选）：比如开头更狠一点，结尾留个身份反转" />
        </div>
        <div className={`script-wrap ${busy ? 'is-busy' : ''}`}>
          <textarea className="script-text" value={text} onChange={e => { setText(e.target.value); setSaved(false) }}
            placeholder={'点「AI 写剧本」根据创意生成，或者直接粘贴你自己的剧本。\n\n建议格式：\n# 标题\n## 人物\n## 第 1 场 · 地点 · 时间\n【画面】……\n角色：台词'} />
          {busy && <div className="script-busy"><Spinner size={18} /><span>正在写剧本</span></div>}
        </div>
        <div className="script-foot">
          <span>{chars} 字</span>
          <span className="grow" />
          <span className={saved ? 'muted' : 'accent-text'}>{saved ? '已保存' : '保存中'}</span>
        </div>
      </div>
    </section>
  )
}
