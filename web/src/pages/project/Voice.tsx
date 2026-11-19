import { useEffect, useMemo, useRef, useState } from 'react'
import { Check, Pencil, Plus, Search, Sparkles, Trash2, Upload, Wand2, AudioLines } from 'lucide-react'
import { api, upload, type Channel, type FishItem, type VoiceSpec } from '../../api'
import { useStore } from '../../store'
import { Empty, Modal, Segmented, Select, Spinner, useMedia } from '../../components/ui'
import { PlayBtn, Recorder } from '../../components/audio'
import { voiceLabel, voiceModeNames } from '../../labels'

export function VoiceButton({ value, name, onSave, assetId, icon }: { value: VoiceSpec; name: string; onSave: (v: VoiceSpec) => Promise<any> | void; assetId?: number; icon?: React.ReactNode }) {
  const [open, setOpen] = useState(false)
  const label = voiceLabel(value)
  return (
    <>
      <button type="button" className={`vbtn ${label ? 'is-set' : ''}`} onClick={() => setOpen(true)} title={label || '设置音色'}>
        {icon || <AudioLines size={14} />}<span>{label || '设置音色'}</span>
      </button>
      {open && <VoicePanel value={value} name={name} assetId={assetId} onClose={() => setOpen(false)} onSave={async v => { await onSave(v); setOpen(false) }} />}
    </>
  )
}

type Tab = 'design' | 'fish' | 'clone' | 'builtin' | 'openai'

