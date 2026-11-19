package main

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

var db *sql.DB

const schema = `
CREATE TABLE IF NOT EXISTS settings (key TEXT PRIMARY KEY, value TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS channels (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  kind TEXT NOT NULL DEFAULT 'image',
  name TEXT NOT NULL,
  base_url TEXT NOT NULL,
  api_key TEXT NOT NULL DEFAULT '',
  api_type TEXT NOT NULL DEFAULT 'images',
  models TEXT NOT NULL DEFAULT '[]',
  caps TEXT NOT NULL DEFAULT '{}',
  sort INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS projects (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  title TEXT NOT NULL,
  idea TEXT NOT NULL DEFAULT '',
  style TEXT NOT NULL DEFAULT '',
  aspect TEXT NOT NULL DEFAULT '9:16',
  episode_sec INTEGER NOT NULL DEFAULT 60,
  shot_target INTEGER NOT NULL DEFAULT 12,
  script TEXT NOT NULL DEFAULT '',
  text_channel_id INTEGER NOT NULL DEFAULT 0,
  text_model TEXT NOT NULL DEFAULT '',
  image_channel_id INTEGER NOT NULL DEFAULT 0,
  image_model TEXT NOT NULL DEFAULT '',
  video_sec INTEGER NOT NULL DEFAULT 5,
  video_res TEXT NOT NULL DEFAULT '768p',
  video_turbo INTEGER NOT NULL DEFAULT 0,
  final_file TEXT NOT NULL DEFAULT '',
  final_at INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS assets (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id INTEGER NOT NULL,
  kind TEXT NOT NULL,
  code TEXT NOT NULL,
  name TEXT NOT NULL,
  description TEXT NOT NULL DEFAULT '',
  prompt TEXT NOT NULL DEFAULT '',
  picked_image_id INTEGER NOT NULL DEFAULT 0,
  sort INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS assets_project ON assets(project_id);
CREATE TABLE IF NOT EXISTS shots (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id INTEGER NOT NULL,
  sort INTEGER NOT NULL DEFAULT 0,
  duration REAL NOT NULL DEFAULT 5,
  camera TEXT NOT NULL DEFAULT '',
  action TEXT NOT NULL DEFAULT '',
  dialogue TEXT NOT NULL DEFAULT '',
  asset_codes TEXT NOT NULL DEFAULT '[]',
  keyframe_prompt TEXT NOT NULL DEFAULT '',
  video_prompt TEXT NOT NULL DEFAULT '',
  picked_image_id INTEGER NOT NULL DEFAULT 0,
  picked_video_id INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS shots_project ON shots(project_id);
CREATE TABLE IF NOT EXISTS images (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id INTEGER NOT NULL DEFAULT 0,
  owner_type TEXT NOT NULL DEFAULT '',
  owner_id INTEGER NOT NULL DEFAULT 0,
  file TEXT NOT NULL,
  mime TEXT NOT NULL DEFAULT '',
  width INTEGER NOT NULL DEFAULT 0,
  height INTEGER NOT NULL DEFAULT 0,
  bytes INTEGER NOT NULL DEFAULT 0,
  source TEXT NOT NULL DEFAULT 'gen',
  prompt TEXT NOT NULL DEFAULT '',
  channel_id INTEGER NOT NULL DEFAULT 0,
  model TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS images_owner ON images(owner_type, owner_id);
CREATE TABLE IF NOT EXISTS tasks (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id INTEGER NOT NULL DEFAULT 0,
  kind TEXT NOT NULL,
  owner_type TEXT NOT NULL DEFAULT '',
  owner_id INTEGER NOT NULL DEFAULT 0,
  status TEXT NOT NULL DEFAULT 'running',
  error TEXT NOT NULL DEFAULT '',
  result TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL,
  finished_at INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS tasks_owner ON tasks(owner_type, owner_id);
CREATE TABLE IF NOT EXISTS videos (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id INTEGER NOT NULL DEFAULT 0,
  shot_id INTEGER NOT NULL DEFAULT 0,
  source TEXT NOT NULL DEFAULT 'project',
  status TEXT NOT NULL DEFAULT 'queued',
  progress REAL NOT NULL DEFAULT 0,
  stage TEXT NOT NULL DEFAULT '',
  prompt TEXT NOT NULL DEFAULT '',
  image_file TEXT NOT NULL DEFAULT '',
  last_file TEXT NOT NULL DEFAULT '',
  seconds INTEGER NOT NULL DEFAULT 5,
  res TEXT NOT NULL DEFAULT '768p',
  aspect TEXT NOT NULL DEFAULT '9:16',
  turbo INTEGER NOT NULL DEFAULT 0,
  seed INTEGER NOT NULL DEFAULT 0,
  file TEXT NOT NULL DEFAULT '',
  bytes INTEGER NOT NULL DEFAULT 0,
  error TEXT NOT NULL DEFAULT '',
  session_id INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL,
  started_at INTEGER NOT NULL DEFAULT 0,
  finished_at INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS videos_shot ON videos(shot_id);
CREATE INDEX IF NOT EXISTS videos_status ON videos(status);
CREATE TABLE IF NOT EXISTS sessions (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  runtime TEXT NOT NULL,
  accelerator TEXT NOT NULL,
  variant TEXT NOT NULL,
  shape TEXT NOT NULL,
  purpose TEXT NOT NULL DEFAULT 'video',
  status TEXT NOT NULL DEFAULT 'creating',
  end_reason TEXT NOT NULL DEFAULT '',
  jobs_done INTEGER NOT NULL DEFAULT 0,
  started_at INTEGER NOT NULL,
  ready_at INTEGER NOT NULL DEFAULT 0,
  ended_at INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS api_keys (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL,
  key_hash TEXT NOT NULL,
  tail TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  last_used_at INTEGER NOT NULL DEFAULT 0
);
`

func openDB(dir string) error {
	var err error
	dsn := "file:" + filepath.Join(dir, "manju.db") + "?_pragma=busy_timeout(8000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)"
	db, err = sql.Open("sqlite", dsn)
	if err != nil {
		return err
	}
	db.SetMaxOpenConns(8)
	if _, err = db.Exec(schema); err != nil {
		return fmt.Errorf("schema: %w", err)
	}
	// 重启时：进行中的生成任务标记中断；正在跑的视频退回队列
	_, _ = db.Exec(`UPDATE tasks SET status='failed', error='服务重启，任务中断', finished_at=? WHERE status='running'`, now())
	_, _ = db.Exec(`UPDATE videos SET status='queued', progress=0, stage='' WHERE status='running'`)
	_, _ = db.Exec(`UPDATE sessions SET status='ended', end_reason='服务重启', ended_at=? WHERE status NOT IN ('ended')`, now())
	return nil
}

func now() int64 { return time.Now().UnixMilli() }

func getSetting(key, def string) string {
	var v string
	if err := db.QueryRow(`SELECT value FROM settings WHERE key=?`, key).Scan(&v); err != nil {
		return def
	}
	return v
}

func setSetting(key, value string) error {
	_, err := db.Exec(`INSERT INTO settings(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
	return err
}

func delSetting(key string) { _, _ = db.Exec(`DELETE FROM settings WHERE key=?`, key) }

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}
