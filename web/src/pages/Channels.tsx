import { useEffect, useMemo, useState } from 'react'
import { Plus, Pencil, Trash2, RefreshCw, Check, Search, KeyRound, X } from 'lucide-react'
import { api, type Cap, type Caps, type Channel, type Preset } from '../api'
import { useStore } from '../store'
import { Empty, Modal, Segmented, Select, Spinner, TagInput } from '../components/ui'
import { apiTypeLabels, capLabels, modeLabels, valueLabel, voiceTypeNames } from '../labels'
import { PageHead } from './Projects'

type Kind = 'image' | 'text' | 'voice'

export default function Channels() {
  const { channels, reloadChannels, toast } = useStore()
  const [kind, setKind] = useState<Kind>(() => (sessionStorage.getItem('manju.chkind') as Kind) || 'image')
  const pick = (k: Kind) => { sessionStorage.setItem('manju.chkind', k); setKind(k) }
  const [edit, setEdit] = useState<Partial<Channel> | null>(null)
  const list = channels.filter(c => c.kind === kind)
  const del = async (c: Channel) => {
    if (!confirm(`删除渠道「${c.name}」？`)) return
    await api(`/api/channels/${c.id}`, { method: 'DELETE' })
    await reloadChannels()
    toast('已删除')
  }
  return (
    <div className="page page-narrow">
      <PageHead title="渠道">
        <Segmented block={false} value={kind} onChange={v => pick(v as Kind)} options={[{ value: 'image', label: '生图' }, { value: 'text', label: '文本' }, { value: 'voice', label: '语音' }]} />
      </PageHead>
      <div className="channels">
        <div className="toolbar">
          <span className="toolbar-title">{list.length} 个{kindName[kind]}渠道</span><span className="grow" />
          <button className="btn btn-primary" onClick={() => setEdit({ kind })}><Plus size={16} />新建渠道</button>
        </div>
        {list.length === 0 && <div className="card card-empty"><Empty art="plug" title={`还没有${kindName[kind]}渠道`} action={<button className="btn btn-primary" onClick={() => setEdit({ kind })}><Plus size={16} />新建渠道</button>} /></div>}
        <div className="ch-list">
          {list.map(c => (
            <article key={c.id} className="card ch">
              <div className="ch-main">
                <div className="ch-title">
                  <span className="ch-mark">{c.name.slice(0, 1)}</span>
                  <div className="ch-name">
                    <h3>{c.name}</h3>
                    <span className="ch-url" title={c.base_url}>{c.base_url}</span>
                  </div>
                </div>
                <div className="ch-tags">
                  {c.kind === 'image' && <span className="mini-tag">{apiTypeLabels[c.api_type]}</span>}
                  {c.kind === 'voice' && <span className="mini-tag">{voiceTypeNames[c.api_type]}</span>}
                  {c.kind !== 'voice' && <span className="mini-tag">{c.models.length} 个模型</span>}
                  {c.kind === 'voice' && c.models.slice(0, 2).map(m => <span key={m} className="mini-tag">{m}</span>)}
                  {c.key_tail && <span className="mini-tag"><KeyRound size={12} />{c.key_tail}</span>}
                  {c.kind === 'text' && c.models.slice(0, 3).map(m => <span key={m} className="mini-tag">{m}</span>)}
                </div>
                {c.kind === 'image' && <CapSummary caps={c.caps} />}
              </div>
              <div className="ch-actions">
                <button className="icon-btn sm" onClick={() => setEdit(c)} aria-label="编辑" title="编辑"><Pencil size={14} /></button>
                <button className="icon-btn sm is-danger" onClick={() => del(c)} aria-label="删除" title="删除"><Trash2 size={14} /></button>
              </div>
            </article>
          ))}
        </div>
      </div>
      {edit && edit.kind === 'voice' && <VoiceChannelEditor initial={edit} onClose={() => setEdit(null)} onSaved={async () => { setEdit(null); await reloadChannels(); toast('渠道已保存') }} />}
      {edit && edit.kind !== 'voice' && <ChannelEditor initial={edit} onClose={() => setEdit(null)} onSaved={async () => { setEdit(null); await reloadChannels(); toast('渠道已保存') }} />}
    </div>
  )
}

const kindName: Record<Kind, string> = { image: '生图', text: '文本', voice: '语音' }
const voiceBase: Record<string, string> = { fish: 'https://api.fish.audio', mimo: 'https://api.xiaomimimo.com/v1', openai: '' }
const fishModels = ['s2.1-pro', 's2-pro', 's2.1-pro-free', 's3-pro', 'drama-3', 's1']

