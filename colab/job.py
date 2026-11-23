# 漫剧工坊：提交一个视频工作流到本机 ComfyUI，流式汇报进度，输出文件路径
import json, base64, time, uuid, urllib.request
import websocket  # websocket-client

WF = json.loads(base64.b64decode("__WF__").decode())
TIMEOUT = __TIMEOUT__
PORT = 8188
cid = uuid.uuid4().hex

def say(*a):
    print("[mj]", *a, flush=True)

def abort_comfy(prompt_id):
    """中断正在跑的 ComfyUI 节点并把该 prompt 从队列里移除，避免取消任务后僵尸占卡。"""
    for path, body in (("/interrupt", {}), ("/queue", {"delete": [prompt_id]})):
        try:
            r = urllib.request.Request(
                f"http://127.0.0.1:{PORT}{path}",
                data=json.dumps(body).encode(),
                headers={"Content-Type": "application/json"},
            )
            urllib.request.urlopen(r, timeout=5)
        except Exception:
            pass

ws = websocket.WebSocket()
ws.connect(f"ws://127.0.0.1:{PORT}/ws?clientId={cid}", timeout=30)
req = urllib.request.Request(f"http://127.0.0.1:{PORT}/prompt", data=json.dumps({"prompt": WF, "client_id": cid}).encode(), headers={"Content-Type": "application/json"})
try:
    pid = json.load(urllib.request.urlopen(req, timeout=60))["prompt_id"]
except urllib.error.HTTPError as e:
    ws.close()
    raise RuntimeError("工作流被拒绝：" + e.read().decode()[:1500])
say("PROMPT", pid)
t0 = time.time()
ws.settimeout(5)
done = False
try:
    while not done:
        if time.time() - t0 > TIMEOUT:
            abort_comfy(pid)
            raise RuntimeError("生成超时")
        try:
            raw = ws.recv()
        except websocket.WebSocketTimeoutException:
            continue
        if not isinstance(raw, str):
            continue
        m = json.loads(raw)
        t, d = m.get("type"), m.get("data", {})
        if d.get("prompt_id") not in (None, pid):
            continue
        if t == "progress":
            say("P", d.get("value", 0), d.get("max", 1), d.get("node", ""))
        elif t == "executing" and d.get("node") is None and d.get("prompt_id") == pid:
            done = True
        elif t == "execution_success":
            done = True
        elif t == "execution_error":
            raise RuntimeError(f"{d.get('node_type','')}: {d.get('exception_message','')}".strip())
        elif t == "execution_interrupted":
            raise RuntimeError("生成被中断")
except (KeyboardInterrupt, SystemExit):
    abort_comfy(pid)
    say("INTERRUPTED", pid)
    raise
finally:
    try:
        ws.close()
    except Exception:
        pass
hist = json.load(urllib.request.urlopen(f"http://127.0.0.1:{PORT}/history/{pid}", timeout=30)).get(pid, {})
st = hist.get("status", {})
if st.get("status_str") == "error":
    raise RuntimeError("工作流执行出错：" + json.dumps(st.get("messages", []))[-1500:])
outs = []
for node_out in hist.get("outputs", {}).values():
    for key, items in node_out.items():
        if isinstance(items, list):
            for it in items:
                if isinstance(it, dict) and it.get("filename") and it.get("type", "output") == "output":
                    sub = it.get("subfolder", "")
                    outs.append(f"/content/ComfyUI/output/{sub + '/' if sub else ''}{it['filename']}")
vids = [o for o in outs if o.lower().endswith((".mp4", ".webm", ".mov", ".mkv"))] or outs
if not vids:
    raise RuntimeError("工作流没有输出文件")
say("OUT", vids[0])
say("TIME", f"{time.time()-t0:.0f}")
