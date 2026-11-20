package main

import (
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata"
)

//go:embed all:web/dist
var webFS embed.FS

var (
	version   = "0.2.3"
	dataDir   string
	adminPass string
	secret    []byte
)

func main() {
	dataDir = env("DATA_DIR", "/data")
	adminPass = os.Getenv("ADMIN_PASSWORD")
	addr := env("LISTEN", ":"+env("PORT", "8080"))
	for _, d := range []string{"", "images", "videos", "audio", "exports", "tmp"} {
		if err := os.MkdirAll(filepath.Join(dataDir, d), 0o755); err != nil {
			log.Fatal(err)
		}
	}
	if err := openDB(dataDir); err != nil {
		log.Fatal(err)
	}
	s := getSetting("session_secret", "")
	if s == "" {
		b := make([]byte, 32)
		_, _ = rand.Read(b)
		s = hex.EncodeToString(b)
		_ = setSetting("session_secret", s)
	}
	secret = []byte(s)
	if adminPass == "" {
		log.Printf("警告：没有设置 ADMIN_PASSWORD，无法登录")
	}
	initWorkflows()
	go runner.loop()
	go resumePipes()

	mux := http.NewServeMux()
	routes(mux)
	log.Printf("漫剧工坊 %s 监听 %s，数据目录 %s", version, addr, dataDir)
	srv := &http.Server{Addr: addr, Handler: logMW(mux), ReadHeaderTimeout: 20 * time.Second}
	log.Fatal(srv.ListenAndServe())
}

