package main

// 角色一致性：所有带参考图的出图（封面、关键帧、定妆图）都优先拿角色的定妆三视图作参考，
// 并把角色的固定造型（发型、服装、配饰）从结构化 look_json（或资产外貌描述 / 人物小传）里抽出来，明确写进提示词。
// 禁用元素（avoid）单独保存，不写入正面提示词，也不拼成「戴不戴帽子」这类否定句。

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

// flexList JSON 既可以是字符串数组，也可以是逗号/顿号分隔的字符串
type flexList []string

func (f *flexList) UnmarshalJSON(b []byte) error {
	var arr []string
	if json.Unmarshal(b, &arr) == nil {
		out := make([]string, 0, len(arr))
		for _, s := range arr {
			if s = strings.TrimSpace(s); s != "" {
				out = append(out, s)
			}
		}
		*f = out
		return nil
	}
	var s string
	if json.Unmarshal(b, &s) == nil {
		parts := regexp.MustCompile(`[，,、;；\n]+`).Split(s, -1)
		out := make([]string, 0, len(parts))
		for _, p := range parts {
			if p = strings.TrimSpace(p); p != "" {
				out = append(out, p)
			}
		}
		*f = out
		return nil
	}
	return fmt.Errorf("应为字符串或字符串数组")
}

// AssetLook 角色结构化固定造型（存 assets.look_json）
type AssetLook struct {
	Age         string   `json:"age,omitempty"`
	Gender      string   `json:"gender,omitempty"`
	Hair        string   `json:"hair,omitempty"`
	Face        string   `json:"face,omitempty"`
	Body        string   `json:"body,omitempty"`
	Outfit      string   `json:"outfit,omitempty"`
	Accessories string   `json:"accessories,omitempty"`
	Palette     string   `json:"palette,omitempty"`
	Avoid       flexList `json:"avoid,omitempty"`
}

func (l AssetLook) empty() bool {
	return strings.TrimSpace(l.Age+l.Gender+l.Hair+l.Face+l.Body+l.Outfit+l.Accessories+l.Palette) == ""
}

func (l AssetLook) hasAnyField() bool {
	return !l.empty() || len(l.Avoid) > 0
}

// merge 把局部更新的 AssetLook 合并到已有造型上（未传字段保留旧值；avoid 传非 nil 时替换，包括显式清空 []）
func (l AssetLook) merge(in AssetLook) AssetLook {
	if s := strings.TrimSpace(in.Age); s != "" {
		l.Age = s
	}
	if s := strings.TrimSpace(in.Gender); s != "" {
		l.Gender = s
	}
	if s := strings.TrimSpace(in.Hair); s != "" {
		l.Hair = s
	}
	if s := strings.TrimSpace(in.Face); s != "" {
		l.Face = s
	}
	if s := strings.TrimSpace(in.Body); s != "" {
		l.Body = s
	}
	if s := strings.TrimSpace(in.Outfit); s != "" {
		l.Outfit = s
	}
	if s := strings.TrimSpace(in.Accessories); s != "" {
		l.Accessories = s
	}
	if s := strings.TrimSpace(in.Palette); s != "" {
		l.Palette = s
	}
	if in.Avoid != nil {
		l.Avoid = in.Avoid
	}
	return l
}

// charRefImage 角色参考图：定妆三视图（正侧背全身，发型服装最完整）> 设定图 > 表情表
func charRefImage(a *Asset) (int64, string) {
	if a == nil {
		return 0, ""
	}
	if a.Kind == "character" && a.SheetImageID > 0 {
		return a.SheetImageID, "定妆三视图"
	}
	if a.PickedImageID > 0 {
		return a.PickedImageID, "设定图"
	}
	if a.Kind == "character" && a.ExprImageID > 0 {
		return a.ExprImageID, "表情表"
	}
	return 0, ""
}

// refNote 参考图说明：三视图要说清是同一人的多个角度，免得画面里画出好几个人
// once=true 用在封面、关键帧：画面里这个人只出现一次。
func refNote(n int, kind, name, note string, once bool) string {
	s := fmt.Sprintf("参考图%d是%s「%s」的%s", n, kind, name, note)
	if note == "定妆三视图" {
		s += "（同一个人的正面、侧面、背面"
		if once {
			s += "，画面里只画这个人一次"
		}
		s += "）"
	}
	return s
}

