import { createContext, useCallback, useContext, useEffect, useRef, useState, type ReactNode } from 'react'
import { api, type Channel } from './api'

export type Page = 'projects' | 'series' | 'project' | 'compute' | 'channels' | 'api'
export interface Route { page: Page; id?: number; ep?: number; step?: number; tab?: string }

export function parseRoute(p: string): Route {
  const e = p.match(/^\/p\/(\d+)\/e\/(\d+)(?:\/(\d))?/)
  if (e) return { page: 'project', id: Number(e[1]), ep: Number(e[2]), step: e[3] ? Number(e[3]) : undefined }
  const m = p.match(/^\/p\/(\d+)(?:\/(\w+))?/)
  if (m) return { page: 'series', id: Number(m[1]), tab: m[2] || undefined }
  if (p.startsWith('/compute')) return { page: 'compute' }
  if (p.startsWith('/channels')) return { page: 'channels' }
  if (p.startsWith('/api')) return { page: 'api' }
  return { page: 'projects' }
}

export function routePath(r: Route): string {
  switch (r.page) {
    case 'project': return `/p/${r.id}/e/${r.ep}${r.step ? '/' + r.step : ''}`
    case 'series': return `/p/${r.id}${r.tab ? '/' + r.tab : ''}`
    case 'compute': return '/compute'
    case 'channels': return '/channels'
    case 'api': return '/api'
  }
  return '/'
}

export type ThemePref = 'system' | 'light' | 'dark'
interface Toast { id: number; text: string; kind: 'ok' | 'err' | 'info' }

interface Store {
  route: Route; go: (r: Route, replace?: boolean) => void
  channels: Channel[]; reloadChannels: () => Promise<void>; channelsLoaded: boolean
  toast: (text: string, kind?: Toast['kind']) => void
  toasts: Toast[]
  theme: ThemePref; setTheme: (t: ThemePref) => void
}

const Ctx = createContext<Store>(null as any)
export const useStore = () => useContext(Ctx)

export function applyTheme(t: ThemePref) {
  const dark = t === 'dark' || (t === 'system' && matchMedia('(prefers-color-scheme: dark)').matches)
  document.documentElement.dataset.theme = dark ? 'dark' : 'light'
  document.querySelectorAll('meta[name="theme-color"]').forEach(m => m.setAttribute('content', dark ? '#0E0F0F' : '#F4F5F1'))
}

export function StoreProvider({ children }: { children: ReactNode }) {
  const [route, setRoute] = useState<Route>(() => parseRoute(location.pathname))
  const [channels, setChannels] = useState<Channel[]>([])
  const [channelsLoaded, setLoaded] = useState(false)
  const [toasts, setToasts] = useState<Toast[]>([])
  const [theme, setThemeState] = useState<ThemePref>(() => (localStorage.getItem('manju.theme') as ThemePref) || 'system')
  const tid = useRef(1)

  useEffect(() => {
    const onPop = () => setRoute(parseRoute(location.pathname))
    window.addEventListener('popstate', onPop)
    return () => window.removeEventListener('popstate', onPop)
  }, [])
  const go = useCallback((r: Route, replace = false) => {
    const p = routePath(r)
    if (p !== location.pathname) {
      if (replace) history.replaceState(null, '', p)
      else history.pushState(null, '', p)
    }
    setRoute(r)
    if (!replace) window.scrollTo(0, 0)
  }, [])
  const reloadChannels = useCallback(async () => {
    try { setChannels(await api<Channel[]>('/api/channels')) } finally { setLoaded(true) }
  }, [])
  useEffect(() => { reloadChannels() }, [reloadChannels])
  const toast = useCallback((text: string, kind: Toast['kind'] = 'ok') => {
    const id = tid.current++
    setToasts(t => [...t, { id, text, kind }])
    setTimeout(() => setToasts(t => t.filter(x => x.id !== id)), kind === 'err' ? 4200 : 2400)
  }, [])
  const setTheme = useCallback((t: ThemePref) => { localStorage.setItem('manju.theme', t); setThemeState(t); applyTheme(t) }, [])
  useEffect(() => {
    applyTheme(theme)
    if (theme !== 'system') return
    const mq = matchMedia('(prefers-color-scheme: dark)')
    const f = () => applyTheme('system')
    mq.addEventListener('change', f)
    return () => mq.removeEventListener('change', f)
  }, [theme])
  return <Ctx.Provider value={{ route, go, channels, reloadChannels, channelsLoaded, toast, toasts, theme, setTheme }}>{children}</Ctx.Provider>
}
