package main

// 角色一致性：所有带参考图的出图（封面、关键帧、定妆图）都优先拿角色的定妆三视图作参考，
// 并把角色的固定造型（发型、服装、配饰）从资产外貌描述和人物小传里抽出来，明确写进提示词。

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

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
	lookSplitRe = regexp.MustCompile(`[。！？；;\n]+`)
	lookWordRe  = regexp.MustCompile(`发|髻|辫|刘海|鬓|簪|穿|衣|袍|衫|裙|裤|鞋|靴|帽|戴|披|外罩|制服|警服|旗袍|长衫|大衣|风衣|手套|围巾|妆|唇|眉|眼|疤|痣|肤|身形|身材|高挑|清瘦|微胖|魁梧|瘦削|包|配饰|首饰|耳环|眼镜`)
	lookSkipRe  = regexp.MustCompile(`性格|动机|说话|口头禅|关系|与.{1,8}(?:有|从|是|相)|视.{1,6}如|目标|秘密`)
)

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
	// 整段不分行：从名字出现处截到下一个「名字，xx岁」式开头前
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

// appearanceSentences 只留写外貌、发型、服装的短句
func appearanceSentences(text, name string) []string {
	var out []string
	for _, s := range lookSplitRe.Split(text, -1) {
		s = strings.TrimSpace(s)
		if s == "" || lookSkipRe.MatchString(s) || !lookWordRe.MatchString(s) {
			continue
		}
		// 去掉开头的「名字，18岁，」
		s = strings.TrimPrefix(s, name)
		s = strings.TrimLeft(s, "，,：: ")
		if m := regexp.MustCompile(`^[0-9一二三四五六七八九十余多]+岁[，,]?`).FindString(s); m != "" {
			s = strings.TrimPrefix(s, m)
		}
		// 「常穿」「常挽成」写成固定造型，免得模型当成可换
		s = strings.NewReplacer("常穿", "穿", "常挽", "挽", "常戴", "戴", "常披", "披").Replace(s)
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

// charLook 角色的固定造型：资产外貌描述 + 人物小传里的外貌句，去重，最多约 160 字
func charLook(p *Project, a *Asset) string {
	if a == nil || a.Kind != "character" {
		return ""
	}
	var parts []string
	seen := map[string]bool{}
	add := func(list []string) {
		for _, s := range list {
			k := strings.ReplaceAll(s, " ", "")
			dup := seen[k]
			for _, have := range parts {
				if strings.Contains(have, k) {
					dup = true
				}
			}
			// 「外貌：A，B」这类带标签的句子：每个小句都已写过就不再重复
			if !dup {
				body := k
				if i := strings.Index(body, "："); i > 0 && utf8.RuneCountInString(body[:i]) <= 6 {
					body = body[i+len("："):]
				}
				all := strings.Join(parts, "，")
				var rest []string
				for _, c := range strings.Split(body, "，") {
					if c != "" && !strings.Contains(all, c) {
						rest = append(rest, c)
					}
				}
				if len(rest) == 0 && len(parts) > 0 {
					dup = true
				}
			}
			if !dup {
				seen[k] = true
				parts = append(parts, s)
			}
		}
	}
	add(appearanceSentences(a.Description, a.Name))
	// 资产描述是定稿：写了造型就不再掺人物小传（小传里多半是旧造型，会把换过的服装拉回去）
	if p != nil && !(hasAny(strings.Join(parts, ""), "发") && hasAny(strings.Join(parts, ""), "穿", "衣", "装", "衫", "裤")) {
		add(appearanceSentences(biosParagraph(p.Bios, a.Name), a.Name))
	}
	look := strings.Join(parts, "，")
	if r := []rune(look); len(r) > 320 {
		look = string(r[:320])
	}
	return look
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
