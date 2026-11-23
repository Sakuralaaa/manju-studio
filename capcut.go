package main

// 剪映草稿导出：结构对照开源项目 pyJianYingDraft（剪映 5.9 草稿格式，draft_content.json + draft_meta_info.json）。
// 轨道：视频（各镜头选定版本）、配音（逐句）、配乐、音效、字幕（文字轨）。时间单位为微秒。
// 素材路径：填写了本机草稿目录时写绝对路径；否则使用剪映的草稿占位前缀，素材放在草稿文件夹的 materials/ 下。

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"
)

func jyID() string {
	h := randHex(16)
	return h
}

func jyUUID() string {
	h := strings.ToUpper(randHex(16))
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

func us(sec float64) int64 { return int64(sec*1_000_000 + 0.5) }

type jyDraft struct {
	materials map[string][]any
	tracks    []map[string]any
	render    int
}

func (d *jyDraft) speed(sp float64) string {
	id := jyID()
	d.materials["speeds"] = append(d.materials["speeds"], map[string]any{"curve_speed": nil, "id": id, "mode": 0, "speed": sp, "type": "speed"})
	return id
}

func segBase(id, mat string, start, dur int64, src map[string]any, sp, vol float64, refs []string, render int) map[string]any {
	return map[string]any{
		"enable_adjust": true, "enable_color_correct_adjust": false, "enable_color_curves": true, "enable_color_match_adjust": false, "enable_color_wheels": true,
		"enable_lut": true, "enable_smart_color_adjust": false, "last_nonzero_volume": 1.0, "reverse": false, "track_attribute": 0, "track_render_index": 0,
		"visible": true, "id": id, "material_id": mat, "target_timerange": map[string]any{"start": start, "duration": dur}, "common_keyframes": []any{},
		"keyframe_refs": []any{}, "source_timerange": src, "speed": sp, "volume": vol, "extra_material_refs": refs, "is_tone_modify": false, "render_index": render,
	}
}

func clipXform(y float64) map[string]any {
	return map[string]any{"alpha": 1.0, "flip": map[string]any{"horizontal": false, "vertical": false}, "rotation": 0.0, "scale": map[string]any{"x": 1.0, "y": 1.0}, "transform": map[string]any{"x": 0.0, "y": y}}
}

func (d *jyDraft) track(typ, name string, segs []any) {
	d.tracks = append(d.tracks, map[string]any{"attribute": 0, "flag": 0, "id": jyID(), "is_default_name": false, "name": name, "segments": segs, "type": typ})
}

func (d *jyDraft) videoMat(path, name string, dur float64, w, h int) string {
	id := jyID()
	d.materials["videos"] = append(d.materials["videos"], map[string]any{
		"audio_fade": nil, "category_id": "", "category_name": "local", "check_flag": 63487,
		"crop":       map[string]any{"upper_left_x": 0.0, "upper_left_y": 0.0, "upper_right_x": 1.0, "upper_right_y": 0.0, "lower_left_x": 0.0, "lower_left_y": 1.0, "lower_right_x": 1.0, "lower_right_y": 1.0},
		"crop_ratio": "free", "crop_scale": 1.0, "duration": us(dur), "height": h, "id": id, "local_material_id": "", "material_id": id,
		"material_name": name, "media_path": "", "path": path, "type": "video", "width": w,
	})
	return id
}

func (d *jyDraft) audioMat(path, name string, dur float64) string {
	id := jyID()
	d.materials["audios"] = append(d.materials["audios"], map[string]any{
		"app_id": 0, "category_id": "", "category_name": "local", "check_flag": 3, "copyright_limit_type": "none", "duration": us(dur), "effect_id": "",
		"formula_id": "", "id": id, "local_material_id": id, "music_id": id, "name": name, "path": path, "source_platform": 0, "type": "extract_music", "wave_points": []any{},
	})
	return id
}

func (d *jyDraft) textMat(text string) string {
	id := jyID()
	n := len([]rune(text))
	content := map[string]any{"styles": []any{map[string]any{"fill": map[string]any{"alpha": 1.0, "content": map[string]any{"render_type": "solid", "solid": map[string]any{"alpha": 1.0, "color": []float64{1, 1, 1}}}},
		"range": []int{0, n}, "size": 7.0, "bold": false, "italic": false, "underline": false, "strokes": []any{}}}, "text": text}
	b, _ := json.Marshal(content)
	d.materials["texts"] = append(d.materials["texts"], map[string]any{"id": id, "content": string(b), "typesetting": 0, "alignment": 1, "letter_spacing": 0.0, "line_spacing": 0.02,
		"line_feed": 1, "line_max_width": 0.82, "force_apply_line_max_width": false, "check_flag": 7, "type": "text", "global_alpha": 1.0})
	return id
}

var jyMaterialKeys = []string{"ai_translates", "audio_balances", "audio_effects", "audio_fades", "audio_track_indexes", "audios", "beats", "canvases", "chromas", "color_curves", "digital_humans", "drafts",
	"effects", "flowers", "green_screens", "handwrites", "hsl", "images", "log_color_wheels", "loudnesses", "manual_deformations", "masks", "material_animations", "material_colors",
	"multi_language_refs", "placeholders", "plugin_effects", "primary_color_wheels", "realtime_denoises", "shapes", "smart_crops", "smart_relights", "sound_channel_mappings", "speeds",
	"stickers", "tail_leaders", "text_templates", "texts", "time_marks", "transitions", "video_effects", "video_trackings", "videos", "vocal_beautifys", "vocal_separations"}

func handleCapcutExport(w http.ResponseWriter, r *http.Request) {
	p, e, err := epCtx(pathID(r))
	if err != nil {
		httpErr(w, 404, err.Error())
		return
	}
	tl, err := buildTimeline(p, e)
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	draftName := safeName(fmt.Sprintf("%s_第%d集", p.Title, e.No))
	root := strings.TrimRight(strings.ReplaceAll(strings.TrimSpace(r.URL.Query().Get("root")), `\`, "/"), "/")
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", "attachment; filename*=UTF-8''"+url.PathEscape(draftName+"_剪映草稿.zip"))
	_ = writeCapcutZip(w, tl, draftName, root)
}

func writeCapcutZip(w io.Writer, tl *timeline, draftName, root string) error {
	p, e := tl.P, tl.E
	draftID := jyUUID()
	matPath := func(name string) string {
		if root != "" {
			return root + "/" + draftName + "/materials/" + name
		}
		return "##_draftpath_placeholder_" + draftID + "_##/materials/" + name
	}
	W, H := deliverDims(p)
	d := &jyDraft{materials: map[string][]any{}}
	for _, k := range jyMaterialKeys {
		d.materials[k] = []any{}
	}
	files := map[string]string{} // 草稿内文件名 → 本地路径
	// 视频轨
	var vsegs []any
	for _, c := range tl.Clips {
		name := fmt.Sprintf("镜头%s%s", c.Shot.Code, filepath.Ext(c.Video.File))
		files[name] = c.Path
		mat := d.videoMat(matPath(name), name, c.Info.dur, c.Info.w, c.Info.h)
		vol := 1.0
		if tl.Dub {
			vol = p.OrigVolume
		}
		seg := segBase(jyID(), mat, us(c.Start), us(c.Dur), map[string]any{"start": us(c.TrimStart), "duration": us(c.Dur)}, 1.0, vol, []string{d.speed(1)}, 0)
		seg["clip"] = clipXform(0)
		seg["uniform_scale"] = map[string]any{"on": true, "value": 1.0}
		seg["hdr_settings"] = map[string]any{"intensity": 1.0, "mode": 1, "nits": 1000}
		vsegs = append(vsegs, seg)
	}
	d.track("video", "视频", vsegs)
	// 配音轨
	if tl.Dub {
		var asegs []any
		for _, c := range tl.Clips {
			for _, ln := range c.Lines {
				name := fmt.Sprintf("配音_%s_%02d%s", c.Shot.Code, ln.L.Sort+1, filepath.Ext(ln.L.AudioFile))
				files[name] = filepath.Join(dataDir, "audio", ln.L.AudioFile)
				mat := d.audioMat(matPath(name), name, ln.L.Duration)
				sp := 1.0
				if ln.Tempo > 1.001 {
					sp = ln.Tempo
				}
				seg := segBase(jyID(), mat, us(c.Start+ln.Start), us(ln.Dur), map[string]any{"start": 0, "duration": us(ln.L.Duration)}, sp, 1.0, []string{d.speed(sp)}, 1)
				seg["clip"], seg["hdr_settings"] = nil, nil
				asegs = append(asegs, seg)
			}
		}
		d.track("audio", "配音", asegs)
	}
	// 音效轨
	var sfx []any
	for _, c := range tl.Clips {
		if c.Shot.SfxFile == "" {
			continue
		}
		name := fmt.Sprintf("音效_%s%s", c.Shot.Code, filepath.Ext(c.Shot.SfxFile))
		path := filepath.Join(dataDir, "audio", c.Shot.SfxFile)
		files[name] = path
		ci, _ := probe(path)
		dur := ci.dur
		if dur > c.Dur {
			dur = c.Dur
		}
		mat := d.audioMat(matPath(name), name, ci.dur)
		seg := segBase(jyID(), mat, us(c.Start), us(dur), map[string]any{"start": 0, "duration": us(dur)}, 1.0, c.Shot.SfxVolume, []string{d.speed(1)}, 2)
		seg["clip"], seg["hdr_settings"] = nil, nil
		sfx = append(sfx, seg)
	}
	if len(sfx) > 0 {
		d.track("audio", "音效", sfx)
	}
	// 配乐轨（按总长截取；长度不够时循环铺满）
	if e.BgmID > 0 {
		if b, err := getBgm(e.BgmID); err == nil && b.Duration > 0 {
			name := "配乐" + filepath.Ext(b.File)
			files[name] = filepath.Join(dataDir, "audio", b.File)
			mat := d.audioMat(matPath(name), name, b.Duration)
			var segs []any
			for t := 0.0; t < tl.Total-0.05; t += b.Duration {
				dur := b.Duration
				if t+dur > tl.Total {
					dur = tl.Total - t
				}
				seg := segBase(jyID(), mat, us(t), us(dur), map[string]any{"start": 0, "duration": us(dur)}, 1.0, e.BgmVolume, []string{d.speed(1)}, 3)
				seg["clip"], seg["hdr_settings"] = nil, nil
				segs = append(segs, seg)
			}
			d.track("audio", "配乐", segs)
		}
	}
	// 字幕轨
	var tsegs []any
	for _, c := range tl.Clips {
		for _, sl := range c.Subs {
			dur := sl.Dur
			if sl.Start+dur > c.Dur {
				dur = c.Dur - sl.Start
			}
			if dur <= 0.05 {
				continue
			}
			seg := segBase(jyID(), d.textMat(sl.L.Text), us(c.Start+sl.Start), us(dur), nil, 1.0, 1.0, []string{}, 4)
			seg["clip"] = clipXform(-0.72)
			seg["uniform_scale"] = map[string]any{"on": true, "value": 1.0}
			tsegs = append(tsegs, seg)
		}
	}
	if len(tsegs) > 0 {
		d.track("text", "字幕", tsegs)
	}
	nowUS := time.Now().UnixMicro()
	content := map[string]any{
		"canvas_config": map[string]any{"height": H, "ratio": "original", "width": W}, "color_space": 0,
		"config": map[string]any{"adjust_max_index": 1, "attachment_info": []any{}, "combination_max_index": 1, "export_range": nil, "extract_audio_last_index": 1, "lyrics_recognition_id": "",
			"lyrics_sync": true, "lyrics_taskinfo": []any{}, "maintrack_adsorb": true, "material_save_mode": 0, "multi_language_current": "none", "multi_language_list": []any{},
			"multi_language_main": "none", "multi_language_mode": "none", "original_sound_last_index": 1, "record_audio_last_index": 1, "sticker_max_index": 1,
			"subtitle_keywords_config": nil, "subtitle_recognition_id": "", "subtitle_sync": true, "subtitle_taskinfo": []any{}, "system_font_list": []any{}, "video_mute": false, "zoom_info_params": nil},
		"cover": nil, "create_time": 0, "duration": us(tl.Total), "extra_info": nil, "fps": 30.0, "free_render_index_mode_on": false, "group_container": nil, "id": jyUUID(),
		"keyframe_graph_list": []any{}, "keyframes": map[string]any{"adjusts": []any{}, "audios": []any{}, "effects": []any{}, "filters": []any{}, "handwrites": []any{}, "stickers": []any{}, "texts": []any{}, "videos": []any{}},
		"last_modified_platform": map[string]any{"app_id": 3704, "app_source": "lv", "app_version": "5.9.0", "os": "windows"},
		"platform":               map[string]any{"app_id": 3704, "app_source": "lv", "app_version": "5.9.0", "os": "windows"},
		"materials":              d.materials, "mutable_config": nil, "name": "", "new_version": "110.0.0", "relationships": []any{}, "render_index_track_mode_on": false,
		"retouch_cover": nil, "source": "default", "static_cover_image_path": "", "time_marks": nil, "tracks": d.tracks, "update_time": 0, "version": 360000,
	}
	meta := map[string]any{
		"cloud_package_completed_time": "", "draft_cloud_capcut_purchase_info": "", "draft_cloud_last_action_download": false, "draft_cloud_materials": []any{},
		"draft_cloud_purchase_info": "", "draft_cloud_template_id": "", "draft_cloud_tutorial_info": "", "draft_cloud_videocut_purchase_info": "", "draft_cover": "",
		"draft_deeplink_url": "", "draft_enterprise_info": map[string]any{"draft_enterprise_extra": "", "draft_enterprise_id": "", "draft_enterprise_name": "", "enterprise_material": []any{}},
		"draft_fold_path": strings.TrimSuffix(root+"/"+draftName, "/"), "draft_id": draftID, "draft_is_ai_packaging_used": false, "draft_is_ai_shorts": false, "draft_is_ai_translate": false,
		"draft_is_article_video_draft": false, "draft_is_from_deeplink": "false", "draft_is_invisible": false,
		"draft_materials": []any{map[string]any{"type": 0, "value": []any{}}, map[string]any{"type": 1, "value": []any{}}, map[string]any{"type": 2, "value": []any{}}, map[string]any{"type": 3, "value": []any{}},
			map[string]any{"type": 6, "value": []any{}}, map[string]any{"type": 7, "value": []any{}}, map[string]any{"type": 8, "value": []any{}}},
		"draft_materials_copied_info": []any{}, "draft_name": draftName, "draft_new_version": "", "draft_removable_storage_device": "", "draft_root_path": root,
		"draft_segment_extra_info": []any{}, "draft_type": "", "tm_draft_cloud_completed": "", "tm_draft_cloud_modified": 0, "tm_draft_create": nowUS, "tm_draft_modified": nowUS,
		"tm_draft_removed": 0, "tm_duration": us(tl.Total),
	}
	if root == "" {
		meta["draft_fold_path"] = ""
	}
	cb, _ := json.MarshalIndent(content, "", "  ")
	mb, _ := json.MarshalIndent(meta, "", "  ")
	z := zipW{zip.NewWriter(w)}
	defer z.Close()
	z.add(draftName+"/draft_content.json", cb)
	z.add(draftName+"/draft_meta_info.json", mb)
	for name, path := range files {
		z.addFile(draftName+"/materials/"+name, path)
	}
	if e.CoverImageID > 0 {
		if im, err := getImage(e.CoverImageID); err == nil {
			z.addFile(draftName+"/draft_cover"+filepath.Ext(im.File), filepath.Join(dataDir, "images", im.File))
		}
	}
	return nil
}
