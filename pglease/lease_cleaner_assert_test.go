// 外部测试包（pglease_test）：worker re-export pglease（lease_ref.go），
// 包内测试引 worker 会成环。本文件钉住 pglease.PGLeaseStore 对
// worker.LeaseCleaner 可选接口的实现（queue#84——清理能力面与 sqllease
// 三方言实现对齐，两个参考实现都必须可被宿主的统一入口探测到）。
package pglease_test

import (
	"github.com/oagentkit/queue/pglease"
	"github.com/oagentkit/queue/worker"
)

var (
	_ worker.LeaseStore   = (*pglease.PGLeaseStore)(nil)
	_ worker.LeaseCleaner = (*pglease.PGLeaseStore)(nil)
)
