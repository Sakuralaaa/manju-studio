package main

// 画室：不挂在作品下的自由出图。智能体用 image_generate 生成的图也放在这里，界面可以看、下载、
// 当参考图、存进任意作品的资产。

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

var studioSeq atomic.Int64

func init() { studioSeq.Store(time.Now().UnixNano() / 1e6) }

const studioWhere = `owner_type IN ('studio','ext')`

type studioIn struct {
	Prompt      string   `json:"prompt"`
	Aspect      string   `json:"aspect"`
	N           int      `json:"n"`
	ChannelID   int64    `json:"channel_id"`
	Model       string   `json:"model"`
	RefImageIDs []int64  `json:"ref_image_ids"`
	RefImages   []string `json:"ref_images"` // base64 或 data URL（智能体直接带图）
	Realistic   bool     `json:"realistic"`
	Owner       string   `json:"-"`
}

// studioPrepare 选渠道、读参考图、拼提示词
func studioPrepare(in studioIn) (*Channel, string, string, Params, []refFile, error) {
	if strings.TrimSpace(in.Prompt) == "" {
		return nil, "", "", Params{}, nil, errors.New("提示词不能为空")
	}
	ch := firstChannel("image")
	if ch == nil {
		return nil, "", "", Params{}, nil, errors.New("还没有生图渠道，请先在「渠道」里添加")
	}
	model := ""
	ch, model, err := applyOverride(ch, model, imgOverride{ChannelID: in.ChannelID, Model: in.Model})
	if err != nil {
		return nil, "", "", Params{}, nil, err
	}
	var refs []refFile
	for _, id := range in.RefImageIDs {
		im, err := getImage(id)
		if err != nil {
			return nil, "", "", Params{}, nil, fmt.Errorf("参考图 %d 不存在", id)
		}
		rf, err := readImageRef(im)
		if err != nil {
			return nil, "", "", Params{}, nil, err
		}
		refs = append(refs, rf)
	}
	for i, s := range in.RefImages {
		s = strings.TrimSpace(s)
		if j := strings.Index(s, ","); strings.HasPrefix(s, "data:") && j > 0 {
			s = s[j+1:]
		}
		data, err := base64.StdEncoding.DecodeString(s)
		if err != nil {
			return nil, "", "", Params{}, nil, fmt.Errorf("第 %d 张参考图不是有效的 base64", i+1)
		}
		ext, mime := sniffFormat(data)
		if ext == "" {
			return nil, "", "", Params{}, nil, fmt.Errorf("第 %d 张参考图格式无法识别", i+1)
		}
		refs = append(refs, refFile{Name: fmt.Sprintf("ref%d.%s", i+1, ext), Mime: mime, Data: data})
	}
	if lim := refLimit(ch); len(refs) > lim {
		if lim == 0 {
			return nil, "", "", Params{}, nil, fmt.Errorf("渠道「%s」不支持参考图，换一个渠道或去掉参考图", ch.Name)
		}
		return nil, "", "", Params{}, nil, fmt.Errorf("渠道「%s」最多 %d 张参考图", ch.Name, lim)
	}
	prompt := strings.TrimSpace(in.Prompt)
	if in.Realistic && !strings.Contains(prompt, realTail) {
		prompt += "。" + realTail
	}
	aspect := in.Aspect
	if aspect == "" {
		aspect = "1:1"
	}
	return ch, model, prompt, paramsFor(ch, aspect, clampInt(in.N, 1, 4)), refs, nil
}

// studioRun 同步出图并入库（owner：studio = 界面，ext = 智能体）
func studioRun(ctx context.Context, in studioIn) ([]*Image, error) {
	ch, model, prompt, params, refs, err := studioPrepare(in)
	if err != nil {
		return nil, err
	}
	imgs, sent, err := generateImages(ctx, ch, model, prompt, params, refs)
	ledgerAdd(0, 0, "image", len(imgs), 0, 0, 0, "")
	if err != nil {
		return nil, err
	}
	owner := in.Owner
	if owner == "" {
		owner = "studio"
	}
	var out []*Image
	for _, data := range imgs {
		if im, err := storeImage(0, owner, 0, data, "gen", sent, ch.ID, model); err == nil {
			out = append(out, im)
		}
	}
	if len(out) == 0 {
		return nil, errors.New("图片保存失败")
	}
	return out, nil
}

