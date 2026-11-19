package main

// 一键成集：梗概 → 剧本 → 资产（含音色）→ 分镜 → 关键帧 → 配音 → 视频（只排队）→ 成片 → 摘要与封面。
// 两种模式：全自动 / 关键处问我（剧本、资产、分镜之后暂停等确认）。每一步都先检查是否已完成，可随时中断后续跑。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
)

type PipeLog struct {
	T    int64  `json:"t"`
	Text string `json:"text"`
}

type Pipe struct {
	ID        int64     `json:"id"`
	EpisodeID int64     `json:"episode_id"`
	Mode      string    `json:"mode"`
	Status    string    `json:"status"` // running / confirm / waiting / done / failed / canceled
	Stage     string    `json:"stage"`
	Message   string    `json:"message"`
	Confirmed []string  `json:"confirmed"`
	Log       []PipeLog `json:"log"`
	StartedAt int64     `json:"started_at"`
	UpdatedAt int64     `json:"updated_at"`
}

var pipeStages = []string{"idea", "script", "assets", "storyboard", "keyframes", "voice", "videos", "final", "wrap"}

func scanPipe(sc interface{ Scan(...any) error }) (*Pipe, error) {
	var p Pipe
	var conf, lg string
	if err := sc.Scan(&p.ID, &p.EpisodeID, &p.Mode, &p.Status, &p.Stage, &p.Message, &conf, &lg, &p.StartedAt, &p.UpdatedAt); err != nil {
		return nil, err
	}
	_ = json.Unmarshal([]byte(conf), &p.Confirmed)
	_ = json.Unmarshal([]byte(lg), &p.Log)
	if p.Confirmed == nil {
		p.Confirmed = []string{}
	}
	if p.Log == nil {
		p.Log = []PipeLog{}
	}
	return &p, nil
}

const pipeCols = `id,episode_id,mode,status,stage,message,confirmed,log,started_at,updated_at`

func latestPipe(eid int64) *Pipe {
	p, err := scanPipe(db.QueryRow(`SELECT `+pipeCols+` FROM pipelines WHERE episode_id=? ORDER BY id DESC LIMIT 1`, eid))
	if err != nil {
		return nil
	}
	return p
}

func getPipe(id int64) *Pipe {
	p, err := scanPipe(db.QueryRow(`SELECT `+pipeCols+` FROM pipelines WHERE id=?`, id))
	if err != nil {
		return nil
	}
	return p
}

var (
	pipeMu      sync.Mutex
	pipeCancels = map[int64]context.CancelFunc{}
)

func (p *Pipe) save() {
	p.UpdatedAt = now()
	if len(p.Log) > 200 {
		p.Log = p.Log[len(p.Log)-200:]
	}
	_, _ = db.Exec(`UPDATE pipelines SET status=?, stage=?, message=?, confirmed=?, log=?, updated_at=? WHERE id=?`, p.Status, p.Stage, p.Message, mustJSON(p.Confirmed), mustJSON(p.Log), p.UpdatedAt, p.ID)
}

func (p *Pipe) say(format string, a ...any) {
	p.Log = append(p.Log, PipeLog{T: now(), Text: fmt.Sprintf(format, a...)})
	p.save()
}

func startPipe(eid int64, mode string) (*Pipe, error) {
	if lp := latestPipe(eid); lp != nil && (lp.Status == "running" || lp.Status == "waiting" || lp.Status == "confirm") {
		return nil, errors.New("这一集已经在一键成集中")
	}
	if mode != "ask" {
		mode = "auto"
	}
	res, err := db.Exec(`INSERT INTO pipelines(episode_id,mode,status,started_at,updated_at) VALUES(?,?, 'running', ?, ?)`, eid, mode, now(), now())
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	p := getPipe(id)
	p.say("开始一键成集（%s）", map[string]string{"auto": "全自动", "ask": "关键处问我"}[mode])
	go runPipe(id)
	return p, nil
}

func cancelPipe(eid int64) {
	lp := latestPipe(eid)
	if lp == nil {
		return
	}
	pipeMu.Lock()
	if c := pipeCancels[lp.ID]; c != nil {
		c()
	}
	pipeMu.Unlock()
	if lp.Status == "running" || lp.Status == "waiting" || lp.Status == "confirm" {
		lp.Status, lp.Message = "canceled", "已停止"
		lp.say("已停止")
	}
}

