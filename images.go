package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "golang.org/x/image/webp"
)

type Image struct {
	ID        int64  `json:"id"`
	ProjectID int64  `json:"project_id"`
	OwnerType string `json:"owner_type"`
	OwnerID   int64  `json:"owner_id"`
	File      string `json:"-"`
	URL       string `json:"url"`
	Mime      string `json:"mime"`
	Width     int    `json:"width"`
	Height    int    `json:"height"`
	Bytes     int64  `json:"bytes"`
	Source    string `json:"source"`
	Prompt    string `json:"prompt"`
	ChannelID int64  `json:"channel_id"`
	Model     string `json:"model"`
	CreatedAt int64  `json:"created_at"`
	Tag       string `json:"tag"`
	EpisodeID int64  `json:"episode_id"`
}

func sniffFormat(data []byte) (ext, mime string) {
	switch {
	case len(data) > 12 && string(data[0:4]) == "RIFF" && string(data[8:12]) == "WEBP":
		return "webp", "image/webp"
	case len(data) > 8 && bytes.HasPrefix(data, []byte{0x89, 'P', 'N', 'G'}):
		return "png", "image/png"
	case len(data) > 3 && data[0] == 0xFF && data[1] == 0xD8:
		return "jpg", "image/jpeg"
	case len(data) > 6 && (string(data[0:6]) == "GIF89a" || string(data[0:6]) == "GIF87a"):
		return "gif", "image/gif"
	}
	return "", ""
}

const imgCols = `id,project_id,owner_type,owner_id,file,mime,width,height,bytes,source,prompt,channel_id,model,created_at,tag,episode_id`

func scanImage(sc interface{ Scan(...any) error }) (*Image, error) {
	var im Image
	if err := sc.Scan(&im.ID, &im.ProjectID, &im.OwnerType, &im.OwnerID, &im.File, &im.Mime, &im.Width, &im.Height, &im.Bytes, &im.Source, &im.Prompt, &im.ChannelID, &im.Model, &im.CreatedAt, &im.Tag, &im.EpisodeID); err != nil {
		return nil, err
	}
	im.URL = "/files/images/" + im.File
	return &im, nil
}

func getImage(id int64) (*Image, error) {
	return scanImage(db.QueryRow(`SELECT `+imgCols+` FROM images WHERE id=?`, id))
}

func listImages(where string, args ...any) []*Image {
	rows, err := db.Query(`SELECT `+imgCols+` FROM images `+where, args...)
	if err != nil {
		return []*Image{}
	}
	defer rows.Close()
	out := []*Image{}
	for rows.Next() {
		if im, err := scanImage(rows); err == nil {
			out = append(out, im)
		}
	}
	return out
}

// storeImage 落盘并入库
func storeImage(projectID int64, ownerType string, ownerID int64, data []byte, source, prompt string, chID int64, model string) (*Image, error) {
	return storeImageX(projectID, 0, ownerType, ownerID, "", data, source, prompt, chID, model)
}

