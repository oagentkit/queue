package worker

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

// 构造期 fail-fast（oagentkit/queue#71）：nil LeaseStore 此前被 NewLeaderElector
// 静默接受——首个 tick 在后台竞选 goroutine 里对 nil 接口调方法，进程直接被炸
// （oagentkit/queue#27 同型失败几何：启动后 panic，调用方毫无拦截机会）。nil store
// 与非正 ttl 是同一类装配期语义破坏，必须在构造期拦截，且先于其他参数闸门。
func TestNewLeaderElectorRejectsNilStore(t *testing.T) {
	func() {
		defer func() {
			r := recover()
			if r == nil {
				t.Fatal("nil LeaseStore 应构造期 panic（此前静默接受，panic 推迟到后台竞选 goroutine）")
			}
			if !strings.Contains(fmt.Sprint(r), "store") {
				t.Fatalf("panic 应指向 store 为 nil: %v", r)
			}
		}()
		_ = NewLeaderElector(nil, "k", "h", time.Second, 100*time.Millisecond)
	}()

	// 对照：合法 store 不被 nil 闸门误伤（构造成功即可参选）。
	e := NewLeaderElector(newMemLeaseStore(), "k", "h", time.Second, 100*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	e.Start(ctx)
	e.Stop()
	cancel()
}
