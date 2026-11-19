import { useEffect, useRef, useState } from 'react'
import { Clapperboard, Download, FileArchive, Captions, Play } from 'lucide-react'
import { api, type Video } from '../../api'
import { useProject, StepHead } from '../Project'
import { Empty, Segmented, Spinner } from '../../components/ui'
import { fmtSec, fmtTime } from '../../labels'
import { arClass } from './media'

export default function Final() {
  const { data, task, run } = useProject()
  const p = data.project
  const [subs, setSubs] = useState(true)
  const [mode, setMode] = useState<'final' | 'seq'>(p.final_url ? 'final' : 'seq')
  const building = task('final', 'project', p.id)
  const clips = data.shots.map(s => {
    const done = s.videos.filter(v => v.status === 'done')
    return { shot: s, v: done.find(v => v.id === s.picked_video_id) || done[done.length - 1] }
  })
  const ready = clips.filter(c => c.v).length
  const total = clips.reduce((t, c) => t + (c.v?.seconds || 0), 0)
  useEffect(() => { if (p.final_url) setMode('final') }, [p.final_url])
  return (
    <section className="stepview">
      <StepHead title="成片" note={`${ready} / ${data.shots.length} 段 · ${fmtSec(total)}`}>
        <a className="btn btn-secondary" href={`/api/projects/${p.id}/export`}><FileArchive size={16} />打包全部素材</a>
      </StepHead>
      <div className="final-grid">
        <div className="card final-stage">
          <div className="final-tabs">
            <Segmented block={false} size="sm" value={mode} onChange={v => setMode(v as any)} options={[{ value: 'final', label: '成片' }, { value: 'seq', label: '按镜头预览' }]} />
          </div>
          <div className={`final-screen ${arClass(p.aspect)}`}>
            {mode === 'final' ? (p.final_url ? <video key={p.final_url} src={p.final_url} controls playsInline /> : <div className="frame-empty"><Clapperboard size={28} strokeWidth={1.5} /><span>还没有合成</span></div>)
              : <SeqPlayer clips={clips.filter(c => c.v).map(c => c.v!)} />}
          </div>
        </div>
        <div className="final-side">
          <div className="card scard">
            <h3 className="scard-title">合成</h3>
            <div className="field"><span className="field-label">字幕</span>
              <Segmented value={subs ? 'on' : 'off'} onChange={v => setSubs(v === 'on')} options={[{ value: 'on', label: '烧录台词' }, { value: 'off', label: '不烧录' }]} />
            </div>
            <button className="btn btn-primary btn-block" disabled={!!building || ready < data.shots.length || data.shots.length === 0}
              onClick={() => run(() => api(`/api/projects/${p.id}/final`, { body: { subtitles: subs } }))}>
              {building ? <><Spinner />合成中</> : <><Clapperboard size={16} />{p.final_url ? '重新合成' : '合成成片'}</>}
            </button>
            {ready < data.shots.length && <p className="side-note">还差 {data.shots.length - ready} 段视频</p>}
          </div>
          {p.final_url && (
            <div className="card scard">
              <h3 className="scard-title">下载</h3>
              <a className="dl-row" href={p.final_url + '?dl=1'} download><Download size={16} /><span>成片.mp4</span><span className="grow" /><span className="muted">{fmtTime(p.final_at)}</span></a>
              <a className="dl-row" href={p.final_url.replace(/\.mp4$/, '.srt') + '?dl=1'} download><Captions size={16} /><span>字幕.srt</span></a>
              <a className="dl-row" href={`/api/projects/${p.id}/export`}><FileArchive size={16} /><span>全部素材.zip</span></a>
            </div>
          )}
        </div>
      </div>
      <div className="seq-strip">
        {clips.map(({ shot, v }) => {
          const kf = shot.images.find(i => i.id === shot.picked_image_id)
          return (
            <div key={shot.id} className={`seq-item ${v ? '' : 'is-missing'}`}>
              <div className={`seq-thumb ${arClass(p.aspect)}`}>{kf ? <img src={kf.url} alt="" loading="lazy" /> : null}</div>
              <span>{shot.code}</span>
            </div>
          )
        })}
      </div>
      {data.shots.length === 0 && <div className="card card-empty"><Empty art="frame" title="还没有分镜" /></div>}
    </section>
  )
}

function SeqPlayer({ clips }: { clips: Video[] }) {
  const [i, setI] = useState(0)
  const [playing, setPlaying] = useState(false)
  const ref = useRef<HTMLVideoElement>(null)
  useEffect(() => { if (playing) ref.current?.play().catch(() => {}) }, [i, playing])
  if (clips.length === 0) return <div className="frame-empty"><span>还没有视频</span></div>
  const cur = clips[Math.min(i, clips.length - 1)]
  return (
    <>
      <video ref={ref} key={cur.id} src={cur.url} controls playsInline onPlay={() => setPlaying(true)} onEnded={() => { if (i < clips.length - 1) setI(i + 1); else { setPlaying(false); setI(0) } }} />
      {!playing && <button className="seq-play" onClick={() => { setPlaying(true); ref.current?.play() }} aria-label="连续播放"><Play size={22} /></button>}
      <span className="seq-count">{i + 1} / {clips.length}</span>
    </>
  )
}