func storeImageX(projectID, episodeID int64, ownerType string, ownerID int64, tag string, data []byte, source, prompt string, chID int64, model string) (*Image, error) {
	ext, mime := sniffFormat(data)
	if ext == "" {
		return nil, errors.New("无法识别的图片格式")
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("图片解码失败：%w", err)
	}
	name := fmt.Sprintf("%d-%s.%s", time.Now().Unix(), randHex(6), ext)
	if err := os.WriteFile(filepath.Join(dataDir, "images", name), data, 0o644); err != nil {
		return nil, err
	}
	res, err := db.Exec(`INSERT INTO images(project_id,owner_type,owner_id,file,mime,width,height,bytes,source,prompt,channel_id,model,created_at,tag,episode_id) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		projectID, ownerType, ownerID, name, mime, cfg.Width, cfg.Height, len(data), source, prompt, chID, model, now(), tag, episodeID)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return getImage(id)
}

func readImageRef(im *Image) (refFile, error) {
	data, err := os.ReadFile(filepath.Join(dataDir, "images", im.File))
	if err != nil {
		return refFile{}, errors.New("参考图读取失败")
	}
	return refFile{Name: im.File, Mime: im.Mime, Data: data}, nil
}

func deleteImageRow(im *Image) {
	_, _ = db.Exec(`DELETE FROM images WHERE id=?`, im.ID)
	_ = os.Remove(filepath.Join(dataDir, "images", im.File))
}

// ---------- 调用生图渠道 ----------

var genSem = make(chan struct{}, 4)

// generateImages 同步调用一次生图渠道，返回图片数据和实际发送的提示词
func generateImages(ctx context.Context, ch *Channel, model, prompt string, p Params, refs []refFile) ([][]byte, string, error) {
	genSem <- struct{}{}
	defer func() { <-genSem }()
	if model == "" {
		if len(ch.Models) == 0 {
			return nil, "", errors.New("渠道还没有选择模型")
		}
		model = ch.Models[0]
	}
	if max := ch.Caps.Refs.Max; ch.Caps.Refs.Mode != "off" && max > 0 && len(refs) > max {
		refs = refs[:max]
	}
	if ch.Caps.Refs.Mode == "off" {
		refs = nil
	}
	plan := buildPlan(ch, JobIn{ChannelID: ch.ID, Model: model, Prompt: prompt, Params: p}, len(refs) > 0)
	var all [][]byte
	var lastErr error
	for i := 0; i < plan.Calls; i++ {
		res, err := doCall(ctx, ch, plan, refs)
		for k, wait := range []time.Duration{3 * time.Second, 10 * time.Second} {
			if err == nil || !isRetryable(err) || ctx.Err() != nil {
				break
			}
			log.Printf("渠道 %d 第 %d 次重试：%v", ch.ID, k+1, err)
			time.Sleep(wait)
			res, err = doCall(ctx, ch, plan, refs)
		}
		if err != nil && ctx.Err() == nil && plan.Edit {
			// 参考图的发送方式设错了：上游明确要 multipart / JSON 时换一种再试，并记到渠道上
			msg := err.Error()
			alt := ""
			if plan.RefMode == "json" && strings.Contains(msg, "multipart/form-data") {
				alt = "multipart"
			} else if plan.RefMode == "multipart" && (strings.Contains(msg, "application/json") || strings.Contains(msg, "expected JSON")) {
				alt = "json"
			}
			if alt != "" {
				log.Printf("渠道 %d 参考图改用 %s 发送", ch.ID, alt)
				plan.RefMode = alt
				setChannelRefMode(ch.ID, alt)
				res, err = doCall(ctx, ch, plan, refs)
			}
		}
		if err != nil && ctx.Err() == nil && isResponseFormatRejected(plan, err) {
			// 上游不认「直接返回图片数据」：改要链接再下载（同主机会带渠道密钥重试）
			log.Printf("渠道 %d 不支持 b64_json，改用链接", ch.ID)
			plan = planWithURL(plan)
			res, err = doCall(ctx, ch, plan, refs)
		}
		if err != nil {
			lastErr = err
			if i == 0 && plan.Calls > 1 {
				break
			}
			continue
		}
		all = append(all, res.images...)
	}
	if len(all) == 0 {
		if lastErr == nil {
			lastErr = errors.New("没有返回图片")
		}
		return nil, plan.Prompt, lastErr
	}
	return all, plan.Prompt, nil
}

// paramsFor 把作品画幅映射到渠道能力：比例原生/写进提示词，或挑最接近的尺寸
func paramsFor(ch *Channel, aspect string, n int) Params {
	p := Params{N: n}
	c := ch.Caps
	if c.Aspect.Mode != "off" {
		p.Aspect = aspect
		if len(c.Aspect.Values) > 0 && !contains(c.Aspect.Values, aspect) && c.Aspect.Mode == "native" {
			p.Aspect = closestRatio(c.Aspect.Values, aspect)
		}
	} else if c.Size.Mode != "off" {
		vals := c.Size.Values
		if len(vals) == 0 {
			vals = []string{"1024x1024", "1024x1536", "1536x1024"}
		}
		p.Size = closestSize(vals, aspect)
	}
	if c.Quality.Mode != "off" && c.Quality.Default != "" {
		p.Quality = c.Quality.Default
	}
	if c.Resolution.Mode != "off" && c.Resolution.Default != "" {
		p.Resolution = c.Resolution.Default
	}
	if c.Format.Mode == "native" {
		p.Format = "png"
		if c.Format.Default != "" {
			p.Format = c.Format.Default
		}
	}
	return p
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

func ratioValue(r string) float64 {
	var a, b float64
	if _, err := fmt.Sscanf(strings.Replace(r, "x", ":", 1), "%g:%g", &a, &b); err != nil || b == 0 {
		return 0
	}
	return a / b
}

func closestRatio(vals []string, want string) string {
	w := ratioValue(want)
	best, bd := want, 1e9
	for _, v := range vals {
		x := ratioValue(v)
		if x == 0 {
			continue
		}
		d := x/w + w/x
		if d < bd {
			best, bd = v, d
		}
	}
	return best
}

func closestSize(vals []string, aspect string) string {
	w := ratioValue(aspect)
	best, bd := "", 1e9
	for _, v := range vals {
		if v == "auto" {
			continue
		}
		x := ratioValue(v)
		if x == 0 {
			continue
		}
		d := x/w + w/x
		if d < bd {
			best, bd = v, d
		}
	}
	if best == "" && len(vals) > 0 {
		return vals[0]
	}
	return best
}

// ---------- 上传 ----------

func readUpload(r *http.Request) ([]byte, error) {
	if err := r.ParseMultipartForm(40 << 20); err != nil {
		return nil, errors.New("上传失败")
	}
	f, _, err := r.FormFile("file")
	if err != nil {
		return nil, errors.New("没有收到文件")
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 40<<20))
	if err != nil {
		return nil, err
	}
	if ext, _ := sniffFormat(data); ext == "" {
		return nil, errors.New("只支持 PNG / JPEG / WEBP / GIF")
	}
	return data, nil
}

func handleImagePick(w http.ResponseWriter, r *http.Request) {
	im, err := getImage(pathID(r))
	if err != nil {
		httpErr(w, 404, "图片不存在")
		return
	}
	if err := pickImage(im); err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

// pickImage 把图设为它所在位置（资产的设定图 / 三视图 / 表情表、镜头关键帧、封面）的选用图
func pickImage(im *Image) error {
	switch im.OwnerType {
	case "asset":
		col := map[string]string{"": "picked_image_id", "sheet": "sheet_image_id", "expr": "expr_image_id"}[im.Tag]
		if col == "" {
			return errors.New("参考照不用选用，它会自动用于出图")
		}
		_, _ = db.Exec(`UPDATE assets SET `+col+`=? WHERE id=?`, im.ID, im.OwnerID)
	case "shot":
		_, _ = db.Exec(`UPDATE shots SET picked_image_id=? WHERE id=?`, im.ID, im.OwnerID)
	case "cover":
		_, _ = db.Exec(`UPDATE episodes SET cover_image_id=? WHERE id=?`, im.ID, im.OwnerID)
	default:
		return errors.New("画室里的图不能直接选用：先用 attach_image 存进资产")
	}
	touchProject(im.ProjectID)
	return nil
}

func handleImageDelete(w http.ResponseWriter, r *http.Request) {
	im, err := getImage(pathID(r))
	if err != nil {
		httpErr(w, 404, "图片不存在")
		return
	}
	_, _ = db.Exec(`UPDATE assets SET picked_image_id=0 WHERE picked_image_id=?`, im.ID)
	_, _ = db.Exec(`UPDATE assets SET sheet_image_id=0 WHERE sheet_image_id=?`, im.ID)
	_, _ = db.Exec(`UPDATE assets SET expr_image_id=0 WHERE expr_image_id=?`, im.ID)
	_, _ = db.Exec(`UPDATE episodes SET cover_image_id=0 WHERE cover_image_id=?`, im.ID)
	_, _ = db.Exec(`UPDATE shots SET picked_image_id=0 WHERE picked_image_id=?`, im.ID)
	deleteImageRow(im)
	writeJSON(w, map[string]any{"ok": true})
}

// ---------- 后台任务（生成剧本、提取资产、生图……） ----------

type Task struct {
	ID         int64  `json:"id"`
	ProjectID  int64  `json:"project_id"`
	EpisodeID  int64  `json:"episode_id"`
	Kind       string `json:"kind"`
	OwnerType  string `json:"owner_type"`
	OwnerID    int64  `json:"owner_id"`
	Status     string `json:"status"`
	Error      string `json:"error"`
	Result     string `json:"result,omitempty"`
	CreatedAt  int64  `json:"created_at"`
	FinishedAt int64  `json:"finished_at"`
}

func setTaskResult(tid int64, v any) {
	if tid <= 0 {
		return
	}
	_, _ = db.Exec(`UPDATE tasks SET result=? WHERE id=?`, mustJSON(v), tid)
}

func getTask(id int64) (*Task, error) {
	var t Task
	err := db.QueryRow(`SELECT id,project_id,episode_id,kind,owner_type,owner_id,status,error,result,created_at,finished_at FROM tasks WHERE id=?`, id).
		Scan(&t.ID, &t.ProjectID, &t.EpisodeID, &t.Kind, &t.OwnerType, &t.OwnerID, &t.Status, &t.Error, &t.Result, &t.CreatedAt, &t.FinishedAt)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func startTask(projectID, episodeID int64, kind, ownerType string, ownerID int64, fn func(ctx context.Context) error) (int64, error) {
	return startTaskR(projectID, episodeID, kind, ownerType, ownerID, func(ctx context.Context, _ int64) error {
		return fn(ctx)
	})
}

func startTaskR(projectID, episodeID int64, kind, ownerType string, ownerID int64, fn func(ctx context.Context, tid int64) error) (int64, error) {
	var busy int
	_ = db.QueryRow(`SELECT COUNT(*) FROM tasks WHERE status='running' AND kind=? AND owner_type=? AND owner_id=?`, kind, ownerType, ownerID).Scan(&busy)
	if busy > 0 {
		return 0, errors.New("正在进行中")
	}
	res, err := db.Exec(`INSERT INTO tasks(project_id,episode_id,kind,owner_type,owner_id,status,created_at) VALUES(?,?,?,?,?, 'running', ?)`, projectID, episodeID, kind, ownerType, ownerID, now())
	if err != nil {
		return 0, err
	}
	id, _ := res.LastInsertId()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		var err error
		func() {
			defer func() {
				if rec := recover(); rec != nil {
					log.Printf("任务 %d 崩溃: %v", id, rec)
					err = fmt.Errorf("内部错误：%v", rec)
				}
			}()
			err = fn(ctx, id)
		}()
		if err != nil {
			_, _ = db.Exec(`UPDATE tasks SET status='failed', error=?, finished_at=? WHERE id=?`, err.Error(), now(), id)
		} else {
			_, _ = db.Exec(`UPDATE tasks SET status='done', finished_at=? WHERE id=?`, now(), id)
		}
		touchProject(projectID)
		touchEpisode(episodeID)
	}()
	return id, nil
}

func listTasks(projectID int64) []*Task {
	// 进行中的 + 最近一分钟内失败的（给前端提示）
	rows, err := db.Query(`SELECT id,project_id,episode_id,kind,owner_type,owner_id,status,error,created_at,finished_at FROM tasks WHERE project_id=? AND (status='running' OR (status='failed' AND finished_at>?)) ORDER BY id`, projectID, now()-90_000)
	if err != nil {
		return []*Task{}
	}
	defer rows.Close()
	out := []*Task{}
	for rows.Next() {
		var t Task
		if rows.Scan(&t.ID, &t.ProjectID, &t.EpisodeID, &t.Kind, &t.OwnerType, &t.OwnerID, &t.Status, &t.Error, &t.CreatedAt, &t.FinishedAt) == nil {
			out = append(out, &t)
		}
	}
	return out
}

func handleTaskGet(w http.ResponseWriter, r *http.Request) {
	t, err := getTask(pathID(r))
	if err != nil {
		httpErr(w, 404, "任务不存在")
		return
	}
	writeJSON(w, t)
}

// setChannelRefMode 记住渠道实际支持的参考图发送方式，下次直接用
func setChannelRefMode(id int64, mode string) {
	ch, err := getChannel(id)
	if err != nil {
		return
	}
	ch.Caps.Refs.Mode = mode
	b, _ := json.Marshal(ch.Caps)
	_, _ = db.Exec(`UPDATE channels SET caps=? WHERE id=?`, string(b), id)
}
