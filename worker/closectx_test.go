package worker

import (
	"context"
	"testing"
	"time"
)

// stopCtx 把旧 Close(grace) 的排空时限编码为带 deadline 的 ctx（Close 契约的
// 测试垫片）：deadline 到期即触发硬取消路径。
func stopCtx(t *testing.T, d time.Duration) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), d)
	t.Cleanup(cancel)
	return ctx
}