export function VoicePanel({ value, name, assetId, onClose, onSave }: { value: VoiceSpec; name: string; assetId?: number; onClose: () => void; onSave: (v: VoiceSpec) => void }) {
  const { channels, go, toast } = useStore()
  const vchs = channels.filter(c => c.kind === 'voice')
  const mimo = vchs.filter(c => c.api_type === 'mimo')
  const fish = vchs.filter(c => c.api_type === 'fish')
  const oai = vchs.filter(c => c.api_type === 'openai')
  const narrow = useMedia('(max-width: 720px)')
  const tabs = ([
    { value: 'design', label: narrow ? '设计' : '设计音色', ok: mimo.length > 0 },
    { value: 'fish', label: '音色库', ok: fish.length > 0 },
    { value: 'clone', label: narrow ? '克隆' : '克隆音色', ok: mimo.length + fish.length > 0 },
    { value: 'builtin', label: narrow ? '内置' : '内置音色', ok: mimo.length > 0 },
    { value: 'openai', label: narrow ? '渠道' : '渠道音色', ok: oai.length > 0 },
  ] as const).filter(t => t.ok)
  const initTab = (): Tab => {
    const m = value.mode === 'design' ? 'design' : value.mode === 'clone' ? 'clone' : value.mode === 'fish' ? 'fish' : value.mode === 'builtin' ? 'builtin' : value.mode === 'openai' ? 'openai' : 'design'
    return (tabs.find(t => t.value === m)?.value || tabs[0]?.value || 'design') as Tab
  }
  const [tab, setTab] = useState<Tab>(initTab)
  const [v, setV] = useState<VoiceSpec>({ speed: 1, pitch: 0, ...value })
  const [text, setText] = useState(value.sample_text || '')
  const [pv, setPv] = useState<{ busy: boolean; url?: string }>({ busy: false })
  const chOf = (id?: number) => vchs.find(c => c.id === id)
  const cur = chOf(v.channel_id)
  const ready = !!v.mode && !!(v.voice || v.sample)
  const preview = async () => {
    setPv({ busy: true })
    try { const r = await api<{ url: string }>('/api/voice/preview', { body: { voice: v, text } }); setPv({ busy: false, url: r.url }); new Audio(r.url).play().catch(() => {}) } catch (e: any) { toast(e.message, 'err'); setPv({ busy: false }) }
  }
  if (vchs.length === 0) return (
    <Modal open onClose={onClose} title={`${name}的音色`}>
      <Empty art="plug" title="还没有语音渠道" action={<button className="btn btn-primary" onClick={() => { sessionStorage.setItem('manju.chkind', 'voice'); onClose(); go({ page: 'channels' }) }}><Plus size={16} />添加语音渠道</button>} />
    </Modal>
  )
  return (
    <Modal open wide onClose={onClose} title={`${name}的音色`} className="voice-modal"
      footer={<>
        <div className="vcur"><span className="muted">当前</span><b title={voiceLabel(v)}>{!ready ? '未选择' : v.title || v.voice ? `${voiceModeNames[v.mode!]} · ${v.title || v.voice}` : voiceModeNames[v.mode!]}</b></div>
        <span className="grow" />
        <button className="btn btn-secondary" disabled={!ready || pv.busy} onClick={preview}>{pv.busy ? <Spinner /> : <AudioLines size={16} />}试听</button>
        <button className="btn btn-primary" disabled={!ready} onClick={() => onSave({ ...v, sample_text: text })}><Check size={16} />保存</button>
      </>}>
      <Segmented value={tab} onChange={x => setTab(x as Tab)} options={tabs.map(t => ({ value: t.value, label: t.label }))} />
      <div className="vbody">
        {tab === 'design' && <DesignTab chs={mimo} name={name} assetId={assetId} v={v} setV={setV} text={text} setText={setText} />}
        {tab === 'fish' && <FishTab chs={fish} v={v} setV={setV} />}
        {tab === 'clone' && <CloneTab chs={[...mimo, ...fish]} name={name} v={v} setV={setV} />}
        {tab === 'builtin' && <BuiltinTab chs={mimo} v={v} setV={setV} text={text} />}
        {tab === 'openai' && <OpenAITab chs={oai} v={v} setV={setV} />}
      </div>
      <div className="form-grid form-grid-3 vtune">
        <label className="field"><span className="field-label">语速</span>
          <div className="unit-input"><input className="input" type="number" min={0.5} max={2} step={0.05} value={v.speed ?? 1} onChange={e => setV({ ...v, speed: Number(e.target.value) })} /><span>倍</span></div>
        </label>
        <label className="field"><span className="field-label">音调</span>
          <div className="unit-input"><input className="input" type="number" min={-6} max={6} step={0.5} value={v.pitch ?? 0} onChange={e => setV({ ...v, pitch: Number(e.target.value) })} /><span>半音</span></div>
        </label>
        <label className="field"><span className="field-label">试听文字</span>
          <input className="input" value={text} onChange={e => setText(e.target.value)} placeholder="你好，很高兴认识你。" />
        </label>
      </div>
      {cur?.api_type === 'mimo' && (
        <label className="field"><span className="field-label">固定语气</span>
          <input className="input" value={v.style || ''} onChange={e => setV({ ...v, style: e.target.value })} placeholder="例如：说话干脆利落，句尾略上扬" />
        </label>
      )}
    </Modal>
  )
}

function ChannelPick({ chs, value, onChange }: { chs: Channel[]; value?: number; onChange: (id: number) => void }) {
  if (chs.length <= 1) return null
  return (
    <div className="field"><span className="field-label">语音渠道</span>
      <Select value={String(value || chs[0].id)} onChange={x => onChange(Number(x))} options={chs.map(c => ({ value: String(c.id), label: c.name }))} />
    </div>
  )
}

