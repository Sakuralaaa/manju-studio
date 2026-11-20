package main

// 算力调度：一台 Colab 运行时 + 视频任务队列。
// 排队的视频攒在一次 GPU 会话里跑完（开机约 5 分钟 / ~1 CU 只付一次），
// 队列空闲超过 N 分钟或会话达到最长时长就自动关机。

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

type ColabCfg struct {
	Accelerator  string             `json:"accelerator"`
	Shape        string             `json:"shape"`
	IdleMin      int                `json:"idle_min"`
	MaxMin       int                `json:"max_min"`
	AutoStart    bool               `json:"auto_start"`
	BootMin      float64            `json:"boot_min"`
	ClipMin      float64            `json:"clip_min"`
	ClipMinTurbo float64            `json:"clip_min_turbo"`
	JobTimeout   int                `json:"job_timeout_min"`
	Rates        map[string]float64 `json:"rates"`
	UpscaleMin   float64            `json:"upscale_min"`
	InterpMin    float64            `json:"interp_min"`
	Mirror       string             `json:"mirror"`
	DriveCache   bool               `json:"drive_cache"`
}

// CU 每小时消耗（第三方估算，可在设置里改）
var defaultRates = map[string]float64{"G4": 8.9, "A100": 11.77, "L4": 4.82, "T4": 1.96, "H100": 18.0, "NONE": 0.07, "V5E1": 4.0, "V6E1": 6.0}

func loadColabCfg() ColabCfg {
	c := ColabCfg{Accelerator: "G4", Shape: "SHAPE_STANDARD", IdleMin: 5, MaxMin: 180, AutoStart: false, BootMin: 6, ClipMin: 4, ClipMinTurbo: 1.5, JobTimeout: 30, UpscaleMin: 1.5, InterpMin: 1}
	_ = json.Unmarshal([]byte(getSetting("colab_cfg", "{}")), &c)
	if c.Rates == nil {
		c.Rates = map[string]float64{}
	}
	for k, v := range defaultRates {
		if _, ok := c.Rates[k]; !ok {
			c.Rates[k] = v
		}
	}
	if c.IdleMin < 1 {
		c.IdleMin = 1
	}
	if c.MaxMin < 10 {
		c.MaxMin = 10
	}
	if c.JobTimeout < 5 {
		c.JobTimeout = 5
	}
	return c
}

func (c ColabCfg) rate(acc string) float64 {
	if r, ok := c.Rates[acc]; ok {
		return r
	}
	return 0
}

// ---------- 运行时状态 ----------

type Runner struct {
	mu          sync.Mutex
	Phase       string // off / creating / connecting / booting / ready / running / stopping
	Runtime     string
	Accelerator string
	Variant     string
	Shape       string
	Purpose     string // video / manual
	Booted      bool
	SessionID   int64
	StartedAt   int64
	ReadyAt     int64
	LastActive  int64
	Stage       string
	StagePct    float64
	CurrentJob  int64
	Message     string
	conn        *ConnInfo
	connExp     time.Time
	jup         *Jupyter
	logs        []string
	wake        chan struct{}
	stopReq     string
	cancelJob   context.CancelFunc
	bootCancel  context.CancelFunc
	postReady   VideoPost
	driveAt     time.Time
}

var runner = &Runner{Phase: "off", wake: make(chan struct{}, 1)}

