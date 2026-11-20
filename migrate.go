package main

import (
	"database/sql"
	"strings"
)

// 第二版结构：作品 = 剧集（设定集 + 多集），资产库跨集共享；配音、配乐、账本、一键成集。
const schemaV2 = `
CREATE TABLE IF NOT EXISTS episodes (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id INTEGER NOT NULL,
  no INTEGER NOT NULL DEFAULT 1,
  title TEXT NOT NULL DEFAULT '',
  idea TEXT NOT NULL DEFAULT '',
  script TEXT NOT NULL DEFAULT '',
  summary TEXT NOT NULL DEFAULT '',
  cliffhanger TEXT NOT NULL DEFAULT '',
  final_file TEXT NOT NULL DEFAULT '',
  final_at INTEGER NOT NULL DEFAULT 0,
  cover_image_id INTEGER NOT NULL DEFAULT 0,
  bgm_id INTEGER NOT NULL DEFAULT 0,
  bgm_volume REAL NOT NULL DEFAULT 0.35,
  bgm_fade_in REAL NOT NULL DEFAULT 1.5,
  bgm_fade_out REAL NOT NULL DEFAULT 2.5,
  bgm_duck INTEGER NOT NULL DEFAULT 1,
  subtitles INTEGER NOT NULL DEFAULT 1,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS episodes_project ON episodes(project_id, no);
CREATE TABLE IF NOT EXISTS lines (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id INTEGER NOT NULL,
  episode_id INTEGER NOT NULL,
  shot_id INTEGER NOT NULL,
  sort INTEGER NOT NULL DEFAULT 0,
  speaker TEXT NOT NULL DEFAULT '',
  asset_id INTEGER NOT NULL DEFAULT 0,
  kind TEXT NOT NULL DEFAULT 'dialogue',
  text TEXT NOT NULL DEFAULT '',
  emotion TEXT NOT NULL DEFAULT '',
  audio_file TEXT NOT NULL DEFAULT '',
  duration REAL NOT NULL DEFAULT 0,
  status TEXT NOT NULL DEFAULT '',
  error TEXT NOT NULL DEFAULT '',
  sig TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS lines_shot ON lines(shot_id, sort);
CREATE INDEX IF NOT EXISTS lines_episode ON lines(episode_id);
CREATE TABLE IF NOT EXISTS bgm (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL,
  file TEXT NOT NULL,
  duration REAL NOT NULL DEFAULT 0,
  bytes INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS ledger (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id INTEGER NOT NULL DEFAULT 0,
  episode_id INTEGER NOT NULL DEFAULT 0,
  kind TEXT NOT NULL,
  count INTEGER NOT NULL DEFAULT 0,
  minutes REAL NOT NULL DEFAULT 0,
  cu REAL NOT NULL DEFAULT 0,
  est_cu REAL NOT NULL DEFAULT 0,
  ref_id INTEGER NOT NULL DEFAULT 0,
  session_id INTEGER NOT NULL DEFAULT 0,
  note TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS ledger_ep ON ledger(project_id, episode_id);
CREATE TABLE IF NOT EXISTS pipelines (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  episode_id INTEGER NOT NULL,
  mode TEXT NOT NULL DEFAULT 'auto',
  status TEXT NOT NULL DEFAULT 'running',
  stage TEXT NOT NULL DEFAULT '',
  message TEXT NOT NULL DEFAULT '',
  confirmed TEXT NOT NULL DEFAULT '[]',
  log TEXT NOT NULL DEFAULT '[]',
  started_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS pipelines_ep ON pipelines(episode_id);
`

