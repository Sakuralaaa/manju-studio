import { useEffect, useRef, useState } from 'react'
import { Check, CloudUpload, ExternalLink, FileJson, KeyRound, Play, Power, RefreshCw, RotateCcw, Terminal, Trash2, X } from 'lucide-react'
import { api, type VCCfg, type ColabCfg, type ColabState, type LedgerRow, type LedgerTotal, type Video, type WorkflowItem } from '../api'
import { useStore } from '../store'
import { Empty, Segmented, Select, Spinner, copyText, useInterval, useMedia } from '../components/ui'
import { PageHead } from './Projects'
import { accName, fmtBytes, fmtClock, fmtCU, fmtTime, phaseLabels } from '../labels'
import { useColab, estimate } from './project/Videos'

type Tab = 'overview' | 'queue' | 'ledger' | 'schedule' | 'model'
const tabs = [{ value: 'overview', label: '概览' }, { value: 'queue', label: '队列' }, { value: 'ledger', label: '账本' }, { value: 'schedule', label: '调度' }, { value: 'model', label: '视频模型' }]
const shapeLabel: Record<string, string> = { SHAPE_STANDARD: '标准', SHAPE_HIGHMEM: '高内存' }
const variantLabel: Record<string, string> = { VARIANT_CPU: 'CPU', VARIANT_GPU: 'GPU', VARIANT_TPU: 'TPU' }

export default function Compute() {
  const [tab, setTab] = useState<Tab>(() => (sessionStorage.getItem('manju.ctab') as Tab) || 'overview')
  const pick = (t: Tab) => { sessionStorage.setItem('manju.ctab', t); setTab(t) }
  const { st, reload } = useColab(3000)
  return (
    <div className="page page-narrow">
      <PageHead title="算力">
        <Segmented block={false} value={tab} onChange={v => pick(v as Tab)} options={tabs} />
      </PageHead>
      {!st ? <div className="loading"><Spinner size={20} /></div> :
        tab === 'overview' ? <Overview st={st} reload={reload} /> : tab === 'queue' ? <Queue st={st} /> : tab === 'ledger' ? <Ledger /> : tab === 'schedule' ? <Schedule st={st} reload={reload} /> : <Models />}
    </div>
  )
}

function Overview({ st, reload }: { st: ColabState; reload: () => void }) {
  return (
    <div className="stack">
      <Account st={st} reload={reload} />
      {st.authorized && <RuntimeCard st={st} reload={reload} />}
      {st.authorized && st.info && <Specs st={st} />}
      {st.sessions.length > 0 && <Sessions st={st} />}
    </div>
  )
}

