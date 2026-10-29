package server

import "testing"

// seedOwner 为非登录契约测试预置真实账户：请求仍走完整身份和凭证校验。
// 初始化/登录 API 与生产 bcrypt 成本由专门测试覆盖，不在每个夹具里重复执行。
func seedOwner(t *testing.T, a *App) {
	t.Helper()
	ok, err := a.db.HasAdministrator()
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		return
	}
	seedFixtureUser(t, a, "admin", "fixture-pass-123", true)
}
