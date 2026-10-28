package design

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// MinArchetypes 是基线要求的最少版式原型数（设计方案 §19.2 G2：10+ 版式原型）。
const MinArchetypes = 10

// 槽位角色白名单。新增角色必须同时改这里与渲染器，避免"数据里写了、渲染器不认识"。
const (
	RoleSlotTitle       = "title"
	RoleSlotSubtitle    = "subtitle"
	RoleSlotBody        = "body"
	RoleSlotBullets     = "bullets"
	RoleSlotVisual      = "visual"
	RoleSlotChart       = "chart"
	RoleSlotNumber      = "number"
	RoleSlotLabel       = "label"
	RoleSlotQuote       = "quote"
	RoleSlotAttribution = "attribution"
	RoleSlotMeta        = "meta"
	RoleSlotCaption     = "caption"
	RoleSlotDivider     = "divider"
	RoleSlotAccent      = "accent"
)

var slotRoles = map[string]bool{
	RoleSlotTitle: true, RoleSlotSubtitle: true, RoleSlotBody: true, RoleSlotBullets: true,
	RoleSlotVisual: true, RoleSlotChart: true, RoleSlotNumber: true, RoleSlotLabel: true,
	RoleSlotQuote: true, RoleSlotAttribution: true, RoleSlotMeta: true, RoleSlotCaption: true,
	RoleSlotDivider: true, RoleSlotAccent: true,
}

// textRoles 是可承载文本的角色，用于"每个原型至少有一个文本槽位"的校验。
var textRoles = map[string]bool{
	RoleSlotTitle: true, RoleSlotSubtitle: true, RoleSlotBody: true, RoleSlotBullets: true,
	RoleSlotNumber: true, RoleSlotLabel: true, RoleSlotQuote: true, RoleSlotAttribution: true,
	RoleSlotMeta: true, RoleSlotCaption: true,
}

// primaryRoles 是"主陈述"角色：一个版式必须能用一句话说清它要讲什么。
var primaryRoles = map[string]bool{
	RoleSlotTitle: true, RoleSlotQuote: true, RoleSlotNumber: true,
}

// Slot 是版式中的一个内容槽位，用 1 基行列与跨度声明。
type Slot struct {
	Role    string `json:"role"`
	Col     int    `json:"col"`
	ColSpan int    `json:"colSpan"`
	Row     int    `json:"row"`
	RowSpan int    `json:"rowSpan"`
	Align   string `json:"align,omitempty"`  // start|center|end
	VAlign  string `json:"vAlign,omitempty"` // top|center|bottom|stretch
}

func (s Slot) cells() (int, int, int, int) {
	return s.Row, s.Row + s.RowSpan - 1, s.Col, s.Col + s.ColSpan - 1
}

// Archetype 是一个版式原型。
type Archetype struct {
	ID      string `json:"id"`
	Purpose string `json:"purpose"`
	Slots   []Slot `json:"slots"`
}

// SlotRect 是槽位的像素投影，供预览页与渲染器共用同一套换算。
type SlotRect struct {
	Slot Slot
	Rect Rect
}

type archetypesFile struct {
	Version    int         `json:"version"`
	Archetypes []Archetype `json:"archetypes"`
}

//go:embed archetypes.json
var archetypesJSON []byte

var (
	archetypeOnce  sync.Once
	archetypeCache []Archetype
	archetypeErr   error
)

// Archetypes 返回内置版式原型（深拷贝，调用方可安全修改）。
func Archetypes() ([]Archetype, error) {
	archetypeOnce.Do(func() {
		archetypeCache, archetypeErr = ParseArchetypes(archetypesJSON, Default())
	})
	if archetypeErr != nil {
		return nil, archetypeErr
	}
	return cloneArchetypes(archetypeCache), nil
}

// ArchetypeByID 按 id 取原型。
func ArchetypeByID(id string) (Archetype, bool) {
	list, err := Archetypes()
	if err != nil {
		return Archetype{}, false
	}
	for _, a := range list {
		if a.ID == id {
			return a, true
		}
	}
	return Archetype{}, false
}

func cloneArchetypes(in []Archetype) []Archetype {
	out := make([]Archetype, len(in))
	for i, a := range in {
		out[i] = a
		out[i].Slots = append([]Slot(nil), a.Slots...)
	}
	return out
}

