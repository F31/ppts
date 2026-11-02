package app

import "github.com/F31/ppts/internal/project"

// PageOriginalText 计算 project.Page 的「原文素材」：并排对照面板左侧那一段。
//
// 与成稿共用 PageSourceText，因此"对照看到的原文"就是"成稿当时读到的素材"
// （来源优先级：显式来源 > 形状文字 > 备注回退），不会出现两套口径。
// 返回 kind ∈ page/notes/custom；文本为空时调用方应把它展示为"本页没有可对照的原文"。
func PageOriginalText(pg *project.Page, source, custom string) (string, string) {
	if pg == nil {
		return "notes", ""
	}
	shapes := make([]SourceShapeText, 0, len(pg.Shapes))
	for _, sh := range pg.Shapes {
		if sh == nil {
			continue
		}
		shapes = append(shapes, SourceShapeText{Kind: sh.Kind, Text: sh.Text})
	}
	return PageSourceText(shapes, pg.NotesText, source, custom)
}