func (r *Runner) poke() {
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

func (r *Runner) logf(format string, a ...any) {
	line := time.Now().Format("15:04:05") + "  " + fmt.Sprintf(format, a...)
	r.mu.Lock()
	r.logs = append(r.logs, line)
	if len(r.logs) > 500 {
		r.logs = r.logs[len(r.logs)-500:]
	}
	r.mu.Unlock()
}

type RunnerView struct {
	Phase       string  `json:"phase"`
	Runtime     string  `json:"runtime"`
	Accelerator string  `json:"accelerator"`
	Variant     string  `json:"variant"`
	Purpose     string  `json:"purpose"`
	Booted      bool    `json:"booted"`
	StartedAt   int64   `json:"started_at"`
	ReadyAt     int64   `json:"ready_at"`
	LastActive  int64   `json:"last_active"`
	Stage       string  `json:"stage"`
	StagePct    float64 `json:"stage_pct"`
	CurrentJob  int64   `json:"current_job"`
	Message     string  `json:"message"`
	Queued      int     `json:"queued"`
	Running     int     `json:"running"`
	CUUsed      float64 `json:"cu_used"`
	IdleLeftSec int64   `json:"idle_left_sec"`
	MaxLeftSec  int64   `json:"max_left_sec"`
	Authorized  bool    `json:"authorized"`
	ServerNow   int64   `json:"server_now"`
}

func (r *Runner) brief() RunnerView {
	r.mu.Lock()
	v := RunnerView{Phase: r.Phase, Runtime: r.Runtime, Accelerator: r.Accelerator, Variant: r.Variant, Purpose: r.Purpose, Booted: r.Booted,
		StartedAt: r.StartedAt, ReadyAt: r.ReadyAt, LastActive: r.LastActive, Stage: r.Stage, StagePct: r.StagePct, CurrentJob: r.CurrentJob, Message: r.Message}
	r.mu.Unlock()
	_ = db.QueryRow(`SELECT COUNT(*) FROM videos WHERE status='queued'`).Scan(&v.Queued)
	_ = db.QueryRow(`SELECT COUNT(*) FROM videos WHERE status='running'`).Scan(&v.Running)
	v.Authorized = loadOAuthClient() != nil && getSetting("colab_refresh_token", "") != ""
	v.ServerNow = now()
	cfg := loadColabCfg()
	if v.StartedAt > 0 && v.Phase != "off" {
		v.CUUsed = float64(now()-v.StartedAt) / 3_600_000 * cfg.rate(v.Accelerator)
		v.MaxLeftSec = (v.StartedAt + int64(cfg.MaxMin)*60_000 - now()) / 1000
		if v.Phase == "ready" && v.LastActive > 0 {
			v.IdleLeftSec = (v.LastActive + int64(cfg.IdleMin)*60_000 - now()) / 1000
		}
	}
	return v
}

func (r *Runner) token() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.conn == nil {
		return ""
	}
	return r.conn.Token
}

// ---------- 开机 / 关机 ----------

func specKey(acc, shape string) RuntimeKey {
	if shape == "" {
		shape = "SHAPE_STANDARD"
	}
	switch acc {
	case "", "NONE", "CPU":
		return RuntimeKey{Variant: "VARIANT_CPU", Accelerator: "NONE", Shape: shape}
	case "V5E1", "V6E1", "V2_8", "V5E8":
		return RuntimeKey{Variant: "VARIANT_TPU", Accelerator: acc, Shape: shape}
	}
	return RuntimeKey{Variant: "VARIANT_GPU", Accelerator: acc, Shape: shape}
}

// Start 申请一台运行时；purpose=video 时开机后自动准备视频环境
func (r *Runner) Start(acc, shape, purpose string) error {
	r.mu.Lock()
	if r.Phase != "off" {
		r.mu.Unlock()
		return errors.New("已经有一台运行时")
	}
	key := specKey(acc, shape)
	r.Phase, r.Accelerator, r.Variant, r.Shape, r.Purpose = "creating", key.Accelerator, key.Variant, key.Shape, purpose
	r.Booted, r.StartedAt, r.ReadyAt, r.LastActive, r.Stage, r.StagePct, r.Message, r.stopReq = false, now(), 0, now(), "申请运行时", 0, "", ""
	r.Runtime = "manju-" + strings.ToLower(randHex(4))
	r.logs = nil
	r.mu.Unlock()
	res, _ := db.Exec(`INSERT INTO sessions(runtime,accelerator,variant,shape,purpose,status,started_at) VALUES(?,?,?,?,?, 'creating', ?)`, "runtimes/"+r.Runtime, key.Accelerator, key.Variant, key.Shape, purpose, now())
	sid, _ := res.LastInsertId()
	r.mu.Lock()
	r.SessionID = sid
	r.mu.Unlock()
	r.logf("申请运行时 %s（%s）", accLabel(key.Accelerator), r.Runtime)
	go r.bringUp(key)
	return nil
}

func accLabel(acc string) string {
	if acc == "NONE" || acc == "" {
		return "CPU"
	}
	return acc
}

