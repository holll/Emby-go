package collage

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

// ---------- 构造带 EXIF 的 JPEG ----------

// buildExifAPP1 生成一个只含 IFD0 tag 0x0112 的 APP1 段（含段标记与长度）。
func buildExifAPP1(orientation uint16, bigEndian bool) []byte {
	var order binary.ByteOrder = binary.LittleEndian
	tiff := []byte("II")
	if bigEndian {
		order = binary.BigEndian
		tiff = []byte("MM")
	}

	buf := new(bytes.Buffer)
	buf.Write(tiff)
	write16(buf, order, 42)     // magic
	write32(buf, order, 8)      // IFD0 偏移
	write16(buf, order, 1)      // entry 数
	write16(buf, order, 0x0112) // tag: Orientation
	write16(buf, order, 3)      // type: SHORT
	write32(buf, order, 1)      // count
	write16(buf, order, orientation)
	write16(buf, order, 0) // padding 到 4 字节
	write32(buf, order, 0) // 下一个 IFD 偏移：无

	payload := append([]byte("Exif\x00\x00"), buf.Bytes()...)
	segment := []byte{0xFF, 0xE1, 0, 0}
	binary.BigEndian.PutUint16(segment[2:], uint16(len(payload)+2))
	return append(segment, payload...)
}

func write16(buf *bytes.Buffer, order binary.ByteOrder, v uint16) {
	var b [2]byte
	order.PutUint16(b[:], v)
	buf.Write(b[:])
}

func write32(buf *bytes.Buffer, order binary.ByteOrder, v uint32) {
	var b [4]byte
	order.PutUint32(b[:], v)
	buf.Write(b[:])
}

// encodeJPEGWithOrientation 编码 src 为 JPEG，并在 SOI 之后插入 APP1 EXIF。
func encodeJPEGWithOrientation(t *testing.T, src image.Image, orientation uint16) []byte {
	t.Helper()
	var raw bytes.Buffer
	if err := jpeg.Encode(&raw, src, &jpeg.Options{Quality: 100}); err != nil {
		t.Fatalf("jpeg encode: %v", err)
	}
	data := raw.Bytes()
	if len(data) < 2 || data[0] != 0xFF || data[1] != 0xD8 {
		t.Fatal("不是 JPEG 流")
	}
	out := append([]byte{}, data[:2]...)
	out = append(out, buildExifAPP1(orientation, false)...)
	return append(out, data[2:]...)
}

// cornerImage 生成一张四角颜色各不相同的图，用来判定 8 种方向变换。
// 宽高不等（60×40），所以 5..8 的宽高对调也能测出来；四角各 12×12 的色块，
// 这样 JPEG 的 4:2:0 色度下采样不会把角点糊成中间色。
func cornerImage() *image.RGBA {
	const w, h, blk = 60, 40, 12
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	fill := func(x0, y0 int, c color.RGBA) {
		for y := y0; y < y0+blk; y++ {
			for x := x0; x < x0+blk; x++ {
				img.SetRGBA(x, y, c)
			}
		}
	}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetRGBA(x, y, color.RGBA{60, 60, 60, 255})
		}
	}
	fill(0, 0, color.RGBA{255, 0, 0, 255})           // 左上：红
	fill(w-blk, 0, color.RGBA{0, 255, 0, 255})       // 右上：绿
	fill(0, h-blk, color.RGBA{0, 0, 255, 255})       // 左下：蓝
	fill(w-blk, h-blk, color.RGBA{255, 255, 0, 255}) // 右下：黄
	return img
}

// ---------- 1..8 方向变换 ----------