function Account({ st, reload }: { st: ColabState; reload: () => void }) {
  const { toast } = useStore()
  const file = useRef<HTMLInputElement>(null)
  const [authUrl, setAuthUrl] = useState('')
  const [back, setBack] = useState('')
  const [busy, setBusy] = useState(false)
  const act = async (fn: () => Promise<any>, ok?: string) => {
    setBusy(true)
    try { await fn(); if (ok) toast(ok); reload() } catch (e: any) { toast(e.message, 'err') } finally { setBusy(false) }
  }
  const onFile = async (f: File) => act(async () => { await api('/api/colab/client', { body: { json: await f.text() } }) }, '客户端已保存')
  const start = () => act(async () => {
    const r = await api<{ url: string }>('/api/colab/auth/start', { method: 'POST' })
    setAuthUrl(r.url)
    window.open(r.url, '_blank', 'noopener')
  })
  const finish = () => act(async () => { await api('/api/colab/auth/finish', { body: { url: back } }); setBack(''); setAuthUrl('') }, '授权成功')
  const step = !st.client ? 1 : !st.authorized || authUrl ? 2 : 3
  return (
    <section className="card gcard">
      <div className="gcard-head">
        <h3>Google 账号</h3>
        <span className="grow" />
        {st.authorized && st.info && <span className="pill-tag is-ok"><Check size={12} strokeWidth={3} />{st.info.tier_label}</span>}
      </div>
      {st.authorized && !authUrl && st.drive?.enabled && !st.drive.scope_ok && (
        <div className="inline-warn"><span>权重缓存需要云端硬盘权限，重新授权一次</span><button className="btn btn-secondary btn-sm" disabled={busy} onClick={start}><KeyRound size={14} />重新授权</button></div>
      )}
      {st.authorized && !authUrl ? (
        <div className="kv-rows">
          <div className="kv-row"><span>订阅</span><b>{st.info?.tier_label || '—'}</b></div>
          <div className="kv-row"><span>客户端</span><b>{st.client?.project || st.client?.id_tail}</b></div>
          <div className="kv-row"><span>刷新于</span><b>{st.info?.fetched_at ? fmtTime(st.info.fetched_at) : '—'}</b></div>
          {st.info?.error && <div className="kv-row"><span>状态</span><b className="danger-text">{st.info.error}</b></div>}
          <div className="row-actions">
            <button className="btn btn-secondary btn-sm" disabled={busy} onClick={() => act(() => api('/api/colab/refresh', { method: 'POST' }), '已刷新')}><RefreshCw size={14} />刷新</button>
            <button className="btn btn-secondary btn-sm" disabled={busy} onClick={start}><KeyRound size={14} />重新授权</button>
            <span className="grow" />
            <button className="btn btn-ghost btn-sm is-danger" disabled={busy} onClick={() => confirm('移除客户端和授权？') && act(() => api('/api/colab/client', { method: 'DELETE' }))}><Trash2 size={14} />移除</button>
          </div>
        </div>
      ) : (
        <ol className="auth-steps">
          <li className={step > 1 ? 'is-done' : 'is-on'}>
            <span className="auth-no">{step > 1 ? <Check size={13} strokeWidth={3} /> : 1}</span>
            <div className="auth-body">
              <b>上传 OAuth 客户端文件</b>
              <span className="muted">桌面应用类型的客户端 JSON</span>
              <div className="row-actions">
                <button className="btn btn-secondary btn-sm" disabled={busy} onClick={() => file.current?.click()}><FileJson size={14} />{st.client ? '更换文件' : '选择文件'}</button>
                {st.client && <span className="muted sm-text">已保存 {st.client.project || st.client.id_tail}</span>}
              </div>
              <input ref={file} type="file" accept=".json,application/json" hidden onChange={e => { const f = e.target.files?.[0]; if (f) onFile(f); e.target.value = '' }} />
            </div>
          </li>
          <li className={step === 2 ? 'is-on' : step > 2 ? 'is-done' : ''}>
            <span className="auth-no">2</span>
            <div className="auth-body">
              <b>{st.drive?.enabled ? '在 Google 授权页同意访问 Colab 和云端硬盘' : '在 Google 授权页同意访问 Colab'}</b>
              <span className="muted">同意后浏览器会跳到一个打不开的 localhost 地址，复制整条地址</span>
              <div className="row-actions">
                <button className="btn btn-secondary btn-sm" disabled={busy || !st.client} onClick={start}><ExternalLink size={14} />打开授权页</button>
                {authUrl && <button className="btn btn-ghost btn-sm" onClick={async () => { await copyText(authUrl); toast('已复制授权链接') }}>复制链接</button>}
              </div>
            </div>
          </li>
          <li className={step === 2 && authUrl ? 'is-on' : ''}>
            <span className="auth-no">3</span>
            <div className="auth-body">
              <b>粘贴跳转后的地址</b>
              <div className="paste-row">
                <input className="input" value={back} onChange={e => setBack(e.target.value)} placeholder="http://localhost/?state=…&code=…" disabled={!st.client} />
                <button className="btn btn-primary" disabled={busy || !back.trim()} onClick={finish}>{busy ? <Spinner /> : <Check size={16} />}完成授权</button>
              </div>
            </div>
          </li>
        </ol>
      )}
    </section>
  )
}