// resumePipes 服务重启后接着跑
func resumePipes() {
	rows, err := db.Query(`SELECT id FROM pipelines WHERE status IN ('running','waiting')`)
	if err != nil {
		return
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	for _, id := range ids {
		if p := getPipe(id); p != nil {
			p.say("服务重启，继续")
		}
		go runPipe(id)
	}
}

func runPipe(id int64) {
	ctx, cancel := context.WithCancel(context.Background())
	pipeMu.Lock()
	if pipeCancels[id] != nil {
		pipeMu.Unlock()
		cancel()
		return
	}
	pipeCancels[id] = cancel
	pipeMu.Unlock()
	defer func() {
		pipeMu.Lock()
		delete(pipeCancels, id)
		pipeMu.Unlock()
		cancel()
		if r := recover(); r != nil {
			log.Printf("一键成集 %d 崩溃：%v", id, r)
			if p := getPipe(id); p != nil {
				p.Status, p.Message = "failed", fmt.Sprintf("内部错误：%v", r)
				p.save()
			}
		}
	}()
	pp := getPipe(id)
	if pp == nil {
		return
	}
	pp.Status = "running"
	pp.save()
	err := pipeBody(ctx, pp)
	if ctx.Err() != nil {
		return
	}
	if errors.Is(err, errPipePause) {
		return
	}
	if err != nil {
		pp.Status, pp.Message = "failed", err.Error()
		pp.say("失败：%s", err.Error())
		return
	}
	pp.Status, pp.Stage, pp.Message = "done", "wrap", "本集完成"
	pp.say("本集完成")
}

var errPipePause = errors.New("pause")

func (pp *Pipe) confirmed(stage string) bool {
	return pp.Mode != "ask" || contains(pp.Confirmed, stage)
}

func (pp *Pipe) askConfirm(stage, msg string) error {
	if pp.confirmed(stage) {
		return nil
	}
	pp.Status, pp.Stage, pp.Message = "confirm", stage, msg
	pp.say("等待确认：%s", msg)
	return errPipePause
}

func (pp *Pipe) setStage(stage, msg string) {
	if pp.Stage != stage || pp.Message != msg {
		pp.Stage, pp.Message = stage, msg
		if pp.Status != "running" {
			pp.Status = "running"
		}
		pp.say("%s", msg)
	}
}

// waitTask 等一个后台任务结束
func waitTask(ctx context.Context, tid int64) error {
	for {
		var st, er string
		if err := db.QueryRow(`SELECT status, error FROM tasks WHERE id=?`, tid).Scan(&st, &er); err != nil {
			return errors.New("任务丢失")
		}
		switch st {
		case "done":
			return nil
		case "failed":
			return errors.New(er)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

// waitIdle 等某作品下指定种类的任务全部结束
func waitIdle(ctx context.Context, pid int64, kinds ...string) error {
	q := `SELECT COUNT(*) FROM tasks WHERE project_id=? AND status='running' AND kind IN ('` + strings.Join(kinds, "','") + `')`
	for {
		var n int
		_ = db.QueryRow(q, pid).Scan(&n)
		if n == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

// runAndWait 启动任务并等待；「正在进行中」时等那个任务
func runAndWait(ctx context.Context, pid int64, kind string, start func() (int64, error)) error {
	tid, err := start()
	if err != nil {
		if err.Error() == "正在进行中" {
			return waitIdle(ctx, pid, kind)
		}
		return err
	}
	err = waitTask(ctx, tid)
	if err != nil && ctx.Err() == nil {
		// 失败重试一次
		tid2, err2 := start()
		if err2 != nil {
			return err
		}
		return waitTask(ctx, tid2)
	}
	return err
}

func pipeBody(ctx context.Context, pp *Pipe) error {
	eid := pp.EpisodeID
	reload := func() (*Project, *Episode, error) { return epCtx(eid) }
	p, e, err := reload()
	if err != nil {
		return err
	}
	// 1. 梗概
	if strings.TrimSpace(e.Idea) == "" && (e.No > 1 || strings.TrimSpace(p.Idea) == "") {
		if strings.TrimSpace(p.Idea) == "" && p.Outline == "" {
			return errors.New("先在作品设定里写下故事创意")
		}
		pp.setStage("idea", "拟定本集梗概")
		if err := runAndWait(ctx, p.ID, "idea", func() (int64, error) { return episodeIdeaTask(p, e) }); err != nil {
			return fmt.Errorf("拟定梗概失败：%w", err)
		}
		p, e, _ = reload()
	}
	// 2. 剧本
	if strings.TrimSpace(e.Script) == "" {
		pp.setStage("script", "写剧本")
		if err := runAndWait(ctx, p.ID, "script", func() (int64, error) { return scriptTask(p, e, "") }); err != nil {
			return fmt.Errorf("写剧本失败：%w", err)
		}
		p, e, _ = reload()
	}
	if err := pp.askConfirm("script", "剧本已写好，确认后继续"); err != nil {
		return err
	}
	// 3. 资产
	if !contains(pp.Confirmed, "extracted") {
		pp.setStage("assets", "从剧本提取资产")
		if err := runAndWait(ctx, p.ID, "extract", func() (int64, error) { return extractTask(p, e) }); err != nil {
			return fmt.Errorf("提取资产失败：%w", err)
		}
		pp.Confirmed = append(pp.Confirmed, "extracted")
		pp.save()
	}
	for round := 0; round < 2; round++ {
		n, err := startMissingAssets(p)
		if n == 0 && err == nil {
			break
		}
		if n == 0 && err != nil {
			return fmt.Errorf("生成资产图失败：%w", err)
		}
		pp.setStage("assets", fmt.Sprintf("生成 %d 张资产设定图", n))
		if err := waitIdle(ctx, p.ID, "image"); err != nil {
			return err
		}
	}
	var noImg []string
	for _, a := range listAssets(p.ID) {
		if a.PickedImageID == 0 {
			noImg = append(noImg, a.Name)
		}
	}
	if len(noImg) > 0 {
		return fmt.Errorf("这些资产没能生成设定图：%s", strings.Join(noImg, "、"))
	}
	if p.AudioMode == "dub" && firstVoiceChannel("mimo") != nil {
		need := p.NarratorVoice.empty()
		for _, a := range listAssets(p.ID) {
			if a.Kind == "character" && a.Voice.empty() {
				need = true
			}
		}
		if need {
			pp.setStage("assets", "为角色设计音色")
			if err := runAndWait(ctx, p.ID, "voices", func() (int64, error) { return designAllTask(p) }); err != nil {
				pp.say("部分音色没设计成功：%s", err.Error())
			}
			p, e, _ = reload()
		}
	}
	if err := pp.askConfirm("assets", "资产设定图已就绪，确认后继续"); err != nil {
		return err
	}
	// 4. 分镜
	if len(listShots(e.ID, false)) == 0 {
		pp.setStage("storyboard", "拆分镜")
		if err := runAndWait(ctx, p.ID, "storyboard", func() (int64, error) { return splitTask(p, e) }); err != nil {
			return fmt.Errorf("拆分镜失败：%w", err)
		}
	}
	if err := pp.askConfirm("storyboard", "分镜已拆好，确认后继续"); err != nil {
		return err
	}
	// 5. 关键帧
	for round := 0; round < 2; round++ {
		n, err := startMissingKeyframes(p, e)
		if n == 0 && err == nil {
			break
		}
		if n == 0 {
			return fmt.Errorf("生成关键帧失败：%w", err)
		}
		pp.setStage("keyframes", fmt.Sprintf("生成 %d 张关键帧", n))
		if err := waitIdle(ctx, p.ID, "image"); err != nil {
			return err
		}
	}
	for _, s := range listShots(e.ID, false) {
		if s.PickedImageID == 0 {
			return fmt.Errorf("镜头 %s 没能生成关键帧", s.Code)
		}
	}
	// 6. 配音
	p, e, _ = reload()
	if p.AudioMode == "dub" {
		pp.setStage("voice", "生成配音")
		err := runAndWait(ctx, p.ID, "lines", func() (int64, error) { return linesTask(p, e, false) })
		if err != nil && err.Error() != "配音都是最新的" && err.Error() != "这一集的分镜里没有台词" {
			return fmt.Errorf("配音失败：%w", err)
		}
	}
	// 7. 视频：只排队；开机由「自动开机」设置决定
	retried := map[int64]int{}
	announced := false
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		shots := listShots(e.ID, true)
		done, pending := 0, 0
		for _, s := range shots {
			if shotVideoFor(s) != nil {
				done++
				continue
			}
			busy := false
			var lastFail *Video
			for _, v := range s.Videos {
				if v.Status == "queued" || v.Status == "running" {
					busy = true
				}
				if v.Status == "failed" {
					lastFail = v
				}
			}
			if busy {
				pending++
				continue
			}
			if lastFail != nil && retried[s.ID] >= 1 && len(s.Videos) > 0 {
				return fmt.Errorf("镜头 %s 视频生成失败：%s", s.Code, lastFail.Error)
			}
			if lastFail != nil {
				retried[s.ID]++
			}
			if s.Chain == "first" {
				if pv := prevShot(s); pv != nil && shotVideoFor(pv) == nil {
					pending++
					continue
				}
			}
			if _, err := queueShot(p, s, queueOpts{}); err != nil {
				return err
			}
			pending++
		}
		if done == len(shots) {
			break
		}
		r := runner.brief()
		cfg := loadColabCfg()
		msg := fmt.Sprintf("视频 %d / %d 段完成", done, len(shots))
		if r.Phase == "off" && !cfg.AutoStart {
			pp.Stage, pp.Message, pp.Status = "videos", fmt.Sprintf("%d 段视频已排队，等待开机渲染", pending), "waiting"
			if !announced {
				pp.say("%s", pp.Message)
				announced = true
			} else {
				pp.save()
			}
		} else {
			if pp.Status != "running" || pp.Message != msg {
				pp.Status = "running"
				pp.Stage, pp.Message = "videos", msg
				pp.save()
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Second):
		}
	}
	pp.setStage("videos", "视频全部完成")
	// 8. 成片
	p, e, _ = reload()
	pp.setStage("final", "合成成片")
	if err := runAndWait(ctx, p.ID, "final", func() (int64, error) { return finalTask(p, e) }); err != nil {
		return fmt.Errorf("合成失败：%w", err)
	}
	// 9. 摘要与封面
	p, e, _ = reload()
	pp.setStage("wrap", "写本集摘要")
	if err := runAndWait(ctx, p.ID, "summary", func() (int64, error) { return summaryTask(p, e) }); err != nil {
		pp.say("摘要没写成：%s", err.Error())
	}
	if e.CoverImageID == 0 {
		pp.setStage("wrap", "生成封面")
		if err := runAndWait(ctx, p.ID, "cover", func() (int64, error) { return coverTask(p, e, "") }); err != nil {
			pp.say("封面没生成：%s", err.Error())
		}
	}
	return nil
}

// ---------- 接口 ----------

func handlePipeStart(w http.ResponseWriter, r *http.Request) {
	_, e, err := epCtx(pathID(r))
	if err != nil {
		httpErr(w, 404, err.Error())
		return
	}
	var in struct {
		Mode string `json:"mode"`
	}
	_ = readJSON(r, &in)
	p, err := startPipe(e.ID, in.Mode)
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	writeJSON(w, p)
}

func handlePipeContinue(w http.ResponseWriter, r *http.Request) {
	lp := latestPipe(pathID(r))
	if lp == nil {
		httpErr(w, 404, "没有进行中的一键成集")
		return
	}
	switch lp.Status {
	case "confirm":
		lp.Confirmed = append(lp.Confirmed, lp.Stage)
		lp.say("已确认")
	case "failed", "canceled":
		lp.say("继续")
	default:
		httpErr(w, 400, "正在进行中")
		return
	}
	lp.Status, lp.Message = "running", ""
	lp.save()
	go runPipe(lp.ID)
	writeJSON(w, lp)
}

func handlePipeCancel(w http.ResponseWriter, r *http.Request) {
	cancelPipe(pathID(r))
	writeJSON(w, map[string]any{"ok": true})
}

func handlePipeGet(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{"pipeline": latestPipe(pathID(r))})
}