// TestApplyOrientationAllEight 用目标图四角反查源图，独立验证 8 种变换。
// 期望值直接按 EXIF 2.31 的定义写成"目标四角 ← 源四角"的映射。
func TestApplyOrientationAllEight(t *testing.T) {
	// 源图四角：lt=红 rt=绿 lb=蓝 rb=黄
	red := color.RGBA{255, 0, 0, 255}
	green := color.RGBA{0, 255, 0, 255}
	blue := color.RGBA{0, 0, 255, 255}
	yellow := color.RGBA{255, 255, 0, 255}

	type want struct {
		w, h           int
		lt, rt, lb, rb color.RGBA // 目标图四角应有的颜色
	}
	cases := map[int]want{
		// 原样
		1: {60, 40, red, green, blue, yellow},
		// 水平镜像：左右对调
		2: {60, 40, green, red, yellow, blue},
		// 旋转 180°
		3: {60, 40, yellow, blue, green, red},
		// 垂直镜像：上下对调
		4: {60, 40, blue, yellow, red, green},
		// 主对角线镜像（转置）
		5: {40, 60, red, blue, green, yellow},
		// 顺时针 90°
		6: {40, 60, blue, red, yellow, green},
		// 副对角线镜像
		7: {40, 60, yellow, green, blue, red},
		// 逆时针 90°
		8: {40, 60, green, yellow, red, blue},
	}

	src := cornerImage()
	for o, exp := range cases {
		got := applyOrientation(src, o)
		b := got.Bounds()
		if b.Dx() != exp.w || b.Dy() != exp.h {
			t.Errorf("orientation %d: 尺寸 %dx%d, 期望 %dx%d", o, b.Dx(), b.Dy(), exp.w, exp.h)
			continue
		}
		check := func(name string, x, y int, want color.RGBA) {
			r, g, bl, _ := got.At(b.Min.X+x, b.Min.Y+y).RGBA()
			have := color.RGBA{uint8(r >> 8), uint8(g >> 8), uint8(bl >> 8), 255}
			if absInt(int(have.R)-int(want.R)) > 2 || absInt(int(have.G)-int(want.G)) > 2 || absInt(int(have.B)-int(want.B)) > 2 {
				t.Errorf("orientation %d: %s = %+v, 期望 %+v", o, name, have, want)
			}
		}
		check("左上", 0, 0, exp.lt)
		check("右上", exp.w-1, 0, exp.rt)
		check("左下", 0, exp.h-1, exp.lb)
		check("右下", exp.w-1, exp.h-1, exp.rb)
	}
}

// orientation 1 与越界值都应当原样返回（不复制、不改尺寸）。
func TestApplyOrientationPassthrough(t *testing.T) {
	src := cornerImage()
	for _, o := range []int{0, 1, 9, -1} {
		if got := applyOrientation(src, o); got != image.Image(src) {
			t.Fatalf("orientation %d 应原样返回", o)
		}
	}
}

// ---------- decodeOriented ----------

func TestDecodeOrientedJPEG(t *testing.T) {
	dir := t.TempDir()
	data := encodeJPEGWithOrientation(t, cornerImage(), 6)
	path := filepath.Join(dir, "rot.jpg")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}

	img, err := decodeOriented(path)
	if err != nil {
		t.Fatalf("decodeOriented: %v", err)
	}
	b := img.Bounds()
	// orientation 6：60×40 → 40×60
	if b.Dx() != 40 || b.Dy() != 60 {
		t.Fatalf("尺寸 %dx%d, 期望 40x60", b.Dx(), b.Dy())
	}
	// 顺时针 90° 后左上角应来自源图左下（蓝）。
	r, g, bl, _ := img.At(b.Min.X, b.Min.Y).RGBA()
	if bl>>8 < 180 || r>>8 > 80 || g>>8 > 80 {
		t.Fatalf("左上角 = (%d,%d,%d), 期望蓝色", r>>8, g>>8, bl>>8)
	}
}

func TestDecodeOrientedBigEndianExif(t *testing.T) {
	dir := t.TempDir()
	var raw bytes.Buffer
	if err := jpeg.Encode(&raw, cornerImage(), &jpeg.Options{Quality: 100}); err != nil {
		t.Fatal(err)
	}
	body := raw.Bytes()
	out := append([]byte{}, body[:2]...)
	out = append(out, buildExifAPP1(3, true)...)
	out = append(out, body[2:]...)
	path := filepath.Join(dir, "be.jpg")
	if err := os.WriteFile(path, out, 0o644); err != nil {
		t.Fatal(err)
	}

	img, err := decodeOriented(path)
	if err != nil {
		t.Fatalf("decodeOriented: %v", err)
	}
	if b := img.Bounds(); b.Dx() != 60 || b.Dy() != 40 {
		t.Fatalf("尺寸 %dx%d, 期望 60x40", b.Dx(), b.Dy())
	}
}

// 非 JPEG（PNG）必须按原始方向返回，且不报错。
func TestDecodeOrientedNonJPEG(t *testing.T) {
	dir := t.TempDir()
	path := writePNG(t, dir, "p.png", poster(30, 45, palette[0]))
	img, err := decodeOriented(path)
	if err != nil {
		t.Fatalf("decodeOriented: %v", err)
	}
	if b := img.Bounds(); b.Dx() != 30 || b.Dy() != 45 {
		t.Fatalf("尺寸 %dx%d, 期望 30x45", b.Dx(), b.Dy())
	}
}