var addCols = []struct{ table, col, def string }{
	{"projects", "world", "TEXT NOT NULL DEFAULT ''"},
	{"projects", "bios", "TEXT NOT NULL DEFAULT ''"},
	{"projects", "outline", "TEXT NOT NULL DEFAULT ''"},
	{"projects", "episodes_plan", "INTEGER NOT NULL DEFAULT 10"},
	{"projects", "audio_mode", "TEXT NOT NULL DEFAULT 'native'"},
	{"projects", "orig_volume", "REAL NOT NULL DEFAULT 0.3"},
	{"projects", "takes", "INTEGER NOT NULL DEFAULT 1"},
	{"projects", "narrator_voice", "TEXT NOT NULL DEFAULT '{}'"},
	{"projects", "voice_channel_id", "INTEGER NOT NULL DEFAULT 0"},
	{"projects", "deliver", "TEXT NOT NULL DEFAULT 'source'"},
	{"projects", "post_upscale", "INTEGER NOT NULL DEFAULT 0"},
	{"projects", "post_interp", "INTEGER NOT NULL DEFAULT 0"},
	{"projects", "kind", "TEXT NOT NULL DEFAULT 'manju'"},
	{"assets", "sheet_image_id", "INTEGER NOT NULL DEFAULT 0"},
	{"assets", "expr_image_id", "INTEGER NOT NULL DEFAULT 0"},
	{"assets", "voice", "TEXT NOT NULL DEFAULT '{}'"},
	{"assets", "base_id", "INTEGER NOT NULL DEFAULT 0"},
	{"shots", "ref_ids", "TEXT NOT NULL DEFAULT '[]'"},
	{"images", "tag", "TEXT NOT NULL DEFAULT ''"},
	{"images", "episode_id", "INTEGER NOT NULL DEFAULT 0"},
	{"shots", "episode_id", "INTEGER NOT NULL DEFAULT 0"},
	{"shots", "chain", "TEXT NOT NULL DEFAULT 'off'"},
	{"shots", "sfx_file", "TEXT NOT NULL DEFAULT ''"},
	{"shots", "sfx_name", "TEXT NOT NULL DEFAULT ''"},
	{"shots", "sfx_volume", "REAL NOT NULL DEFAULT 0.8"},
	{"videos", "episode_id", "INTEGER NOT NULL DEFAULT 0"},
	{"videos", "last_image_file", "TEXT NOT NULL DEFAULT ''"},
	{"videos", "post", "TEXT NOT NULL DEFAULT '{}'"},
	{"videos", "est_cu", "REAL NOT NULL DEFAULT 0"},
	{"tasks", "episode_id", "INTEGER NOT NULL DEFAULT 0"},
	{"channels", "voices", "TEXT NOT NULL DEFAULT '[]'"},
	{"sessions", "boot_sec", "REAL NOT NULL DEFAULT 0"},
}

func hasCol(table, col string) bool {
	rows, err := db.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		return false
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, typ string
		var nn, pk int
		var dflt sql.NullString
		if rows.Scan(&cid, &name, &typ, &nn, &dflt, &pk) == nil && strings.EqualFold(name, col) {
			return true
		}
	}
	return false
}

func migrate() error {
	if _, err := db.Exec(schemaV2); err != nil {
		return err
	}
	for _, c := range addCols {
		if !hasCol(c.table, c.col) {
			if _, err := db.Exec(`ALTER TABLE ` + c.table + ` ADD COLUMN ` + c.col + ` ` + c.def); err != nil {
				return err
			}
		}
	}
	_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS shots_episode ON shots(episode_id, sort)`)
	// 旧的单集作品：建第 1 集，把剧本、成片、分镜、视频、任务挂上去
	rows, err := db.Query(`SELECT id, idea, script, final_file, final_at FROM projects WHERE id NOT IN (SELECT project_id FROM episodes)`)
	if err != nil {
		return err
	}
	type old struct {
		id           int64
		idea, script string
		final        string
		finalAt      int64
	}
	var olds []old
	for rows.Next() {
		var o old
		if rows.Scan(&o.id, &o.idea, &o.script, &o.final, &o.finalAt) == nil {
			olds = append(olds, o)
		}
	}
	rows.Close()
	for _, o := range olds {
		res, err := db.Exec(`INSERT INTO episodes(project_id,no,title,idea,script,final_file,final_at,cliffhanger,created_at,updated_at) VALUES(?,1,'',?,?,?,?,?,?,?)`,
			o.id, o.idea, o.script, o.final, o.finalAt, cliffOf(o.script), now(), now())
		if err != nil {
			return err
		}
		eid, _ := res.LastInsertId()
		for _, q := range []string{`UPDATE shots SET episode_id=? WHERE project_id=? AND episode_id=0`, `UPDATE videos SET episode_id=? WHERE project_id=? AND episode_id=0`, `UPDATE tasks SET episode_id=? WHERE project_id=? AND episode_id=0`} {
			_, _ = db.Exec(q, eid, o.id)
		}
	}
	migrateSizeB64()
	return nil
}

// migrateSizeB64 一次性：已建好的「尺寸型」生图渠道（尺寸原生、比例不用、逐张请求、要链接）
// 改为直接返回图片数据——该类中转的图片链接下载会 401。上游不认时出图会自动退回链接。
func migrateSizeB64() {
	if getSetting("mig_size_b64", "") != "" {
		return
	}
	cs, err := listChannels()
	if err != nil {
		return
	}
	for _, c := range cs {
		r := c.Caps.Response
		if c.Kind != "image" || c.APIType != "images" || c.Caps.Size.Mode != "native" || c.Caps.Aspect.Mode != "off" ||
			c.Caps.N.Mode != "loop" || r.Mode != "native" || len(r.Values) != 1 || r.Values[0] != "url" {
			continue
		}
		c.Caps.Response.Values = []string{"b64_json"}
		_, _ = db.Exec(`UPDATE channels SET caps=? WHERE id=?`, mustJSON(c.Caps), c.ID)
	}
	_ = setSetting("mig_size_b64", "1")
}
