package main

// 花费账本：每集实际用掉的 GPU 分钟 / CU（含开机与空闲的分摊）、生图张数、配音条数、对话次数，与估算对照。

import (
	"context"
	"net/http"
	"sort"
)

func ledgerAdd(pid, eid int64, kind string, count int, minutes, cu, est float64, note string) {
	if count == 0 && minutes == 0 && cu == 0 {
		return
	}
	_, _ = db.Exec(`INSERT INTO ledger(project_id,episode_id,kind,count,minutes,cu,est_cu,note,created_at) VALUES(?,?,?,?,?,?,?,?,?)`, pid, eid, kind, count, minutes, cu, est, note, now())
}

// callChatL 调文本渠道并记账
func callChatL(ctx context.Context, pid, eid int64, ch *Channel, model, system, user string, temperature float64) (string, error) {
	text, err := callChat(ctx, ch, model, system, user, temperature)
	ledgerAdd(pid, eid, "llm", 1, 0, 0, 0, "")
	return text, err
}

type LedgerRow struct {
	ProjectID  int64   `json:"project_id"`
	EpisodeID  int64   `json:"episode_id"`
	Title      string  `json:"title"`
	Episode    string  `json:"episode"`
	Videos     int     `json:"videos"`
	GPUMin     float64 `json:"gpu_min"`
	VideoCU    float64 `json:"video_cu"`
	OverheadCU float64 `json:"overhead_cu"`
	CU         float64 `json:"cu"`
	EstCU      float64 `json:"est_cu"`
	Images     int     `json:"images"`
	TTS        int     `json:"tts"`
	LLM        int     `json:"llm"`
}

// sessionOverhead 每个会话的「开机 + 空闲」CU，按该会话里各集生成的视频数分摊
func sessionOverhead() map[[2]int64]float64 {
	cfg := loadColabCfg()
	out := map[[2]int64]float64{}
	rows, err := db.Query(`SELECT id, accelerator, started_at, ended_at FROM sessions WHERE status='ended' AND ended_at>0`)
	if err != nil {
		return out
	}
	type sess struct {
		id      int64
		acc     string
		st, en  int64
	}
	var ss []sess
	for rows.Next() {
		var s sess
		if rows.Scan(&s.id, &s.acc, &s.st, &s.en) == nil {
			ss = append(ss, s)
		}
	}
	rows.Close()
	for _, s := range ss {
		total := float64(s.en-s.st) / 3_600_000 * cfg.rate(s.acc)
		type ep struct {
			pid, eid int64
			n        int
			cu       float64
		}
		var eps []ep
		r2, err := db.Query(`SELECT v.project_id, v.episode_id, COUNT(*), COALESCE(SUM(l.cu),0) FROM videos v LEFT JOIN ledger l ON l.kind='video' AND l.ref_id=v.id WHERE v.session_id=? AND v.status='done' GROUP BY v.project_id, v.episode_id`, s.id)
		if err != nil {
			continue
		}
		var used float64
		var n int
		for r2.Next() {
			var e ep
			if r2.Scan(&e.pid, &e.eid, &e.n, &e.cu) == nil {
				eps = append(eps, e)
				used += e.cu
				n += e.n
			}
		}
		r2.Close()
		over := total - used
		if over < 0 {
			over = 0
		}
		if n == 0 {
			out[[2]int64{0, 0}] += over
			continue
		}
		for _, e := range eps {
			out[[2]int64{e.pid, e.eid}] += over * float64(e.n) / float64(n)
		}
	}
	return out
}

