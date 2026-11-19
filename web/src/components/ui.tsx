import { useEffect, useLayoutEffect, useRef, useState, type ReactNode } from 'react'
import { createPortal } from 'react-dom'
import { Check, ChevronDown, Minus, Plus, X } from 'lucide-react'

// ---------- 弹出层定位 ----------
function usePopover(open: boolean, anchor: React.RefObject<HTMLElement>, height: number, onClose: () => void, align: 'left' | 'right' = 'left') {
  const [pos, setPos] = useState<{ left: number; top: number; width: number } | null>(null)
  useLayoutEffect(() => {
    if (!open || !anchor.current) { setPos(null); return }
    const r = anchor.current.getBoundingClientRect()
    const h = Math.min(320, height)
    const up = r.bottom + h + 8 > window.innerHeight && r.top > h + 8
    setPos({ left: align === 'left' ? r.left : r.right, top: up ? r.top - h - 4 : r.bottom + 4, width: r.width })
  }, [open, height])
  useEffect(() => {
    if (!open) return
    const close = (e: Event) => {
      const t = e.target as Node
      if (anchor.current?.contains(t) || (t as HTMLElement).closest?.('.pop')) return
      onClose()
    }
    const esc = (e: KeyboardEvent) => e.key === 'Escape' && onClose()
    const scroll = (e: Event) => { if (!(e.target as HTMLElement)?.closest?.('.pop')) onClose() }
    window.addEventListener('mousedown', close)
    window.addEventListener('scroll', scroll, true)
    window.addEventListener('keydown', esc)
    window.addEventListener('resize', onClose)
    return () => {
      window.removeEventListener('mousedown', close); window.removeEventListener('scroll', scroll, true)
      window.removeEventListener('keydown', esc); window.removeEventListener('resize', onClose)
    }
  }, [open])
  return pos
}

// ---------- 下拉选择 ----------
export interface Opt { value: string; label: string; hint?: string; glyph?: string }
export function Select({ value, options, onChange, placeholder = '请选择', disabled, className = '', size = 'md' }: {
  value: string; options: Opt[]; onChange: (v: string) => void; placeholder?: string; disabled?: boolean; className?: string; size?: 'sm' | 'md'
}) {
  const [open, setOpen] = useState(false)
  const ref = useRef<HTMLButtonElement>(null)
  const cur = options.find(o => o.value === value)
  const pos = usePopover(open, ref, options.length * 32 + 10, () => setOpen(false))
  return (
    <div className={`select select-${size} ${open ? 'is-open' : ''} ${className}`}>
      <button ref={ref} type="button" className="select-btn" disabled={disabled} onClick={() => setOpen(o => !o)}>
        <span className={`select-val ${cur ? '' : 'is-ph'}`} title={cur?.label}>{cur ? cur.label : placeholder}</span>
        <ChevronDown size={16} className="select-caret" />
      </button>
      {open && pos && createPortal(
        <div className="pop menu" style={{ left: pos.left, top: pos.top, minWidth: pos.width }}>
          {options.length === 0 && <div className="menu-empty">暂无选项</div>}
          {options.map(o => (
            <button key={o.value} type="button" className={`menu-item ${o.value === value ? 'is-on' : ''}`} onClick={() => { onChange(o.value); setOpen(false) }}>
              <span className="menu-label">{o.label}</span>
              {o.hint && <span className="menu-hint">{o.hint}</span>}
              <Check size={14} className="menu-check" />
            </button>
          ))}
        </div>, document.body)}
    </div>
  )
}

// ---------- 比例图形 ----------
export function Glyph({ r }: { r: string }) {
  if (!r || r === 'auto') return <span className="glyph"><i className="is-auto" /></span>
  const [a, b] = r.split(/[:x×]/).map(Number)
  const k = a && b ? a / b : 1
  const w = k >= 1 ? 12 : Math.max(5, 12 * k)
  const h = k >= 1 ? Math.max(5, 12 / k) : 12
  return <span className="glyph"><i style={{ width: w, height: h }} /></span>
}