function RuntimeCard({ st, reload }: { st: ColabState; reload: () => void }) {
  const { toast } = useStore()
  const r = st.runner
  const on = r.phase !== 'off'
  const [busy, setBusy] = useState(false)
  const [manualAcc, setManualAcc] = useState('NONE')
  const [logs, setLogs] = useState<string[]>([])
  const [showLog, setShowLog] = useState(false)
  const loadLog = () => api<{ lines: string[] }>('/api/colab/log').then(x => setLogs(x.lines)).catch(() => {})
  useEffect(() => { loadLog() }, [])
  useInterval(loadLog, on || showLog ? 3000 : null)
  const act = async (path: string, body?: any) => {
    setBusy(true)
    try { await api(path, { body: body ?? {} }); reload() } catch (e: any) { toast(e.message, 'err') } finally { setBusy(false) }
  }
  const gpus = (st.info?.specs || []).filter(s => s.eligible && s.key.variant === 'VARIANT_GPU')
  const manualOpts = [{ value: 'NONE', label: 'CPU' }, ...Array.from(new Set(gpus.map(g => g.key.accelerator))).map(a => ({ value: a, label: a }))]
  const est = estimate(st, Math.max(1, r.queued), false)
  const elapsed = on && r.started_at ? (r.server_now - r.started_at) / 1000 : 0
  return (
    <section className="card gcard">
      <div className="gcard-head">
        <h3>运行时</h3>
        <span className={`phase is-${r.phase}`}><i />{on ? `${accName(r.accelerator)}${r.purpose === 'cache' ? ' · 预缓存' : ''} · ${phaseLabels[r.phase]}` : phaseLabels.off}</span>
        <span className="grow" />
      </div>
      <div className="rt-stats">
        <div><b>{on ? fmtClock(elapsed) : '—'}</b><span>已运行</span></div>
        <div><b>{on ? fmtCU(r.cu_used) : '—'}</b><span>已用 CU</span></div>
        <div><b>{r.queued}</b><span>排队视频</span></div>
        <div><b>{on ? fmtClock(Math.max(0, r.max_left_sec)) : `${st.cfg.max_min} 分`}</b><span>{on ? '距最长时长' : '最长会话'}</span></div>
      </div>
      {(r.phase === 'booting' || r.phase === 'creating' || r.phase === 'connecting') && (
        <div className="boot">
          <div className="boot-label"><span>{r.stage || phaseLabels[r.phase]}</span><span className="grow" />{r.stage_pct > 0 && <span>{Math.round(r.stage_pct)}%</span>}</div>
          <div className="bar"><i style={{ width: `${r.stage_pct || (r.phase === 'booting' ? 4 : 2)}%` }} /></div>
        </div>
      )}
      {!on && r.message && <div className="inline-warn is-danger"><span title={r.message}>上次开机失败：{r.message}</span></div>}
      <div className="row-actions wrap">
        {on ? (
          <button className="btn btn-secondary" disabled={busy || r.phase === 'stopping'} onClick={() => confirm('确定关机？') && act('/api/colab/stop')}><Power size={16} />关机</button>
        ) : <>
          <button className="btn btn-primary" disabled={busy} onClick={() => act('/api/colab/start', { purpose: 'video' })}>
            <Play size={16} />开机渲染<span className="cost">{accName(st.cfg.accelerator)} · ≈ {fmtCU(est?.cu || 0)} CU</span>
          </button>
          <div className="split-act">
            <Select size="md" className="acc-sel" value={manualAcc} onChange={setManualAcc} options={manualOpts} />
            <button className="btn btn-secondary" disabled={busy} onClick={() => act('/api/colab/start', { purpose: 'manual', accelerator: manualAcc })}><Terminal size={16} />仅开机</button>
          </div>
        </>}
        <span className="grow" />
        <button className="btn btn-ghost btn-sm" onClick={() => setShowLog(!showLog)}>{showLog ? '收起日志' : '查看日志'}</button>
      </div>
      {showLog && <pre className="logbox">{logs.length ? logs.slice(-80).join('\n') : '暂无日志'}</pre>}
      {on && (r.phase === 'ready' || r.phase === 'running') && <Console />}
    </section>
  )
}

function Console() {
  const [code, setCode] = useState("import subprocess\nprint(subprocess.run('nvidia-smi -L || uname -a', shell=True, capture_output=True, text=True).stdout)")
  const [out, setOut] = useState<{ stdout: string; stderr: string; result: string; error?: string } | null>(null)
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')
  const exec = async () => {
    setBusy(true); setErr('')
    try { setOut(await api('/api/colab/exec', { body: { code } })) } catch (e: any) { setErr(e.message) } finally { setBusy(false) }
  }
  return (
    <div className="console">
      <div className="console-head"><Terminal size={14} /><span>运行代码</span><span className="grow" /><button className="btn btn-secondary btn-sm" disabled={busy || !code.trim()} onClick={exec}>{busy ? <Spinner size={13} /> : <Play size={14} />}运行</button></div>
      <textarea className="input console-in" rows={4} value={code} onChange={e => setCode(e.target.value)} spellCheck={false} />
      {(out || err) && <pre className={`logbox ${err || out?.error ? 'is-err' : ''}`}>{err || [out!.stdout, out!.stderr, out!.result && `→ ${out!.result}`, out!.error].filter(Boolean).join('\n') || '（无输出）'}</pre>}
    </div>
  )
}

