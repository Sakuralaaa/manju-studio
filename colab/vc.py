# 漫剧工坊：生成后统一音色（demucs 分离人声 / 环境声 → Seed-VC 把人声转成角色母带的音色 → 和环境声混回去）
# 跑在视频运行时上（和 ComfyUI 同一台机器），第一次用到时才安装；可重复执行。
# 做法来自 2026-11-21 在 A100 上实测可用的脚本（bench/vc_gpu.sh / vc_run.sh）：
#   依赖装到 /content/vclib（--target，不动 ComfyUI 的环境），scipy / numpy 只装轮子并锁成机器上已有的版本；
#   装完删掉 vclib 里的 torch / numpy / 旧 typing 补丁包等（会顶掉系统里的新版本）；
#   给 seed-vc 的 bigvgan `_from_pretrained` 的 proxies / resume_download 参数补默认值（新版 huggingface_hub 不再传）。
import os, sys, json, time, glob, base64, shutil, hashlib, subprocess, threading, urllib.request, urllib.error

CFG = json.loads(base64.b64decode("__CFG__").decode())
PY = sys.executable
SEED = "/content/seed-vc"
LIB = "/content/vclib"
W = "/content/mj_vc"
OK = LIB + "/.mj_ok"
FAIL = "/content/mj_vc_setup.fail"
SETUP_LOG = "/content/mj_vc_setup.log"
SETUP_SH = "/content/mj_vc_setup.sh"
HF_HOME = W + "/hf"
TORCH_HOME = W + "/torch"
DRIVE = CFG.get("drive") or {}
DRIVE_STATUS = "/content/mj_vc_drive_status.json"


def say(*a):
    print("[mj]", *a, flush=True)


def stage(name):
    say("STAGE", name)


REQ = "\n".join([
    "accelerate", "librosa", "huggingface-hub>=0.28.1", "munch==4.0.0", "einops==0.8.0", "descript-audio-codec==1.0.0",
    "pydub==0.25.1", "resemblyzer", "transformers==4.46.3", "soundfile", "hydra-core==1.3.2", "pyyaml", "python-dotenv", "demucs",
]) + "\n"

SETUP = r"""set -e
cd /content
rm -f __FAIL__
rm -rf __LIB__
if [ ! -d __SEED__/.git ]; then rm -rf __SEED__; git clone -q --depth 1 __REPO__ __SEED__; fi
printf '%s' "__REQ__" > /content/mj_vc_req.txt
__PY__ -c "import scipy,numpy,torch,torchaudio;print('scipy=='+scipy.__version__);print('numpy=='+numpy.__version__);print('torch=='+torch.__version__.split('+')[0]);print('torchaudio=='+torchaudio.__version__.split('+')[0])" > /content/mj_vc_cons.txt
__PY__ -m pip install -q --only-binary=scipy,numpy --target __LIB__ -r /content/mj_vc_req.txt -c /content/mj_vc_cons.txt
cd __LIB__ && rm -rf torch torch-* torchaudio* torchvision* numpy* nvidia* triton* typing.py typing-* dataclasses.py enum enum34* bin functorch
cd /content
sed -i 's/proxies: Optional\[Dict\],/proxies: Optional[Dict] = None,/; s/resume_download: bool,/resume_download: bool = False,/' __SEED__/modules/bigvgan/bigvgan.py
PYTHONPATH=__LIB__:__SEED__ __PY__ -c "import demucs, munch, hydra, transformers, librosa; print('VCOK')"
touch __OK__
"""


def setup_script():
    s = SETUP
    for k, v in {"__FAIL__": FAIL, "__SEED__": SEED, "__REPO__": CFG.get("repo") or "https://github.com/Plachtaa/seed-vc",
                 "__REQ__": REQ, "__PY__": PY, "__LIB__": LIB, "__OK__": OK}.items():
        s = s.replace(k, v)
    return s


def setup_running():
    try:
        pid = int(open("/content/mj_vc_setup.pid").read().strip())
        os.kill(pid, 0)
        return True
    except Exception:
        return False


def start_setup():
    """后台安装（视频渲染期间就能装好）；已装好或正在装就什么都不做。"""
    if os.path.exists(OK):
        return "ready"
    if setup_running():
        return "installing"
    open(SETUP_SH, "w").write(setup_script())
    log = open(SETUP_LOG, "a")
    p = subprocess.Popen(["bash", "-c", f"bash {SETUP_SH} || touch {FAIL}"], stdout=log, stderr=subprocess.STDOUT, start_new_session=True)
    open("/content/mj_vc_setup.pid", "w").write(str(p.pid))
    return "started"