var (
	// 按小句切分（逗号也切），避免整句里含一个字就把性格描写全吸进来
	lookSplitRe = regexp.MustCompile(`[。！？；;，,\n]+`)
	// 按具体外观词匹配，避免单字「眼」「发」「穿」「包」误伤「眼神」「发誓」「拆穿」「抱枕」
	lookWordRe = regexp.MustCompile(
		`头发|短发|长发|中发|卷发|直发|碎发|黑发|白发|银发|金发|红发|褐发|棕发|发型|发色|发梢|发髻|丸子头|马尾|双马尾|辫子|麻花辫|刘海|偏分|中分|三七分|大背头|寸头|鬓角|发簪|发带|发卡|` +
			`五官|眉眼|剑眉|浓眉|柳叶眉|细眉|双眼皮|单眼皮|丹凤眼|杏眼|桃花眼|眼眸|双眸|瞳孔|鼻梁|高鼻|直鼻|唇形|薄唇|厚唇|红唇|下颌|颧骨|瓜子脸|鹅蛋脸|方脸|圆脸|脸型|骨相|肤色|肤质|冷白|小麦色|疤痕|泪痣|酒窝|雀斑|胎记|胡须|胡茬|淡妆|浓妆|红妆|` +
			`身形|身材|体态|体格|高挑|清瘦|瘦削|微胖|魁梧|宽肩|窄腰|修长|挺拔|冻龄|视觉年龄|` +
			`穿着|身穿|身着|外搭|内搭|叠穿|常穿|穿[^越透拆行过破帮插刺]|服装|上衣|外衣|外套|大衣|风衣|夹克|皮衣|皮夹克|西装|西服|衬衫|衬衣|T恤|t恤|毛衣|开衫|卫衣|马甲|背心|长袍|长衫|旗袍|汉服|制服|校服|警服|军装|礼服|连衣裙|长裙|短裙|长裤|短裤|西装裤|牛仔裤|工装裤|运动鞋|皮鞋|长靴|短靴|高跟鞋|围巾|手套|披风|斗篷|` +
			`佩戴|常戴|耳钉|耳环|项链|吊坠|项圈|戒指|手链|手环|护腕|腕表|手表|腕饰|眼镜|金丝眼镜|墨镜|发冠|玉佩|领带|领结|胸针|鸭舌帽|贝雷帽|草帽|斗笠|丝巾|单肩包|双肩包|背包|配饰|首饰`,
	)
	// 性格、心理、动作、剧情、以及否定短语不进入正面固定造型
	lookSkipRe = regexp.MustCompile(
		`性格|动机|说话|口头禅|关系|与.{1,8}(?:有|从|是|相)|视.{1,6}如|目标|秘密|` +
			`眼神|眼底|眼里|眼眶|防备|心理|逃避|发誓|发现|发生|发问|发白|发抖|毒舌|傲娇|倔强|冷幽默|吃瓜|共情|嘴碎|反应|张罗|爱笑|自嘲|拆解|行为|冲劲|挡在身后`,
	)
	negClauseRe = regexp.MustCompile(`^(?:(?:绝对)?(?:不戴|不拿|不穿|不带|不佩戴|不要|别戴|禁止|严禁|无任何|无帽|无配饰|无装饰|双手自然空着|绝无)|(?:无|暂无|没有)$)`)
	negParenRe  = regexp.MustCompile(`[（(][^（）()]{0,40}(?:不戴|不拿|不穿|无帽|不带|绝无|空着|禁止)[^（）()]{0,40}[）)]`)
	agePrefixRe = regexp.MustCompile(`^[0-9一二三四五六七八九十余多]+岁[左右]?[，,]?`)
)

// stripNegatives 去掉字段里的否定括号说明和纯否定小句，防止把「不戴帽子」拼进正面提示词
func stripNegatives(s string) string {
	s = negParenRe.ReplaceAllString(s, "")
	var kept []string
	for _, seg := range lookSplitRe.Split(s, -1) {
		seg = strings.TrimSpace(seg)
		if seg == "" || negClauseRe.MatchString(seg) {
			continue
		}
		kept = append(kept, seg)
	}
	return strings.Join(kept, "，")
}

// formatPositive 把结构化 AssetLook 拼成简洁、无否定词、无重复的正面造型描述（不含 avoid）
func (l AssetLook) formatPositive() string {
	var parts []string
	if ag := stripNegatives(strings.TrimSpace(strings.TrimSpace(l.Age) + " " + strings.TrimSpace(l.Gender))); ag != "" {
		parts = append(parts, ag)
	}
	add := func(prefix, raw string) {
		v := strings.TrimRight(stripNegatives(strings.TrimSpace(raw)), "。；;，,")
		if v == "" {
			return
		}
		parts = append(parts, prefix+v)
	}
	add("发型：", l.Hair)
	add("五官：", l.Face)
	add("身形：", l.Body)
	if o := stripNegatives(strings.TrimSpace(l.Outfit)); o != "" {
		o = strings.TrimPrefix(strings.TrimPrefix(strings.TrimPrefix(o, "常穿"), "穿着"), "穿")
		o = strings.TrimRight(strings.TrimSpace(o), "。；;，,")
		if o != "" {
			parts = append(parts, "穿"+o)
		}
	}
	if ac := stripNegatives(strings.TrimSpace(l.Accessories)); ac != "" {
		ac = strings.TrimPrefix(strings.TrimPrefix(strings.TrimPrefix(ac, "常戴"), "佩戴"), "戴")
		ac = strings.TrimRight(strings.TrimSpace(ac), "。；;，,")
		if ac != "" {
			parts = append(parts, "戴"+ac)
		}
	}
	add("主色调：", l.Palette)
	return joinClausesBounded(parts, "；", 420)
}

