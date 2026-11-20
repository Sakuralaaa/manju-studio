package main

// 配乐库（上传，各集选用）、镜头音效、每集封面。

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

type Bgm struct {
	ID        int64   `json:"id"`
	Name      string  `json:"name"`
	File      string  `json:"-"`
	URL       string  `json:"url"`
	Duration  float64 `json:"duration"`
	Bytes     int64   `json:"bytes"`
	CreatedAt int64   `json:"created_at"`
}

func listBgm(where string, args ...any) []*Bgm {
	rows, err := db.Query(`SELECT id,name,file,duration,bytes,created_at FROM bgm `+where, args...)
	if err != nil {
		return []*Bgm{}
	}
	defer rows.Close()
	out := []*Bgm{}
	for rows.Next() {
		var b Bgm
		if rows.Scan(&b.ID, &b.Name, &b.File, &b.Duration, &b.Bytes, &b.CreatedAt) == nil {
			b.URL = "/files/audio/" + b.File
			out = append(out, &b)
		}
	}
	return out
}

func getBgm(id int64) (*Bgm, error) {
	bs := listBgm(`WHERE id=?`, id)
	if len(bs) == 0 {
		return nil, errors.New("配乐不存在")
	}
	return bs[0], nil
}

func handleBgmList(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, listBgm(`ORDER BY id DESC`))
}

// storeAudioUpload 上传的音频统一转成 mp3（44.1 kHz 立体声）
func storeAudioUpload(r *http.Request, prefix string, maxSec int) (string, float64, string, error) {
	if err := r.ParseMultipartForm(120 << 20); err != nil {
		return "", 0, "", errors.New("上传失败")
	}
	f, fh, err := r.FormFile("file")
	if err != nil {
		return "", 0, "", errors.New("没有收到文件")
	}
	defer f.Close()
	tmp, err := os.MkdirTemp(filepath.Join(dataDir, "tmp"), "up-")
	if err != nil {
		return "", 0, "", err
	}
	defer os.RemoveAll(tmp)
	in := filepath.Join(tmp, "in")
	out, _ := os.Create(in)
	_, _ = out.ReadFrom(f)
	out.Close()
	name := fmt.Sprintf("%s-%d-%s.mp3", prefix, now()/1000, randHex(4))
	dst := filepath.Join(dataDir, "audio", name)
	args := []string{"-i", in, "-vn"}
	if maxSec > 0 {
		args = append(args, "-t", fmt.Sprint(maxSec))
	}
	args = append(args, "-ac", "2", "-ar", "44100", "-b:a", "192k", dst)
	if err := runFF(context.Background(), "音频转换", args...); err != nil {
		return "", 0, "", errors.New("无法识别的音频文件")
	}
	ci, _ := probe(dst)
	base := strings.TrimSuffix(fh.Filename, filepath.Ext(fh.Filename))
	return name, ci.dur, base, nil
}

func handleBgmUpload(w http.ResponseWriter, r *http.Request) {
	name, dur, base, err := storeAudioUpload(r, "bgm", 900)
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	if n := strings.TrimSpace(r.FormValue("name")); n != "" {
		base = n
	}
	fi, _ := os.Stat(filepath.Join(dataDir, "audio", name))
	var size int64
	if fi != nil {
		size = fi.Size()
	}
	res, err := db.Exec(`INSERT INTO bgm(name,file,duration,bytes,created_at) VALUES(?,?,?,?,?)`, orDefault(base, "配乐"), name, dur, size, now())
	if err != nil {
		httpErr(w, 500, err.Error())
		return
	}
	id, _ := res.LastInsertId()
	b, _ := getBgm(id)
	writeJSON(w, b)
}

func handleBgmDelete(w http.ResponseWriter, r *http.Request) {
	b, err := getBgm(pathID(r))
	if err != nil {
		httpErr(w, 404, err.Error())
		return
	}
	_ = os.Remove(filepath.Join(dataDir, "audio", b.File))
	_, _ = db.Exec(`DELETE FROM bgm WHERE id=?`, b.ID)
	_, _ = db.Exec(`UPDATE episodes SET bgm_id=0 WHERE bgm_id=?`, b.ID)
	writeJSON(w, map[string]any{"ok": true})
}

func handleShotSfxUpload(w http.ResponseWriter, r *http.Request) {
	s, err := getShot(pathID(r))
	if err != nil {
		httpErr(w, 404, "镜头不存在")
		return
	}
	name, _, base, err := storeAudioUpload(r, "sfx", 30)
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	if s.SfxFile != "" {
		_ = os.Remove(filepath.Join(dataDir, "audio", s.SfxFile))
	}
	_, _ = db.Exec(`UPDATE shots SET sfx_file=?, sfx_name=? WHERE id=?`, name, base, s.ID)
	touchEpisode(s.EpisodeID)
	writeJSON(w, map[string]any{"ok": true, "url": "/files/audio/" + name})
}

