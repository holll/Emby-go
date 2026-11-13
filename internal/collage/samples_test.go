package collage

import (
	"flag"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// 本文件只用于人工查看示例输出，不参与常规断言。
// 触发方式：COLLAGE_SAMPLES=1 go test -run TestSamples，或直接 go test -run TestSamples。

// digitGlyphs 是 5×7 的点阵数字，用来在示例海报上画序号。
var digitGlyphs = [10][7]string{
	{"01110", "10001", "10001", "10001", "10001", "10001", "01110"},
	{"00100", "01100", "00100", "00100", "00100", "00100", "01110"},
	{"01110", "10001", "00001", "00010", "00100", "01000", "11111"},
	{"11111", "00010", "00100", "00010", "00001", "10001", "01110"},
	{"00010", "00110", "01010", "10010", "11111", "00010", "00010"},
	{"11111", "10000", "11110", "00001", "00001", "10001", "01110"},
	{"00110", "01000", "10000", "11110", "10001", "10001", "01110"},
	{"11111", "00001", "00010", "00100", "01000", "01000", "01000"},
	{"01110", "10001", "10001", "01110", "10001", "10001", "01110"},
	{"01110", "10001", "10001", "01111", "00001", "00010", "01100"},
}

// numberedPoster 画一张 2:3 竖版示例海报：底色 + 白色序号 + 顶部与底部细条。
// 序号让每一列在拼贴里都能直接读出是第几张输入。
func numberedPoster(n int, base color.RGBA) *image.RGBA {
	const w, h, scale, gap = 300, 450, 20, 20
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetRGBA(x, y, base)
		}
	}

	white := color.RGBA{0xFF, 0xFF, 0xFF, 0xFF}
	// 顶部与底部细条：给裁剪位置一个可读的参照。
	for y := 0; y < 12; y++ {
		for x := 0; x < w; x++ {
			img.SetRGBA(x, y, white)
		}
	}

	digits := []int{n % 10}
	if n >= 10 {
		digits = []int{n / 10, n % 10}
	}
	glyphW, glyphH := 5*scale, 7*scale
	totalW := len(digits)*glyphW + (len(digits)-1)*gap
	x0 := (w - totalW) / 2
	y0 := (h - glyphH) / 2
	for i, d := range digits {
		gx := x0 + i*(glyphW+gap)
		for row, bits := range digitGlyphs[d] {
			for col, bit := range bits {
				if bit != '1' {
					continue
				}
				for dy := 0; dy < scale; dy++ {
					for dx := 0; dx < scale; dx++ {
						img.SetRGBA(gx+col*scale+dx, y0+row*scale+dy, white)
					}
				}
			}
		}
	}
	return img
}

// samplesRequested 判断是否要生成示例：显式环境变量，或 -run 里点名 TestSamples。
func samplesRequested() bool {
	if os.Getenv("COLLAGE_SAMPLES") != "" {
		return true
	}
	f := flag.Lookup("test.run")
	return f != nil && strings.Contains(f.Value.String(), "TestSamples")
}

func TestSamples(t *testing.T) {
	if !samplesRequested() {
		t.Skip("设置 COLLAGE_SAMPLES=1 或 -run TestSamples 才会生成示例图")
	}

	const outDir = "/tmp/collage-samples"
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", outDir, err)
	}
	srcDir := t.TempDir()

	// 10 张合成输入，按序号取色。
	paths := make([]string, 10)
	for i := 0; i < 10; i++ {
		paths[i] = writePNG(t, srcDir, string(rune('a'+i))+".png", numberedPoster(i+1, palette[i]))
	}

	for _, n := range []int{1, 2, 3, 4, 6, 10} {
		data, err := RenderWebP(paths[:n], Options{Reflection: true}, 90)
		if err != nil {
			t.Fatalf("%d 张: RenderWebP: %v", n, err)
		}
		name := filepath.Join(outDir, "s"+itoa(n)+".webp")
		if err := os.WriteFile(name, data, 0o644); err != nil {
			t.Fatalf("写 %s: %v", name, err)
		}
		t.Logf("写出 %s（%d 字节）", name, len(data))
	}

	// 同一 4 张布局的 PNG，便于人眼直接打开。
	img, err := Render(paths[:4], Options{Reflection: true})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	pngPath := filepath.Join(outDir, "s4.png")
	file, err := os.Create(pngPath)
	if err != nil {
		t.Fatalf("create %s: %v", pngPath, err)
	}
	if err := png.Encode(file, img); err != nil {
		file.Close()
		t.Fatalf("encode %s: %v", pngPath, err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close %s: %v", pngPath, err)
	}
	t.Logf("写出 %s", pngPath)
}

// itoa 避免为一个测试再引 strconv。
func itoa(n int) string {
	return strconv.Itoa(n)
}