func env(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

func routes(m *http.ServeMux) {
	m.HandleFunc("GET /api/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"ok": true, "version": version})
	})
	m.HandleFunc("POST /api/login", handleLogin)
	m.HandleFunc("POST /api/logout", handleLogout)
	m.HandleFunc("GET /api/me", handleMe)

	a := func(p string, h http.HandlerFunc) { m.Handle(p, auth(h)) }

	// 渠道
	a("GET /api/presets", handlePresets)
	a("GET /api/channels", handleChannelList)
	a("POST /api/channels", handleChannelSave)
	a("PUT /api/channels/{id}", handleChannelSave)
	a("DELETE /api/channels/{id}", handleChannelDelete)
	a("POST /api/channels/probe", handleChannelProbe)

	// 作品（剧集）
	a("GET /api/projects", handleProjectList)
	a("POST /api/projects", handleProjectCreate)
	a("GET /api/projects/{id}", handleProjectGet)
	a("PUT /api/projects/{id}", handleProjectUpdate)
	a("DELETE /api/projects/{id}", handleProjectDelete)
	a("POST /api/projects/{id}/bible/generate", handleBibleGenerate)
	a("POST /api/projects/{id}/episodes", handleEpisodeCreate)
	a("POST /api/projects/{id}/assets", handleAssetCreate)
	a("POST /api/projects/{id}/assets/generate-all", handleAssetsGenerateAll)
	a("POST /api/projects/{id}/sheets/generate-all", handleSheetsGenerateAll)
	a("POST /api/projects/{id}/voices/design-all", handleVoicesDesignAll)
	a("GET /api/episodes/{id}", handleEpisodeGet)
	a("PUT /api/episodes/{id}", handleEpisodeUpdate)
	a("DELETE /api/episodes/{id}", handleEpisodeDelete)
	a("POST /api/episodes/{id}/idea", handleEpisodeIdea)
	a("POST /api/episodes/{id}/summary", handleEpisodeSummary)
	a("POST /api/episodes/{id}/script/generate", handleScriptGenerate)
	a("POST /api/episodes/{id}/assets/extract", handleAssetsExtract)
	a("POST /api/episodes/{id}/shots/split", handleShotsSplit)
	a("POST /api/episodes/{id}/shots", handleShotCreate)
	a("POST /api/episodes/{id}/shots/order", handleShotOrder)
	a("POST /api/episodes/{id}/keyframes/generate-all", handleKeyframesGenerateAll)
	a("POST /api/episodes/{id}/videos/queue", handleEpisodeVideosQueue)
	a("POST /api/episodes/{id}/lines/generate", handleLinesGenerate)
	a("POST /api/episodes/{id}/final", handleFinalBuild)
	a("POST /api/episodes/{id}/cover", handleCoverGenerate)
	a("GET /api/episodes/{id}/covers", handleCoverList)
	a("GET /api/episodes/{id}/export", handleEpisodeExport)
	a("GET /api/episodes/{id}/capcut", handleCapcutExport)
	a("GET /api/episodes/{id}/autopilot", handlePipeGet)
	a("POST /api/episodes/{id}/autopilot", handlePipeStart)
	a("POST /api/episodes/{id}/autopilot/continue", handlePipeContinue)
	a("POST /api/episodes/{id}/autopilot/cancel", handlePipeCancel)
	a("PUT /api/assets/{id}", handleAssetUpdate)
	a("DELETE /api/assets/{id}", handleAssetDelete)
	a("POST /api/assets/{id}/generate", handleAssetGenerate)
	a("POST /api/assets/{id}/upload", handleAssetUpload)
	a("POST /api/assets/{id}/voice/describe", handleVoiceDescribe)
	a("PUT /api/shots/{id}", handleShotUpdate)
	a("DELETE /api/shots/{id}", handleShotDelete)
	a("GET /api/shots/{id}/refs", handleShotRefs)
	a("POST /api/shots/{id}/keyframe", handleKeyframeGenerate)
	a("POST /api/shots/{id}/keyframe/upload", handleKeyframeUpload)
	a("POST /api/shots/{id}/video", handleShotVideo)
	a("POST /api/shots/{id}/sfx", handleShotSfxUpload)
	a("DELETE /api/shots/{id}/sfx", handleShotSfxDelete)
	a("POST /api/images/{id}/pick", handleImagePick)
	a("POST /api/images/{id}/copy", handleImageCopy)
	a("GET /api/studio", handleStudioList)
	a("POST /api/studio/generate", handleStudioGenerate)
	a("POST /api/studio/upload", handleStudioUpload)
	a("GET /api/reflib", handleRefLibrary)
	a("DELETE /api/images/{id}", handleImageDelete)
	a("POST /api/videos/{id}/pick", handleVideoPick)
	a("POST /api/videos/{id}/cancel", handleVideoCancel)
	a("GET /api/tasks/{id}", handleTaskGet)
	a("PUT /api/lines/{id}", handleLineUpdate)
	a("POST /api/lines/{id}/generate", handleLineGenerate)

	// 配音 / 配乐
	a("GET /api/voice/meta", handleVoiceMeta)
	a("POST /api/voice/preview", handleVoicePreview)
	a("POST /api/voice/design", handleVoiceDesign)
	a("POST /api/voice/sample", handleVoiceSample)
	a("GET /api/voice/fish", handleFishModels)
	a("POST /api/voice/fish", handleFishCreate)
	a("PUT /api/voice/fish/{mid}", handleFishUpdate)
	a("DELETE /api/voice/fish/{mid}", handleFishDelete)
	a("GET /api/bgm", handleBgmList)
	a("POST /api/bgm", handleBgmUpload)
	a("DELETE /api/bgm/{id}", handleBgmDelete)
	a("GET /api/ledger", handleLedger)

	// 算力
	a("GET /api/colab", handleColabState)
	a("PUT /api/colab/settings", handleColabSettings)
	a("POST /api/colab/client", handleColabClientUpload)
	a("DELETE /api/colab/client", handleColabClientDelete)
	a("POST /api/colab/auth/start", handleColabAuthStart)
	a("POST /api/colab/auth/finish", handleColabAuthFinish)
	a("POST /api/colab/refresh", handleColabRefresh)
	a("POST /api/colab/start", handleColabStart)
	a("POST /api/colab/drive/reset", handleDriveReset)
	a("POST /api/colab/stop", handleColabStop)
	a("POST /api/colab/exec", handleColabExec)
	a("GET /api/colab/log", handleColabLog)
	a("GET /api/workflows", handleWorkflowList)
	a("PUT /api/workflows/{key}", handleWorkflowSave)
	a("POST /api/workflows/{key}/reset", handleWorkflowReset)
	a("GET /api/videos", handleVideoList)

	// 对外接口
	a("GET /api/keys", handleKeyList)
	a("POST /api/keys", handleKeyCreate)
	a("DELETE /api/keys/{id}", handleKeyDelete)
	a("GET /api/antigravity/rules", handleAgentRules)

	// 文件
	a("GET /files/images/{name}", serveData("images"))
	a("GET /files/videos/{name}", serveData("videos"))
	a("GET /files/exports/{name}", serveData("exports"))
	a("GET /files/audio/{name}", serveData("audio"))

	// 给其他智能体用：固定密钥
	k := func(p string, h http.HandlerFunc) { m.Handle(p, keyAuth(h)) }
	k("GET /v1/colab", extColabStatus)
	k("GET /v1/colab/specs", extColabSpecs)
	k("POST /v1/colab/start", extColabStart)
	k("POST /v1/colab/stop", extColabStop)
	k("POST /v1/colab/exec", extColabExec)
	k("POST /v1/videos", extVideoSubmit)
	k("GET /v1/videos/{id}", extVideoGet)
	k("GET /v1/videos/{id}/file", extVideoFile)
	k("GET /v1/projects", extProjects)
	k("GET /v1/projects/{id}/shots", extShots)
	k("POST /v1/images", extImageGenerate)
	k("POST /v1/images/{id}/copy", extImageCopy)
	k("GET /v1/channels/image", extImageChannels)
	k("POST /v1/assets/{id}/generate", extAssetGenerate)
	k("GET /v1/projects/{id}/episodes", extEpisodes)
	k("POST /v1/projects/{id}/episodes", extEpisodeCreate)
	k("GET /v1/episodes/{id}", extEpisodeGet)
	k("GET /v1/episodes/{id}/final", extEpisodeFinal)
	k("POST /v1/episodes/{id}/autopilot", extAutopilotStart)
	k("GET /v1/episodes/{id}/autopilot", extAutopilotGet)
	k("POST /v1/episodes/{id}/autopilot/continue", extAutopilotContinue)
	k("POST /v1/episodes/{id}/autopilot/cancel", extAutopilotCancel)
	k("POST /v1/episodes/{id}/lines", extLinesGenerate)
	k("POST /v1/tts", extTTS)
	k("GET /v1/audio/{name}", extAudioFile)
	k("GET /v1/images/{id}/file", extImageFile)
	k("POST /mcp", handleMCP)
	m.HandleFunc("GET /v1/dl/{token}", handleSignedDL) // 限时下载链接（令牌即授权），给外部编剧下载视频
	m.HandleFunc("GET /mcp", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Allow", "POST")
		httpErr(w, 405, "只支持 POST")
	})
	m.HandleFunc("DELETE /mcp", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })

	sub, _ := fs.Sub(webFS, "web/dist")
	fileServer := http.FileServer(http.FS(sub))
	m.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/")
		if p != "" {
			if st, err := fs.Stat(sub, p); err == nil && !st.IsDir() {
				if strings.HasPrefix(p, "static/") {
					w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				}
				fileServer.ServeHTTP(w, r)
				return
			}
		}
		if strings.HasPrefix(p, "api/") || strings.HasPrefix(p, "v1/") {
			httpErr(w, 404, "接口不存在")
			return
		}
		b, err := fs.ReadFile(sub, "index.html")
		if err != nil {
			http.Error(w, "前端未构建", 500)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write(b)
	})
}

// ---------- 工具 ----------

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(v)
}

func httpErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": msg})
}

func readJSON(r *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 64<<20))
	if err := dec.Decode(v); err != nil {
		return errors.New("请求格式错误")
	}
	return nil
}

func pathID(r *http.Request) int64 {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	return id
}

func serveData(sub string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := filepath.Base(r.PathValue("name"))
		if name == "." || strings.Contains(name, "..") {
			http.NotFound(w, r)
			return
		}
		if sub != "exports" {
			w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
		}
		if r.URL.Query().Get("dl") != "" {
			w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
		}
		http.ServeFile(w, r, filepath.Join(dataDir, sub, name))
	}
}

type statusRecorder struct {
	http.ResponseWriter
	code int
}

func (s *statusRecorder) WriteHeader(c int) { s.code = c; s.ResponseWriter.WriteHeader(c) }
func (s *statusRecorder) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func logMW(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, code: 200}
		h.ServeHTTP(rec, r)
		if (strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/v1/") || r.URL.Path == "/mcp") && !(r.Method == "GET" && rec.code < 400) {
			log.Printf("%s %s %d %s", r.Method, r.URL.Path, rec.code, time.Since(start).Round(time.Millisecond))
		}
	})
}