function DesignTab({ chs, name, assetId, v, setV, text, setText }: { chs: Channel[]; name: string; assetId?: number; v: VoiceSpec; setV: (v: VoiceSpec) => void; text: string; setText: (s: string) => void }) {
  const { toast } = useStore()
  const [ch, setCh] = useState(chs.find(c => c.id === v.channel_id)?.id || chs[0]?.id)
  const [desc, setDesc] = useState(v.mode === 'design' ? v.desc || '' : '')
  const [drafting, setDrafting] = useState(false)
  const [busy, setBusy] = useState(false)
  const [cands, setCands] = useState<{ sample: string; url: string; duration: number }[]>([])
  const draft = async () => {
    if (!assetId) return
    setDrafting(true)
    try { const r = await api<{ desc: string; sample_text: string }>(`/api/assets/${assetId}/voice/describe`, { method: 'POST' }); setDesc(r.desc); if (!text) setText(r.sample_text) } catch (e: any) { toast(e.message, 'err') } finally { setDrafting(false) }
  }
  useEffect(() => { if (!desc && assetId) draft() }, [])
  const gen = async () => {
    setBusy(true)
    try { const r = await api<{ candidates: any[]; channel_id: number }>('/api/voice/design', { body: { channel_id: ch, desc, text, n: 3 } }); setCands(r.candidates); setCh(r.channel_id) } catch (e: any) { toast(e.message, 'err') } finally { setBusy(false) }
  }
  return (
    <div className="stack">
      <ChannelPick chs={chs} value={ch} onChange={setCh} />
      <div className="field"><span className="field-label">音色描述</span>
        <div className={`desc-wrap ${drafting ? 'is-busy' : ''}`}>
          <textarea className="input" rows={3} value={desc} onChange={e => setDesc(e.target.value)} placeholder="性别、年龄感、音色质感、语速、性格、口音" />
          {drafting && <div className="desc-busy"><Spinner size={14} /><span>正在根据「{name}」起草</span></div>}
        </div>
        <div className="row-actions">
          {assetId && <button className="btn btn-ghost btn-sm" disabled={drafting} onClick={draft}><Wand2 size={14} />AI 起草</button>}
          <span className="grow" />
          <button className="btn btn-primary btn-sm" disabled={busy || !desc.trim()} onClick={gen}>{busy ? <Spinner size={13} /> : <Sparkles size={14} />}生成候选<span className="cost">3 个</span></button>
        </div>
      </div>
      {(cands.length > 0 || (v.mode === 'design' && v.sample)) && (
        <div className="vcands">
          {v.mode === 'design' && v.sample && !cands.some(c => c.sample === v.sample) && (
            <div className="vcand is-on"><PlayBtn src={`/files/audio/${v.sample}`} /><span>当前音色</span><span className="grow" /><Check size={14} /></div>
          )}
          {cands.map((c, i) => {
            const on = v.mode === 'design' && v.sample === c.sample
            return (
              <button key={c.sample} type="button" className={`vcand ${on ? 'is-on' : ''}`} onClick={() => setV({ ...v, channel_id: ch, mode: 'design', sample: c.sample, desc, voice: '', title: `${name}的声音`, sample_text: text })}>
                <PlayBtn src={c.url} /><span>候选 {i + 1}</span><span className="muted">{c.duration.toFixed(1)} 秒</span><span className="grow" />{on ? <Check size={14} /> : <span className="vpick">选用</span>}
              </button>
            )
          })}
        </div>
      )}
    </div>
  )
}