// ---------- 分段控件（可带「更多」） ----------
export function Segmented({ value, options, onChange, disabled, size = 'md', more, block = true, moreCols = 4 }: {
  value: string; options: Opt[]; onChange: (v: string) => void; disabled?: boolean; size?: 'sm' | 'md'
  more?: Opt[]; block?: boolean; moreCols?: number
}) {
  const [open, setOpen] = useState(false)
  const ref = useRef<HTMLButtonElement>(null)
  const inMore = more?.find(o => o.value === value)
  const pos = usePopover(open, ref, Math.ceil((more?.length || 0) / moreCols) * 36 + 12, () => setOpen(false), 'right')
  return (
    <div className={`seg seg-${size} ${block ? 'is-block' : ''} ${disabled ? 'is-disabled' : ''}`} role="radiogroup">
      {options.map(o => (
        <button key={o.value} type="button" role="radio" aria-checked={o.value === value} disabled={disabled}
          className={`seg-item ${o.value === value ? 'is-on' : ''}`} onClick={() => onChange(o.value)}>
          {o.glyph !== undefined && <Glyph r={o.glyph} />}<span>{o.label}</span>
        </button>
      ))}
      {more && more.length > 0 && (
        <button ref={ref} type="button" disabled={disabled} className={`seg-item seg-more ${inMore ? 'is-on' : ''} ${open ? 'is-open' : ''}`} onClick={() => setOpen(o => !o)}>
          {inMore ? <>{inMore.glyph !== undefined && <Glyph r={inMore.glyph} />}<span>{inMore.label}</span></> : <span>更多</span>}
          <ChevronDown size={12} />
        </button>
      )}
      {open && pos && more && createPortal(
        <div className="pop more-pop" style={{ left: pos.left, top: pos.top, ['--cols' as any]: moreCols }}>
          {more.map(o => (
            <button key={o.value} type="button" className={`more-item ${o.value === value ? 'is-on' : ''}`} onClick={() => { onChange(o.value); setOpen(false) }}>
              {o.glyph !== undefined && <Glyph r={o.glyph} />}<span>{o.label}</span>
            </button>
          ))}
        </div>, document.body)}
    </div>
  )
}

// ---------- 步进器 ----------
export function Stepper({ value, min = 1, max = 10, onChange, disabled }: { value: number; min?: number; max?: number; onChange: (v: number) => void; disabled?: boolean }) {
  return (
    <div className={`stepper ${disabled ? 'is-disabled' : ''}`}>
      <button type="button" disabled={disabled || value <= min} onClick={() => onChange(Math.max(min, value - 1))} aria-label="减少"><Minus size={14} /></button>
      <span className="stepper-val">{value}</span>
      <button type="button" disabled={disabled || value >= max} onClick={() => onChange(Math.min(max, value + 1))} aria-label="增加"><Plus size={14} /></button>
    </div>
  )
}

// ---------- 弹窗 ----------
export function Modal({ open, onClose, title, children, footer, wide, className = '' }: {
  open: boolean; onClose: () => void; title?: ReactNode; children: ReactNode; footer?: ReactNode; wide?: boolean; className?: string
}) {
  useEffect(() => {
    if (!open) return
    const esc = (e: KeyboardEvent) => e.key === 'Escape' && onClose()
    window.addEventListener('keydown', esc)
    document.body.classList.add('no-scroll')
    return () => { window.removeEventListener('keydown', esc); document.body.classList.remove('no-scroll') }
  }, [open, onClose])
  if (!open) return null
  return createPortal(
    <div className="modal-back" onMouseDown={e => e.target === e.currentTarget && onClose()}>
      <div className={`modal ${wide ? 'modal-wide' : ''} ${className}`} role="dialog" aria-modal="true">
        {title !== undefined && (
          <div className="modal-head">
            <h3>{title}</h3>
            <button className="icon-btn" onClick={onClose} aria-label="关闭"><X size={18} /></button>
          </div>
        )}
        <div className="modal-body">{children}</div>
        {footer && <div className="modal-foot">{footer}</div>}
      </div>
    </div>, document.body)
}

// ---------- 标签输入 ----------
export function TagInput({ value, onChange, placeholder = '输入后回车' }: { value: string[]; onChange: (v: string[]) => void; placeholder?: string }) {
  const [draft, setDraft] = useState('')
  const commit = () => {
    const parts = draft.split(/[,，\s]+/).map(s => s.trim()).filter(Boolean)
    if (parts.length) onChange([...value, ...parts.filter(p => !value.includes(p))])
    setDraft('')
  }
  return (
    <div className="taginput">
      {value.map(t => (
        <span key={t} className="tag">{t}<button type="button" onClick={() => onChange(value.filter(x => x !== t))} aria-label="移除"><X size={12} /></button></span>
      ))}
      <input value={draft} placeholder={value.length ? '' : placeholder} onChange={e => setDraft(e.target.value)}
        onKeyDown={e => {
          if (e.key === 'Enter' || e.key === ',' || e.key === '，') { e.preventDefault(); commit() }
          else if (e.key === 'Backspace' && !draft && value.length) onChange(value.slice(0, -1))
        }} onBlur={commit} />
    </div>
  )
}

