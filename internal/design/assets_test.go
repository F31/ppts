package design

import (
	"strings"
	"testing"
)

const validAssets = `{"version":1,"assets":[
 {"id":"icon-arrow","kind":"icon","version":1,"license":"self","file":"assets/arrow.svg"},
 {"id":"ill-harness","kind":"illustration","version":2,"license":"CC-BY","attribution":"Example Studio","file":"assets/harness.svg"}
]}`

func TestParseAssetsValid(t *testing.T) {
	list, err := ParseAssets([]byte(validAssets))
	if err != nil {
		t.Fatalf("合法清单解析失败: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("资产数 = %d，期望 2", len(list))
	}
	if list[1].Attribution == "" {
		t.Error("CC-BY 资产应保留署名")
	}
}

func TestParseAssetsRejects(t *testing.T) {
	cases := map[string]struct {
		data string
		want string
	}{
		"缺 license":  {`{"version":1,"assets":[{"id":"a","kind":"icon","version":1,"file":"a.svg"}]}`, "缺少 license"},
		"未知 license": {`{"version":1,"assets":[{"id":"a","kind":"icon","version":1,"license":"WTFPL","file":"a.svg"}]}`, "不在允许集合"},
		"CC-BY 缺署名":  {`{"version":1,"assets":[{"id":"a","kind":"icon","version":1,"license":"CC-BY","file":"a.svg"}]}`, "attribution"},
		"重复 id": {`{"version":1,"assets":[
			{"id":"a","kind":"icon","version":1,"license":"self","file":"a.svg"},
			{"id":"a","kind":"icon","version":1,"license":"self","file":"b.svg"}]}`, "重复"},
		"未知种类":   {`{"version":1,"assets":[{"id":"a","kind":"video","version":1,"license":"self","file":"a.svg"}]}`, "种类"},
		"扩展名不支持": {`{"version":1,"assets":[{"id":"a","kind":"icon","version":1,"license":"self","file":"a.gif"}]}`, "扩展名"},
		"版本非正":   {`{"version":1,"assets":[{"id":"a","kind":"icon","version":0,"license":"self","file":"a.svg"}]}`, "version"},
		"清单版本错":  {`{"version":2,"assets":[]}`, "版本必须为 1"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := ParseAssets([]byte(c.data))
			if err == nil {
				t.Fatalf("应被拒绝")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("错误 %q 未包含 %q", err.Error(), c.want)
			}
		})
	}
}
