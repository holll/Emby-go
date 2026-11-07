package stabilityrehearsal

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Static delivery contracts, not evidence of installation, systemd sandbox
// execution, media mount permissions, or an upgrade/rollback support matrix.
func TestNativeDeploymentServiceSample(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "deploy", "emby-go.service"))
	if err != nil {
		t.Fatal(err)
	}
	unit := "\n" + string(data) + "\n"
	for _, field := range []string{
		"User=root", "Group=root",
		"WorkingDirectory=/opt/emby-go",
		"Environment=PATH=/usr/local/bin:/usr/bin:/bin",
		"UMask=0077",
		"ExecStart=/opt/emby-go/emby-go -c /etc/emby-go/config.yaml",
		"Restart=always", "RestartSec=3s", "TimeoutStopSec=30s", "KillMode=control-group",
		"NoNewPrivileges=true", "ProtectSystem=strict", "ProtectHome=true", "PrivateTmp=true",
		"ReadWritePaths=/opt/emby-go /etc/emby-go",
	} {
		if !strings.Contains(unit, "\n"+field+"\n") {
			t.Errorf("missing active unit field %q", field)
		}
	}
}

func TestNativeDeploymentReadmeAndAssets(t *testing.T) {
	root := filepath.Join("..", "..")
	for _, name := range []string{"Dockerfile", "docker-compose.yml"} {
		if _, err := os.Stat(filepath.Join(root, "deploy", name)); !os.IsNotExist(err) {
			t.Errorf("retired deployment sample %s still exists or cannot be checked: %v", name, err)
		}
	}
	data, err := os.ReadFile(filepath.Join(root, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	readme := string(data)
	for _, retired := range []string{"docker compose", "docker build", "deploy/Dockerfile", "deploy/docker-compose.yml", "Docker 镜像已内置"} {
		if strings.Contains(readme, retired) {
			t.Errorf("README still recommends retired entry %q", retired)
		}
	}
	for _, required := range []string{
		"不再支持 Docker", "deploy/emby-go.service", "deploy/install.sh", "/opt/emby-go/emby-go",
		"媒体库权限必须显式配置", "ReadWritePaths=/srv/media", "不会授予 Unix/NAS 写权限",
		"ffprobe -version", "ffprobe_path", "不会继承交互式 shell 的自定义 PATH",
		"不代表已通过真实 systemd", "SHA256SUMS", "旧 Release 不保证提供",
		"API-CONTRACT.md#410-ffprobe-执行边界n-01-本地已验收子项",
	} {
		if !strings.Contains(readme, required) {
			t.Errorf("README missing deployment constraint %q", required)
		}
	}
}