func ledgerRows(pid int64) []*LedgerRow {
	rows := map[[2]int64]*LedgerRow{}
	get := func(p, e int64) *LedgerRow {
		k := [2]int64{p, e}
		if rows[k] == nil {
			rows[k] = &LedgerRow{ProjectID: p, EpisodeID: e}
		}
		return rows[k]
	}
	q := `SELECT project_id, episode_id, kind, SUM(count), SUM(minutes), SUM(cu), SUM(est_cu) FROM ledger`
	args := []any{}
	if pid > 0 {
		q += ` WHERE project_id=?`
		args = append(args, pid)
	}
	q += ` GROUP BY project_id, episode_id, kind`
	r, err := db.Query(q, args...)
	if err == nil {
		for r.Next() {
			var p, e int64
			var kind string
			var c int
			var m, cu, est float64
			if r.Scan(&p, &e, &kind, &c, &m, &cu, &est) != nil {
				continue
			}
			row := get(p, e)
			switch kind {
			case "video":
				row.Videos += c
				row.GPUMin += m
				row.VideoCU += cu
				row.EstCU += est
			case "image":
				row.Images += c
			case "tts":
				row.TTS += c
			case "llm":
				row.LLM += c
			}
		}
		r.Close()
	}
	for k, v := range sessionOverhead() {
		if pid > 0 && k[0] != pid {
			continue
		}
		get(k[0], k[1]).OverheadCU += v
	}
	out := []*LedgerRow{}
	for _, row := range rows {
		row.CU = row.VideoCU + row.OverheadCU
		if row.ProjectID > 0 {
			_ = db.QueryRow(`SELECT title FROM projects WHERE id=?`, row.ProjectID).Scan(&row.Title)
			if row.Title == "" {
				row.Title = "已删除的作品"
			}
		} else {
			row.Title = "接口与调试"
		}
		if row.EpisodeID > 0 {
			if e, err := getEpisode(row.EpisodeID); err == nil {
				row.Episode = epLabel(e)
			} else {
				row.Episode = "已删除的集"
			}
		} else if row.ProjectID > 0 {
			row.Episode = "整部作品"
		}
		out = append(out, row)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ProjectID != out[j].ProjectID {
			return out[i].ProjectID > out[j].ProjectID
		}
		return out[i].EpisodeID < out[j].EpisodeID
	})
	return out
}

func ledgerSummary(pid int64) map[string]any {
	var t LedgerRow
	for _, r := range ledgerRows(pid) {
		t.Videos += r.Videos
		t.GPUMin += r.GPUMin
		t.CU += r.CU
		t.EstCU += r.EstCU
		t.Images += r.Images
		t.TTS += r.TTS
		t.LLM += r.LLM
		t.OverheadCU += r.OverheadCU
	}
	return map[string]any{"videos": t.Videos, "gpu_min": t.GPUMin, "cu": t.CU, "est_cu": t.EstCU, "overhead_cu": t.OverheadCU, "images": t.Images, "tts": t.TTS, "llm": t.LLM}
}

func handleLedger(w http.ResponseWriter, r *http.Request) {
	pid := int64(0)
	if v := r.URL.Query().Get("project_id"); v != "" {
		pid = atoi64(v)
	}
	cfg := loadColabCfg()
	rows, _ := db.Query(`SELECT id,accelerator,purpose,status,end_reason,jobs_done,started_at,ready_at,ended_at,boot_sec FROM sessions ORDER BY id DESC LIMIT 30`)
	sessions := []map[string]any{}
	if rows != nil {
		for rows.Next() {
			var id, jobs, st, rd, en int64
			var acc, purpose, status, reason string
			var boot float64
			if rows.Scan(&id, &acc, &purpose, &status, &reason, &jobs, &st, &rd, &en, &boot) == nil {
				end := en
				if end == 0 {
					end = now()
				}
				var vmin, vcu float64
				_ = db.QueryRow(`SELECT COALESCE(SUM(l.minutes),0), COALESCE(SUM(l.cu),0) FROM ledger l JOIN videos v ON v.id=l.ref_id WHERE l.kind='video' AND v.session_id=?`, id).Scan(&vmin, &vcu)
				sessions = append(sessions, map[string]any{"id": id, "accelerator": accLabel(acc), "purpose": purpose, "status": status, "end_reason": reason, "jobs_done": jobs,
					"started_at": st, "ended_at": en, "minutes": float64(end-st) / 60000, "cu": float64(end-st) / 3_600_000 * cfg.rate(acc), "boot_min": boot / 60, "video_min": vmin, "video_cu": vcu})
			}
		}
		rows.Close()
	}
	writeJSON(w, map[string]any{"rows": ledgerRows(pid), "total": ledgerSummary(pid), "sessions": sessions})
}

func atoi64(s string) int64 {
	var n int64
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int64(c-'0')
	}
	return n
}