function VoiceChannelEditor({ initial, onClose, onSaved }: { initial: Partial<Channel>; onClose: () => void; onSaved: () => void }) {
  const { toast } = useStore()
  const isNew = !initial.id
  const [ch, setCh] = useState<Partial<Channel>>(() => ({ name: '', api_type: 'fish', base_url: voiceBase.fish, models: ['s2.1-pro'], voices: [], ...initial, ...(initial.id ? { api_key: '' } : { api_key: '' }) }))
  const [probe, setProbe] = useState<{ busy: boolean; ok?: boolean; msg?: string }>({ busy: false })
  const [saving, setSaving] = useState(false)
  const setType = (t: string) => setCh(c => ({ ...c, api_type: t as any, base_url: !c.base_url || Object.values(voiceBase).includes(c.base_url) ? voiceBase[t] : c.base_url,
    models: t === 'fish' ? ['s2.1-pro'] : t === 'mimo' ? ['mimo-v2.5-tts'] : ['tts-1'], name: c.name || voiceTypeNames[t] }))
  const doProbe = async () => {
    setProbe({ busy: true })
    try {
      const r = await api<{ ok: boolean; error?: string; latency_ms?: number }>('/api/channels/probe', { body: { id: initial.id || 0, base_url: ch.base_url, api_key: ch.api_key, api_type: ch.api_type } })
      setProbe({ busy: false, ok: r.ok, msg: r.ok ? `连接正常 · ${r.latency_ms} 毫秒` : r.error })
    } catch (e: any) { setProbe({ busy: false, ok: false, msg: e.message }) }
  }
  const save = async () => {
    if (!ch.name?.trim() || !ch.base_url?.trim()) { toast('名称和接口地址不能为空', 'info'); return }
    setSaving(true)
    try { await api(isNew ? '/api/channels' : `/api/channels/${initial.id}`, { method: isNew ? 'POST' : 'PUT', body: { ...ch, kind: 'voice' } }); onSaved() } catch (e: any) { toast(e.message, 'err') } finally { setSaving(false) }
  }
  return (
    <Modal open onClose={onClose} title={isNew ? '新建语音渠道' : '编辑渠道'}
      footer={<><button className="btn btn-secondary" onClick={onClose}>取消</button><button className="btn btn-primary" onClick={save} disabled={saving}>{saving && <Spinner />}保存</button></>}>
      <div className="field"><span className="field-label">类型</span>
        <Segmented value={ch.api_type || 'fish'} onChange={setType} disabled={!isNew} options={['fish', 'mimo', 'openai'].map(t => ({ value: t, label: voiceTypeNames[t] }))} />
      </div>
      <label className="field"><span className="field-label">名称</span><input className="input" value={ch.name || ''} onChange={e => setCh({ ...ch, name: e.target.value })} placeholder={voiceTypeNames[ch.api_type || 'fish']} /></label>
      <label className="field"><span className="field-label">接口地址</span><input className="input" value={ch.base_url || ''} onChange={e => setCh({ ...ch, base_url: e.target.value })} placeholder="https://example.com/v1" /></label>
      <label className="field"><span className="field-label">密钥</span>
        <input className="input" type="password" autoComplete="new-password" value={ch.api_key || ''} onChange={e => setCh({ ...ch, api_key: e.target.value })} placeholder={initial.key_tail ? `已保存 ${initial.key_tail}，留空不修改` : '粘贴密钥'} />
      </label>
      {ch.api_type === 'fish' && <div className="field"><span className="field-label">合成模型</span>
        <div className="chips">{fishModels.map(m => <button key={m} type="button" className={`chip ${ch.models?.[0] === m ? 'is-on' : ''}`} onClick={() => setCh({ ...ch, models: [m] })}>{m}</button>)}</div>
      </div>}
      {ch.api_type === 'openai' && <>
        <div className="field"><span className="field-label">模型</span><TagInput value={ch.models || []} onChange={v => setCh({ ...ch, models: v })} placeholder="tts-1，输入后回车" /></div>
        <div className="field"><span className="field-label">音色</span><TagInput value={ch.voices || []} onChange={v => setCh({ ...ch, voices: v })} placeholder="alloy，输入后回车" /></div>
      </>}
      <div className="row-actions">
        {probe.msg && <span className={`probe ${probe.ok ? 'ok' : 'err'}`}>{probe.ok ? <Check size={13} /> : <X size={13} />}{probe.msg}</span>}
        <span className="grow" />
        {ch.api_type !== 'mimo' && <button className="btn btn-secondary btn-sm" onClick={doProbe} disabled={probe.busy || !ch.base_url}>{probe.busy ? <Spinner size={13} /> : <RefreshCw size={13} />}测试连接</button>}
      </div>
    </Modal>
  )
}

