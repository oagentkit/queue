package worker

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// 组件名门禁（oagentkit/queue#121）：AGENTS.md「日志」条与 org权威数据源
// matrix-tools/modules.json（queue 条目 loggingRule）给的都是**字面值**
// `oagentkit-queue`，不是 `oagentkit-<module>` 通配——#59 按通配把
// agentkit-leader/-worker 改成 oagentkit-leader/-worker，方向与双契约字面不符，
// CHANGELOG 却记成「已对齐」。按字面名过滤/路由的下游因此查不到本组件日志。
//
// 本测试扫描本包源码里全部 logx.Delegating 实参：出现第二个组件名即红，
// 细分（leader/worker）只允许走事件名与结构化字段。
var delegatingArgRe = regexp.MustCompile(`logx\.Delegating\("([^"]*)"\)`)

func TestDelegatingComponentMatchesContract(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("worker 包无 go 文件——门禁空转")
	}
	seen := 0
	for _, f := range files {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range delegatingArgRe.FindAllStringSubmatch(string(src), -1) {
			seen++
			if m[1] != "oagentkit-queue" {
				t.Errorf("%s: logx.Delegating(%q) 与契约字面值 oagentkit-queue 不符（AGENTS.md 日志条 / modules.json loggingRule，queue#121）", f, m[1])
			}
		}
	}
	if seen == 0 {
		t.Fatal("未扫到任何 logx.Delegating——正则或文件布局漂移，门禁失效")
	}
}