// ParseArchetypes 解析并自检版式原型。任何一项不合法都返回错误，不做"尽力而为"。
func ParseArchetypes(data []byte, t Theme) ([]Archetype, error) {
	var f archetypesFile
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("design: 版式原型 JSON 解析失败: %w", err)
	}
	if f.Version != 1 {
		return nil, fmt.Errorf("design: 版式原型版本必须为 1，实际 %d", f.Version)
	}
	// 先逐项校验，再查数量：单项有错时给出的定位信息远比"数量不足"有用。
	seen := map[string]bool{}
	for i := range f.Archetypes {
		a := &f.Archetypes[i]
		if err := validateArchetype(a, t); err != nil {
			return nil, err
		}
		if seen[a.ID] {
			return nil, fmt.Errorf("design: 版式原型 id 重复: %s", a.ID)
		}
		seen[a.ID] = true
	}
	if len(f.Archetypes) < MinArchetypes {
		return nil, fmt.Errorf("design: 版式原型至少 %d 个，实际 %d", MinArchetypes, len(f.Archetypes))
	}
	return f.Archetypes, nil
}

func validateArchetype(a *Archetype, t Theme) error {
	if strings.TrimSpace(a.ID) == "" {
		return fmt.Errorf("design: 版式原型缺少 id")
	}
	if strings.ToLower(a.ID) != a.ID || strings.ContainsAny(a.ID, " \t") {
		return fmt.Errorf("design: 版式原型 id 必须是小写无空格的短横线命名: %q", a.ID)
	}
	if strings.TrimSpace(a.Purpose) == "" {
		return fmt.Errorf("design: 版式原型 %s 缺少 purpose（何时使用）", a.ID)
	}
	if len(a.Slots) == 0 {
		return fmt.Errorf("design: 版式原型 %s 没有槽位", a.ID)
	}

	occupied := map[[2]int]int{} // (row,col) → 槽位下标
	hasText, hasPrimary := false, false
	for i := range a.Slots {
		s := &a.Slots[i]
		if !slotRoles[s.Role] {
			return fmt.Errorf("design: 版式 %s 槽位 %d 的角色 %q 不在允许集合内", a.ID, i, s.Role)
		}
		if s.Align == "" {
			s.Align = "start"
		}
		if s.VAlign == "" {
			s.VAlign = "top"
		}
		switch s.Align {
		case "start", "center", "end":
		default:
			return fmt.Errorf("design: 版式 %s 槽位 %d 的 align %q 非法（start|center|end）", a.ID, i, s.Align)
		}
		switch s.VAlign {
		case "top", "center", "bottom", "stretch":
		default:
			return fmt.Errorf("design: 版式 %s 槽位 %d 的 vAlign %q 非法（top|center|bottom|stretch）", a.ID, i, s.VAlign)
		}
		// 越界由 CellRect 统一判定：它与渲染器用同一段换算，不会出现"校验通过但画出去"。
		if _, err := t.Grid.CellRect(t.Canvas, s.Col, s.ColSpan, s.Row, s.RowSpan); err != nil {
			return fmt.Errorf("design: 版式 %s 槽位 %d（%s）越界: %w", a.ID, i, s.Role, err)
		}
		r0, r1, c0, c1 := s.cells()
		for r := r0; r <= r1; r++ {
			for c := c0; c <= c1; c++ {
				if prev, ok := occupied[[2]int{r, c}]; ok {
					return fmt.Errorf("design: 版式 %s 的槽位 %d（%s）与槽位 %d（%s）重叠于第 %d 行第 %d 列",
						a.ID, prev, a.Slots[prev].Role, i, s.Role, r, c)
				}
				occupied[[2]int{r, c}] = i
			}
		}
		if textRoles[s.Role] {
			hasText = true
		}
		if primaryRoles[s.Role] {
			hasPrimary = true
		}
	}
	if !hasText {
		return fmt.Errorf("design: 版式 %s 没有任何文本槽位", a.ID)
	}
	if !hasPrimary {
		return fmt.Errorf("design: 版式 %s 缺少主陈述槽位（title|quote|number）", a.ID)
	}
	return nil
}

// Rects 把槽位投影为像素矩形，顺序与 Slots 一致。
func (a Archetype) Rects(t Theme) ([]SlotRect, error) {
	out := make([]SlotRect, 0, len(a.Slots))
	for i, s := range a.Slots {
		r, err := t.Grid.CellRect(t.Canvas, s.Col, s.ColSpan, s.Row, s.RowSpan)
		if err != nil {
			return nil, fmt.Errorf("design: 版式 %s 槽位 %d 换算失败: %w", a.ID, i, err)
		}
		out = append(out, SlotRect{Slot: s, Rect: r})
	}
	return out, nil
}

// SlotsByRole 返回某角色的全部槽位（如 compare 的两栏 body）。
func (a Archetype) SlotsByRole(role string) []Slot {
	var out []Slot
	for _, s := range a.Slots {
		if s.Role == role {
			out = append(out, s)
		}
	}
	return out
}

// IDs 返回全部原型 id（稳定排序，供预览页与测试使用）。
func IDs(list []Archetype) []string {
	ids := make([]string, 0, len(list))
	for _, a := range list {
		ids = append(ids, a.ID)
	}
	sort.Strings(ids)
	return ids
}