func (r *Runner) bringUp(key RuntimeKey) {
	ctx, cancel := context.WithCancel(context.Background())
	r.mu.Lock()
	r.bootCancel = cancel
	name := "runtimes/" + r.Runtime
	id := r.Runtime
	purpose := r.Purpose
	r.mu.Unlock()
	defer cancel()
	fail := func(msg string) {
		r.logf("失败：%s", msg)
		r.mu.Lock()
		r.Message = msg
		r.mu.Unlock()
		r.Stop("开机失败：" + msg)
	}
	if err := colabCreateRuntime(ctx, id, key); err != nil {
		fail(err.Error())
		return
	}
	r.setPhase("connecting", "等待分配")
	var rt *Runtime
	for i := 0; i < 90; i++ {
		if ctx.Err() != nil {
			return
		}
		x, err := colabGetRuntime(ctx, name)
		if err == nil && x.ConnectionInfo != nil && x.ConnectionInfo.URL != "" {
			rt = x
			break
		}
		time.Sleep(5 * time.Second)
	}
	if rt == nil {
		fail("等待分配超时")
		return
	}
	r.setConn(rt.ConnectionInfo)
	jup := newJupyter(rt.ConnectionInfo.URL, r.token)
	for i := 0; i < 30; i++ {
		if err := jup.Ping(ctx); err == nil {
			break
		}
		time.Sleep(3 * time.Second)
	}
	r.mu.Lock()
	r.jup = jup
	r.mu.Unlock()
	_, _ = db.Exec(`UPDATE sessions SET status='ready', ready_at=? WHERE id=?`, now(), r.SessionID)
	r.logf("运行时已连接")
	// 先跑一段探测，确认内核可用
	res, err := jup.Exec(ctx, "import subprocess,platform\nprint(platform.python_version())\nprint(subprocess.run('nvidia-smi --query-gpu=name,memory.total --format=csv,noheader',shell=True,capture_output=True,text=True).stdout.strip() or 'CPU')", nil)
	if err != nil {
		fail("内核不可用：" + err.Error())
		return
	}
	if res.Error != "" {
		fail("内核报错：" + res.Error)
		return
	}
	r.logf("环境：Python %s", strings.ReplaceAll(strings.TrimSpace(res.Stdout), "\n", " · "))
	r.mu.Lock()
	r.ReadyAt, r.LastActive = now(), now()
	r.mu.Unlock()
	if purpose == "cache" {
		r.setPhase("booting", "预缓存模型")
		n, err := r.precache(ctx)
		if err != nil {
			fail(err.Error())
			return
		}
		r.Stop(fmt.Sprintf("预缓存完成，新增 %d 个文件", n))
		return
	}
	if purpose == "video" {
		r.setPhase("booting", "准备视频环境")
		if err := r.bootstrap(ctx); err != nil {
			fail(err.Error())
			return
		}
		r.mu.Lock()
		r.Booted = true
		bootSec := float64(now()-r.ReadyAt) / 1000
		r.mu.Unlock()
		_, _ = db.Exec(`UPDATE sessions SET boot_sec=? WHERE id=?`, bootSec, r.SessionID)
	}
	r.setPhase("ready", "")
	r.touch()
	r.poke()
}

func (r *Runner) setPhase(p, stage string) {
	r.mu.Lock()
	if r.Phase != "stopping" && r.Phase != "off" {
		r.Phase = p
	}
	r.Stage, r.StagePct = stage, 0
	r.mu.Unlock()
}

func (r *Runner) setConn(c *ConnInfo) {
	exp, err := time.Parse(time.RFC3339Nano, c.ExpireTime)
	if err != nil {
		exp = time.Now().Add(50 * time.Minute)
	}
	r.mu.Lock()
	r.conn = c
	r.connExp = exp
	r.mu.Unlock()
}

func (r *Runner) touch() {
	r.mu.Lock()
	r.LastActive = now()
	r.mu.Unlock()
}

