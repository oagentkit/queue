package worker

// 2026-11-11 issue 清零战回归测试（#103）：钉住 Stop→Start 重启竞态。

import (
	"context"
	"testing"
	"time"
)

// newSweepElector 测试用单副本选主器（短轮询，快选定快让位）。
func newSweepElector(t *testing.T) *LeaderElector {
	t.Helper()
	return NewLeaderElector(newMemLeaseStore(), "poller", "sweep-A",
		200*time.Millisecond, 5*time.Millisecond)
}

// awaitLeader 轮询等待当选（竞选是异步的，Start 返回 ≠ 已当选）。
func awaitLeader(e *LeaderElector, msg string) bool {
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && !e.IsLeader() {
		time.Sleep(5 * time.Millisecond)
	}
	return e.IsLeader()
}

// TestStopThenStartRestartsImmediately_Issue103 Stop 返回后立即 Start 必须成功
// 重启竞选——Stop 等满「done + quit」双信号，close(done) 与 running 清零之间的
// 窗口不再让 Start 命中 already_running 被静默吞掉。
func TestStopThenStartRestartsImmediately_Issue103(t *testing.T) {
	e := newSweepElector(t)
	ctx := context.Background()
	e.Start(ctx)
	if !awaitLeader(e, "单副本应当选") {
		t.Fatal("单副本应当选")
	}
	e.Stop()
	// Stop 返回后立即 Start（原竞态窗口：GOMAXPROCS>1 时可现 already_running）。
	e.Start(ctx)
	if !awaitLeader(e, "重启后必须重新参选") {
		t.Fatal("Stop 后立即 Start 的副本必须重新参选（queue#103）")
	}
	e.Stop()
}

// TestStopThenStartLoop_Issue103 高频 Stop→Start 循环下重启恒生效
// （放大原竞态窗口的触发面）。
func TestStopThenStartLoop_Issue103(t *testing.T) {
	e := newSweepElector(t)
	ctx := context.Background()
	for i := range 50 {
		e.Start(ctx)
		if !awaitLeader(e, "当选") {
			t.Fatalf("第 %d 轮应当选", i)
		}
		e.Stop()
		e.Start(ctx)
		if !awaitLeader(e, "重启参选") {
			t.Fatalf("第 %d 轮 Stop 后立即 Start 必须重新参选（queue#103）", i)
		}
		e.Stop()
	}
}
