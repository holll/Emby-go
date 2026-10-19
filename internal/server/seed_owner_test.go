package server

import "testing"

// seedOwner 确保 id=1 管理员存在：旧夹具直接写入 token/API key，
// 认证现在要求账户行存在（fail-closed），测试也要按真实初始化顺序建账户。
func seedOwner(t *testing.T, a *App) {
	t.Helper()
	ok, err := a.db.HasAdministrator()
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		return
	}
	if err := a.db.InitializeAdministrator("admin", "fixture-pass-123"); err != nil {
		t.Fatal(err)
	}
}
