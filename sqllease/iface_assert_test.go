package sqllease_test

import (
	"github.com/oagentkit/queue/sqllease"
	"github.com/oagentkit/queue/worker"
)

// 编译期钉住能力面（queue#84）：sqllease.Store 必须实现 worker.LeaseCleaner
// （可选接口，类型断言探测——往 LeaseStore 里加方法会编译期打断所有既有实现，
// 与 TokenLeaseStore 同一道纪律）。接口漂移在这里红，而不是宿主的周期清理
// 静默失效。
//
// 放外部测试包：worker re-export pglease（lease_ref.go），包内测试引 worker
// 会成环（先例见 pglease_test 各文件与 sqlqueue/scopeassert_test.go）。
var (
	_ worker.LeaseStore   = (*sqllease.Store)(nil)
	_ worker.LeaseCleaner = (*sqllease.Store)(nil)
)