// Stop 删除运行时（异步），reason 记入会话
func (r *Runner) Stop(reason string) {
	r.mu.Lock()
	if r.Phase == "off" || r.Phase == "stopping" {
		r.mu.Unlock()
		return
	}
	r.Phase = "stopping"
	r.Stage = "关机中"
	name := "runtimes/" + r.Runtime
	sid := r.SessionID
	if r.cancelJob != nil {
		r.cancelJob()
	}
	if r.bootCancel != nil {
		r.bootCancel()
	}
	r.mu.Unlock()
	r.logf("关机：%s", reason)
	go func() {
		if loadColabCfg().DriveCache {
			r.driveSync()
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		var err error
		for i := 0; i < 4; i++ {
			if err = colabDeleteRuntime(ctx, name); err == nil {
				break
			}
			time.Sleep(5 * time.Second)
		}
		if err != nil {
			r.logf("删除运行时失败：%s", err.Error())
		}
		var done int
		_ = db.QueryRow(`SELECT COUNT(*) FROM videos WHERE session_id=? AND status='done'`, sid).Scan(&done)
		_, _ = db.Exec(`UPDATE sessions SET status='ended', end_reason=?, jobs_done=?, ended_at=? WHERE id=?`, reason, done, now(), sid)
		r.mu.Lock()
		r.Phase, r.Runtime, r.jup, r.conn, r.Booted, r.CurrentJob, r.Stage, r.StagePct = "off", "", nil, nil, false, 0, "", 0
		r.postReady = VideoPost{}
		r.StartedAt, r.ReadyAt = 0, 0
		if strings.HasPrefix(reason, "开机失败") {
			_ = setSetting("colab_autostart_block", "1") // 开机失败后不再自动重试，等手动开机
			r.Message = strings.TrimPrefix(reason, "开机失败：")
		} else {
			r.Message = ""
		}
		r.mu.Unlock()
		r.logf("已关机")
	}()
}

// ---------- 调度循环 ----------

func (r *Runner) loop() {
	// 启动时清理上次遗留的本应用运行时（服务重启后不再受控）
	go func() {
		time.Sleep(3 * time.Second)
		if getSetting("colab_refresh_token", "") == "" {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		rts, err := colabRuntimes(ctx)
		if err != nil {
			return
		}
		for _, rt := range rts {
			if strings.HasPrefix(rt.Name, "runtimes/manju-") {
				log.Printf("清理遗留运行时 %s", rt.Name)
				_ = colabDeleteRuntime(ctx, rt.Name)
			}
		}
	}()
	t := time.NewTicker(3 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-t.C:
		case <-r.wake:
		}
		r.tick()
	}
}

func (r *Runner) tick() {
	cfg := loadColabCfg()
	var queued int
	_ = db.QueryRow(`SELECT COUNT(*) FROM videos WHERE status='queued'`).Scan(&queued)
	r.mu.Lock()
	phase, booted, purpose, started, last := r.Phase, r.Booted, r.Purpose, r.StartedAt, r.LastActive
	connExp := r.connExp
	r.mu.Unlock()

	switch phase {
	case "off":
		if queued > 0 && cfg.AutoStart && getSetting("colab_refresh_token", "") != "" && getSetting("colab_autostart_block", "") == "" {
			if err := r.Start(cfg.Accelerator, cfg.Shape, "video"); err == nil {
				r.logf("队列里有 %d 个视频，自动开机", queued)
			}
		}
		return
	case "ready":
	default:
		return
	}
	// 令牌快到期就刷新
	if time.Until(connExp) < 10*time.Minute {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		if rt, err := colabGetRuntime(ctx, "runtimes/"+r.Runtime); err == nil && rt.ConnectionInfo != nil {
			r.setConn(rt.ConnectionInfo)
		} else if err != nil {
			var ce *colabErr
			if errors.As(err, &ce) && ce.Code == 404 {
				cancel()
				r.Stop("运行时已被回收")
				return
			}
		}
		cancel()
	}
	if now()-started > int64(cfg.MaxMin)*60_000 {
		r.Stop("达到最长会话时长")
		return
	}
	if cfg.DriveCache && booted && time.Since(r.driveAt) > 10*time.Minute {
		r.driveAt = time.Now()
		go r.driveSync()
	}
	if queued > 0 && purpose == "video" && booted {
		r.runNext(cfg)
		return
	}
	if queued > 0 && purpose == "video" && !booted {
		return
	}
	if now()-last > int64(cfg.IdleMin)*60_000 {
		r.Stop(fmt.Sprintf("空闲 %d 分钟", cfg.IdleMin))
	}
}

func (r *Runner) bootstrap(ctx context.Context) error {
	return r.bootstrapWith(ctx, r.neededPost())
}

func (r *Runner) bootstrapWith(ctx context.Context, post VideoPost) error {
	cfgText := workflowText("boot")
	var boot map[string]any
	_ = json.Unmarshal([]byte(cfgText), &boot)
	r.mu.Lock()
	jup := r.jup
	r.mu.Unlock()
	boot = bootConfig(ctx, jup, boot, post)
	code := strings.Replace(bootstrapPy, "__CFG__", base64.StdEncoding.EncodeToString([]byte(mustJSON(boot))), 1)
	bctx, cancel := context.WithTimeout(ctx, 60*time.Minute)
	defer cancel()
	var buf strings.Builder
	res, err := jup.Exec(bctx, code, func(stream, text string) {
		r.touch()
		buf.WriteString(text)
		for {
			s := buf.String()
			i := strings.Index(s, "\n")
			if i < 0 {
				break
			}
			line := s[:i]
			buf.Reset()
			buf.WriteString(s[i+1:])
			r.handleBootLine(line)
		}
	})
	if err != nil {
		return errors.New("准备环境失败：" + err.Error())
	}
	if res.Error != "" {
		return errors.New("准备环境失败：" + res.Error)
	}
	if !strings.Contains(res.Stdout, "[mj] READY") {
		return errors.New("准备环境没有完成")
	}
	r.mu.Lock()
	r.postReady = post
	r.mu.Unlock()
	return nil
}

func (r *Runner) handleBootLine(line string) {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "[mj] ") {
		return
	}
	body := strings.TrimPrefix(line, "[mj] ")
	switch {
	case strings.HasPrefix(body, "STAGE "):
		st := strings.TrimPrefix(body, "STAGE ")
		pct := 0.0
		if i := strings.LastIndex(st, " "); i > 0 {
			if v, err := strconv.ParseFloat(st[i+1:], 64); err == nil {
				pct, st = v, st[:i]
			}
		}
		r.mu.Lock()
		changed := r.Stage != st
		r.Stage, r.StagePct = st, pct
		r.mu.Unlock()
		if changed {
			r.logf("%s", st)
		}
	case strings.HasPrefix(body, "CACHED"):
		r.logf("预缓存完成：新传 %s 个文件", strings.TrimSpace(strings.TrimPrefix(body, "CACHED")))
	case strings.HasPrefix(body, "READY"):
		r.logf("视频环境就绪 %s", strings.TrimSpace(strings.TrimPrefix(body, "READY")))
	default:
		r.logf("%s", body)
	}
}