func handleShotSfxDelete(w http.ResponseWriter, r *http.Request) {
	s, err := getShot(pathID(r))
	if err != nil {
		httpErr(w, 404, "镜头不存在")
		return
	}
	if s.SfxFile != "" {
		_ = os.Remove(filepath.Join(dataDir, "audio", s.SfxFile))
	}
	_, _ = db.Exec(`UPDATE shots SET sfx_file='', sfx_name='' WHERE id=?`, s.ID)
	writeJSON(w, map[string]any{"ok": true})
}

// ---------- 封面 ----------

func coverTask(p *Project, e *Episode, title string) (int64, error) {
	ch, model, err := imageChannelFor(p)
	if err != nil {
		return 0, err
	}
	if strings.TrimSpace(title) == "" {
		title = p.Title
	}
	sub := fmt.Sprintf("第%d集", e.No)
	var refs []refFile
	var who []string
	assets := listAssets(p.ID)
	// 主角（全剧出场最多的角色）+ 本集第一个场景作参考；主角优先用定妆三视图
	var lead, scene *Asset
	for _, a := range assets {
		if id, _ := charRefImage(a); a.Kind == "character" && id > 0 && (lead == nil || a.Appear > lead.Appear) {
			lead = a
		}
	}
	for _, s := range listShots(e.ID, false) {
		for _, c := range s.Assets {
			for _, a := range assets {
				if a.Code == c && a.Kind == "scene" && a.PickedImageID > 0 && scene == nil {
					scene = a
				}
			}
		}
	}
	if lead == nil {
		// 还没有任何角色图：至少把出场最多的角色造型写进提示词
		for _, a := range assets {
			if a.Kind == "character" && (lead == nil || a.Appear > lead.Appear) {
				lead = a
			}
		}
	}
	limit := refLimit(ch)
	kinds := map[string]string{"character": "角色", "scene": "场景", "prop": "道具"}
	for _, a := range []*Asset{lead, scene} {
		if a == nil || len(refs) >= limit {
			continue
		}
		id, note := charRefImage(a)
		if id == 0 {
			continue
		}
		if im, err := getImage(id); err == nil {
			if rf, err := readImageRef(im); err == nil {
				refs = append(refs, rf)
				who = append(who, refNote(len(refs), kinds[a.Kind], a.Name, note, true))
			}
		}
	}
	gist := strings.TrimRight(strings.TrimSpace(orDefault(e.Summary, orDefault(e.Idea, p.Idea))), "。.！!？?；;，, ")
	prompt := fmt.Sprintf("竖屏短剧海报封面。画面风格：%s。主视觉：%s。画面上方用醒目的中文艺术字写出标题「%s」，标题下方用较小的字写「%s」，文字清晰、无错别字、不出现其他文字，标题只在画面上方出现一次，画中的信纸、招牌等物件上不写字。电影海报级构图与光影，人物情绪强烈。",
		orDefault(p.Style, "精致国漫"), gist, title, sub)
	if l := lookLine(p, lead); l != "" {
		prompt += l + "。"
	}
	if len(who) > 0 {
		prompt += strings.Join(who, "，") + "；人物的脸、发型、服装与参考图完全一致。"
	}
	params := paramsFor(ch, p.Aspect, 1)
	return startTask(p.ID, e.ID, "cover", "episode", e.ID, func(ctx context.Context) error {
		imgs, sent, err := generateImages(ctx, ch, model, prompt, params, refs)
		ledgerAdd(p.ID, e.ID, "image", len(imgs), 0, 0, 0, "")
		if err != nil {
			return err
		}
		var first int64
		for _, data := range imgs {
			if im, err := storeImageX(p.ID, e.ID, "cover", e.ID, "", data, "gen", sent, ch.ID, model); err == nil && first == 0 {
				first = im.ID
			}
		}
		if first == 0 {
			return errors.New("图片保存失败")
		}
		_, _ = db.Exec(`UPDATE episodes SET cover_image_id=? WHERE id=?`, first, e.ID)
		return nil
	})
}

func handleCoverGenerate(w http.ResponseWriter, r *http.Request) {
	p, e, err := epCtx(pathID(r))
	if err != nil {
		httpErr(w, 404, err.Error())
		return
	}
	var in struct {
		Title string `json:"title"`
	}
	_ = readJSON(r, &in)
	tid, err := coverTask(p, e, in.Title)
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	writeJSON(w, map[string]any{"task_id": tid})
}

func handleCoverList(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, listImages(`WHERE owner_type='cover' AND owner_id=? ORDER BY id`, pathID(r)))
}