// biosParagraph 人物小传里写这个人的那一段：优先以名字开头的段落
func biosParagraph(bios, name string) string {
	if name == "" {
		return ""
	}
	paras := strings.Split(bios, "\n")
	for _, para := range paras {
		if strings.HasPrefix(strings.TrimLeft(strings.TrimSpace(para), "#*-·•【[ 　0123456789.、"), name) {
			return para
		}
	}
	i := strings.Index(bios, name)
	if i < 0 {
		return ""
	}
	rest := bios[i:]
	if r := []rune(rest); len(r) > 260 {
		rest = string(r[:260])
	}
	return rest
}

// appearanceSentences 按小句提取写外貌、发型、服装、配饰的正面短句
func appearanceSentences(text, name string) []string {
	cleaned := negParenRe.ReplaceAllString(text, "")
	var out []string
	for _, s := range lookSplitRe.Split(cleaned, -1) {
		s = strings.TrimSpace(s)
		if s == "" || negClauseRe.MatchString(s) || lookSkipRe.MatchString(s) || !lookWordRe.MatchString(s) {
			continue
		}
		s = strings.TrimPrefix(s, name)
		s = strings.TrimLeft(s, "，,：: ")
		if m := agePrefixRe.FindString(s); m != "" {
			s = strings.TrimPrefix(s, m)
		}
		s = strings.NewReplacer("常穿", "穿", "常挽", "挽", "常戴", "戴", "常披", "披").Replace(s)
		s = strings.TrimSpace(s)
		if s != "" && !negClauseRe.MatchString(s) {
			out = append(out, s)
		}
	}
	return out
}

// joinClausesBounded 按整句边界拼接，超限时丢弃尾部整句而不是拦腰截断半个词
func joinClausesBounded(parts []string, sep string, maxRunes int) string {
	var out []string
	used := 0
	sepLen := utf8.RuneCountInString(sep)
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		n := utf8.RuneCountInString(p)
		add := n
		if len(out) > 0 {
			add += sepLen
		}
		if len(out) > 0 && used+add > maxRunes {
			break
		}
		out = append(out, p)
		used += add
	}
	return strings.Join(out, sep)
}

// charLook 角色的固定造型：优先用结构化 look_json；否则从资产外貌描述和人物小传里按小句抽取，去重且按整句边界限长
func charLook(p *Project, a *Asset) string {
	if a == nil || a.Kind != "character" {
		return ""
	}
	if !a.Look.empty() {
		return a.Look.formatPositive()
	}
	var parts []string
	seen := map[string]bool{}
	norm := func(s string) string {
		k := strings.ReplaceAll(s, " ", "")
		if i := strings.Index(k, "："); i > 0 && utf8.RuneCountInString(k[:i]) <= 6 {
			k = k[i+len("："):]
		}
		return k
	}
	add := func(list []string) {
		for _, s := range list {
			k := norm(s)
			if k == "" || seen[k] {
				continue
			}
			dup := false
			for _, have := range parts {
				hk := norm(have)
				if strings.Contains(hk, k) {
					dup = true
					break
				}
			}
			if !dup {
				seen[k] = true
				parts = append(parts, s)
			}
		}
	}
	add(appearanceSentences(a.Description, a.Name))
	joined := strings.Join(parts, "")
	if p != nil && !(hasAny(joined, "发", "刘海", "偏分", "寸头") && hasAny(joined, "穿", "衣", "装", "衫", "裤", "裙", "夹克", "大衣", "毛衣")) {
		add(appearanceSentences(biosParagraph(p.Bios, a.Name), a.Name))
	}
	return joinClausesBounded(parts, "，", 420)
}

// lookLine 写进提示词的一句：「X」的固定造型：……（发型、服装必须照此）
func lookLine(p *Project, a *Asset) string {
	look := charLook(p, a)
	if look == "" {
		return ""
	}
	return fmt.Sprintf("「%s」的固定造型：%s（发型、服装、配饰必须严格照此，不要改变）", a.Name, look)
}

func hasAny(s string, keys ...string) bool {
	for _, k := range keys {
		if strings.Contains(s, k) {
			return true
		}
	}
	return false
}
