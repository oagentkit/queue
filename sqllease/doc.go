// Package sqllease — worker.LeaseStore 的 database/sql 参考实现（三方言）。
//
// PostgreSQL / MySQL / SQLite 共一套「判-占-续」逻辑，方言差异收在 Dialect
// 的三处 SQL 表达式里（占位符 / DB 时钟表达式 / 建表类型）。宿主传入 *sql.DB
// 即用；PostgreSQL 专用形态见 pglease（ON CONFLICT 单语句，worker 包已
// re-export）；有自定义需求（非 SQL 存储、批量多 key）再自实现——接口不变。
//
// # 纪律（每个自实现宿主都会踩的坑，本实现一次做对）
//
//   - 原子性：「判-占-续」由**单语句条件 UPDATE** 完成（WHERE key=? AND
//     (holder=? OR expires_at<DB now())），不退化成 SELECT-then-UPDATE——
//     查后写是跨实例竞窗，不是选主（worker.LeaseStore 正确性红线）；
//   - DB 时钟：expires 写入与过期判定同钟（NOW() / strftime('now')），
//     应用时钟漂移不再等量放大/缩短租约时长（worker.LeaseStore doc 的红线，
//     oagentkit/queue#26）；
//   - INSERT 冲突降噪：条件 UPDATE 无行 → INSERT；多副本竞态下他实例抢先
//     INSERT 是**预期路径**——冲突错误不外露（database/sql 无 ORM logger，
//     不产生下游 gorm 默认 logger 的 duplicate-key 红字刷屏），静默回退再试
//     一次条件 UPDATE；
//   - holder 唯一性：Release 仅 DELETE holder 本人的行（优雅停机让位，
//     非持有者 no-op）。
package sqllease