def wait_setup(timeout=1800):
    st = start_setup()
    if st == "ready":
        return
    stage("安装 Seed-VC / demucs")
    t0 = time.time()
    while not os.path.exists(OK):
        if os.path.exists(FAIL) and not setup_running():
            tail = open(SETUP_LOG).read()[-1500:] if os.path.exists(SETUP_LOG) else ""
            raise RuntimeError("安装 Seed-VC / demucs 失败：" + tail)
        if time.time() - t0 > timeout:
            raise RuntimeError("安装 Seed-VC / demucs 超时")
        time.sleep(3)
    say("INFO", f"Seed-VC / demucs 就绪 {time.time() - t0:.0f}s")


def env():
    e = dict(os.environ)
    e["PYTHONPATH"] = LIB + ":" + SEED
    e["HF_HOME"] = HF_HOME
    e["TORCH_HOME"] = TORCH_HOME
    e["HF_HUB_ENABLE_HF_TRANSFER"] = "0"
    return e


def run(cmd, cwd=None, what=""):
    r = subprocess.run(cmd, shell=True, capture_output=True, text=True, cwd=cwd, env=env())
    if r.returncode != 0:
        tail = (r.stderr or r.stdout)[-2000:]
        raise RuntimeError(f"{what or cmd.split()[0]} 失败：{tail}")
    return r.stdout


# ---------- 权重的云端硬盘缓存（和视频权重同一个文件夹） ----------

def weight_dirs():
    return [d for d in (SEED + "/checkpoints", HF_HOME, TORCH_HOME) if os.path.isdir(d)]


def drive_token():
    try:
        return open(DRIVE["token_file"]).read().strip()
    except Exception:
        return ""


def restore_weights():
    """云端硬盘里有缓存的权重包就先拉下来解开（只在这台机器第一次用时）。"""
    if not DRIVE.get("enabled") or os.path.exists(W + "/.weights_ok"):
        return
    fid = DRIVE.get("weights_id")
    if not fid:
        return
    stage("从云端硬盘读取变声权重")
    tar = W + "/weights.tar"
    try:
        req = urllib.request.Request(f"https://www.googleapis.com/drive/v3/files/{fid}?alt=media", headers={"Authorization": "Bearer " + drive_token()})
        with urllib.request.urlopen(req, timeout=120) as r, open(tar, "wb") as out:
            shutil.copyfileobj(r, out, 8 << 20)
        subprocess.run(f"tar -xf '{tar}' -C /", shell=True, check=True)
        open(W + "/.weights_ok", "w").write("1")
        say("INFO", "变声权重来自云端硬盘缓存")
    except Exception as e:
        say("WARN", "读取云端硬盘缓存失败，改从网上下载", str(e)[:200])
    finally:
        if os.path.exists(tar):
            os.remove(tar)


def drive_upload(path, name, folder):
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
                return json.load(urllib.request.urlopen(r, timeout=300)).get("id")
            except urllib.error.HTTPError as e:
                if e.code == 308:
                    rng = e.headers.get("Range")
                    off = int(rng.split("-")[1]) + 1 if rng else 0
                    continue
                raise
    return None


def upload_weights_bg():
    """第一次成功跑完后，把下载好的权重打成一个包放进云端硬盘缓存（后台线程，不耽误出片）。"""
    if not DRIVE.get("enabled") or not DRIVE.get("folder_id") or DRIVE.get("weights_id") or os.path.exists(W + "/.weights_up"):
        return
    open(W + "/.weights_up", "w").write("1")
    name = DRIVE.get("weights_name") or "mj_vc_weights.tar"

    def work():
        tar = W + "/upload_" + name
        try:
            dirs = " ".join("'" + d.lstrip("/") + "'" for d in weight_dirs())
            if not dirs:
                return
            subprocess.run(f"tar -cf '{tar}' -C / {dirs}", shell=True, check=True)
            fid = drive_upload(tar, name, DRIVE["folder_id"])
            if fid:
                st = {}
                try:
                    st = json.load(open(DRIVE_STATUS))
                except Exception:
                    pass
                st.setdefault("files", {})[name] = {"id": fid, "size": os.path.getsize(tar)}
                json.dump(st, open(DRIVE_STATUS, "w"))
        except Exception as e:
            print("[mj] WARN 变声权重上传缓存失败", str(e)[:200], flush=True)
        finally:
            if os.path.exists(tar):
                os.remove(tar)

    threading.Thread(target=work, daemon=True).start()


# ---------- 一条转换 ----------

def free_comfy():
    """显存不够时让 ComfyUI 先卸载模型（下一条视频会重新加载，慢 2 分钟左右）。"""
    try:
        req = urllib.request.Request("http://127.0.0.1:8188/free", data=json.dumps({"unload_models": True, "free_memory": True}).encode(),
                                     headers={"Content-Type": "application/json"}, method="POST")
        urllib.request.urlopen(req, timeout=30).read()
        time.sleep(3)
        return True
    except Exception:
        return False