function CapSummary({ caps }: { caps: Caps }) {
  const keys: (keyof Caps)[] = ['aspect', 'size', 'resolution', 'quality', 'n', 'format', 'background', 'refs']
  return (
    <div className="cap-sum">
      {keys.filter(k => caps[k] && caps[k].mode !== 'off').map(k => (
        <span key={k} className={`cap-pill mode-${caps[k].mode}`}>
          {capLabels[k]}<i>{k === 'refs' ? `≤${caps[k].max || 0}` : k === 'n' ? `≤${caps[k].max || 1}` : modeLabels[caps[k].mode]}</i>
        </span>
      ))}
    </div>
  )
}

const emptyCaps = (): Caps => ({
  size: { mode: 'off' }, aspect: { mode: 'off' }, resolution: { mode: 'off' }, quality: { mode: 'off' }, n: { mode: 'native', max: 1 },
  format: { mode: 'off' }, compression: { mode: 'off' }, background: { mode: 'off' }, moderation: { mode: 'off' }, response: { mode: 'off' }, refs: { mode: 'off', max: 0 },
})

const capModes: Record<keyof Caps, string[]> = {
  size: ['native', 'prompt', 'off'], aspect: ['native', 'prompt', 'off'], resolution: ['native', 'prompt', 'off'], quality: ['native', 'prompt', 'off'],
  n: ['native', 'loop', 'off'], format: ['native', 'off'], compression: ['native', 'off'], background: ['native', 'prompt', 'off'], moderation: ['native', 'off'],
  response: ['native', 'off'], refs: ['multipart', 'json', 'off'],
}
const capHasValues: (keyof Caps)[] = ['size', 'aspect', 'resolution', 'quality', 'format', 'background', 'moderation']
// 各项常用可选值：点一下加入/移除，特殊值仍可手动输入
const capCommon: Partial<Record<keyof Caps, string[]>> = {
  aspect: ['1:1', '3:4', '4:3', '9:16', '16:9', '2:3', '3:2', '21:9'],
  size: ['auto', '1024x1024', '1536x1024', '1024x1536', '2048x2048', '1792x1024', '1024x1792'],
  resolution: ['1k', '2k', '4k'],
  quality: ['auto', 'low', 'medium', 'high'],
  format: ['png', 'jpeg', 'webp'],
  background: ['auto', 'transparent', 'opaque'],
  moderation: ['auto', 'low'],
}
const capStarter: Partial<Record<keyof Caps, string[]>> = {
  aspect: ['1:1', '3:4', '4:3', '9:16', '16:9'], size: ['1024x1024', '1536x1024', '1024x1536'], resolution: ['1k', '2k'],
  quality: ['low', 'medium', 'high'], format: ['png', 'jpeg', 'webp'], background: ['auto', 'transparent', 'opaque'], moderation: ['auto', 'low'],
}
const capOrder: (keyof Caps)[] = ['aspect', 'size', 'resolution', 'quality', 'n', 'format', 'compression', 'background', 'moderation', 'response', 'refs']