func handleStudioList(w http.ResponseWriter, r *http.Request) {
	before, _ := strconv.ParseInt(r.URL.Query().Get("before"), 10, 64)
	where := `WHERE ` + studioWhere
	args := []any{}
	if before > 0 {
		where += ` AND id<?`
		args = append(args, before)
	}
	ims := listImages(where+` ORDER BY id DESC LIMIT 60`, args...)
	var tasks []*Task
	rows, err := db.Query(`SELECT id,project_id,episode_id,kind,owner_type,owner_id,status,error,created_at,finished_at FROM tasks WHERE owner_type='studio' AND (status='running' OR (status='failed' AND finished_at>?)) ORDER BY id`, now()-90_000)
	if err == nil {
		for rows.Next() {
			var t Task
			if rows.Scan(&t.ID, &t.ProjectID, &t.EpisodeID, &t.Kind, &t.OwnerType, &t.OwnerID, &t.Status, &t.Error, &t.CreatedAt, &t.FinishedAt) == nil {
				tasks = append(tasks, &t)
			}
		}
		rows.Close()
	}
	if tasks == nil {
		tasks = []*Task{}
	}
	writeJSON(w, map[string]any{"images": ims, "tasks": tasks, "more": len(ims) == 60})
}

func handleStudioGenerate(w http.ResponseWriter, r *http.Request) {
	var in studioIn
	if err := readJSON(r, &in); err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	// 先校验，出错直接返回，不建任务
	if _, _, _, _, _, err := studioPrepare(in); err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	in.Owner = "studio"
	tid, err := startTask(0, 0, "studio", "studio", studioSeq.Add(1), func(ctx context.Context) error {
		_, err := studioRun(ctx, in)
		return err
	})
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	writeJSON(w, map[string]any{"task_id": tid})
}

func handleStudioUpload(w http.ResponseWriter, r *http.Request) {
	data, err := readUpload(r)
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	im, err := storeImage(0, "studio", 0, data, "upload", "", 0, "")
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	writeJSON(w, im)
}

// copyImageTo 把任意一张图复制进资产（设定图 / 三视图 / 表情表 / 参考图）。复制而不是移动：画室和原资产里的图都保留。
func copyImageTo(imgID, assetID int64, tag string) (*Image, *Asset, error) {
	im, err := getImage(imgID)
	if err != nil {
		return nil, nil, errors.New("图片不存在")
	}
	a, err := getAsset(assetID)
	if err != nil {
		return nil, nil, errors.New("资产不存在")
	}
	if tag != "" && tag != "sheet" && tag != "expr" && tag != "ref" {
		return nil, nil, errors.New("tag 只能是 空（设定图）/ sheet / expr / ref")
	}
	if (tag == "sheet" || tag == "expr") && a.Kind != "character" {
		return nil, nil, errors.New("只有角色有三视图和表情表")
	}
	rf, err := readImageRef(im)
	if err != nil {
		return nil, nil, err
	}
	nim, err := storeImageX(a.ProjectID, 0, "asset", a.ID, tag, rf.Data, im.Source, im.Prompt, im.ChannelID, im.Model)
	if err != nil {
		return nil, nil, err
	}
	if col := tagCol[tag]; col != "" {
		_, _ = db.Exec(`UPDATE assets SET `+col+`=? WHERE id=?`, nim.ID, a.ID)
	}
	touchProject(a.ProjectID)
	return nim, a, nil
}

func handleImageCopy(w http.ResponseWriter, r *http.Request) {
	var in struct {
		AssetID int64  `json:"asset_id"`
		Tag     string `json:"tag"`
	}
	if err := readJSON(r, &in); err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	im, _, err := copyImageTo(pathID(r), in.AssetID, in.Tag)
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	writeJSON(w, im)
}

// handleRefLibrary 全部作品里有图的资产（给「选参考图」用）
func handleRefLibrary(w http.ResponseWriter, r *http.Request) {
	type item struct {
		ProjectID int64    `json:"project_id"`
		Project   string   `json:"project"`
		Assets    []*Asset `json:"assets"`
	}
	out := []item{}
	rows, err := db.Query(`SELECT id,title FROM projects ORDER BY updated_at DESC`)
	if err != nil {
		writeJSON(w, out)
		return
	}
	type pr struct {
		id    int64
		title string
	}
	var ps []pr
	for rows.Next() {
		var x pr
		if rows.Scan(&x.id, &x.title) == nil {
			ps = append(ps, x)
		}
	}
	rows.Close()
	for _, p := range ps {
		var as []*Asset
		for _, a := range listAssets(p.id) {
			if len(a.Images) > 0 {
				as = append(as, a)
			}
		}
		if len(as) > 0 {
			out = append(out, item{p.id, p.title, as})
		}
	}
	writeJSON(w, out)
}