function FishTab({ chs, v, setV }: { chs: Channel[]; v: VoiceSpec; setV: (v: VoiceSpec) => void }) {
  const { toast } = useStore()
  const [ch, setCh] = useState(chs.find(c => c.id === v.channel_id)?.id || chs[0]?.id)
  const [scope, setScope] = useState<'public' | 'mine'>('public')
  const [q, setQ] = useState('')
  const [items, setItems] = useState<FishItem[] | null>(null)
  const [page, setPage] = useState(1)
  const [more, setMore] = useState(false)
  const [creating, setCreating] = useState(false)
  const load = async (pg = 1, append = false) => {
    if (!append) setItems(null)
    try {
      const r = await api<{ items: FishItem[]; has_more: boolean }>(`/api/voice/fish?channel_id=${ch}&page=${pg}&q=${encodeURIComponent(q)}${scope === 'mine' ? '&self=1' : ''}`)
      setItems(x => append && x ? [...x, ...r.items] : r.items); setMore(r.has_more); setPage(pg)
    } catch (e: any) { toast(e.message, 'err'); setItems([]) }
  }
  useEffect(() => { load(1) }, [scope, ch])
  const pick = (it: FishItem) => setV({ ...v, channel_id: ch, mode: 'fish', voice: it.id, title: it.title, cover: it.cover, preview: it.sample, sample: '' })
  const rename = async (it: FishItem) => {
    const t = prompt('新名称', it.title)
    if (!t?.trim()) return
    try { await api(`/api/voice/fish/${it.id}`, { method: 'PUT', body: { channel_id: ch, title: t.trim() } }); load(1) } catch (e: any) { toast(e.message, 'err') }
  }
  const del = async (it: FishItem) => {
    if (!confirm(`删除音色「${it.title}」？`)) return
    try { await api(`/api/voice/fish/${it.id}?channel_id=${ch}`, { method: 'DELETE' }); load(1) } catch (e: any) { toast(e.message, 'err') }
  }
  return (
    <div className="stack">
      <ChannelPick chs={chs} value={ch} onChange={setCh} />
      <div className="vtool">
        <Segmented block={false} size="sm" value={scope} onChange={x => setScope(x as any)} options={[{ value: 'public', label: '公开音色' }, { value: 'mine', label: '我的音色' }]} />
        <div className="search vsearch"><Search size={14} /><input value={q} onChange={e => setQ(e.target.value)} onKeyDown={e => e.key === 'Enter' && load(1)} placeholder="搜索音色" /></div>
        {scope === 'mine' && <button className="btn btn-secondary btn-sm" onClick={() => setCreating(true)}><Plus size={14} />新建音色</button>}
      </div>
      {!items ? <div className="loading"><Spinner /></div> : items.length === 0 ? <div className="vempty">{scope === 'mine' ? '还没有自己的音色' : '没有找到'}</div> : (
        <div className="fgrid">
          {items.map(it => {
            const on = v.mode === 'fish' && v.voice === it.id
            return (
              <div key={it.id} className={`fitem ${on ? 'is-on' : ''}`} onClick={() => pick(it)} title={it.desc || it.title}>
                <div className="fcover">{it.cover ? <img src={it.cover} alt="" loading="lazy" /> : <AudioLines size={16} />}</div>
                <div className="fmain">
                  <b>{it.title}</b>
                  <span className="muted">{it.mine ? (it.state === 'trained' ? '可用' : '处理中') : `${fmtUses(it.uses)} 次使用`}</span>
                </div>
                <span onClick={e => e.stopPropagation()} className="fact">
                  <PlayBtn src={it.sample} />
                  {it.mine && <button className="icon-btn sm" onClick={() => rename(it)} aria-label="改名" title="改名"><Pencil size={13} /></button>}
                  {it.mine && <button className="icon-btn sm is-danger" onClick={() => del(it)} aria-label="删除" title="删除"><Trash2 size={13} /></button>}
                </span>
              </div>
            )
          })}
        </div>
      )}
      {more && items && items.length > 0 && <button className="btn btn-ghost btn-sm vmore" onClick={() => load(page + 1, true)}>加载更多</button>}
      {creating && <FishCreate ch={ch!} onClose={() => setCreating(false)} onCreated={it => { setCreating(false); setV({ ...v, channel_id: ch, mode: 'fish', voice: it.id, title: it.title, sample: '' }); setScope('mine'); load(1) }} />}
    </div>
  )
}

function fmtUses(n: number) { return n >= 10000 ? (n / 10000).toFixed(n >= 100000 ? 0 : 1) + ' 万' : String(n) }

type Sample = { blob: Blob; name: string; url: string; text: string }