// ---------- 执行一个视频任务 ----------

func (r *Runner) runNext(cfg ColabCfg) {
	var id int64
	if err := db.QueryRow(`SELECT id FROM videos WHERE status='queued' ORDER BY id LIMIT 1`).Scan(&id); err != nil {
		return
	}
	v, err := getVideo(id)
	if err != nil {
		return
	}
	r.mu.Lock()
	sid := r.SessionID
	jup := r.jup
	r.Phase, r.CurrentJob = "running", id
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(cfg.JobTimeout+5)*time.Minute)
	r.cancelJob = cancel
	r.mu.Unlock()
	defer func() {
		cancel()
		r.mu.Lock()
		r.cancelJob = nil
		r.CurrentJob = 0
		if r.Phase == "running" {
			r.Phase = "ready"
		}
		r.LastActive = now()
		r.mu.Unlock()
		r.poke()
	}()
	if err := r.ensurePost(ctx, v.Post); err != nil {
		r.logf("准备后期环境失败：%s", err.Error())
	}
	_, _ = db.Exec(`UPDATE videos SET status='running', stage='上传关键帧', progress=0, started_at=?, session_id=?, error='' WHERE id=?`, now(), sid, id)
	r.logf("开始视频 #%d", id)
	start := time.Now()
	out, err := r.renderVideo(ctx, jup, v, cfg)
	if err != nil {
		var st string
		_ = db.QueryRow(`SELECT status FROM videos WHERE id=?`, id).Scan(&st)
		if st == "canceled" {
			r.logf("视频 #%d 已取消", id)
			return
		}
		r.mu.Lock()
		stopping := r.Phase == "stopping"
		r.mu.Unlock()
		if stopping {
			_, _ = db.Exec(`UPDATE videos SET status='queued', progress=0, stage='', session_id=0 WHERE id=?`, id)
			r.logf("视频 #%d 退回队列", id)
			return
		}
		r.logf("视频 #%d 失败：%s", id, err.Error())
		_, _ = db.Exec(`UPDATE videos SET status='failed', error=?, stage='', finished_at=? WHERE id=?`, err.Error(), now(), id)
		// 运行时没了：退出会话，剩下的留在队列里
		if jerr := jup.Ping(context.Background()); jerr != nil {
			r.Stop("运行时失联")
		}
		return
	}
	fi, _ := os.Stat(filepath.Join(dataDir, "videos", out))
	var size int64
	if fi != nil {
		size = fi.Size()
	}
	_, _ = db.Exec(`UPDATE videos SET status='done', file=?, bytes=?, progress=1, stage='', finished_at=? WHERE id=?`, out, size, now(), id)
	mins := time.Since(start).Minutes()
	r.mu.Lock()
	acc := r.Accelerator
	r.mu.Unlock()
	_, _ = db.Exec(`INSERT INTO ledger(project_id,episode_id,kind,count,minutes,cu,est_cu,ref_id,session_id,created_at) VALUES(?,?, 'video', 1, ?,?,?,?,?,?)`,
		v.ProjectID, v.EpisodeID, mins, mins/60*cfg.rate(acc), v.EstCU, id, sid, now())
	if v.ShotID > 0 {
		_, _ = db.Exec(`UPDATE shots SET picked_video_id=? WHERE id=? AND (picked_video_id=0 OR picked_video_id NOT IN (SELECT id FROM videos WHERE status='done'))`, id, v.ShotID)
		touchProject(v.ProjectID)
	}
	_, _ = db.Exec(`UPDATE sessions SET jobs_done=jobs_done+1 WHERE id=?`, sid)
	r.logf("视频 #%d 完成，用时 %s", id, time.Since(start).Round(time.Second))
}

