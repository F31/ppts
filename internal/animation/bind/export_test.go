package bind

import "testing"

func TestScoreMatchesUnexportedSimilarity(t *testing.T) {
	doc := loadFixture(t)
	n := 0
	for _, page := range doc.Pages {
		for _, e := range page.Elements {
			for _, s := range page.Segments {
				want, _ := similarity(s.Text, e.Text, e.Kind)
				got := Score(s.Text, e.Text, e.Kind)
				if got != want {
					t.Fatalf("Score(%q, %q, %s) = %v, similarity = %v", s.Text, e.Text, e.Kind, got, want)
				}
				n++
			}
		}
	}
	if n < 20 {
		t.Fatalf("金标对数太少: %d", n)
	}
}

func TestIsAuxMatchesUnexported(t *testing.T) {
	cases := []Element{
		{ID: "pn", Kind: "page_number", Text: "2"},
		{ID: "ft", Kind: "footer", Text: "内部资料"},
		{ID: "nb", Kind: "note", Text: "图：示意"},
		{ID: "src", Kind: "bullet", Text: "资料来源：公开数据"},
		{ID: "title", Kind: "title", Text: "智能办公"},
		{ID: "card", Kind: "bullet", Text: "智慧城市"},
	}
	for _, e := range cases {
		if got, want := IsAux(e), isAux(e); got != want {
			t.Fatalf("IsAux(%s) = %v, isAux = %v", e.ID, got, want)
		}
	}
	if !IsAux(cases[0]) {
		t.Fatal("page_number 必须是 aux")
	}
	if IsAux(cases[4]) {
		t.Fatal("标题不是 aux")
	}
}
