package server

// BuildVersion 由 main 在启动时注入：构建期 -ldflags 写入 main.version，
// main 再转发到此处，供管理页外壳渲染真实版本号。默认值对应本地开发构建。
var BuildVersion = "dev"

// repoURL 项目仓库地址，管理页左下角展示。
const repoURL = "https://github.com/holll/Emby-go"