function Specs({ st }: { st: ColabState }) {
  const specs = [...(st.info?.specs || [])].sort((a, b) => Number(!!b.eligible) - Number(!!a.eligible))
  return (
    <section className="card gcard">
      <div className="gcard-head"><h3>可用规格</h3><span className="muted">{specs.filter(s => s.eligible).length} / {specs.length}</span></div>
      <div className="specs">
        {specs.map((s, i) => (
          <div key={i} className={`spec ${s.eligible ? '' : 'is-off'} ${s.key.accelerator === st.cfg.accelerator && s.key.shape === st.cfg.shape ? 'is-default' : ''}`}>
            <div className="spec-top"><b>{accName(s.key.accelerator)}</b><span className="mini-tag">{variantLabel[s.key.variant] || s.key.variant}</span></div>
            <span className="spec-sub">{shapeLabel[s.key.shape] || s.key.shape} · {s.eligible ? `${fmtCU(st.cfg.rates[s.key.accelerator] ?? 0)} CU/时` : '不可用'}</span>
          </div>
        ))}
      </div>
    </section>
  )
}

function Sessions({ st }: { st: ColabState }) {
  return (
    <section className="card gcard">
      <div className="gcard-head"><h3>最近会话</h3></div>
      <div className="table">
        <div className="tr th"><span>开始</span><span>规格</span><span>时长</span><span>视频</span><span>CU</span><span className="col-reason">结束原因</span></div>
        {st.sessions.map(s => (
          <div key={s.id} className="tr">
            <span>{fmtTime(s.started_at)}</span>
            <span>{s.accelerator}{s.purpose === 'manual' ? ' · 代码' : s.purpose === 'cache' ? ' · 预缓存' : ''}</span>
            <span>{s.minutes < 1 ? '<1 分' : `${Math.round(s.minutes)} 分`}</span>
            <span>{s.jobs_done}</span>
            <span>{fmtCU(s.cu)}</span>
            <span className="col-reason" title={s.end_reason}>{s.status === 'ended' ? s.end_reason || '—' : '进行中'}</span>
          </div>
        ))}
      </div>
    </section>
  )
}

function Queue({ st }: { st: ColabState }) {
  const { toast } = useStore()
  const [data, setData] = useState<{ videos: Video[]; titles: Record<string, string>; queue_minutes: number } | null>(null)
  const load = () => api('/api/videos').then(setData).catch(() => {})
  useEffect(() => { load() }, [])
  useInterval(load, 3000)
  if (!data) return <div className="loading"><Spinner size={20} /></div>
  const statusText: Record<string, string> = { queued: '排队', waiting_prev: '等上一镜', running: '生成中', done: '完成', failed: '失败', canceled: '已取消' }
  const est = estimate(st, 0, false)
  return (
    <div className="stack">
      <section className="card gcard">
        <div className="gcard-head"><h3>视频队列</h3><span className="muted">{st.runner.queued} 个排队 · 预计 {Math.round(data.queue_minutes + (est?.mins || 0))} 分钟 · ≈ {fmtCU(((data.queue_minutes + (est?.mins || 0)) / 60) * (st.cfg.rates[est?.acc || 'G4'] ?? 0))} CU</span></div>
        {data.videos.length === 0 ? <Empty art="frame" title="队列是空的" /> : (
          <div className="qlist">
            {data.videos.map(v => (
              <div key={v.id} className={`qrow is-${v.status}`}>
                <div className="qthumb">{v.image_url && <img src={v.image_url} alt="" loading="lazy" />}</div>
                <div className="qmain">
                  <b title={v.prompt}>{v.project_id ? data.titles[v.project_id] || '作品' : '接口任务'} · #{v.id}</b>
                  <span className="muted" title={v.error || v.prompt}>{v.status === 'failed' ? v.error : `${v.seconds} 秒 · ${v.res}${v.turbo ? ' · Turbo' : ''} · ${fmtTime(v.created_at)}`}</span>
                </div>
                <div className="qstate">
                  {v.status === 'running' ? <><div className="bar sm"><i style={{ width: `${Math.round(v.progress * 100)}%` }} /></div><span>{Math.round(v.progress * 100)}%</span></> :
                    <span className={`qtag is-${v.status}`}>{v.status === 'queued' ? `第 ${v.position} 位` : statusText[v.status]}</span>}
                </div>
                {(v.status === 'queued' || v.status === 'waiting_prev' || v.status === 'running') ?
                  <button className="icon-btn sm" onClick={() => api(`/api/videos/${v.id}/cancel`, { method: 'POST' }).then(() => { toast('已取消'); load() })} aria-label="取消" title="取消"><X size={14} /></button> :
                  <span className="icon-gap" />}
              </div>
            ))}
          </div>
        )}
      </section>
    </div>
  )
}

