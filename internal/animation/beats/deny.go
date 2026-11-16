package beats

import (
	"regexp"
	"strings"

	"github.com/F31/ppts/internal/animation/bind"
)

// 与 bind 内部 pageW/pageH、app.baseBindKind 同一套 1080p 画布。
const (
	denyPageW = 1920.0
	denyPageH = 1080.0
)

// 与 bind.markerRe / app.bindMarkerRe 同一谓词：最多两位数字，可带前导 0。
var pageNumberTextRe = regexp.MustCompile(`^0?\p{Nd}{1,2}$`)

// pageNumberLook 是页脚页码：Kind=page_number，或短数字且盒原点在右下角。
// 原点而不是中心，与 baseBindKind 一致。
func pageNumberLook(e bind.Element) bool {
	if e.Kind == "page_number" {
		return true
	}
	if !pageNumberTextRe.MatchString(strings.TrimSpace(e.Text)) {
		return false
	}
	return e.Box.X > 0.85*denyPageW && e.Box.Y > 0.85*denyPageH
}

func trimText(s string) string {
	return strings.TrimSpace(s)
}

func collectCandidates(in Input) (usable []bind.Element, deny map[string]struct{}, reasons map[string]int) {
	deny = make(map[string]struct{})
	reasons = make(map[string]int)
	for _, e := range in.Elements {
		if pageNumberLook(e) {
			deny[e.ID] = struct{}{}
			reasons["page_number"]++
			continue
		}
		if bind.IsAux(e) {
			deny[e.ID] = struct{}{}
			reasons["aux"]++
			continue
		}
		if in.CompileIDs != nil {
			if _, ok := in.CompileIDs[e.ID]; !ok {
				continue
			}
		}
		usable = append(usable, e)
	}
	return usable, deny, reasons
}
