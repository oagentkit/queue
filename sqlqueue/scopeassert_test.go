package sqlqueue_test

import (
	"github.com/oagentkit/queue/sqlqueue"
	"github.com/oagentkit/queue/worker"
)

// sqlqueue.Store 必须实现 worker.ScopedHeartbeater——Pool 的心跳按在飞任务集
// 续期靠这条断言在编译期兜住（接口漂移会在这里红，而不是在生产里退化成盲续，
// 那时症状是「panic 任务永久卡 running」，静默）。
//
// 同理 worker.DetailedClaimer（queue#70）：attempts 的领取侧透出靠它，接口
// 漂移在这里编译期红，而不是宿主的毒任务闸门静默失明。
//
// 放外部测试包：包内测试引入 worker 会与 worker 的测试依赖成环（先例见
// pglease_test.go 的同款注释）。
var (
	_ worker.ScopedHeartbeater = (*sqlqueue.Store)(nil)
	_ worker.DetailedClaimer   = (*sqlqueue.Store)(nil)
)
