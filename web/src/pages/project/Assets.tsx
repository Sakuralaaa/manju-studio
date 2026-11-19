import { useState } from 'react'
import { ArrowRight, Pencil, Plus, Sparkles, Trash2, Wand2, UserRound, Mountain, Box } from 'lucide-react'
import { api, type Asset } from '../../api'
import { useProject, StepHead, Cost } from '../Project'
import { Empty, Modal, Segmented, Spinner } from '../../components/ui'
import { kindLabels } from '../../labels'
import { Picker, UploadBtn } from './media'

const kindIcon = { character: UserRound, scene: Mountain, prop: Box }

export default function Assets() {
  const { data, task, run, goStep } = useProject()
  const p = data.project
  const [edit, setEdit] = useState<Partial<Asset> | null>(null)
  const extracting = task('extract', 'project', p.id)
  const missing = data.assets.filter(a => a.images.length === 0 && !task('image', 'asset', a.id)).length
  const groups = (['character', 'scene', 'prop'] as const).map(k => ({ k, list: data.assets.filter(a => a.kind === k) }))
  return (
    <section className="stepview">
      <StepHead title="资产" note={data.assets.length ? `${data.steps.assets_ok} / ${data.assets.length} 已选定` : undefined}>
        <button className="btn btn-secondary" disabled={!!extracting || !p.script.trim()} onClick={() => run(() => api(`/api/projects/${p.id}/assets/extract`, { method: 'POST' }))}>
          {extracting ? <Spinner /> : <Wand2 size={16} />}{data.assets.length ? '补充提取' : '从剧本提取'}<Cost>1 次对话</Cost>
        </button>
        <button className="btn btn-secondary" disabled={missing === 0} onClick={() => run(() => api(`/api/projects/${p.id}/assets/generate-all`, { method: 'POST' }), `开始生成 ${missing} 张`)}>
          <Sparkles size={16} />全部生成<Cost>{missing} 张</Cost>
        </button>
        <button className="btn btn-primary" disabled={!data.steps.done[2]} onClick={() => goStep(4)}>下一步：分镜<ArrowRight size={16} /></button>
      </StepHead>
      {data.assets.length === 0 && (
        <div className="card card-empty">
          {extracting ? <div className="loading"><Spinner size={18} /><span>正在从剧本里找角色、场景和道具</span></div> :
            <Empty art="stack" title="还没有资产" text="从剧本提取角色、场景和道具，再逐个生成设定图。" action={<button className="btn btn-secondary" onClick={() => setEdit({ kind: 'character' })}><Plus size={16} />手动添加</button>} />}
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
  const busy = !!task('image', 'asset', a.id)
  return (
    <article className="card acard">
      <div className="acard-head">
        <span className="code">{a.code}</span>
        <h4 title={a.name}>{a.name}</h4>
        <span className="grow" />
        <button className="icon-btn sm" onClick={onEdit} aria-label="编辑" title="编辑"><Pencil size={14} /></button>
      </div>
      <Picker images={a.images} picked={a.picked_image_id} aspect={aspect} busy={busy} empty={<span>未生成</span>} />
      <p className="acard-desc" data-clamp title={a.description}>{a.description || '没有描述'}</p>
      <div className="acard-foot">
        <button className="btn btn-secondary btn-sm" disabled={busy} onClick={() => run(() => api(`/api/assets/${a.id}/generate`, { body: { n: 1 } }))}>
          {busy ? <Spinner size={13} /> : <Sparkles size={14} />}{a.images.length ? '再来一张' : '生成'}<Cost>1 张</Cost>
        </button>
        <span className="grow" />
        <UploadBtn path={`/api/assets/${a.id}/upload`} label="" className="icon-btn sm" />
      </div>
    </article>
  )
}

function AssetEditor({ initial, onClose }: { initial: Partial<Asset>; onClose: () => void }) {
  const { data, run } = useProject()
  const [a, setA] = useState<Partial<Asset>>({ name: '', description: '', prompt: '', ...initial })
  const isNew = !initial.id
  const save = async () => {
    const ok = await run(() => isNew ? api(`/api/projects/${data.project.id}/assets`, { body: a }) : api(`/api/assets/${a.id}`, { method: 'PUT', body: a }), '已保存')
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
