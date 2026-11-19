import { useState } from 'react'
import { ArrowRight, Pencil, Plus, Sparkles, Trash2, Wand2, UserRound, Mountain, Box, AudioLines } from 'lucide-react'
import { api, type Asset } from '../../api'
import { useProject, StepHead, Cost } from '../Project'
import { Empty, Modal, Segmented, Spinner } from '../../components/ui'
import { kindLabels, tagNames } from '../../labels'
import { Picker, UploadBtn } from './media'
import { VoiceButton } from './Voice'

const kindIcon = { character: UserRound, scene: Mountain, prop: Box }

export default function Assets() {
  const { data, task, run, goStep, library } = useProject()
  const p = data.project, e = data.episode
  const [edit, setEdit] = useState<Partial<Asset> | null>(null)
  const extracting = task('extract', 'project', p.id)
  const missing = data.assets.filter(a => !a.images.some(i => i.tag === '') && !task('image', 'asset', a.id)).length
  const groups = (['character', 'scene', 'prop'] as const).map(k => ({ k, list: data.assets.filter(a => a.kind === k) }))
  const noVoice = data.assets.filter(a => a.kind === 'character' && !a.voice?.mode).length + (p.audio_mode === 'dub' && !p.narrator_voice?.mode ? 1 : 0)
  const designing = task('voices', 'project', p.id)
  const ok = data.assets.length > 0 && data.assets.every(a => a.picked_image_id > 0)
  return (
    <section className="stepview">
      <StepHead title={library ? '资产库' : '资产'} note={data.assets.length ? `${data.assets.filter(a => a.picked_image_id > 0).length} / ${data.assets.length} 已选定` : undefined}>
        {!library && <button className="btn btn-secondary" disabled={!!extracting || !e?.script.trim()} onClick={() => run(() => api(`/api/episodes/${e.id}/assets/extract`, { method: 'POST' }))}>
          {extracting ? <Spinner /> : <Wand2 size={16} />}{data.assets.length ? '补充提取' : '从剧本提取'}<Cost>1 次对话</Cost>
        </button>}
        <button className="btn btn-secondary" disabled={noVoice === 0 || !!designing} onClick={() => run(() => api(`/api/projects/${p.id}/voices/design-all`, { method: 'POST' }), '开始设计音色')}>
          {designing ? <Spinner /> : <AudioLines size={16} />}一键设计音色<Cost>{noVoice} 个</Cost>
        </button>
        <button className="btn btn-secondary" disabled={missing === 0} onClick={() => run(() => api(`/api/projects/${p.id}/assets/generate-all`, { method: 'POST' }), `开始生成 ${missing} 张`)}>
          <Sparkles size={16} />全部生成<Cost>{missing} 张</Cost>
        </button>
        {!library && <button className="btn btn-primary" disabled={!ok} onClick={() => goStep(4)}>下一步：分镜<ArrowRight size={16} /></button>}
      </StepHead>
      {data.assets.length === 0 && (
        <div className="card card-empty">
          {extracting ? <div className="loading"><Spinner size={18} /><span>正在从剧本里找角色、场景和道具</span></div> :
            <Empty art="stack" title="还没有资产" action={<button className="btn btn-secondary" onClick={() => setEdit({ kind: 'character' })}><Plus size={16} />手动添加</button>} />}
        </div>
      )}
      {groups.filter(g => g.list.length > 0).map(g => {
        const Icon = kindIcon[g.k]
        return (
          <div key={g.k} className="agroup">
            <div className="agroup-head">
              <Icon size={16} /><h3>{kindLabels[g.k]}</h3><span className="muted">{g.list.length}</span>
              <span className="grow" />
              <button className="btn btn-ghost btn-sm" onClick={() => setEdit({ kind: g.k })}><Plus size={14} />添加{kindLabels[g.k]}</button>
            </div>
            <div className={`agrid agrid-${g.k} ${g.k === 'scene' ? 'agrid-scene-' + p.aspect.replace(':', '-') : ''}`}>
              {g.list.map(a => <AssetCard key={a.id} a={a} aspect={g.k === 'character' ? '3:4' : g.k === 'prop' ? '1:1' : p.aspect} onEdit={() => setEdit(a)} />)}
            </div>
          </div>
        )
      })}
      {data.assets.length > 0 && groups.some(g => g.list.length === 0) && (
        <div className="add-row">
          {groups.filter(g => g.list.length === 0).map(g => <button key={g.k} className="btn btn-ghost btn-sm" onClick={() => setEdit({ kind: g.k })}><Plus size={14} />添加{kindLabels[g.k]}</button>)}
        </div>
      )}
      {edit && <AssetEditor initial={edit} onClose={() => setEdit(null)} />}
    </section>
  )
}

