package main

import "strings"

// 作品类型：manju 漫剧（默认）/ live 真人剧。画风与类型独立：漫剧也可以选真人风画面（仿真人漫剧）。
func normKind(k string) string {
	if k == "live" {
		return "live"
	}
	return "manju"
}

func isLive(p *Project) bool { return p != nil && p.Kind == "live" }

var realWords = []string{"写实", "真人", "实拍", "电影感", "仿真人", "超写实", "胶片"}

// isRealistic 画面是否走真人质感：真人剧，或画风里写了写实/真人一类的词
func isRealistic(p *Project) bool {
	if p == nil {
		return false
	}
	if isLive(p) {
		return true
	}
	for _, w := range realWords {
		if strings.Contains(p.Style, w) {
			return true
		}
	}
	return false
}

// realTail 真人质感的统一补充描述，用在定妆图、关键帧、封面
// 2026-11-20 三个生图中转实测调过：写明「保留毛孔、不过度磨皮、色调克制」后塑料感明显减少；不写人种时有的模型会出欧美脸
const realTail = "真人实拍质感，像国产网剧或电影的剧照：真实的皮肤纹理，保留毛孔和细微瑕疵，不过度磨皮，自然肤色，50mm 镜头，浅景深，电影级布光但色调克制自然，人物比例与神态真实；人物默认是中国人面孔，设定另有说明除外；不要动漫、插画、二次元、CG 或游戏渲染感"

// realSheetExtra 真人风定妆三视图额外附一张面部特写：真人脸比动漫脸更难保持一致，特写给后续镜头当参考
const realSheetExtra = "画面右上角另附一张该角色的正面面部特写，用来锁定长相"

// realVideoTail 视频描述用的真人补充
const realVideoTail = "真人实拍质感，电影镜头语言，动作与表情自然真实，不要动漫或卡通感"

// realStyle 画风为空时的默认写法
func styleOr(p *Project, manjuDefault string) string {
	if s := strings.TrimSpace(p.Style); s != "" {
		return s
	}
	if isRealistic(p) {
		return "电影感真人写实"
	}
	return manjuDefault
}

// sysFor 按作品类型改写各环节的系统提示词：真人剧把「漫剧」相关说法换成真人短剧
func sysFor(p *Project, sys string) string {
	if isLive(p) {
		r := strings.NewReplacer(
			"竖屏漫剧（AI 生成的连载短篇动态漫画剧）", "竖屏真人短剧（AI 生成、真人实拍质感的连载短剧）",
			"漫剧美术统筹", "真人短剧美术与选角统筹",
			"漫剧分镜师", "真人短剧分镜导演",
			"连载竖屏漫剧", "连载竖屏真人短剧",
			"连载漫剧", "连载真人短剧",
			"漫剧", "真人短剧",
			"无法画出来", "无法拍出来",
		)
		sys = r.Replace(sys)
		sys += "\n本剧是真人剧：人物是真人演员的样貌与表演，表情和动作要克制可信，避免夸张的漫画化表现。"
	}
	if isRealistic(p) && strings.Contains(sys, "分镜") {
		sys += "\n画面是真人实拍质感：镜头语言按影视拍摄来写，多用正反打、过肩、浅景深特写和自然光效，keyframe_prompt 与 video_prompt里不要出现动漫、漫画、二次元等字眼。"
	}
	return sys
}

func dramaWord(p *Project) string {
	if isLive(p) {
		return "短剧"
	}
	return "漫剧"
}
