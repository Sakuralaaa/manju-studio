package main

// 权重缓存（可选，默认关闭）：用 drive.file 权限（只能访问本应用自己建的文件）在云端硬盘建一个文件夹，
// 第一次会话下载完权重后在运行时里后台上传到这个文件夹，之后的会话直接从云端硬盘多线程拉取，
// 不再每次从 Hugging Face 下载约 42 GB。访问令牌由应用写进运行时的 /content/mj_drive_token，每 10 分钟刷新。

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const driveScope = "https://www.googleapis.com/auth/drive.file"

type driveFile struct {
	ID   string `json:"id"`
	Size int64  `json:"size"`
}

type driveState struct {
	FolderID string               `json:"folder_id"`
	Files    map[string]driveFile `json:"files"`
}

func loadDriveState() driveState {
	var s driveState
	_ = json.Unmarshal([]byte(getSetting("drive_cache", "{}")), &s)
	if s.Files == nil {
		s.Files = map[string]driveFile{}
	}
	return s
}

func saveDriveState(s driveState) { _ = setSetting("drive_cache", mustJSON(s)) }

// grantedScopes 授权时记下的范围；旧授权没有记录时向 Google 查一次
func grantedScopes(ctx context.Context) string {
	if s := getSetting("colab_scopes", ""); s != "" {
		return s
	}
	tok, err := colabAccessToken(ctx)
	if err != nil {
		return ""
	}
	req, _ := http.NewRequestWithContext(ctx, "GET", "https://oauth2.googleapis.com/tokeninfo?access_token="+url.QueryEscape(tok), nil)
	resp, err := googleHTTP.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	var out struct {
		Scope string `json:"scope"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if out.Scope != "" {
		_ = setSetting("colab_scopes", out.Scope)
	}
	return out.Scope
}

func hasDriveScope(ctx context.Context) bool {
	return strings.Contains(grantedScopes(ctx), driveScope)
}

func driveCall(ctx context.Context, method, u string, body any, out any) (int, error) {
	tok, err := colabAccessToken(ctx)
	if err != nil {
		return 0, err
	}
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequestWithContext(ctx, method, u, rd)
	req.Header.Set("Authorization", "Bearer "+tok)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := googleHTTP.Do(req)
	if err != nil {
		return 0, errors.New("连接云端硬盘失败：" + shortErr(err))
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if resp.StatusCode >= 300 {
		return resp.StatusCode, fmt.Errorf("云端硬盘返回 %d：%s", resp.StatusCode, googleMsg(raw))
	}
	if out != nil {
		_ = json.Unmarshal(raw, out)
	}
	return resp.StatusCode, nil
}

func ensureDriveFolder(ctx context.Context) (driveState, error) {
	s := loadDriveState()
	if s.FolderID != "" {
		var f struct {
			ID      string `json:"id"`
			Trashed bool   `json:"trashed"`
		}
		code, err := driveCall(ctx, "GET", "https://www.googleapis.com/drive/v3/files/"+s.FolderID+"?fields=id,trashed", nil, &f)
		if err == nil && !f.Trashed {
			return s, nil
		}
		if code != 404 && err != nil {
			return s, err
		}
		s = driveState{Files: map[string]driveFile{}}
	}
	var f struct {
		ID string `json:"id"`
	}
	if _, err := driveCall(ctx, "POST", "https://www.googleapis.com/drive/v3/files?fields=id", map[string]any{"name": "漫剧工坊权重缓存", "mimeType": "application/vnd.google-apps.folder"}, &f); err != nil {
		return s, err
	}
	s.FolderID = f.ID
	saveDriveState(s)
	return s, nil
}

func pushDriveToken(ctx context.Context, jup *Jupyter) error {
	tok, err := colabAccessToken(ctx)
	if err != nil {
		return err
	}
	return jup.Upload(ctx, "/content/mj_drive_token", []byte(tok))
}

func driveBootCfg(ctx context.Context, jup *Jupyter) (map[string]any, error) {
	if !hasDriveScope(ctx) {
		return nil, errors.New("授权里没有云端硬盘权限，需要重新授权")
	}
	s, err := ensureDriveFolder(ctx)
	if err != nil {
		return nil, err
	}
	if err := pushDriveToken(ctx, jup); err != nil {
		return nil, err
	}
	return map[string]any{"enabled": true, "folder_id": s.FolderID, "token_file": "/content/mj_drive_token", "status_file": "/content/mj_drive_status.json", "files": s.Files}, nil
}

// driveSync 刷新运行时里的令牌，并把后台上传完成的文件编号记下来
func (r *Runner) driveSync() {
	r.mu.Lock()
	jup := r.jup
	r.mu.Unlock()
	if jup == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	_ = pushDriveToken(ctx, jup)
	var buf bytes.Buffer
	if _, err := jup.Download(ctx, "/content/mj_drive_status.json", &buf); err != nil {
		return
	}
	var st struct {
		Files map[string]driveFile `json:"files"`
	}
	if json.Unmarshal(buf.Bytes(), &st) != nil {
		return
	}
	s := loadDriveState()
	n := 0
	for k, v := range st.Files {
		if v.ID != "" && s.Files[k].ID != v.ID {
			s.Files[k] = v
			n++
		}
	}
	if n > 0 {
		saveDriveState(s)
		r.logf("云端硬盘缓存新增 %d 个权重文件", n)
	}
}

func driveView(ctx context.Context) map[string]any {
	cfg := loadColabCfg()
	s := loadDriveState()
	var total int64
	for _, f := range s.Files {
		total += f.Size
	}
	scopeOK := false
	if getSetting("colab_refresh_token", "") != "" && cfg.DriveCache {
		scopeOK = hasDriveScope(ctx)
	}
	return map[string]any{"enabled": cfg.DriveCache, "scope_ok": scopeOK, "folder": s.FolderID != "", "files": len(s.Files), "bytes": total}
}

func handleDriveReset(w http.ResponseWriter, r *http.Request) {
	saveDriveState(driveState{Files: map[string]driveFile{}})
	writeJSON(w, map[string]any{"ok": true})
}
