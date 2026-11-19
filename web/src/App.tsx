import { useEffect, useState } from 'react'
import { Sun, Moon, ArrowRight, LogOut } from 'lucide-react'
import { api, setUnauthorizedHandler, type RunnerView } from './api'
import { StoreProvider, useStore, type Page } from './store'
import { Spinner, useInterval } from './components/ui'
import Projects from './pages/Projects'
import ProjectPage from './pages/Project'
import Series from './pages/Series'
import Compute from './pages/Compute'
import Channels from './pages/Channels'
import ApiPage from './pages/ApiPage'
import { accName, phaseLabels } from './labels'

const nav: { p: Page; label: string }[] = [
  { p: 'projects', label: '作品' },
  { p: 'compute', label: '算力' },
  { p: 'channels', label: '渠道' },
  { p: 'api', label: '接口' },
]

export default function App() {
  const [authed, setAuthed] = useState<boolean | null>(null)
  const load = () => api<{ authed: boolean }>('/api/me').then(x => setAuthed(x.authed)).catch(() => setAuthed(false))
  useEffect(() => {
    setUnauthorizedHandler(() => setAuthed(false))
    load()
  }, [])
  if (authed === null) return <div className="boot"><Spinner size={20} /></div>
  if (!authed) return <Login onOk={load} />
  return <StoreProvider><Shell /></StoreProvider>
}

// 圆相 + 播放：漫剧工坊的标记
export function Seal({ size = 28 }: { size?: number }) {
  return (
    <svg className="seal" width={size} height={size} viewBox="0 0 32 32" aria-hidden="true">
      <path d="M21.75 6.04 L22.80 6.65 L23.78 7.36 L24.69 8.18 L25.50 9.09 L26.23 10.10 L26.84 11.17 L27.34 12.31 L27.72 13.51 L27.97 14.74 L28.09 16.00 L28.07 17.27 L27.92 18.53 L27.63 19.78 L27.21 20.99 L26.66 22.15 L25.99 23.26 L25.20 24.28 L24.30 25.22 L23.31 26.06 L22.23 26.78 L21.07 27.39 L19.86 27.87 L18.60 28.22 L17.31 28.43 L16.00 28.50 L14.69 28.43 L13.40 28.22 L12.14 27.87 L10.93 27.39 L9.77 26.78 L8.69 26.06 L7.70 25.22 L6.80 24.28 L6.01 23.26 L5.34 22.15 L4.79 20.99 L4.37 19.78 L4.08 18.53 L3.93 17.27 L3.91 16.00 L4.03 14.74 L4.28 13.51 L4.66 12.31 L5.16 11.17 L5.77 10.10 L6.50 9.09 L7.31 8.18 L8.22 7.36 L9.20 6.65 L10.25 6.04 L10.75 6.91 L9.87 7.56 L9.06 8.29 L8.34 9.10 L7.71 9.97 L7.17 10.90 L6.74 11.88 L6.42 12.89 L6.20 13.92 L6.09 14.96 L6.09 16.00 L6.19 17.03 L6.40 18.04 L6.71 19.02 L7.11 19.96 L7.61 20.85 L8.19 21.68 L8.85 22.44 L9.58 23.13 L10.37 23.74 L11.23 24.27 L12.12 24.71 L13.06 25.05 L14.02 25.30 L15.01 25.45 L16.00 25.50 L16.99 25.45 L17.98 25.30 L18.94 25.05 L19.88 24.71 L20.77 24.27 L21.63 23.74 L22.42 23.13 L23.15 22.44 L23.81 21.68 L24.39 20.85 L24.89 19.96 L25.29 19.02 L25.60 18.04 L25.81 17.03 L25.91 16.00 L25.91 14.96 L25.80 13.92 L25.58 12.89 L25.26 11.88 L24.83 10.90 L24.29 9.97 L23.66 9.10 L22.94 8.29 L22.13 7.56 L21.25 6.91Z" fill="currentColor" />
      <path d="M13.6 11.9 L20.4 16 L13.6 20.1 Z" fill="currentColor" strokeLinejoin="round" stroke="currentColor" strokeWidth="1.2" />
    </svg>
  )
}

