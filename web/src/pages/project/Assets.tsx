import { useState } from 'react'
import { ArrowRight, Pencil, Plus, Sparkles, Trash2, Wand2, UserRound, Mountain, Box, AudioLines, CopyPlus, Images, X, CornerDownRight } from 'lucide-react'
import { api, type Asset } from '../../api'
import { useProject, StepHead, Cost } from '../Project'
import { Empty, Modal, Segmented, Select, Spinner, useMedia } from '../../components/ui'
import { ImageLibrary, ModelSelect, UploadPick } from '../../components/imgtools'
import { kindLabels, tagNames } from '../../labels'
import { Picker, UploadBtn } from './media'
import { LibSaveBtn, VoiceButton } from './Voice'

const kindIcon = { character: UserRound, scene: Mountain, prop: Box }

export default function Assets() {
  const { data, task, run, goStep, library, patch } = useProject()
  const p = data.project, e = data.episode
  const [edit, setEdit] = useState<Partial<Asset> | null>(null)
  const extracting = task('extract', 'project', p.id)
  const missing = data.assets.filter(a => !a.images.some(i => i.tag === '') && !task('image', 'asset', a.id)).length
  const groups = (['character', 'scene', 'prop'] as const).map(k => ({ k, list: data.assets.filter(a => a.kind === k) }))
  const noVoice = data.assets.filter(a => a.kind === 'character' && !a.voice?.mode).length + (p.audio_mode === 'dub' && !p.narrator_voice?.mode ? 1 : 0)
  const designing = task('voices', 'project', p.id)
  const noSheet = data.assets.filter(a => a.kind === 'character' && a.picked_image_id > 0 && !a.images.some(i => i.tag === 'sheet') && !task('image-sheet', 'asset', a.id)).length
  const ok = data.assets.length > 0 && data.assets.every(a => a.picked_image_id > 0)
  return (
    <section className="stepview">
      <StepHead title={library ? '资产库' : '资产'} note={data.assets.length ? `${data.assets.filter(a => a.picked_image_id > 0).length} / ${data.assets.length} 已选定` : undefined}>
        <ModelSelect size="md" value={{ channel_id: p.image_channel_id, model: p.image_model }} onChange={cm => patch({ image_channel_id: cm.channel_id, image_model: cm.model })} />
        {!library && <button className="btn btn-secondary" disabled={!!extracting || !e?.script.trim()} onClick={() => run(() => api(`/api/episodes/${e.id}/assets/extract`, { method: 'POST' }))}>
          {extracting ? <Spinner /> : <Wand2 size={16} />}{data.assets.length ? '补充提取' : '从剧本提取'}<Cost>1 次对话</Cost>
        </button>}
        <button className="btn btn-secondary" disabled={noVoice === 0 || !!designing} onClick={() => run(() => api(`/api/projects/${p.id}/voices/design-all`, { method: 'POST' }), '开始设计音色')}>
          {designing ? <Spinner /> : <AudioLines size={16} />}一键设计音色<Cost>{noVoice} 个</Cost>
        </button>
        <button className="btn btn-secondary" disabled={missing === 0} onClick={() => run(() => api(`/api/projects/${p.id}/assets/generate-all`, { method: 'POST' }), `开始生成 ${missing} 张`)}>
          <Sparkles size={16} />全部生成<Cost>{missing} 张</Cost>
        </button>
        {noSheet > 0 && <button className="btn btn-secondary" title="关键帧锁脸主要靠三视图" onClick={() => run(() => api(`/api/projects/${p.id}/sheets/generate-all`, { method: 'POST' }), `开始生成 ${noSheet} 张三视图`)}>
          <Sparkles size={16} />补齐三视图<Cost>{noSheet} 张</Cost>
        </button>}
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
              {sortVariants(g.list).map(a => <AssetCard key={a.id} a={a} base={data.assets.find(b => b.id === a.base_id)} aspect={g.k === 'character' ? '3:4' : g.k === 'prop' ? '1:1' : p.aspect} onEdit={() => setEdit(a)}
                onVariant={() => setEdit({ kind: 'character', base_id: a.id, name: `${a.name}（）`, description: a.description })} />)}
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

// 另一时期紧跟在本体后面
function sortVariants(list: Asset[]) {
  const ids = new Set(list.map(a => a.id))
  const out: Asset[] = []
  for (const a of list) {
    if (a.base_id && ids.has(a.base_id)) continue
    out.push(a, ...list.filter(v => v.base_id === a.id))
  }
  return out
}

type Tab = '' | 'sheet' | 'expr' | 'ref'

function AssetCard({ a, base, aspect, onEdit, onVariant }: { a: Asset; base?: Asset; aspect: string; onEdit: () => void; onVariant: () => void }) {
  const { task, run } = useProject()
  const [tag, setTag] = useState<Tab>('')
  const [lib, setLib] = useState(false)
  const isChar = a.kind === 'character'
  const kind = tag ? 'image-' + tag : 'image'
  const busy = !!task(kind, 'asset', a.id)
  const ims = a.images.filter(i => (i.tag || '') === tag)
  const refs = a.images.filter(i => i.tag === 'ref')
  const picked = tag === 'sheet' ? a.sheet_image_id : tag === 'expr' ? a.expr_image_id : a.picked_image_id
  const saveVoice = (v: any) => run(() => api(`/api/assets/${a.id}`, { method: 'PUT', body: { name: a.name, description: a.description, prompt: a.prompt, voice: v } }), '音色已保存')
  const tabs: Tab[] = isChar ? ['', 'sheet', 'expr', 'ref'] : ['', 'ref']
  const narrow = useMedia('(max-width: 640px)')
  const short: Record<Tab, string> = { '': '设定', sheet: '三视', expr: '表情', ref: '参考' }
  const lockHint = base ? `会拿「${base.name}」的图锁脸` : refs.length ? `会拿${refs.length}张参考图${isChar ? '锁脸' : '定外形'}` : ''
  return (
    <article className={`card acard ${base ? 'is-variant' : ''}`}>
      <div className="acard-head">
        <span className="code">{a.code}</span>
        <h4 title={a.name}>{a.name}</h4>
        {base && <span className="acard-base" title={`${base.code} ${base.name} 的另一时期：出图时拿本体的图锁脸，发型服装按这个角色自己的外观`}><CornerDownRight size={12} /></span>}
        <span className="grow" />
        {isChar && !base && <button className="icon-btn sm" onClick={onVariant} aria-label="加一个时期" title="加一个时期 / 造型（同一个人，锁脸）"><CopyPlus size={14} /></button>}
        <button className="icon-btn sm" onClick={onEdit} aria-label="编辑" title="编辑"><Pencil size={14} /></button>
      </div>
      <Segmented size="sm" value={tag} onChange={v => setTag(v as Tab)} options={tabs.map(t => ({ value: t, label: narrow && isChar ? short[t] : t === 'ref' && refs.length ? `参考 ${refs.length}` : t === 'ref' ? '参考' : tagNames[t] }))} />
      {tag === 'ref' ? (
        <div className="refbox">
          {refs.length === 0 && <div className="refbox-empty">{isChar ? '放一张脸清楚的照片或图，生成设定图时用它锁脸' : '放一张参考图，生成时参考它的外形和风格'}</div>}
          <div className="refbox-grid">
            {refs.map(r => (
              <span key={r.id} className="refbox-item">
                <img src={r.url} alt="" />
                <button onClick={() => run(() => api(`/api/images/${r.id}`, { method: 'DELETE' }))} aria-label="移除参考图" title="移除"><X size={12} /></button>
              </span>
            ))}
          </div>
        </div>
      ) : (
        <div className={`look look-${tag || 'base'}`}>
          <Picker images={ims} picked={picked} aspect={aspect} busy={busy} empty={<span>{tag && !a.picked_image_id ? '先选定设定图' : '未生成'}</span>} />
        </div>
      )}
      {tag !== 'ref' && (isChar ? <div className="vrow"><VoiceButton value={a.voice || {}} name={a.name} assetId={a.id} onSave={saveVoice} onApplied={() => run(async () => {}, '已换成音色库的音色')} />{!!a.voice?.mode && <LibSaveBtn name={`${a.name}的声音`} body={{ asset_id: a.id }} />}</div> : <p className="acard-desc" data-clamp title={a.description}>{a.description || '没有描述'}</p>)}
      <div className="acard-foot">
        {tag === 'ref' ? <>
          <button className="btn btn-secondary btn-sm" onClick={() => setLib(true)}><Images size={14} />从图库选</button>
          <span className="grow" />
          <UploadPick path={`/api/assets/${a.id}/upload?tag=ref`} label="" className="icon-btn sm" onDone={() => run(async () => {}, '已加参考图')} />
        </> : <>
          <button className="btn btn-secondary btn-sm" disabled={busy || (!!tag && !a.picked_image_id)} title={lockHint} onClick={() => run(() => api(`/api/assets/${a.id}/generate`, { body: { n: 1, tag } }))}>
            {busy ? <Spinner size={13} /> : <Sparkles size={14} />}{ims.length ? '再来一张' : '生成'}<Cost>1 张</Cost>
          </button>
          <span className="grow" />
          <UploadBtn path={`/api/assets/${a.id}/upload${tag ? '?tag=' + tag : ''}`} label="" className="icon-btn sm" />
        </>}
      </div>
      <ImageLibrary open={lib} onClose={() => setLib(false)} multi max={4} title={`给「${a.name}」选参考图`}
        onPick={imgs => run(async () => { for (const im of imgs) await api(`/api/images/${im.id}/copy`, { body: { asset_id: a.id, tag: 'ref' } }) }, `已加 ${imgs.length} 张参考图`)} />
    </article>
  )
}

function AssetEditor({ initial, onClose }: { initial: Partial<Asset>; onClose: () => void }) {
  const { data, run } = useProject()
  const [a, setA] = useState<Partial<Asset>>({ name: '', description: '', prompt: '', ...initial })
  const isNew = !initial.id
  const others = data.assets.filter(x => x.kind === 'character' && x.id !== initial.id && !x.base_id)
  const save = async () => {
    const body = { kind: a.kind, name: a.name, description: a.description, prompt: a.prompt, base_id: (a.kind || 'character') === 'character' ? a.base_id || 0 : 0 }
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
      {(a.kind || 'character') === 'character' && others.length > 0 && <div className="field"><span className="field-label">同一个人的另一时期</span>
        <Select value={String(a.base_id || 0)} options={[{ value: '0', label: '不是（独立角色）' }, ...others.map(x => ({ value: String(x.id), label: `${x.code} ${x.name}` }))]} onChange={v => setA({ ...a, base_id: Number(v) })} />
        <span className="field-hint">选了本体后，生成设定图和三视图会拿本体的图锁脸，年龄、发型、服装按下面的外观描述画</span>
      </div>}
      <label className="field"><span className="field-label">名称</span><input className="input" value={a.name} onChange={e => setA({ ...a, name: e.target.value })} /></label>
      <label className="field"><span className="field-label">外观描述</span><textarea className="input" rows={3} value={a.description} onChange={e => setA({ ...a, description: e.target.value })} placeholder={a.base_id ? "写这个时期和本体不同的地方：年龄感、发型发色、服装配饰……" : "固定特征：年龄感、发型发色、服装配色……"} /></label>
      <label className="field"><span className="field-label">生图提示词</span><textarea className="input" rows={4} value={a.prompt} onChange={e => setA({ ...a, prompt: e.target.value })} placeholder="留空则用名称和外观描述" /></label>
    </Modal>
  )
}
