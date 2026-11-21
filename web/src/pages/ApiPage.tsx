import { useEffect, useState } from 'react'
import { Copy, FileText, KeyRound, Plus, Trash2 } from 'lucide-react'
import { api, type ApiKey } from '../api'
import { useStore } from '../store'
import { Empty, Modal, Segmented, Spinner, copyText } from '../components/ui'
import { PageHead } from './Projects'
import { fmtTime } from '../labels'

const tools: [string, string][] = [
  ['get_writing_context', '写作上下文：设定集、前情、角色造型、本集状态'],
  ['save_bible', '写设定集：世界观、人物小传、总纲'],
  ['save_synopsis', '写本集梗概'],
  ['save_script', '写剧本（含台词角色与语气）'],
  ['save_shots', '写分镜：镜头、画面、台词、资产、衔接'],
  ['save_episode_summary', '写本集摘要与卡点'],
  ['upsert_asset', '新建或更新角色、场景、道具外观（look 只写静态外貌，道具备注写 props_note）'],
  ['update_shot', '改单个镜头（保留已有关键帧和视频）'],
  ['create_project', '新建作品'],
  ['update_project', '改作品设置（画风、画幅、时长、生图渠道、声音模式）'],
  ['delete_asset', '删除资产（需 confirm）'],
  ['detach_image', '从资产里拿掉一张图（需 confirm）'],
  ['view_asset_images', '看资产设定图、三视图、表情表'],
  ['view_shot', '看关键帧与视频首尾帧'],
  ['view_image', '按编号看任意图片（画室、候选、关键帧）'],
  ['listen_dub', '听台词配音'],
  ['listen_voice', '听音色样本 / 设计候选 / 原声参考'],
  ['clone_voice', '用镜头原声或上传音频克隆角色音色'],
  ['get_video_link', '视频限时下载地址'],
  ['list_projects', '作品列表与缺失步骤'],
  ['list_episodes', '设定集与各集进度'],
  ['create_episode', '新建下一集'],
  ['get_episode', '一集的剧本、分镜与状态'],
  ['autopilot_start', '一键成集（含外部编剧模式）'],
  ['autopilot_status', '一键成集进度'],
  ['generate_dub', '生成整集配音'],
  ['colab_status', '运行时状态、排队数、已用 CU'],
  ['colab_specs', '可用运行时规格'],
  ['colab_start', '开机（视频 / 仅运行代码）'],
  ['colab_stop', '关机'],
  ['colab_run_python', '在运行时上执行 Python'],
  ['video_submit', '提交图生视频任务'],
  ['video_status', '查询视频任务'],
  ['list_shots', '作品分镜'],
  ['list_image_channels', '生图渠道、模型与参考图上限'],
  ['image_generate', '自由出图（放进画室，可带参考图、指定模型）'],
  ['generate_asset_image', '给角色 / 场景出图（自动带参考图锁脸）'],
  ['generate_keyframe', '给镜头出关键帧（可指定模型、单次追加要求、只锁脸）'],
  ['pick_image', '换选用（定稿）'],
  ['upload_image', '上传图片（用户在对话里发的图）'],
  ['attach_image', '把图存进资产（设定图 / 三视图 / 表情表 / 参考照）'],
]

const rest: [string, string, string][] = [
  ['GET', '/v1/colab', '运行时状态'],
  ['GET', '/v1/colab/specs', '可用规格'],
  ['POST', '/v1/colab/start', '开机 {accelerator, purpose}'],
  ['POST', '/v1/colab/stop', '关机'],
  ['POST', '/v1/colab/exec', '执行代码 {code, timeout_sec}'],
  ['POST', '/v1/videos', '提交视频 {prompt, image_url | image_base64 | image_id | shot_id, seconds, turbo}'],
  ['GET', '/v1/videos/{id}', '视频状态'],
  ['GET', '/v1/videos/{id}/file', '下载视频'],
  ['GET', '/v1/projects', '作品列表'],
  ['GET', '/v1/projects/{id}/shots', '分镜'],
  ['POST', '/v1/images', '生图（进画室）{prompt, channel_id, model, aspect, n, ref_image_ids}'],
  ['GET', '/v1/channels/image', '生图渠道与模型'],
  ['POST', '/v1/assets/{id}/generate', '资产出图 {tag, n, channel_id, model}'],
  ['POST', '/v1/images/{id}/copy', '存进资产 {asset_id, tag}'],
  ['GET', '/v1/images/{id}/file', '下载图片'],
]

