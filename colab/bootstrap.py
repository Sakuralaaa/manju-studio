# 漫剧工坊：在 Colab 运行时上准备 ComfyUI + 视频模型（可重复执行）
import os, sys, json, time, base64, subprocess, threading, urllib.request

CFG = json.loads(base64.b64decode("__CFG__").decode())
ROOT = "/content/ComfyUI"
PORT = 8188

def say(*a):
    print("[mj]", *a, flush=True)

def stage(name, pct=None):
    say("STAGE", name if pct is None else f"{name} {pct:.0f}")

def sh(cmd, quiet=True):
    r = subprocess.run(cmd, shell=True, capture_output=True, text=True)
    if r.returncode != 0:
        say("ERR", cmd.split()[0], (r.stderr or r.stdout)[-1500:])
        raise RuntimeError(f"命令失败：{cmd[:80]}")
    return r.stdout

def comfy_alive():
    try:
        urllib.request.urlopen(f"http://127.0.0.1:{PORT}/system_stats", timeout=3).read()
        return True
    except Exception:
        return False

if comfy_alive():
    say("READY", "已在运行")
else:
    t0 = time.time()
    # 1. ComfyUI
    stage("安装 ComfyUI")
    if not os.path.exists(f"{ROOT}/main.py"):
        sh(f"git clone --depth 1 {CFG['comfy_repo']} {ROOT}")
    sh(f"cd {ROOT} && {sys.executable} -m pip install -q -r requirements.txt")
    sh(f"{sys.executable} -m pip install -q websocket-client")
    sage = False
    if CFG.get("sage"):
        stage("安装 SageAttention")
        try:
            sh(f"{sys.executable} -m pip install -q {CFG.get('sage_pkg') or 'sageattention'}")
            sh(f"{sys.executable} -c 'import sageattention'")
            sage = True
        except Exception as e:
            say("WARN", "SageAttention 不可用，改用默认注意力", str(e)[:200])
    say("INFO", f"依赖就绪 {time.time()-t0:.0f}s")

    # 2. 权重：多线程下载
    stage("下载模型", 0)
    try:
        sh("which aria2c || (apt-get -qq update && apt-get -qq install -y aria2)")
        aria = True
    except Exception:
        aria = False
    files = []
    for f in CFG["files"]:
        d = os.path.join(ROOT, "models", f["dir"])
        os.makedirs(d, exist_ok=True)
        name = f.get("name") or f["url"].split("?")[0].rsplit("/", 1)[-1]
        files.append({"url": f["url"], "dir": d, "name": name, "path": os.path.join(d, name), "size": 0})

    def head_size(u):
        try:
            req = urllib.request.Request(u, method="HEAD")
            with urllib.request.urlopen(req, timeout=30) as r:
                return int(r.headers.get("x-linked-size") or r.headers.get("content-length") or 0)
        except Exception:
            return 0
    for f in files:
        f["size"] = head_size(f["url"])
    todo = [f for f in files if not (os.path.exists(f["path"]) and not os.path.exists(f["path"] + ".aria2") and (f["size"] == 0 or os.path.getsize(f["path"]) == f["size"]))]
    total = sum(f["size"] for f in files) or 1
    done_flag = {"v": False, "err": None}

    def worker():
        try:
            if not todo:
                return
            if aria:
                lst = "/content/mj_downloads.txt"
                with open(lst, "w") as fh:
                    for f in todo:
                        fh.write(f"{f['url']}\n  dir={f['dir']}\n  out={f['name']}\n")
                n = CFG.get("threads", 16)
                sh(f"aria2c -q -c -x {n} -s {n} -k 4M -j {min(len(todo), 5)} --file-allocation=none --console-log-level=error --summary-interval=0 -i {lst}")
            else:
                import shutil
                for f in todo:
                    with urllib.request.urlopen(f["url"], timeout=60) as r, open(f["path"], "wb") as out:
                        shutil.copyfileobj(r, out, 8 << 20)
        except Exception as e:
            done_flag["err"] = e
        finally:
            done_flag["v"] = True

    th = threading.Thread(target=worker, daemon=True)
    th.start()
    last = 0
    while not done_flag["v"]:
        time.sleep(5)
        have = sum(os.path.getsize(f["path"]) if os.path.exists(f["path"]) else 0 for f in files)
        pct = min(99.0, have * 100.0 / total)
        if time.time() - last > 10:
            stage("下载模型", pct)
            last = time.time()
    if done_flag["err"]:
        raise done_flag["err"]
    stage("下载模型", 100)
    say("INFO", f"模型就绪 {time.time()-t0:.0f}s")

    # 3. 启动 ComfyUI
    stage("启动 ComfyUI")
    def launch(use_sage):
        args = [sys.executable, "main.py", "--listen", "127.0.0.1", "--port", str(PORT), "--disable-auto-launch"] + CFG.get("extra_args", [])
        if use_sage:
            args.append("--use-sage-attention")
        log = open("/content/comfy.log", "a")
        return subprocess.Popen(args, cwd=ROOT, stdout=log, stderr=subprocess.STDOUT, start_new_session=True)
    proc = launch(sage)
    for i in range(240):
        if comfy_alive():
            break
        if proc.poll() is not None:
            if sage:
                say("WARN", "带 SageAttention 启动失败，改用默认注意力")
                sage = False
                proc = launch(False)
                continue
            say("ERR", open("/content/comfy.log").read()[-3000:])
            raise RuntimeError("ComfyUI 启动失败")
        time.sleep(1)
    if not comfy_alive():
        say("ERR", open("/content/comfy.log").read()[-3000:])
        raise RuntimeError("ComfyUI 启动超时")
    say("READY", f"用时 {time.time()-t0:.0f}s", "sage" if sage else "")