func (r *Runner) renderVideo(ctx context.Context, jup *Jupyter, v *Video, cfg ColabCfg) (string, error) {
	img, err := os.ReadFile(filepath.Join(dataDir, "images", v.ImageFile))
	if err != nil {
		return "", errors.New("关键帧文件不存在")
	}
	ext := filepath.Ext(v.ImageFile)
	remote := fmt.Sprintf("mj_%d_%s%s", v.ID, randHex(3), ext)
	if err := jup.Upload(ctx, "/content/ComfyUI/input/"+remote, img); err != nil {
		return "", err
	}
	lastRemote := ""
	if v.LastImageFile != "" {
		img2, err := os.ReadFile(filepath.Join(dataDir, "images", v.LastImageFile))
		if err != nil {
			return "", errors.New("尾帧文件不存在")
		}
		lastRemote = fmt.Sprintf("mj_%d_last_%s%s", v.ID, randHex(3), filepath.Ext(v.LastImageFile))
		if err := jup.Upload(ctx, "/content/ComfyUI/input/"+lastRemote, img2); err != nil {
			return "", err
		}
	}
	key := "standard"
	if v.Turbo {
		key = "turbo"
	}
	w, h := videoDims(v.Aspect, v.Res)
	seed := v.Seed
	if seed == 0 {
		seed = int64(time.Now().UnixNano() % 1_000_000_000_000)
	}
	wf, err := fillWorkflow(workflowText(key), map[string]any{
		"PROMPT": v.Prompt, "IMAGE": remote, "WIDTH": w, "HEIGHT": h, "LENGTH": framesFor(v.Seconds), "FPS": loadFrameRule().FPS, "SEED": seed, "PREFIX": fmt.Sprintf("manju/v%d", v.ID),
	})
	if err != nil {
		return "", errors.New("工作流格式错误：" + err.Error())
	}
	if lastRemote != "" {
		if err := injectLastFrame(wf, lastRemote); err != nil {
			return "", err
		}
	}
	if v.Post.Upscale || v.Post.Interp {
		pw, ph := deliverDimsFor(v.Aspect)
		if err := injectPost(wf, v.Post, pw, ph); err != nil {
			return "", err
		}
	}
	code := strings.Replace(jobPy, "__WF__", base64.StdEncoding.EncodeToString([]byte(mustJSON(wf))), 1)
	code = strings.Replace(code, "__TIMEOUT__", strconv.Itoa(cfg.JobTimeout*60), 1)
	_, _ = db.Exec(`UPDATE videos SET stage='生成中', seed=? WHERE id=?`, seed, v.ID)
	var outPath string
	var buf strings.Builder
	res, err := jup.Exec(ctx, code, func(stream, text string) {
		r.touch()
		buf.WriteString(text)
		sc := bufio.NewScanner(strings.NewReader(buf.String()))
		complete := strings.HasSuffix(buf.String(), "\n")
		var lines []string
		for sc.Scan() {
			lines = append(lines, sc.Text())
		}
		buf.Reset()
		if !complete && len(lines) > 0 {
			buf.WriteString(lines[len(lines)-1])
			lines = lines[:len(lines)-1]
		}
		for _, ln := range lines {
			f := strings.Fields(strings.TrimSpace(ln))
			if len(f) < 2 || f[0] != "[mj]" {
				continue
			}
			switch f[1] {
			case "P":
				if len(f) >= 4 {
					val, _ := strconv.ParseFloat(f[2], 64)
					max, _ := strconv.ParseFloat(f[3], 64)
					if max > 0 {
						p := math.Min(0.98, val/max)
						_, _ = db.Exec(`UPDATE videos SET progress=? WHERE id=?`, p, v.ID)
					}
				}
			case "OUT":
				outPath = strings.TrimSpace(strings.SplitN(ln, "OUT", 2)[1])
			}
		}
	})
	if err != nil {
		return "", err
	}
	if res.Error != "" {
		return "", errors.New(res.Error)
	}
	if outPath == "" {
		for _, ln := range strings.Split(res.Stdout, "\n") {
			if strings.HasPrefix(ln, "[mj] OUT ") {
				outPath = strings.TrimSpace(strings.TrimPrefix(ln, "[mj] OUT "))
			}
		}
	}
	if outPath == "" {
		return "", errors.New("没有拿到输出文件")
	}
	_, _ = db.Exec(`UPDATE videos SET stage='回传视频' WHERE id=?`, v.ID)
	ext2 := filepath.Ext(outPath)
	if ext2 == "" {
		ext2 = ".mp4"
	}
	name := fmt.Sprintf("v%d-%s%s", v.ID, randHex(4), ext2)
	f, err := os.Create(filepath.Join(dataDir, "videos", name))
	if err != nil {
		return "", err
	}
	_, err = jup.Download(ctx, outPath, f)
	f.Close()
	if err != nil {
		_ = os.Remove(filepath.Join(dataDir, "videos", name))
		return "", err
	}
	return name, nil
}