function Schedule({ st, reload }: { st: ColabState; reload: () => void }) {
  const { toast } = useStore()
  const [c, setC] = useState<ColabCfg>(st.cfg)
  const save = async () => {
    try { await api('/api/colab/settings', { method: 'PUT', body: c }); toast('已保存'); reload() } catch (e: any) { toast(e.message, 'err') }
  }
  const [caching, setCaching] = useState(false)
  const precache = async () => {
    if (!confirm('用 CPU 运行时把还没缓存的模型文件下载并传到云端硬盘，完成后自动关机。开始？')) return
    setCaching(true)
    try { await api('/api/colab/start', { body: { purpose: 'cache' } }); toast('已开机，进度在概览里看'); reload() } catch (e: any) { toast(e.message, 'err') } finally { setCaching(false) }
  }
  const d = st.drive
  const missing = d ? Math.max(0, (d.need || 0) - (d.have || 0)) : 0
  const canCache = st.authorized && st.cfg.drive_cache && !!d?.scope_ok && st.runner.phase === 'off'
  const gpuOpts = Array.from(new Set([...(st.info?.specs || []).filter(s => s.eligible && s.key.variant === 'VARIANT_GPU').map(s => s.key.accelerator), c.accelerator])).map(a => ({ value: a, label: a }))
  const num = (k: keyof ColabCfg, label: string, unit: string, step = 1) => (
    <label className="field"><span className="field-label">{label}</span>
      <div className="unit-input"><input className="input" type="number" step={step} min={0} value={c[k] as number} onChange={e => setC({ ...c, [k]: Number(e.target.value) })} /><span>{unit}</span></div>
    </label>
  )
  return (
    <div className="stack">
      <section className="card gcard">
        <div className="gcard-head"><h3>开机与关机</h3></div>
        <div className="form-grid">
          <div className="field"><span className="field-label">视频用的加速器</span><Select value={c.accelerator} onChange={v => setC({ ...c, accelerator: v })} options={gpuOpts} /></div>
          <div className="field"><span className="field-label">形态</span><Segmented value={c.shape} onChange={v => setC({ ...c, shape: v })} options={[{ value: 'SHAPE_STANDARD', label: '标准' }, { value: 'SHAPE_HIGHMEM', label: '高内存' }]} /></div>
          {num('idle_min', '队列空闲多久关机', '分钟')}
          {num('max_min', '单次会话最长', '分钟')}
          {num('job_timeout_min', '单段视频超时', '分钟')}
          <div className="field"><span className="field-label">有排队视频时</span><Segmented value={c.auto_start ? 'auto' : 'manual'} onChange={v => setC({ ...c, auto_start: v === 'auto' })} options={[{ value: 'manual', label: '手动开机' }, { value: 'auto', label: '自动开机' }]} /></div>
        </div>
      </section>
      <section className="card gcard">
        <div className="gcard-head"><h3>估算</h3></div>
        <div className="form-grid form-grid-3">
          {num('boot_min', '开机与准备环境', '分钟', 0.5)}
          {num('clip_min', '每段（标准）', '分钟', 0.5)}
          {num('clip_min_turbo', '每段（Turbo）', '分钟', 0.5)}
          {num('upscale_min', '每段放大', '分钟', 0.5)}
          {num('interp_min', '每段补帧', '分钟', 0.5)}
        </div>
        <div className="rates">
          {Object.keys(c.rates).sort().map(k => (
            <label key={k} className="rate"><span>{accName(k)}</span><div className="unit-input"><input className="input" type="number" step={0.01} min={0} value={c.rates[k]} onChange={e => setC({ ...c, rates: { ...c.rates, [k]: Number(e.target.value) } })} /><span>CU/时</span></div></label>
          ))}
        </div>
      </section>
      <section className="card gcard">
        <div className="gcard-head"><h3>模型权重</h3>
          {st.drive?.files > 0 && <span className="muted">已缓存 {st.drive.files} 个文件 · {fmtBytes(st.drive.bytes)}</span>}
        </div>
        <div className="form-grid">
          <label className="field"><span className="field-label">下载镜像</span>
            <input className="input" value={c.mirror || ''} onChange={e => setC({ ...c, mirror: e.target.value })} placeholder="https://huggingface.co" />
          </label>
          <div className="field"><span className="field-label">云端硬盘缓存</span>
            <Segmented value={c.drive_cache ? 'on' : 'off'} onChange={v => setC({ ...c, drive_cache: v === 'on' })} options={[{ value: 'off', label: '关闭' }, { value: 'on', label: '开启' }]} />
          </div>
        </div>
        {c.drive_cache && st.drive && !st.drive.scope_ok && st.authorized && <div className="inline-warn"><span>需要在概览里重新授权，添加云端硬盘权限</span></div>}
        {st.cfg.drive_cache && d && d.need > 0 && (
          <div className="cache-row">
            <span className={missing ? '' : 'muted'}>{missing ? `当前模型文件 ${d.need} 个，还有 ${missing} 个没缓存` : `当前模型文件 ${d.need} 个，已全部缓存`}</span>
            <span className="grow" />
            {d.files > 0 && <button className="btn btn-ghost btn-sm" onClick={() => confirm('清空缓存记录？云端硬盘里的文件不会删除。') && api('/api/colab/drive/reset', { method: 'POST' }).then(reload)}>清空缓存记录</button>}
            {missing > 0 && <button className="btn btn-secondary btn-sm" disabled={!canCache || caching} title={st.runner.phase !== 'off' ? '先关掉当前运行时' : ''} onClick={precache}>{caching ? <Spinner size={13} /> : <CloudUpload size={14} />}预缓存</button>}
          </div>
        )}
        {st.cfg.drive_cache && missing > 0 && <p className="muted sm-text">预缓存用 CPU 运行时（约 {fmtCU(st.cfg.rates['NONE'] ?? 0.07)} CU/时）下载后上传，传完自动关机，之后开 GPU 直接从云端硬盘拉取。</p>}
        {!st.cfg.drive_cache && d && d.files > 0 && <div className="row-actions"><span className="grow" /><button className="btn btn-ghost btn-sm" onClick={() => confirm('清空缓存记录？云端硬盘里的文件不会删除。') && api('/api/colab/drive/reset', { method: 'POST' }).then(reload)}>清空缓存记录</button></div>}
      </section>
      <div className="row-actions"><span className="grow" /><button className="btn btn-primary" onClick={save}><Check size={16} />保存</button></div>
      <VoiceConvCard />
    </div>
  )
}