function ChannelEditor({ initial, onClose, onSaved }: { initial: Partial<Channel>; onClose: () => void; onSaved: () => void }) {
  const { toast } = useStore()
  const isNew = !initial.id
  const [presets, setPresets] = useState<Preset[]>([])
  const isText = initial.kind === 'text'
  const [ch, setCh] = useState<Partial<Channel>>(() => ({ name: '', base_url: '', api_key: '', api_type: isText ? 'chat' : 'images', models: [], caps: emptyCaps(), ...initial, ...(initial.id ? { api_key: '' } : {}) }))
  const [remote, setRemote] = useState<string[] | null>(null)
  const [probe, setProbe] = useState<{ busy: boolean; ok?: boolean; msg?: string }>({ busy: false })
  const [mq, setMq] = useState('')
  const [manual, setManual] = useState('')
  const [saving, setSaving] = useState(false)
  const [preset, setPreset] = useState('')

  useEffect(() => { if (!isText) api<Preset[]>('/api/presets').then(setPresets).catch(() => {}) }, [])
  const caps = (ch.caps || emptyCaps()) as Caps
  const setCap = (k: keyof Caps, patch: Partial<Cap>) => setCh(c => ({ ...c, caps: { ...(c.caps as Caps), [k]: { ...(c.caps as Caps)[k], ...patch } } }))

  const applyPreset = (p: Preset) => {
    setPreset(p.key)
    if (!isNew) {
      // 编辑已有渠道：只套用能力档案，不动名称、地址和模型
      setCh(c => ({ ...c, api_type: p.api_type, caps: JSON.parse(JSON.stringify(p.caps)) }))
      return
    }
    setCh(c => ({
      ...c,
      name: c.name || p.name,
      base_url: p.base_url || c.base_url,
      api_type: p.api_type,
      models: p.models && p.models.length ? (c.models?.length ? c.models : p.models.slice(0, 1)) : c.models,
      caps: JSON.parse(JSON.stringify(p.caps)),
    }))
    if (p.models?.length) setRemote(r => Array.from(new Set([...(r || []), ...p.models!])))
  }

  const doProbe = async () => {
    setProbe({ busy: true })
    try {
      const res = await api<{ ok: boolean; models?: string[]; error?: string; latency_ms?: number }>('/api/channels/probe', { body: { id: initial.id || 0, base_url: ch.base_url, api_key: ch.api_key } })
      if (res.ok) {
        setRemote(Array.from(new Set([...(res.models || []), ...(ch.models || [])])))
        setProbe({ busy: false, ok: true, msg: `连接正常 · ${res.latency_ms} 毫秒 · ${res.models?.length || 0} 个模型` })
      } else setProbe({ busy: false, ok: false, msg: res.error })
    } catch (e: any) { setProbe({ busy: false, ok: false, msg: e.message }) }
  }

  const toggleModel = (m: string) => setCh(c => ({ ...c, models: c.models?.includes(m) ? c.models.filter(x => x !== m) : [...(c.models || []), m] }))
  const allModels = useMemo(() => Array.from(new Set([...(remote || []), ...(ch.models || [])])), [remote, ch.models])
  const shownModels = allModels.filter(m => !mq || m.toLowerCase().includes(mq.toLowerCase()))

  const save = async () => {
    if (!ch.name?.trim() || !ch.base_url?.trim()) { toast('名称和接口地址不能为空', 'info'); return }
    setSaving(true)
    try {
      await api(isNew ? '/api/channels' : `/api/channels/${initial.id}`, { method: isNew ? 'POST' : 'PUT', body: ch })
      onSaved()
    } catch (e: any) { toast(e.message, 'err') }
    finally { setSaving(false) }
  }

  return (
    <Modal open onClose={onClose} wide title={isNew ? (isText ? '新建文本渠道' : '新建生图渠道') : '编辑渠道'} className="ch-editor"
      footer={<><button className="btn btn-secondary" onClick={onClose}>取消</button><button className="btn btn-primary" onClick={save} disabled={saving}>{saving && <Spinner />}保存</button></>}>
      {presets.length > 0 && (
        <div className="field">
          <span className="field-label">{isNew ? '快速填充' : '套用能力档案'}</span>
          <div className="presets">
            {presets.map(p => <button key={p.key} className={`preset ${preset === p.key ? 'is-on' : ''}`} onClick={() => applyPreset(p)}>{p.name}</button>)}
          </div>
        </div>
      )}
      <div className="form-grid">
        <label className="field"><span className="field-label">名称</span><input className="input" value={ch.name || ''} onChange={e => setCh({ ...ch, name: e.target.value })} /></label>
        {isText ? <div className="field"><span className="field-label">接口类型</span><Segmented value="chat" onChange={() => {}} options={[{ value: 'chat', label: '对话补全' }]} /></div> : <div className="field"><span className="field-label">接口类型</span>
          <Segmented value={ch.api_type || 'images'} onChange={v => setCh({ ...ch, api_type: v as any })} options={[{ value: 'images', label: apiTypeLabels.images }, { value: 'chat', label: apiTypeLabels.chat }]} />
        </div>}
        <label className="field span-2"><span className="field-label">接口地址</span><input className="input" value={ch.base_url || ''} onChange={e => setCh({ ...ch, base_url: e.target.value })} placeholder="https://example.com/v1" /></label>
        <label className="field span-2"><span className="field-label">密钥</span>
          <input className="input" type="password" autoComplete="new-password" value={ch.api_key || ''} onChange={e => setCh({ ...ch, api_key: e.target.value })} placeholder={initial.key_tail ? `已保存 ${initial.key_tail}，留空不修改` : 'sk-…'} />
        </label>
      </div>

      <div className="field">
        <div className="field-row">
          <span className="field-label">模型</span>
          <span className="grow" />
          {probe.msg && <span className={`probe ${probe.ok ? 'ok' : 'err'}`}>{probe.ok ? <Check size={13} /> : <X size={13} />}{probe.msg}</span>}
          <button className="btn btn-secondary btn-sm" onClick={doProbe} disabled={probe.busy || !ch.base_url}>{probe.busy ? <Spinner size={13} /> : <RefreshCw size={13} />}测试连接并拉取</button>
        </div>
        <div className="models">
          {allModels.length > 8 && <div className="search sm"><Search size={14} /><input value={mq} onChange={e => setMq(e.target.value)} placeholder="筛选模型" /></div>}
          <div className="model-list">
            {shownModels.map(m => (
              <button key={m} className={`model ${ch.models?.includes(m) ? 'is-on' : ''}`} onClick={() => toggleModel(m)}>
                <span className="cbox">{ch.models?.includes(m) && <Check size={11} strokeWidth={3} />}</span><span >{m}</span>
              </button>
            ))}
            {allModels.length === 0 && <span className="muted sm-text">暂无模型</span>}
          </div>
          <div className="model-add">
            <input className="input" value={manual} onChange={e => setManual(e.target.value)} placeholder="手动添加模型名"
              onKeyDown={e => { if (e.key === 'Enter' && manual.trim()) { const m = manual.trim(); setCh(c => ({ ...c, models: Array.from(new Set([...(c.models || []), m])) })); setManual('') } }} />
            <button className="btn btn-secondary btn-sm" onClick={() => { const m = manual.trim(); if (m) { setCh(c => ({ ...c, models: Array.from(new Set([...(c.models || []), m])) })); setManual('') } }}><Plus size={13} />添加</button>
          </div>
        </div>
      </div>

      {!isText && <div className="field">
        <span className="field-label">能力档案</span>
        <div className="caps">
          {capOrder.map(k => {
            const cap = caps[k] || { mode: 'off' }
            const off = cap.mode === 'off'
            return (
              <div key={k} className={`cap ${off ? 'is-off' : ''}`}>
                <div className="cap-name">{capLabels[k]}</div>
                <div className="cap-mode">
                  <Segmented size="sm" value={cap.mode} onChange={v => {
                    const empty = !(cap.values || []).length
                    const start = v !== 'off' && empty && capStarter[k] ? capStarter[k]! : null
                    setCap(k, start ? { mode: v as any, values: start, default: start[0] } : { mode: v as any })
                  }} options={capModes[k].map(m => ({ value: m, label: modeLabels[m] }))} />
                </div>
                <div className="cap-detail">
                  {!off && capHasValues.includes(k) && (
                    <div className="cap-values">
                      <TagInput value={cap.values || []} onChange={values => setCap(k, { values, default: values.includes(cap.default || '') ? cap.default : values[0] })} placeholder="可选值" />
                      {(cap.values || []).length > 1 && (
                        <Select size="sm" className="cap-default" value={cap.default || cap.values![0]} onChange={v => setCap(k, { default: v })}
                          options={(cap.values || []).map(v => ({ value: v, label: '默认 ' + valueLabel(k, v) }))} />
                      )}
                    </div>
                  )}
                  {!off && (k === 'n' || k === 'refs') && (
                    <label className="cap-max"><span>上限</span><input className="input" type="number" min={k === 'n' ? 1 : 0} max={k === 'n' ? 16 : 32} value={cap.max ?? 0} onChange={e => setCap(k, { max: Number(e.target.value) })} /></label>
                  )}
                  {!off && k === 'response' && (
                    <Segmented size="sm" value={cap.values?.[0] || 'url'} onChange={v => setCap(k, { values: [v] })} options={[{ value: 'url', label: '链接' }, { value: 'b64_json', label: '内嵌数据' }]} />
                  )}
                </div>
                {!off && capCommon[k] && (
                  <div className="cap-quick">
                    {capCommon[k]!.map(v => {
                      const vs = cap.values || []
                      const has = vs.includes(v)
                      return (
                        <button key={v} type="button" className={`qchip ${has ? 'is-on' : ''}`} onClick={() => {
                          const values = has ? vs.filter(x => x !== v) : [...vs, v]
                          setCap(k, { values, default: values.includes(cap.default || '') ? cap.default : values[0] })
                        }}>{has && <Check size={12} strokeWidth={3} />}{valueLabel(k, v)}</button>
                      )
                    })}
                  </div>
                )}
              </div>
            )
          })}
        </div>
      </div>}
    </Modal>
  )
}
