// 外部测试包（pglease_test）：worker 现 re-export pglease（lease_ref.go），
// worker → pglease 的 import 若在包内测试里再引 worker 会成环——接口断言
// 放外部测试包，两侧互不可见地通过。
package pglease_test

import (
	"database/sql"
	"testing"

	"github.com/oagentkit/queue/pglease"
	"github.com/oagentkit/queue/worker"
)

// 编译期断言：满足 worker.LeaseStore 接口。
var _ worker.LeaseStore = (*pglease.PGLeaseStore)(nil)

func TestImplementsLeaseStore(t *testing.T) {
	var s worker.LeaseStore = pglease.NewPGLeaseStore(&sql.DB{})
	_ = s
}
