// 生成后统一音色（0.2.11）：角色母带、镜头开关、每条视频的原版 / 转换版
import { useEffect, useState } from 'react'
import { Disc3, RefreshCw, Wand2 } from 'lucide-react'
import { api, type Asset, type ColabState, type Shot, type Video, type VcMasterView } from '../../api'
import { useProject } from '../Project'
import { useStore } from '../../store'
import { Modal, Select, Spinner } from '../../components/ui'
import { PlayBtn } from '../../components/audio'

const fmtSec = (s: number) => s > 0 ? `${s.toFixed(1)} 秒` : '时长未知'

// MasterButton 角色卡片上的「母带」：统一音色时转成谁的声音
export function MasterButton({ a }: { a: Asset }) {
  const [open, setOpen] = useState(false)
  const n = (a.voice_refs?.length || 0) + (a.voice?.sample ? 1 : 0)
  return (
    <>
      <button type="button" className={`icon-btn sm ${a.vc_master ? 'is-on' : ''}`} onClick={() => setOpen(true)} disabled={n === 0}
        title={n === 0 ? '还没有可做音色母带的样本（原声参考或克隆 / 设计的音色样本）' : a.vc_master ? '音色母带：已指定' : '音色母带：自动'} aria-label="音色母带">
        <Disc3 size={14} />
      </button>
      {open && <MasterModal a={a} onClose={() => setOpen(false)} />}
    </>
  )
}

function MasterModal({ a, onClose }: { a: Asset; onClose: () => void }) {
  const { run } = useProject()
  const { toast } = useStore()
  const [mv, setMv] = useState<VcMasterView | null>(null)
  const [busy, setBusy] = useState(false)
  useEffect(() => { api<VcMasterView>(`/api/assets/${a.id}/vc-master`).then(setMv).catch(e => toast(e.message, 'err')) }, [a.id])
  const pick = async (sample: string) => {
    setBusy(true)
    try { setMv(await api<VcMasterView>(`/api/assets/${a.id}/vc-master`, { method: 'PUT', body: { sample } })); run(async () => {}) } catch (e: any) { toast(e.message, 'err') } finally { setBusy(false) }
  }
  return (
    <Modal open onClose={onClose} title={`${a.name} · 音色母带`} footer={<><span className="grow" /><button className="btn btn-primary" onClick={onClose}>完成</button></>}>
      <p className="muted sm-text">「生成后统一音色」会把这个角色说话的镜头里的人声转成母带的音色。不指定时自动用最长的原声参考，没有原声参考就用音色样本。</p>
      {!mv ? <Spinner /> : (
        <div className="stack" style={{ gap: 8, marginTop: 12 }}>
          <label className={`vcm-row ${!mv.chosen ? 'is-on' : ''}`}>
            <input type="radio" checked={!mv.chosen} disabled={busy} onChange={() => pick('auto')} />
            <span className="grow">自动{!mv.chosen && mv.current ? `（现在用：${mv.current.label} · ${fmtSec(mv.current.duration)}）` : ''}</span>
          </label>
          {mv.candidates.map(c => (
            <label key={c.sample} className={`vcm-row ${mv.chosen === c.sample ? 'is-on' : ''}`}>
              <input type="radio" checked={mv.chosen === c.sample} disabled={busy} onChange={() => pick(c.sample)} />
              <span className="grow">{c.label} · {fmtSec(c.duration)}</span>
              <PlayBtn src={c.url} />
            </label>
          ))}
          {mv.candidates.length === 0 && <p className="muted">还没有可做母带的样本。可以在音色面板里克隆 / 设计音色，或从音色库选一个带原声参考的音色。</p>}
          {mv.current && mv.how && mv.how.startsWith('本体') && <p className="muted sm-text">现在用的是{mv.how}。</p>}
        </div>
      )}
    </Modal>
  )
}

// ShotUnify 镜头卡片上的单独开关
export function ShotUnify({ s }: { s: Shot }) {
  const { data, run } = useProject()
  const inherit = data.project.voice_unify ? '跟随作品（开）' : '跟随作品（关）'
  return (
    <Select size="sm" className="vu-select" value={s.voice_unify || ''} onChange={v => run(() => api(`/api/shots/${s.id}/voice-unify`, { method: 'PUT', body: { mode: v || 'inherit' } }), '已保存')}
      options={[{ value: '', label: `统一音色：${inherit}` }, { value: 'on', label: '统一音色：本镜头开' }, { value: 'off', label: '统一音色：本镜头关' }]} />
  )
}

const vcLabel: Record<string, string> = { auto: '出片后统一音色', queued: '统一音色排队中', running: '统一音色中', done: '已统一音色', failed: '统一音色失败', skipped: '未统一音色' }

// TakeVoice 一条视频的原版 / 转换版切换、状态和「重做统一音色」
export function TakeVoice({ v, st, onView }: { v: Video; st: ColabState | null; onView: (version: 'original' | 'converted') => void }) {
  const { run } = useProject()
  const [busy, setBusy] = useState(false)
  const hasVc = !!v.vc_url
  const inFlight = v.vc_status === 'queued' || v.vc_status === 'running'
  const redo = async () => {
    const off = !st || st.runner.phase === 'off'
    const start = off && !!st?.runner.authorized && confirm('运行时没开。现在开机（视频环境）来做统一音色吗？会消耗 Colab 算力单元。\n选「取消」只排队，下次开机时再做。')
    setBusy(true)
    try {
      await run(() => api(`/api/videos/${v.id}/voice-convert`, { body: { start } }).then((r: any) => { if (r.start_error) throw new Error('已排队，开机失败：' + r.start_error) }), start ? '已排队并开机' : '已排队统一音色')
    } finally { setBusy(false) }
  }
  const pick = (use: 'original' | 'converted') => { onView(use); run(() => api(`/api/videos/${v.id}/version`, { body: { use } }), use === 'converted' ? '成片用统一音色版' : '成片用原版') }
  const reason = (v.vc_status === 'failed' || v.vc_status === 'skipped') ? v.vc_error : ''
  return (
    <div className="vcrow">
      {hasVc ? (
        <div className="seg seg-sm" role="radiogroup">
          <button type="button" className={`seg-item ${!v.vc_use ? 'is-on' : ''}`} onClick={() => pick('original')}><span>原版</span></button>
          <button type="button" className={`seg-item ${v.vc_use ? 'is-on' : ''}`} onClick={() => pick('converted')}><span>统一音色</span></button>
        </div>
      ) : v.vc_status ? (
        <span className={`sm-text ${v.vc_status === 'failed' ? 'danger-text' : v.vc_status === 'running' ? 'accent-text' : 'muted'}`} title={reason || undefined}>
          {inFlight && <Spinner size={11} />} {vcLabel[v.vc_status]}{reason ? `：${reason}` : ''}
        </span>
      ) : <span className="muted sm-text">原版</span>}
      {hasVc && inFlight && <span className="accent-text sm-text"><Spinner size={11} /> 重做中</span>}
      {hasVc && v.vc_status === 'failed' && <span className="danger-text sm-text" title={v.vc_error}>重做失败</span>}
      <span className="grow" />
      <button type="button" className="icon-btn sm" disabled={busy || inFlight || v.vc_status === 'auto'} onClick={redo}
        title={hasVc ? '重做统一音色（换母带 / 参数后）' : '统一音色：把人声转成说话角色的母带音色'} aria-label="统一音色">
        {busy ? <Spinner size={13} /> : hasVc ? <RefreshCw size={14} /> : <Wand2 size={14} />}
      </button>
    </div>
  )
}
