module github.com/474420502/execute

// 依赖 Go 1.24+ 的原因:
//   - 泛型类型别名(generic type alias)自 Go 1.24 起正式可用;
//   - 标准库 testing/synctest 自 Go 1.25 起稳定, 用于虚拟时钟的确定性测试;
//   - 已在本机 go1.27.2 上通过完整测试.
go 1.25.0