// EXIF 损坏（截断 / 非法偏移 / 非法 type）时降级为原始方向，不能让整张图不可用。
func TestDecodeOrientedBadExifDegrades(t *testing.T) {
	var raw bytes.Buffer
	if err := jpeg.Encode(&raw, cornerImage(), &jpeg.Options{Quality: 100}); err != nil {
		t.Fatal(err)
	}
	body := raw.Bytes()

	bad := map[string][]byte{
		"截断的 APP1": func() []byte {
			seg := buildExifAPP1(6, false)
			seg = seg[:len(seg)/2] // 长度字段还在，载荷被砍
			out := append([]byte{}, body[:2]...)
			out = append(out, seg...)
			return append(out, body[2:]...)
		}(),
		"非法 IFD 偏移": func() []byte {
			seg := buildExifAPP1(6, false)
			// TIFF 起点在 seg 的 4+6 字节处，IFD0 偏移在 +4。
			base := 4 + 6
			binary.LittleEndian.PutUint32(seg[base+4:], 0xFFFF)
			out := append([]byte{}, body[:2]...)
			out = append(out, seg...)
			return append(out, body[2:]...)
		}(),
		"非法 TIFF magic": func() []byte {
			seg := buildExifAPP1(6, false)
			binary.LittleEndian.PutUint16(seg[4+6+2:], 7)
			out := append([]byte{}, body[:2]...)
			out = append(out, seg...)
			return append(out, body[2:]...)
		}(),
		"APP1 但不是 Exif（XMP）": func() []byte {
			payload := []byte("http://ns.adobe.com/xap/1.0/\x00<xmp/>")
			seg := []byte{0xFF, 0xE1, 0, 0}
			binary.BigEndian.PutUint16(seg[2:], uint16(len(payload)+2))
			seg = append(seg, payload...)
			out := append([]byte{}, body[:2]...)
			out = append(out, seg...)
			return append(out, body[2:]...)
		}(),
	}

	dir := t.TempDir()
	for name, data := range bad {
		path := filepath.Join(dir, "bad.jpg")
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatal(err)
		}
		img, err := decodeOriented(path)
		if err != nil {
			t.Fatalf("%s: decodeOriented 报错: %v", name, err)
		}
		if b := img.Bounds(); b.Dx() != 60 || b.Dy() != 40 {
			t.Fatalf("%s: 尺寸 %dx%d, 期望原始 60x40（应降级为不旋转）", name, b.Dx(), b.Dy())
		}
	}
}

func TestDecodeOrientedMissingFile(t *testing.T) {
	if _, err := decodeOriented(filepath.Join(t.TempDir(), "nope.jpg")); err == nil {
		t.Fatal("缺失文件应返回错误")
	}
}

// 像素数超上限的图必须被判为不可用，而不是尝试分配。
func TestDecodeOrientedRejectsHugeImage(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "huge.png")
	// 用 1 像素高的 PNG 头声明一个超大宽高：写一个真实但"声明过大"的 PNG 代价高，
	// 这里直接改 IHDR 的宽高，CRC 不校验（DecodeConfig 不查 CRC 也能拿到尺寸）。
	var buf bytes.Buffer
	if err := pngEncodeTiny(&buf); err != nil {
		t.Fatal(err)
	}
	data := buf.Bytes()
	// IHDR 数据从偏移 16 开始：宽 4 字节、高 4 字节。
	binary.BigEndian.PutUint32(data[16:], 100_000)
	binary.BigEndian.PutUint32(data[20:], 100_000) // 1e10 像素
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := decodeOriented(path); err == nil {
		t.Fatal("超大尺寸应被判为不可用")
	}
	// 也必须在 Render 里被跳过，而不是 panic。
	if _, err := Render([]string{path}, Options{}); err == nil {
		t.Fatal("Render 应返回 ErrNoImages")
	}
}

// pngEncodeTiny 编码一张 1×1 PNG，作为改 IHDR 的基底。
func pngEncodeTiny(buf *bytes.Buffer) error {
	return png.Encode(buf, image.NewRGBA(image.Rect(0, 0, 1, 1)))
}