// 生成后统一音色的参数（Seed-VC + demucs，跑在视频运行时上）
function VoiceConvCard() {
  const { toast } = useStore()
  const [c, setC] = useState<VCCfg | null>(null)
  const [def, setDef] = useState<VCCfg | null>(null)
  useEffect(() => { api<{ cfg: VCCfg; defaults: VCCfg }>('/api/voice-conv/settings').then(r => { setC(r.cfg); setDef(r.defaults) }).catch(() => {}) }, [])
  if (!c || !def) return null
  const save = async (x: VCCfg) => {
    try { const r = await api<{ cfg: VCCfg }>('/api/voice-conv/settings', { method: 'PUT', body: x }); setC(r.cfg); toast('已保存') } catch (e: any) { toast(e.message, 'err') }
  }
  const num = (k: keyof VCCfg, label: string, step: number, min: number, max: number, unit = '') => (
    <label className="field"><span className="field-label">{label}</span>
      <div className="unit-input"><input className="input" type="number" step={step} min={min} max={max} value={c[k] as number} onChange={e => setC({ ...c, [k]: Number(e.target.value) })} /><span>{unit || ' '}</span></div>
    </label>
  )
  const onOff = (k: keyof VCCfg, label: string, on = '开启', off = '关闭') => (
    <div className="field"><span className="field-label">{label}</span>
      <Segmented value={c[k] ? 'on' : 'off'} onChange={v => setC({ ...c, [k]: v === 'on' })} options={[{ value: 'off', label: off }, { value: 'on', label: on }]} />
    </div>
  )
  return (
    <section className="card gcard">
      <div className="gcard-head"><h3>生成后统一音色</h3><span className="muted">demucs 分离人声 + Seed-VC，跑在视频运行时上</span></div>
      <p className="muted sm-text">作品设置或视频步骤里打开后，每条视频出来就把人声转成说话角色的音色母带（第一次会先装 Seed-VC / demucs，约几分钟，视频渲染期间就开始装；开了云端硬盘缓存时权重也会缓存）。每条约 1 分钟。</p>
      <div className="vcfg-grid">
        {num('diffusion_steps', '扩散步数', 1, 4, 100, '步')}
        {num('cfg_rate', 'CFG 强度', 0.05, 0, 1)}
        {onOff('f0', '音高条件 f0（较慢）', '开', '关')}
        {c.f0 && onOff('auto_f0', '自动对齐音高')}
        {c.f0 && num('semi_tone', '升降半音', 1, -12, 12, '半音')}
        {num('length_adjust', '语速倍数', 0.05, 0.5, 2, '×')}
        {onOff('separate_master', '母带先去掉配乐')}
        {num('max_master_sec', '母带最长', 1, 3, 60, '秒')}
        {num('vocal_gain', '转换后人声音量', 0.1, 0.1, 4, '×')}
        {onOff('auto_use', '转换完自动选用', '选用转换版', '保持原版')}
      </div>
      <div className="row-actions">
        <span className="muted sm-text">默认：{def.diffusion_steps} 步 · CFG {def.cfg_rate} · f0 关 · 语速 1×</span>
        <span className="grow" />
        <button className="btn btn-ghost btn-sm" onClick={() => save(def)}><RotateCcw size={14} />恢复默认</button>
        <button className="btn btn-secondary btn-sm" onClick={() => save(c)}><Check size={14} />保存</button>
      </div>
    </section>
  )
}