// ---------- 空状态 ----------
export function Empty({ title, text, art = 'frame', action }: { title: string; text?: string; art?: 'frame' | 'book' | 'stack' | 'plug'; action?: ReactNode }) {
  return (
    <div className="empty">
      <EmptyArt kind={art} />
      <div className="empty-title">{title}</div>
      {text && <div className="empty-text">{text}</div>}
      {action}
    </div>
  )
}

function EmptyArt({ kind }: { kind: string }) {
  const common = { className: 'empty-art', viewBox: '0 0 64 48', fill: 'none' } as const
  if (kind === 'book') return (
    <svg {...common}><path d="M8 10c8-3 16-3 24 2v28c-8-5-16-5-24-2V10Z" /><path d="M56 10c-8-3-16-3-24 2v28c8-5 16-5 24-2V10Z" /></svg>
  )
  if (kind === 'stack') return (
    <svg {...common}><rect x="14" y="6" width="36" height="28" rx="4" /><rect x="10" y="12" width="36" height="28" rx="4" className="ea-front" /><path d="M16 34l8-9 6 6 4-4 8 7" /></svg>
  )
  if (kind === 'plug') return (
    <svg {...common}><rect x="8" y="16" width="20" height="16" rx="4" /><rect x="36" y="16" width="20" height="16" rx="4" /><path d="M28 24h8" /></svg>
  )
  return (
    <svg {...common}><rect x="8" y="6" width="48" height="36" rx="4" /><path d="M14 36l11-12 7 8 5-5 13 9" /><circle cx="44" cy="16" r="3" /></svg>
  )
}

export function Spinner({ size = 16 }: { size?: number }) {
  return <span className="spinner" style={{ width: size, height: size }} />
}

export function useInterval(fn: () => void, ms: number | null) {
  const ref = useRef(fn)
  ref.current = fn
  useEffect(() => {
    if (ms === null) return
    const t = setInterval(() => ref.current(), ms)
    return () => clearInterval(t)
  }, [ms])
}

export function useNow(active: boolean) {
  const [n, setN] = useState(Date.now())
  useInterval(() => setN(Date.now()), active ? 100 : null)
  return n
}

export function useMedia(q: string) {
  const [m, setM] = useState(() => matchMedia(q).matches)
  useEffect(() => {
    const mq = matchMedia(q)
    const f = () => setM(mq.matches)
    mq.addEventListener('change', f)
    return () => mq.removeEventListener('change', f)
  }, [q])
  return m
}

export async function copyText(t: string) {
  try { await navigator.clipboard.writeText(t); return true } catch {
    const ta = document.createElement('textarea'); ta.value = t; document.body.appendChild(ta); ta.select()
    const ok = document.execCommand('copy'); ta.remove(); return ok
  }
}

// ---------- 锚定弹出面板（自动选择上方或下方） ----------
export function Pop({ anchor, open, onClose, width = 320, children, className = '' }: {
  anchor: React.RefObject<HTMLElement>; open: boolean; onClose: () => void; width?: number; children: ReactNode; className?: string
}) {
  const [st, setSt] = useState<React.CSSProperties | null>(null)
  useLayoutEffect(() => {
    if (!open || !anchor.current) { setSt(null); return }
    const place = () => {
      const r = anchor.current!.getBoundingClientRect()
      const vw = window.innerWidth, vh = window.innerHeight
      const w = Math.min(width, vw - 16)
      const left = Math.max(8, Math.min(r.left, vw - w - 8))
      const below = vh - r.bottom - 16, above = r.top - 16
      if (above > below) setSt({ left, width: w, bottom: vh - r.top + 8, maxHeight: above - 8 })
      else setSt({ left, width: w, top: r.bottom + 8, maxHeight: below - 8 })
    }
    place()
    window.addEventListener('resize', place)
    return () => window.removeEventListener('resize', place)
  }, [open, width])
  useEffect(() => {
    if (!open) return
    const close = (e: Event) => {
      const t = e.target as HTMLElement
      if (anchor.current?.contains(t) || t.closest?.('.pop')) return
      onClose()
    }
    const esc = (e: KeyboardEvent) => e.key === 'Escape' && onClose()
    window.addEventListener('mousedown', close)
    window.addEventListener('keydown', esc)
    return () => { window.removeEventListener('mousedown', close); window.removeEventListener('keydown', esc) }
  }, [open])
  if (!open || !st) return null
  return createPortal(<div className={`pop panel-pop ${className}`} style={st}>{children}</div>, document.body)
}
