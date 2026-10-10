package engine

import (
	"testing"

	"github.com/474420502/execute/basic"
)

// Engine 必须满足公共执行器契约; 由编译器在构建期强制检查.
var (
	_ basic.Executor[int]    = (*Engine[int])(nil)
	_ basic.Executor[string] = (*Engine[string])(nil)
)

// TestEngineSatisfiesContract 兜底确认断言在测试期也被编译.
func TestEngineSatisfiesContract(t *testing.T) {
	var e *Engine[int]
	var _ basic.Executor[int] = e
}