// ---------- 页面接口 ----------

func handleColabState(w http.ResponseWriter, r *http.Request) {
	c := loadOAuthClient()
	var client any
	if c != nil {
		tail := c.ClientID
		if i := strings.Index(tail, "-"); i > 0 {
			tail = tail[:i]
		}
		if len(tail) > 6 {
			tail = "••••" + tail[len(tail)-4:]
		}
		client = map[string]any{"id_tail": tail, "project": c.ProjectID}
	}
	rows, _ := db.Query(`SELECT id,runtime,accelerator,purpose,status,end_reason,jobs_done,started_at,ready_at,ended_at FROM sessions ORDER BY id DESC LIMIT 8`)
	sessions := []map[string]any{}
	cfg := loadColabCfg()
	if rows != nil {
		for rows.Next() {
			var id, jobs, st, rd, en int64
			var rt, acc, purpose, status, reason string
			if rows.Scan(&id, &rt, &acc, &purpose, &status, &reason, &jobs, &st, &rd, &en) == nil {
				end := en
				if end == 0 {
					end = now()
				}
				sessions = append(sessions, map[string]any{"id": id, "accelerator": accLabel(acc), "purpose": purpose, "status": status, "end_reason": reason,
					"jobs_done": jobs, "started_at": st, "ended_at": en, "minutes": float64(end-st) / 60000, "cu": float64(end-st) / 3_600_000 * cfg.rate(acc)})
			}
		}
		rows.Close()
	}
	writeJSON(w, map[string]any{
		"client":     client,
		"authorized": getSetting("colab_refresh_token", "") != "",
		"info":       cachedColabInfo(),
		"cfg":        cfg,
		"runner":     runner.brief(),
		"sessions":   sessions,
		"drive":      driveView(r.Context()),
	})
}