function SampleList({ samples, setSamples, multi }: { samples: Sample[]; setSamples: (s: Sample[]) => void; multi: boolean }) {
  const file = useRef<HTMLInputElement>(null)
  const add = (b: Blob, name: string) => setSamples(multi ? [...samples, { blob: b, name, url: URL.createObjectURL(b), text: '' }] : [{ blob: b, name, url: URL.createObjectURL(b), text: '' }])
  return (
    <div className="stack">
      <div className="row-actions">
        <Recorder onDone={b => add(b, `录音 ${samples.length + 1}`)} />
        <button type="button" className="btn btn-secondary" onClick={() => file.current?.click()}><Upload size={16} />上传音频</button>
        <input ref={file} type="file" accept="audio/*,video/webm" multiple={multi} hidden onChange={e => { Array.from(e.target.files || []).forEach(f => add(f, f.name)); e.target.value = '' }} />
      </div>
      {samples.map((s, i) => (
        <div key={s.url} className="srow">
          <PlayBtn src={s.url} />
          <span className="sname" title={s.name}>{s.name}</span>
          <input className="input" value={s.text} onChange={e => setSamples(samples.map((x, k) => k === i ? { ...x, text: e.target.value } : x))} placeholder="这段录音说的话（可选）" />
          <button className="icon-btn sm is-danger" onClick={() => setSamples(samples.filter((_, k) => k !== i))} aria-label="移除" title="移除"><Trash2 size={13} /></button>
        </div>
      ))}
    </div>
  )
}

function FishCreate({ ch, onClose, onCreated }: { ch: number; onClose: () => void; onCreated: (it: { id: string; title: string }) => void }) {
  const { toast } = useStore()
  const [title, setTitle] = useState('')
  const [samples, setSamples] = useState<Sample[]>([])
  const [busy, setBusy] = useState(false)
  const create = async () => {
    setBusy(true)
    try {
      const fd = new FormData()
      fd.append('channel_id', String(ch)); fd.append('title', title.trim())
      samples.forEach((s, i) => { fd.append('voices', s.blob, `s${i}.${s.blob.type.includes('webm') ? 'webm' : 'audio'}`); fd.append('texts', s.text) })
      const r = await api<{ id: string; title: string }>('/api/voice/fish', { raw: fd })
      toast('音色已创建'); onCreated(r)
    } catch (e: any) { toast(e.message, 'err') } finally { setBusy(false) }
  }
  return (
    <Modal open onClose={onClose} title="新建音色"
      footer={<><button className="btn btn-secondary" onClick={onClose}>取消</button><button className="btn btn-primary" disabled={busy || !title.trim() || samples.length === 0} onClick={create}>{busy && <Spinner />}创建</button></>}>
      <label className="field"><span className="field-label">名称</span><input className="input" autoFocus value={title} onChange={e => setTitle(e.target.value)} /></label>
      <div className="field"><span className="field-label">声音样本</span><SampleList samples={samples} setSamples={setSamples} multi /></div>
    </Modal>
  )
}

function CloneTab({ chs, name, v, setV }: { chs: Channel[]; name: string; v: VoiceSpec; setV: (v: VoiceSpec) => void }) {
  const { toast } = useStore()
  const [ch, setCh] = useState(chs.find(c => c.id === v.channel_id)?.id || chs[0]?.id)
  const [samples, setSamples] = useState<Sample[]>([])
  const [busy, setBusy] = useState(false)
  const chan = chs.find(c => c.id === ch)
  const make = async () => {
    setBusy(true)
    try {
      if (chan?.api_type === 'fish') {
        const fd = new FormData()
        fd.append('channel_id', String(ch)); fd.append('title', `${name}的声音`)
        samples.forEach((s, i) => { fd.append('voices', s.blob, `s${i}.audio`); fd.append('texts', s.text) })
        const r = await api<{ id: string; title: string }>('/api/voice/fish', { raw: fd })
        setV({ ...v, channel_id: ch, mode: 'fish', voice: r.id, title: r.title, sample: '' })
      } else {
        const r = await upload<{ sample: string }>('/api/voice/sample', samples[0].blob)
        setV({ ...v, channel_id: ch, mode: 'clone', sample: r.sample, voice: '', title: `${name}的声音`, sample_text: samples[0].text })
      }
      toast('已生成音色，试听后保存')
    } catch (e: any) { toast(e.message, 'err') } finally { setBusy(false) }
  }
  return (
    <div className="stack">
      {chs.length > 1 && <div className="field"><span className="field-label">语音渠道</span>
        <Select value={String(ch)} onChange={x => { setCh(Number(x)); setSamples([]) }} options={chs.map(c => ({ value: String(c.id), label: c.name }))} />
      </div>}
      <SampleList samples={samples} setSamples={setSamples} multi={chan?.api_type === 'fish'} />
      {v.mode === 'clone' && v.sample && <div className="vcand is-on"><PlayBtn src={`/files/audio/${v.sample}`} /><span>当前样本</span><span className="grow" /><Check size={14} /></div>}
      <div className="row-actions"><span className="grow" /><button className="btn btn-primary btn-sm" disabled={busy || samples.length === 0} onClick={make}>{busy ? <Spinner size={13} /> : <Wand2 size={14} />}用这些样本克隆</button></div>
    </div>
  )
}

