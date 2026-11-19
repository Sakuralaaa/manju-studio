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


POST = CFG.get("post") or {}
DRIVE = CFG.get("drive") or {}

def launch(use_sage):
    args = [sys.executable, "main.py", "--listen", "127.0.0.1", "--port", str(PORT), "--disable-auto-launch"] + CFG.get("extra_args", [])
    if use_sage:
        args.append("--use-sage-attention")
    log = open("/content/comfy.log", "a")
    return subprocess.Popen(args, cwd=ROOT, stdout=log, stderr=subprocess.STDOUT, start_new_session=True)

def wait_comfy(proc, sage):
    for i in range(240):
        if comfy_alive():
            return proc, sage
        if proc.poll() is not None:
            if sage:
                say("WARN", "带 SageAttention 启动失败，改用默认注意力")
                sage = False
                proc = launch(False)
                continue
            say("ERR", open("/content/comfy.log").read()[-3000:])
            raise RuntimeError("ComfyUI 启动失败")
        time.sleep(1)
    say("ERR", open("/content/comfy.log").read()[-3000:])
    raise RuntimeError("ComfyUI 启动超时")

def ensure_post():
    """画质后期组件：放大模型（只是文件）、补帧自定义节点（新装需要重启 ComfyUI）。返回是否新装了节点。"""
    new_node = False
    if POST.get("upscale") and POST.get("upscale_model"):
        d = os.path.join(ROOT, "models", "upscale_models")
        os.makedirs(d, exist_ok=True)
        dst = os.path.join(d, POST["upscale_model"])
        if not os.path.exists(dst):
            stage("下载放大模型")
            sh(f"curl -sSL -o '{dst}.part' '{POST['upscale_url']}' && mv '{dst}.part' '{dst}'")
    if POST.get("interp") and POST.get("interp_repo"):
        d = os.path.join(ROOT, "custom_nodes", "ComfyUI-Frame-Interpolation")
        if not os.path.exists(d):
            stage("安装补帧节点")
            sh(f"git clone --depth 1 {POST['interp_repo']} '{d}'")
            if os.path.exists(os.path.join(d, "install.py")):
                sh(f"cd '{d}' && {sys.executable} install.py")
            elif os.path.exists(os.path.join(d, "requirements-no-cupy.txt")):
                sh(f"{sys.executable} -m pip install -q -r '{d}/requirements-no-cupy.txt'")
            new_node = True
    return new_node

def drive_token():
    try:
        return open(DRIVE["token_file"]).read().strip()
    except Exception:
        return ""

def drive_status_write(files):
    try:
        json.dump({"files": files}, open(DRIVE["status_file"], "w"))
    except Exception:
        pass

def drive_upload(path, name, folder):
    """可续传上传到云端硬盘；每个分块前重新读取令牌（应用每 10 分钟刷新一次）。"""
    size = os.path.getsize(path)
    meta = json.dumps({"name": name, "parents": [folder]}).encode()
    req = urllib.request.Request("https://www.googleapis.com/upload/drive/v3/files?uploadType=resumable&fields=id,size", data=meta, method="POST",
                                 headers={"Authorization": "Bearer " + drive_token(), "Content-Type": "application/json; charset=UTF-8", "X-Upload-Content-Length": str(size)})
    loc = urllib.request.urlopen(req, timeout=60).headers["Location"]
    chunk = 64 << 20
    off = 0
    with open(path, "rb") as fh:
        while off < size:
            fh.seek(off)
            data = fh.read(chunk)
            end = off + len(data) - 1
            r = urllib.request.Request(loc, data=data, method="PUT", headers={"Authorization": "Bearer " + drive_token(), "Content-Range": f"bytes {off}-{end}/{size}"})
            try:
                resp = urllib.request.urlopen(r, timeout=300)
                return json.load(resp).get("id")
            except urllib.error.HTTPError as e:
                if e.code == 308:
                    rng = e.headers.get("Range")
                    off = int(rng.split("-")[1]) + 1 if rng else 0
                    continue
                raise
    return None

def drive_upload_all(files):
    """后台线程：把这次从网上下载的权重传到云端硬盘缓存文件夹。"""
    done = {}
    for f in files:
        try:
            fid = drive_upload(f["path"], f["name"], DRIVE["folder_id"])
            if fid:
                done[f["name"]] = {"id": fid, "size": os.path.getsize(f["path"])}
                drive_status_write(done)
        except Exception as e:
            print("[mj] WARN 上传缓存失败", f["name"], str(e)[:200], flush=True)

if comfy_alive():
    if ensure_post():
        stage("重启 ComfyUI")
        subprocess.run(f"pkill -f 'main.py --listen 127.0.0.1 --port {PORT}'", shell=True)
        time.sleep(3)
        wait_comfy(launch(False), False)
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
    cached = (DRIVE.get("files") or {}) if DRIVE.get("enabled") else {}
    from_drive = []
    for f in todo:
        c = cached.get(f["name"])
        if c and c.get("id") and (f["size"] == 0 or int(c.get("size") or 0) == f["size"]):
            f["drive_id"] = c["id"]
            from_drive.append(f)
    to_upload = [f for f in todo if "drive_id" not in f] if DRIVE.get("enabled") else []
    if from_drive:
        say("INFO", f"从云端硬盘缓存读取 {len(from_drive)} 个文件")
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
                        if "drive_id" in f:
                            fh.write(f"https://www.googleapis.com/drive/v3/files/{f['drive_id']}?alt=media\n  dir={f['dir']}\n  out={f['name']}\n  header=Authorization: Bearer {drive_token()}\n")
                        else:
                            fh.write(f"{f['url']}\n  dir={f['dir']}\n  out={f['name']}\n")
                n = CFG.get("threads", 16)
                sh(f"aria2c -q -c -x {n} -s {n} -k 4M -j {min(len(todo), 5)} --file-allocation=none --console-log-level=error --summary-interval=0 -i {lst}")
            else:
                import shutil
                for f in todo:
                    if "drive_id" in f:
                        req = urllib.request.Request(f"https://www.googleapis.com/drive/v3/files/{f['drive_id']}?alt=media", headers={"Authorization": "Bearer " + drive_token()})
                    else:
                        req = f["url"]
                    with urllib.request.urlopen(req, timeout=60) as r, open(f["path"], "wb") as out:
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
    if to_upload:
        say("INFO", f"后台把 {len(to_upload)} 个权重文件传到云端硬盘缓存")
        threading.Thread(target=drive_upload_all, args=(to_upload,), daemon=True).start()
    ensure_post()

    # 3. 启动 ComfyUI
    stage("启动 ComfyUI")
    proc, sage = wait_comfy(launch(sage), sage)
    say("READY", f"用时 {time.time()-t0:.0f}s", "sage" if sage else "")