func handleColabSettings(w http.ResponseWriter, r *http.Request) {
	var in ColabCfg
	if err := readJSON(r, &in); err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	cur := loadColabCfg()
	if in.Accelerator != "" {
		cur.Accelerator = in.Accelerator
	}
	if in.Shape != "" {
		cur.Shape = in.Shape
	}
	if in.IdleMin > 0 {
		cur.IdleMin = in.IdleMin
	}
	if in.MaxMin > 0 {
		cur.MaxMin = in.MaxMin
	}
	if in.BootMin > 0 {
		cur.BootMin = in.BootMin
	}
	if in.ClipMin > 0 {
		cur.ClipMin = in.ClipMin
	}
	if in.ClipMinTurbo > 0 {
		cur.ClipMinTurbo = in.ClipMinTurbo
	}
	if in.JobTimeout > 0 {
		cur.JobTimeout = in.JobTimeout
	}
	cur.AutoStart = in.AutoStart
	if in.UpscaleMin > 0 {
		cur.UpscaleMin = in.UpscaleMin
	}
	if in.InterpMin > 0 {
		cur.InterpMin = in.InterpMin
	}
	cur.Mirror = strings.TrimRight(strings.TrimSpace(in.Mirror), "/")
	cur.DriveCache = in.DriveCache
	for k, v := range in.Rates {
		if v >= 0 {
			cur.Rates[k] = v
		}
	}
	_ = setSetting("colab_cfg", mustJSON(cur))
	runner.poke()
	writeJSON(w, cur)
}

func handleColabStart(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Accelerator string `json:"accelerator"`
		Shape       string `json:"shape"`
		Purpose     string `json:"purpose"`
	}
	_ = readJSON(r, &in)
	if err := startRuntime(in.Accelerator, in.Shape, in.Purpose); err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	writeJSON(w, runner.brief())
}

func startRuntime(acc, shape, purpose string) error {
	if getSetting("colab_refresh_token", "") == "" {
		return errors.New("还没有授权 Colab")
	}
	cfg := loadColabCfg()
	if acc == "" {
		acc = cfg.Accelerator
	}
	if shape == "" {
		shape = cfg.Shape
	}
	if purpose == "cache" {
		if !cfg.DriveCache {
			return errors.New("先在「调度」里打开云端硬盘缓存并保存")
		}
		if !hasDriveScope(context.Background()) {
			return errors.New("授权里没有云端硬盘权限，先在概览里重新授权")
		}
		acc, shape = "NONE", "SHAPE_STANDARD"
	} else if purpose != "manual" {
		purpose = "video"
	}
	if purpose == "video" && specKey(acc, shape).Variant != "VARIANT_GPU" {
		return errors.New("视频生成需要 GPU 运行时")
	}
	delSetting("colab_autostart_block")
	return runner.Start(acc, shape, purpose)
}

func handleColabStop(w http.ResponseWriter, r *http.Request) {
	runner.Stop("手动关机")
	writeJSON(w, map[string]any{"ok": true})
}

func handleColabLog(w http.ResponseWriter, r *http.Request) {
	runner.mu.Lock()
	logs := append([]string{}, runner.logs...)
	runner.mu.Unlock()
	writeJSON(w, map[string]any{"lines": logs})
}

// execOnRuntime 在当前运行时上执行代码（对外接口 / 调试用）
func execOnRuntime(ctx context.Context, code string) (*ExecResult, error) {
	runner.mu.Lock()
	jup := runner.jup
	phase := runner.Phase
	runner.mu.Unlock()
	if jup == nil || (phase != "ready" && phase != "running" && phase != "booting") {
		return nil, errors.New("运行时还没就绪")
	}
	runner.touch()
	defer runner.touch()
	return jup.Exec(ctx, code, nil)
}

func handleColabExec(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Code    string `json:"code"`
		Timeout int    `json:"timeout"`
	}
	if err := readJSON(r, &in); err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	to := in.Timeout
	if to <= 0 || to > 1800 {
		to = 300
	}
	ctx, cancel := context.WithTimeout(r.Context(), time.Duration(to)*time.Second)
	defer cancel()
	res, err := execOnRuntime(ctx, in.Code)
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	writeJSON(w, res)
}

// estimateCU 估算一批视频的 CU：没开机时含开机时间
func estimateCU(n int, turbo bool) map[string]any {
	cfg := loadColabCfg()
	b := runner.brief()
	acc := cfg.Accelerator
	boot := cfg.BootMin
	if b.Phase != "off" && b.Variant == "VARIANT_GPU" {
		acc = b.Accelerator
		if b.Booted {
			boot = 0
		}
	}
	per := cfg.ClipMin
	if turbo {
		per = cfg.ClipMinTurbo
	}
	queuedMin := float64(b.Queued) * cfg.ClipMin
	mins := boot + float64(n)*per
	return map[string]any{"cu": mins / 60 * cfg.rate(acc), "minutes": mins, "accelerator": accLabel(acc), "rate": cfg.rate(acc), "boot_min": boot, "ahead_min": queuedMin}
}

var _ = http.StatusOK