function ComputeChip() {
  const { go } = useStore()
  const [r, setR] = useState<RunnerView | null>(null)
  const load = () => api<{ runner: RunnerView }>('/api/colab').then(x => setR(x.runner)).catch(() => {})
  useEffect(() => { load() }, [])
  useInterval(load, 5000)
  if (!r) return null
  const on = r.phase !== 'off'
  const label = on ? `${accName(r.accelerator)} · ${phaseLabels[r.phase]}` : r.queued > 0 ? `排队 ${r.queued}` : '算力空闲'
  return (
    <button className={`cchip is-${r.phase} hide-xs`} onClick={() => go({ page: 'compute' })} title="算力">
      <i className="cchip-dot" />{label}
    </button>
  )
}

function Shell() {
  const { route, go, toasts, setTheme } = useStore()
  const [, force] = useState(0)
  const [scrolled, setScrolled] = useState(false)
  useEffect(() => {
    const f = () => setScrolled(window.scrollY > 4)
    f(); window.addEventListener('scroll', f, { passive: true })
    return () => window.removeEventListener('scroll', f)
  }, [])
  const isDark = document.documentElement.dataset.theme === 'dark'
  const logout = async () => { await api('/api/logout', { method: 'POST' }); location.reload() }
  const active: Page = route.page === 'project' || route.page === 'series' ? 'projects' : route.page
  return (
    <div className={`shell route-${route.page}`}>
      <header className={`topbar ${scrolled ? 'is-scrolled' : ''}`}>
        <button className="brand" onClick={() => go({ page: 'projects' })} aria-label="漫剧工坊"><Seal /><b>漫剧工坊</b></button>
        <nav className="nav">
          {nav.map(n => (
            <button key={n.p} className={`nav-item ${active === n.p ? 'is-on' : ''}`} onClick={() => go({ page: n.p })}>{n.label}</button>
          ))}
        </nav>
        <div className="top-actions">
          <ComputeChip />
          <button className="icon-btn" onClick={() => { setTheme(isDark ? 'light' : 'dark'); force(x => x + 1) }} aria-label="切换主题" title={isDark ? '浅色' : '深色'}>{isDark ? <Sun size={18} /> : <Moon size={18} />}</button>
          <button className="icon-btn hide-xs" onClick={logout} aria-label="退出登录" title="退出登录"><LogOut size={18} /></button>
        </div>
      </header>
      <main className="main">
        {route.page === 'projects' && <Projects />}
        {route.page === 'series' && route.id && <Series key={route.id} id={route.id} />}
        {route.page === 'project' && route.ep && <ProjectPage key={route.ep} id={route.ep} />}
        {route.page === 'compute' && <Compute />}
        {route.page === 'channels' && <Channels />}
        {route.page === 'api' && <ApiPage />}
      </main>
      <div className="toasts">{toasts.map(t => <div key={t.id} className={`toast is-${t.kind}`}>{t.text}</div>)}</div>
    </div>
  )
}

function Login({ onOk }: { onOk: () => void }) {
  const [pw, setPw] = useState('')
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState(false)
  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    setBusy(true); setErr('')
    try { await api('/api/login', { body: { password: pw } }); onOk() } catch (e: any) { setErr(e.message) } finally { setBusy(false) }
  }
  return (
    <div className="login">
      <form className="login-card" onSubmit={submit}>
        <Seal size={48} />
        <h1>漫剧工坊</h1>
        <input className="input" type="password" autoFocus autoComplete="current-password" placeholder="管理员密码" value={pw} onChange={e => setPw(e.target.value)} />
        {err && <div className="login-err">{err}</div>}
        <button className="btn btn-primary btn-block" disabled={busy || !pw}>{busy ? <Spinner /> : <>进入<ArrowRight size={16} /></>}</button>
      </form>
    </div>
  )
}
