// lease_ref.go pglease 参考实现的 re-export（oagentkit/queue#26）：
// 「实现 LeaseStore 接口」此前是默认路径——下游按接口自写、按 pglease 旧形态
// 复制，会静默丢掉 DB 时钟语义修复（expires 写入改用应用 time.Now()，副本间
// 时钟漂移等量放大/缩短租约时长，双头/换主延迟且无报错）。把参考实现提到
// worker 包名下，「直接用」成为最短路径；自实现退化为有自定义需求时的选项。

package worker

import (
	"database/sql"

	"github.com/oagentkit/queue/pglease"
)

// PGLeaseStore worker.LeaseStore 的 PostgreSQL 参考实现（pglease.PGLeaseStore
// 的类型别名，同一个类型）。DB 时钟语义（expires 写入与过期判定同钟）已在
// 实现内做对；用 WithTable 换表名。
type PGLeaseStore = pglease.PGLeaseStore

// PGLeaseDefaultTable pglease 缺省租约表名（re-export）。
const PGLeaseDefaultTable = pglease.DefaultTable

// NewPGLeaseStore 构造 PostgreSQL 租约存储（表名用 PGLeaseDefaultTable；
// WithTable 覆盖）。等价 pglease.NewPGLeaseStore。
func NewPGLeaseStore(db *sql.DB) *PGLeaseStore {
	return pglease.NewPGLeaseStore(db)
}
