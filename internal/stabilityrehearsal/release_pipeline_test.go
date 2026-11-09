package stabilityrehearsal

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 发布流水线的静态契约。真实故障：旧 workflow 只上传二进制，没有 SHA256SUMS，
// 导致 install.sh 与页面升级在“最新 Release”上必然失败。这里钉住二进制+
// SHA256SUMS 同批、校验和生成与 install.sh 的失败关闭。deploy/* 与
// config.example.yaml 留在仓库中，不再随 Release 发布。
// 它不证明任何一次真实构建、上传或安装成功。
func TestReleasePipelinePublishesDeployables(t *testing.T) {
	root := filepath.Join("..", "..")
	workflow := readText(t, filepath.Join(root, ".github", "workflows", "release.yml"))
	install := readText(t, filepath.Join(root, "deploy", "install.sh"))

	for _, required := range []string{
		"- 'v*'",
		"go build",
		"-X main.version=${version}",
		"-X main.commit=${commit}",
		"-X main.buildTime=${build_time}",
		"./dist/emby-go-linux-amd64 -version",
		"sha256sum emby-go-* > SHA256SUMS",
		"sha256sum -c SHA256SUMS",
		"files: dist/*",
	} {
		if !strings.Contains(workflow, required) {
			t.Errorf("release workflow missing %q", required)
		}
	}
	if strings.Contains(workflow, "cp deploy/install.sh") || strings.Contains(workflow, "config.example.yaml dist/") {
		t.Error("release workflow must not package deploy scripts or config sample")
	}

	// 每个 curl 都必须限制协议与重定向，不能有一条落回明文。
	if curls, guarded := strings.Count(install, "curl -fsSL"), strings.Count(install, "--proto-redir '=https'"); curls != guarded {
		t.Errorf("install.sh has %d curl calls but %d protocol-redirect guards", curls, guarded)
	}
	// 没有 SHA256SUMS 时必须停止，不能退化为无校验安装。
	for _, required := range []string{
		"未提供 SHA256SUMS",
		"grep -E \"  $ASSET\\$\" SHA256SUMS | sha256sum -c -",
		`die "SHA256SUMS 校验失败，未安装任何文件"`,
		"/opt/emby-go/emby-go",
	} {
		if !strings.Contains(install, required) {
			t.Errorf("install.sh missing %q", required)
		}
	}
	if strings.Contains(install, "sha256sum -c --ignore-missing") || strings.Contains(install, "curl -k") {
		t.Error("install.sh must not weaken checksum or TLS verification")
	}
}

func readText(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