function Ledger() {
  const [d, setD] = useState<{ rows: LedgerRow[]; total: LedgerTotal; sessions: any[] } | null>(null)
  useEffect(() => { api<any>('/api/ledger').then(setD) }, [])
  if (!d) return <div className="loading"><Spinner /></div>
  const t = d.total
  const diff = t.est_cu > 0 ? (t.cu - t.est_cu) / t.est_cu : 0
  return (
    <div className="stack">
      <div className="rt-stats ledger-stats">
        <div><b>{fmtCU(t.cu)}</b><span>实际 CU</span></div>
        <div><b>{fmtCU(t.est_cu)}</b><span>估算 CU{t.est_cu > 0 ? ` · ${diff >= 0 ? '+' : ''}${Math.round(diff * 100)}%` : ''}</span></div>
        <div><b>{Math.round(t.gpu_min)}</b><span>视频 GPU 分钟</span></div>
        <div><b>{fmtCU(t.overhead_cu)}</b><span>开机与空闲 CU</span></div>
      </div>
      <section className="card gcard">
        <div className="gcard-head"><h3>按集</h3></div>
        {d.rows.length === 0 ? <Empty art="stack" title="还没有记录" /> : (
          <div className="table ltable">
            <div className="tr th"><span>作品</span><span>集</span><span>视频</span><span>GPU 分钟</span><span>实际 CU</span><span>估算 CU</span><span>生图</span><span>配音</span><span>对话</span></div>
            {d.rows.map(r => (
              <div key={`${r.project_id}-${r.episode_id}`} className="tr">
                <span title={r.title}>{r.title}</span><span title={r.episode}>{r.episode || '—'}</span><span>{r.videos}</span><span>{r.gpu_min.toFixed(1)}</span>
                <span>{fmtCU(r.cu)}</span><span className="muted">{fmtCU(r.est_cu)}</span><span>{r.images}</span><span>{r.tts}</span><span>{r.llm}</span>
              </div>
            ))}
          </div>
        )}
      </section>
      <section className="card gcard">
        <div className="gcard-head"><h3>会话</h3></div>
        {d.sessions.length === 0 ? <Empty art="stack" title="还没有会话" /> : (
          <div className="table stable">
            <div className="tr th"><span>开始</span><span>规格</span><span>总时长</span><span>开机</span><span>出视频</span><span>CU</span></div>
            {d.sessions.map(s => (
              <div key={s.id} className="tr">
                <span>{fmtTime(s.started_at)}</span><span>{s.accelerator}</span><span>{Math.round(s.minutes)} 分</span><span>{s.boot_min ? `${s.boot_min.toFixed(1)} 分` : '—'}</span>
                <span>{s.video_min ? `${s.video_min.toFixed(1)} 分` : '—'}</span><span>{fmtCU(s.cu)}</span>
              </div>
            ))}
          </div>
        )}
      </section>
    </div>
  )
}