export default function ApiPage() {
  const { toast } = useStore()
  const [keys, setKeys] = useState<ApiKey[] | null>(null)
  const [name, setName] = useState('')
  const [created, setCreated] = useState<string | null>(null)
  const [adding, setAdding] = useState(false)
  const [agWay, setAgWay] = useState<'remote' | 'bridge'>('remote')
  const load = () => api<ApiKey[]>('/api/keys').then(setKeys).catch(() => setKeys([]))
  useEffect(() => { load() }, [])
  const origin = location.origin
  const create = async () => {
    try { const r = await api<{ key: string }>('/api/keys', { body: { name } }); setCreated(r.key); setName(''); setAdding(false); load() } catch (e: any) { toast(e.message, 'err') }
  }
  const copy = async (t: string) => { await copyText(t); toast('已复制') }
  const agConf = JSON.stringify(agWay === 'remote'
    ? { mcpServers: { manju: { serverUrl: `${origin}/mcp`, headers: { Authorization: 'Bearer <密钥>' }, timeoutSeconds: 600 } } }
    : { mcpServers: { manju: { command: '/path/to/manju-mcp-bridge', env: { MANJU_MCP_URL: `${origin}/mcp`, MANJU_API_KEY: '<密钥>' }, timeoutSeconds: 600 } } }, null, 2)
  const copyRules = async () => {
    try { const r = await fetch('/api/antigravity/rules'); if (!r.ok) throw new Error('读取规则文件失败'); await copy(await r.text()) } catch (e: any) { toast(e.message, 'err') }
  }
  const mcpConf = JSON.stringify({ mcpServers: { manju: { type: 'http', url: `${origin}/mcp`, headers: { Authorization: 'Bearer <密钥>' } } } }, null, 2)
  return (
    <div className="page page-narrow">
      <PageHead title="接口" />
      <div className="stack">
        <section className="card gcard">
          <div className="gcard-head"><h3>密钥</h3><span className="grow" /><button className="btn btn-primary btn-sm" onClick={() => setAdding(true)}><Plus size={14} />新建密钥</button></div>
          {!keys ? <div className="loading"><Spinner /></div> : keys.length === 0 ? <Empty art="plug" title="还没有密钥" /> : (
            <div className="klist">
              {keys.map(k => (
                <div key={k.id} className="krow">
                  <span className="kicon"><KeyRound size={16} /></span>
                  <div className="kmain"><b>{k.name}</b><span className="muted" title={`mj-••••${k.tail}`}>mj-••••{k.tail} · 创建于 {fmtTime(k.created_at)}{k.last_used_at ? ` · 最近使用 ${fmtTime(k.last_used_at)}` : ' · 未使用'}</span></div>
                  <button className="icon-btn sm is-danger" onClick={() => confirm(`吊销「${k.name}」？使用它的智能体会立即失去访问。`) && api(`/api/keys/${k.id}`, { method: 'DELETE' }).then(load)} aria-label="吊销" title="吊销"><Trash2 size={14} /></button>
                </div>
              ))}
            </div>
          )}
        </section>
        <section className="card gcard" id="antigravity">
          <div className="gcard-head"><h3>Antigravity</h3><span className="mini-tag">外部编剧</span></div>
          <ol className="ag-steps">
            <li>在上面新建一个密钥。</li>
            <li>在 Antigravity 的智能体面板点「…」→ MCP 服务器 → 管理 → 查看原始配置，把下面这段加进 <code>mcp_config.json</code>，<code>&lt;密钥&gt;</code> 换成你的密钥。</li>
            <li>把规则文件存为 Antigravity 工作区根目录下的 <code>AGENTS.md</code>，Gemini 会按漫剧的写法工作。</li>
            <li>在这里的「一键成集」选「外部编剧」，然后在 Antigravity 里说「接着写第 N 集」。</li>
          </ol>
          <Segmented value={agWay} onChange={v => setAgWay(v as any)} options={[{ value: 'remote', label: '远程连接（推荐）' }, { value: 'bridge', label: '本地桥接' }]} />
          <pre className="codeblock">{agConf}</pre>
          <div className="ag-row">
            <button className="btn btn-primary btn-sm" onClick={() => copy(agConf)}><Copy size={14} />复制配置</button>
            <button className="btn btn-secondary btn-sm" onClick={copyRules}><FileText size={14} />复制规则文件</button>
          </div>
        </section>
        <section className="card gcard">
          <div className="gcard-head"><h3>MCP</h3><span className="mini-tag">可流式 HTTP</span></div>
          <div className="endpoint"><code>{origin}/mcp</code><button className="icon-btn sm" onClick={() => copy(`${origin}/mcp`)} aria-label="复制" title="复制"><Copy size={14} /></button></div>
          <pre className="codeblock">{mcpConf}</pre>
          <div className="tool-grid">
            {tools.map(([n, d]) => <div key={n} className="tool"><code>{n}</code><span title={d}>{d}</span></div>)}
          </div>
        </section>
        <section className="card gcard">
          <div className="gcard-head"><h3>REST</h3><span className="muted">请求头 Authorization: Bearer &lt;密钥&gt;</span></div>
          <div className="rest">
            {rest.map(([m, p, d]) => <div key={m + p} className="rest-row"><span className={`verb is-${m.toLowerCase()}`}>{m}</span><code>{p}</code><span className="rest-desc">{d}</span></div>)}
          </div>
          <pre className="codeblock">{`curl -X POST ${origin}/v1/videos \\
  -H "Authorization: Bearer <密钥>" -H "Content-Type: application/json" \\
  -d '{"image_url":"https://…/frame.png","prompt":"她缓缓回头，风吹起发梢","seconds":5}'`}</pre>
        </section>
      </div>
      <Modal open={adding} onClose={() => setAdding(false)} title="新建密钥"
        footer={<><button className="btn btn-secondary" onClick={() => setAdding(false)}>取消</button><button className="btn btn-primary" onClick={create}>创建</button></>}>
        <label className="field"><span className="field-label">名称</span><input className="input" autoFocus value={name} onChange={e => setName(e.target.value)} placeholder="例如：写作助手" onKeyDown={e => e.key === 'Enter' && create()} /></label>
      </Modal>
      <Modal open={!!created} onClose={() => setCreated(null)} title="密钥只显示这一次"
        footer={<button className="btn btn-primary" onClick={() => setCreated(null)}>好了</button>}>
        
        <div className="endpoint big"><code>{created}</code><button className="icon-btn sm" onClick={() => created && copy(created)} aria-label="复制" title="复制"><Copy size={14} /></button></div>
      </Modal>
    </div>
  )
}