function AssetCard({ a, aspect, onEdit }: { a: Asset; aspect: string; onEdit: () => void }) {
  const { task, run } = useProject()
  const [tag, setTag] = useState<'' | 'sheet' | 'expr'>('')
  const isChar = a.kind === 'character'
  const kind = tag ? 'image-' + tag : 'image'
  const busy = !!task(kind, 'asset', a.id)
  const ims = a.images.filter(i => (i.tag || '') === tag)
  const picked = tag === 'sheet' ? a.sheet_image_id : tag === 'expr' ? a.expr_image_id : a.picked_image_id
  const saveVoice = (v: any) => run(() => api(`/api/assets/${a.id}`, { method: 'PUT', body: { name: a.name, description: a.description, prompt: a.prompt, voice: v } }), '音色已保存')
  return (
    <article className="card acard">
      <div className="acard-head">
        <span className="code">{a.code}</span>
        <h4 title={a.name}>{a.name}</h4>
        <span className="grow" />
        <button className="icon-btn sm" onClick={onEdit} aria-label="编辑" title="编辑"><Pencil size={14} /></button>
      </div>
      {isChar && <Segmented size="sm" value={tag} onChange={v => setTag(v as any)} options={(['', 'sheet', 'expr'] as const).map(t => ({ value: t, label: tagNames[t] }))} />}
      <div className={`look look-${tag || 'base'}`}>
        <Picker images={ims} picked={picked} aspect={aspect} busy={busy} empty={<span>{tag && !a.picked_image_id ? '先选定设定图' : '未生成'}</span>} />
      </div>
      {isChar ? <VoiceButton value={a.voice || {}} name={a.name} assetId={a.id} onSave={saveVoice} /> : <p className="acard-desc" data-clamp title={a.description}>{a.description || '没有描述'}</p>}
      <div className="acard-foot">
        <button className="btn btn-secondary btn-sm" disabled={busy || (!!tag && !a.picked_image_id)} onClick={() => run(() => api(`/api/assets/${a.id}/generate`, { body: { n: 1, tag } }))}>
          {busy ? <Spinner size={13} /> : <Sparkles size={14} />}{ims.length ? '再来一张' : '生成'}<Cost>1 张</Cost>
        </button>
        <span className="grow" />
        <UploadBtn path={`/api/assets/${a.id}/upload${tag ? '?tag=' + tag : ''}`} label="" className="icon-btn sm" />
      </div>
    </article>
  )
}

function AssetEditor({ initial, onClose }: { initial: Partial<Asset>; onClose: () => void }) {
  const { data, run } = useProject()
  const [a, setA] = useState<Partial<Asset>>({ name: '', description: '', prompt: '', ...initial })
  const isNew = !initial.id
  const save = async () => {
    const body = { kind: a.kind, name: a.name, description: a.description, prompt: a.prompt }
    const ok = await run(() => isNew ? api(`/api/projects/${data.project.id}/assets`, { body }) : api(`/api/assets/${a.id}`, { method: 'PUT', body }), '已保存')
    if (ok) onClose()
  }
  return (
    <Modal open onClose={onClose} title={isNew ? '添加资产' : `编辑 ${initial.code}`}
      footer={<>
        {!isNew && <button className="btn btn-ghost is-danger" onClick={async () => { if (confirm(`删除「${initial.name}」和它的全部图片？`) && await run(() => api(`/api/assets/${initial.id}`, { method: 'DELETE' }), '已删除')) onClose() }}><Trash2 size={16} />删除</button>}
        <span className="grow" />
        <button className="btn btn-secondary" onClick={onClose}>取消</button><button className="btn btn-primary" disabled={!a.name?.trim()} onClick={save}>保存</button></>}>
      {isNew && <div className="field"><span className="field-label">类型</span>
        <Segmented value={a.kind || 'character'} onChange={v => setA({ ...a, kind: v as any })} options={[{ value: 'character', label: '角色' }, { value: 'scene', label: '场景' }, { value: 'prop', label: '道具' }]} />
      </div>}
      <label className="field"><span className="field-label">名称</span><input className="input" value={a.name} onChange={e => setA({ ...a, name: e.target.value })} /></label>
      <label className="field"><span className="field-label">外观描述</span><textarea className="input" rows={3} value={a.description} onChange={e => setA({ ...a, description: e.target.value })} placeholder="固定特征：年龄感、发型发色、服装配色……" /></label>
      <label className="field"><span className="field-label">生图提示词</span><textarea className="input" rows={4} value={a.prompt} onChange={e => setA({ ...a, prompt: e.target.value })} placeholder="留空则用名称和外观描述" /></label>
    </Modal>
  )
}