function Models() {
  const narrow = useMedia('(max-width: 720px)')
  const { toast } = useStore()
  const [items, setItems] = useState<WorkflowItem[] | null>(null)
  const [key, setKey] = useState('standard')
  const [text, setText] = useState('')
  const load = () => api<WorkflowItem[]>('/api/workflows').then(x => { setItems(x); const cur = x.find(i => i.key === key) || x[0]; setText(cur.text) })
  useEffect(() => { load() }, [])
  if (!items) return <div className="loading"><Spinner size={20} /></div>
  const cur = items.find(i => i.key === key)!
  const pick = (k: string) => { setKey(k); setText(items.find(i => i.key === k)!.text) }
  const pretty = () => { try { setText(JSON.stringify(JSON.parse(text), null, 2)) } catch { toast('JSON 格式有误', 'err') } }
  const save = async () => { try { await api(`/api/workflows/${key}`, { method: 'PUT', body: { text } }); toast('已保存'); load() } catch (e: any) { toast(e.message, 'err') } }
  const reset = async () => { if (!confirm('恢复默认？')) return; await api(`/api/workflows/${key}/reset`, { method: 'POST' }); const x = await api<WorkflowItem[]>('/api/workflows'); setItems(x); setText(x.find(i => i.key === key)!.text); toast('已恢复默认') }
  return (
    <section className="card gcard">
      <div className="gcard-head">
        {narrow ? <Select size="sm" className="wf-sel" value={key} onChange={pick} options={items.map(i => ({ value: i.key, label: i.name }))} /> :
          <Segmented block={false} size="sm" value={key} onChange={pick} options={items.map(i => ({ value: i.key, label: i.name }))} />}
        <span className="grow" />
        {cur.custom && <span className="pill-tag">已修改</span>}
      </div>
      <div className="ph-row">
        {cur.kind === 'workflow' ? ['{{PROMPT}}', '{{IMAGE}}', '{{WIDTH}}', '{{HEIGHT}}', '{{LENGTH}}', '{{FPS}}', '{{SEED}}', '{{PREFIX}}'].map(p => <code key={p} className="ph">{p}</code>) :
          cur.kind === 'files' ? <span className="muted sm-text">每项：下载地址 url，放到 ComfyUI/models 下的目录 dir</span> : <span className="muted sm-text">{key === 'boot' ? 'ComfyUI 仓库；插件 custom_nodes（仓库地址列表）；帧率 fps；帧数取「除以 frame_step 余 frame_rem」，如 4k+1 填 4 和 1；SageAttention（G4 优先用预编译轮子 sage_wheel）、TE-Speed 加速 te_speed（只作用于标准工作流，删掉这项就不装）、下载线程数、启动参数' : '放大与补帧所用的模型和插件'}</span>}
      </div>
      <textarea className="input code-area" rows={22} value={text} onChange={e => setText(e.target.value)} spellCheck={false} />
      <div className="row-actions">
        <button className="btn btn-ghost btn-sm" onClick={pretty}>整理格式</button>
        <span className="grow" />
        <button className="btn btn-secondary btn-sm" onClick={reset}><RotateCcw size={14} />恢复默认</button>
        <button className="btn btn-primary btn-sm" onClick={save}><Check size={14} />保存</button>
      </div>
    </section>
  )
}