function BuiltinTab({ chs, v, setV, text }: { chs: Channel[]; v: VoiceSpec; setV: (v: VoiceSpec) => void; text: string }) {
  const [ch, setCh] = useState(chs.find(c => c.id === v.channel_id)?.id || chs[0]?.id)
  const [list, setList] = useState<{ id: string; label: string; hint: string }[]>([])
  const [pv, setPv] = useState<Record<string, string>>({})
  const [busy, setBusy] = useState('')
  const { toast } = useStore()
  useEffect(() => { api<{ mimo_builtin: any[] }>('/api/voice/meta').then(r => setList(r.mimo_builtin)) }, [])
  const listen = async (id: string) => {
    if (pv[id]) { new Audio(pv[id]).play(); return }
    setBusy(id)
    try { const r = await api<{ url: string }>('/api/voice/preview', { body: { voice: { channel_id: ch, mode: 'builtin', voice: id }, text } }); setPv({ ...pv, [id]: r.url }); new Audio(r.url).play().catch(() => {}) } catch (e: any) { toast(e.message, 'err') } finally { setBusy('') }
  }
  return (
    <div className="stack">
      <ChannelPick chs={chs} value={ch} onChange={setCh} />
      <div className="bgrid">
        {list.map(b => {
          const on = v.mode === 'builtin' && v.voice === b.id && v.channel_id === ch
          return (
            <div key={b.id} className={`fitem ${on ? 'is-on' : ''}`} onClick={() => setV({ ...v, channel_id: ch, mode: 'builtin', voice: b.id, title: b.label, sample: '' })}>
              <div className="fmain"><b>{b.label}</b><span className="muted">{b.hint}</span></div>
              <span className="fact" onClick={e => e.stopPropagation()}><button className="icon-btn sm" onClick={() => listen(b.id)} aria-label="试听" title="试听">{busy === b.id ? <Spinner size={13} /> : <AudioLines size={14} />}</button></span>
            </div>
          )
        })}
      </div>
    </div>
  )
}

function OpenAITab({ chs, v, setV }: { chs: Channel[]; v: VoiceSpec; setV: (v: VoiceSpec) => void }) {
  const [ch, setCh] = useState(chs.find(c => c.id === v.channel_id)?.id || chs[0]?.id)
  const chan = chs.find(c => c.id === ch)
  const voices = useMemo(() => chan?.voices?.length ? chan.voices : ['alloy', 'echo', 'fable', 'onyx', 'nova', 'shimmer'], [chan])
  return (
    <div className="stack">
      <ChannelPick chs={chs} value={ch} onChange={setCh} />
      <div className="chips">
        {voices.map(x => <button key={x} type="button" className={`chip ${v.mode === 'openai' && v.voice === x && v.channel_id === ch ? 'is-on' : ''}`} onClick={() => setV({ ...v, channel_id: ch, mode: 'openai', voice: x, title: x, sample: '' })}>{x}</button>)}
      </div>
    </div>
  )
}
