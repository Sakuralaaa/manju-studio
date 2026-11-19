import { useEffect, useRef, useState } from 'react'
import { Pause, Play, Mic, Square } from 'lucide-react'
import { Spinner } from './ui'

// 全局只播一个声音
let current: HTMLAudioElement | null = null
let currentStop: (() => void) | null = null

export function PlayBtn({ src, size = 'sm', label, disabled, className = '' }: { src?: string; size?: 'sm' | 'md'; label?: string; disabled?: boolean; className?: string }) {
  const [state, setState] = useState<'idle' | 'loading' | 'playing'>('idle')
  const ref = useRef<HTMLAudioElement | null>(null)
  useEffect(() => () => { if (ref.current && current === ref.current) { current.pause(); current = null } }, [])
  const stop = () => { ref.current?.pause(); setState('idle') }
  const toggle = () => {
    if (!src) return
    if (state !== 'idle') { stop(); return }
    if (current && currentStop) currentStop()
    const a = new Audio(src)
    ref.current = a; current = a; currentStop = stop
    setState('loading')
    a.oncanplay = () => setState('playing')
    a.onended = () => setState('idle')
    a.onerror = () => setState('idle')
    a.play().catch(() => setState('idle'))
  }
  const Icon = state === 'playing' ? Pause : Play
  const cls = label ? `btn btn-secondary btn-${size}` : `icon-btn ${size}`
  return (
    <button type="button" className={`${cls} ${className} ${state === 'playing' ? 'is-playing' : ''}`} disabled={disabled || !src} onClick={toggle} aria-label={state === 'playing' ? '停止' : '播放'} title={state === 'playing' ? '停止' : '播放'}>
      {state === 'loading' ? <Spinner size={13} /> : <Icon size={14} />}{label}
    </button>
  )
}

// 浏览器录音（MediaRecorder），最长 60 秒
export function Recorder({ onDone, disabled }: { onDone: (b: Blob) => void; disabled?: boolean }) {
  const [rec, setRec] = useState<MediaRecorder | null>(null)
  const [sec, setSec] = useState(0)
  const [err, setErr] = useState('')
  const timer = useRef<any>(null)
  const start = async () => {
    setErr('')
    try {
      const stream = await navigator.mediaDevices.getUserMedia({ audio: true })
      const r = new MediaRecorder(stream)
      const chunks: BlobPart[] = []
      r.ondataavailable = e => chunks.push(e.data)
      r.onstop = () => {
        stream.getTracks().forEach(t => t.stop())
        clearInterval(timer.current)
        setRec(null)
        onDone(new Blob(chunks, { type: r.mimeType || 'audio/webm' }))
      }
      r.start()
      setRec(r); setSec(0)
      const t0 = Date.now()
      timer.current = setInterval(() => {
        const s = Math.floor((Date.now() - t0) / 1000)
        setSec(s)
        if (s >= 60) r.stop()
      }, 250)
    } catch { setErr('无法使用麦克风') }
  }
  useEffect(() => () => clearInterval(timer.current), [])
  return rec ? (
    <button type="button" className="btn btn-secondary is-rec" onClick={() => rec.stop()}><Square size={14} />停止录音<span className="cost">{sec} 秒</span></button>
  ) : (
    <button type="button" className="btn btn-secondary" disabled={disabled} onClick={start} title={err || undefined}><Mic size={16} />{err || '录音'}</button>
  )
}