def separate(src_wav, out_dir, name):
    voc = f"{out_dir}/htdemucs/{name}/vocals.wav"
    bed = f"{out_dir}/htdemucs/{name}/no_vocals.wav"
    if not (os.path.exists(voc) and os.path.exists(bed)):
        run(f"{PY} -m demucs --two-stems vocals -n htdemucs -o '{out_dir}' '{src_wav}'", what="demucs 分离人声")
    return voc, bed


def prepare_master(path, p):
    """母带：（可选）先分离掉配乐，再去掉首尾静音、响度归一，单声道 44.1k，最长 max_master_sec 秒。按内容缓存。"""
    h = hashlib.sha1(open(path, "rb").read()).hexdigest()[:12] + ("s" if p.get("separate_master", True) else "r")
    out = f"{W}/masters/{h}.wav"
    if os.path.exists(out):
        return out
    os.makedirs(f"{W}/masters", exist_ok=True)
    raw = f"{W}/masters/{h}_raw.wav"
    run(f"ffmpeg -y -loglevel error -i '{path}' -ac 2 -ar 44100 '{raw}'", what="读取母带")
    src = raw
    if p.get("separate_master", True):
        src, _ = separate(raw, f"{W}/masters/sep", f"{h}_raw")
    mx = float(p.get("max_master_sec") or 25)
    af = "silenceremove=start_periods=1:start_threshold=-40dB,areverse,silenceremove=start_periods=1:start_threshold=-40dB,areverse,loudnorm"
    run(f"ffmpeg -y -loglevel error -i '{src}' -af '{af}' -t {mx} -ac 1 -ar 44100 '{out}'", what="处理母带")
    return out


def convert(job):
    p = job.get("params") or {}
    vid = job["video"]
    tag = job["tag"]
    os.makedirs(W, exist_ok=True)
    wav = f"{W}/{tag}.wav"
    stage("分离人声")
    run(f"ffmpeg -y -loglevel error -i '{vid}' -vn -ac 2 -ar 44100 '{wav}'", what="抽取音轨")
    voc, bed = separate(wav, f"{W}/sep", tag)
    stage("处理母带")
    master = prepare_master(job["master"], p)
    out_dir = f"{W}/vc_{tag}"
    shutil.rmtree(out_dir, ignore_errors=True)
    os.makedirs(out_dir)
    f0 = bool(p.get("f0"))
    args = [f"--source '{voc}'", f"--target '{master}'", f"--output '{out_dir}'",
            f"--diffusion-steps {int(p.get('diffusion_steps') or 30)}", f"--length-adjust {float(p.get('length_adjust') or 1.0)}",
            f"--inference-cfg-rate {float(p.get('cfg_rate') if p.get('cfg_rate') is not None else 0.7)}",
            f"--f0-condition {f0}", "--fp16 True"]
    if f0:
        args.append(f"--auto-f0-adjust {bool(p.get('auto_f0', True))}")
        if int(p.get("semi_tone") or 0):
            args.append(f"--semi-tone-shift {int(p['semi_tone'])}")
    stage("Seed-VC 转换音色")
    cmd = f"{PY} inference.py " + " ".join(args)
    try:
        run(cmd, cwd=SEED, what="Seed-VC")
    except RuntimeError as e:
        if "out of memory" not in str(e).lower() or not free_comfy():
            raise
        say("WARN", "显存不够，已让 ComfyUI 卸载模型后重试")
        run(cmd, cwd=SEED, what="Seed-VC")
    outs = sorted(glob.glob(out_dir + "/*.wav"))
    if not outs:
        raise RuntimeError("Seed-VC 没有输出")
    stage("混音")
    res = f"{W}/{tag}_vc.mp4"
    vg = float(p.get("vocal_gain") or 1.0)
    fc = f"[1:a]aresample=44100,pan=stereo|c0=c0|c1=c0,volume={vg}[a1];[2:a][a1]amix=inputs=2:duration=first:normalize=0[a]"
    run(f"ffmpeg -y -loglevel error -i '{vid}' -i '{outs[0]}' -i '{bed}' -filter_complex '{fc}' -map 0:v -map '[a]' -c:v copy -c:a aac -b:a 192k '{res}'", what="混音")
    return res


if CFG.get("mode") == "setup_bg":
    say("SETUP", start_setup())
elif CFG.get("mode") == "drive_status":
    try:
        print("[mj] DRIVE " + json.dumps(json.load(open(DRIVE_STATUS))), flush=True)
    except Exception:
        print("[mj] DRIVE {}", flush=True)
else:
    t0 = time.time()
    wait_setup()
    restore_weights()
    out = convert(CFG["job"])
    upload_weights_bg()
    say("INFO", f"用时 {time.time() - t0:.0f}s")
    say("OUT", out)
