package design

import (
	"fmt"
	"strings"
	"testing"
)

// archJSON 拼一个只含指定槽位的原型，减少负向用例的样板。
func archJSON(id string, slots ...string) string {
	return fmt.Sprintf(`{"version":1,"archetypes":[{"id":%q,"purpose":"测试用","slots":[%s]}]}`, id, strings.Join(slots, ","))
}

func slot(role string, col, colSpan, row, rowSpan int) string {
	return fmt.Sprintf(`{"role":%q,"col":%d,"colSpan":%d,"row":%d,"rowSpan":%d}`, role, col, colSpan, row, rowSpan)
}

func TestBuiltinArchetypesParse(t *testing.T) {
	list, err := Archetypes()
	if err != nil {
		t.Fatalf("内置版式原型解析失败: %v", err)
	}
	if len(list) < MinArchetypes {
		t.Fatalf("版式原型 %d 个，少于要求的 %d 个", len(list), MinArchetypes)
	}
	want := []string{"big-number", "bullets", "chart-focus", "closing", "compare", "cover", "full-bleed", "quote", "section", "split"}
	got := IDs(list)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("原型 id 集合不符:\n got %v\nwant %v", got, want)
	}
}

func TestBuiltinArchetypesFitCanvas(t *testing.T) {
	th := Default()
	list, err := Archetypes()
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	for _, a := range list {
		rects, err := a.Rects(th)
		if err != nil {
			t.Fatalf("%s 换算失败: %v", a.ID, err)
		}
		if len(rects) != len(a.Slots) {
			t.Errorf("%s 槽位数 %d ≠ 换算结果 %d", a.ID, len(a.Slots), len(rects))
		}
		for _, sr := range rects {
			if sr.Rect.X < 0 || sr.Rect.Y < 0 ||
				sr.Rect.X+sr.Rect.Width > th.Canvas.Width ||
				sr.Rect.Y+sr.Rect.Height > th.Canvas.Height {
				t.Errorf("%s 的槽位 %s 落在画布外: %+v", a.ID, sr.Slot.Role, sr.Rect)
			}
			if sr.Slot.Align == "" || sr.Slot.VAlign == "" {
				t.Errorf("%s 的槽位 %s 缺少对齐默认值", a.ID, sr.Slot.Role)
			}
		}
	}
}

func TestCompareHasTwoBodySlots(t *testing.T) {
	a, ok := ArchetypeByID("compare")
	if !ok {
		t.Fatal("缺少 compare 原型")
	}
	if n := len(a.SlotsByRole(RoleSlotBody)); n != 2 {
		t.Errorf("compare 的 body 槽位 = %d，期望 2", n)
	}
	if _, ok := ArchetypeByID("no-such"); ok {
		t.Error("不存在的 id 不应命中")
	}
}

func TestArchetypeRejectsOutOfBounds(t *testing.T) {
	data := archJSON("bad-bounds", slot("title", 11, 3, 1, 1))
	_, err := ParseArchetypes([]byte(data), Default())
	if err == nil {
		t.Fatal("col=11 span=3 应被拒绝")
	}
	if !strings.Contains(err.Error(), "右边界 13") {
		t.Errorf("错误应给出右边界 13，实际: %v", err)
	}
}

func TestArchetypeRejectsOverlap(t *testing.T) {
	data := archJSON("bad-overlap", slot("title", 1, 6, 3, 2), slot("body", 1, 4, 4, 1))
	_, err := ParseArchetypes([]byte(data), Default())
	if err == nil {
		t.Fatal("槽位重叠应被拒绝")
	}
	if !strings.Contains(err.Error(), "重叠") {
		t.Errorf("错误应说明重叠，实际: %v", err)
	}
}

func TestArchetypeRejectsMissingPrimary(t *testing.T) {
	data := archJSON("bad-primary", slot("body", 1, 12, 1, 2))
	_, err := ParseArchetypes([]byte(data), Default())
	if err == nil {
		t.Fatal("缺少主陈述槽位应被拒绝")
	}
	if !strings.Contains(err.Error(), "主陈述") {
		t.Errorf("错误应说明缺少主陈述，实际: %v", err)
	}
}

func TestArchetypeRejectsUnknownRole(t *testing.T) {
	data := archJSON("bad-role", slot("title", 1, 12, 1, 1), slot("footerish", 1, 6, 8, 1))
	_, err := ParseArchetypes([]byte(data), Default())
	if err == nil {
		t.Fatal("未知角色应被拒绝")
	}
	if !strings.Contains(err.Error(), "footerish") {
		t.Errorf("错误应指出未知角色，实际: %v", err)
	}
}

func TestArchetypeRejectsBadIDAndPurpose(t *testing.T) {
	cases := map[string]string{
		"大写 id":     archJSON("Bad-ID", slot("title", 1, 12, 1, 1)),
		"空 purpose": `{"version":1,"archetypes":[{"id":"x","purpose":"","slots":[` + slot("title", 1, 12, 1, 1) + `]}]}`,
	}
	for name, data := range cases {
		if _, err := ParseArchetypes([]byte(data), Default()); err == nil {
			t.Errorf("%s 应被拒绝", name)
		}
	}
}

func TestArchetypeRejectsUnknownField(t *testing.T) {
	data := `{"version":1,"archetypes":[{"id":"x","purpose":"y","oops":1,"slots":[` + slot("title", 1, 12, 1, 1) + `]}]}`
	if _, err := ParseArchetypes([]byte(data), Default()); err == nil {
		t.Fatal("未知字段应被拒绝（防止拼错字段名被静默忽略）")
	}
}

func TestArchetypeRequiresMinimumCount(t *testing.T) {
	one := slot("title", 1, 12, 1, 1)
	data := fmt.Sprintf(`{"version":1,"archetypes":[
		{"id":"a","purpose":"p","slots":[%s]},
		{"id":"b","purpose":"p","slots":[%s]}]}`, one, one)
	_, err := ParseArchetypes([]byte(data), Default())
	if err == nil {
		t.Fatal("少于 10 个应被拒绝")
	}
	if !strings.Contains(err.Error(), "至少 10") {
		t.Errorf("错误应给出数量下限，实际: %v", err)
	}
}
