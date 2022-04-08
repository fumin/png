package png

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestEncode(t *testing.T) {
	outDir, err := os.MkdirTemp("", t.Name())
	if err != nil {
		t.Fatalf("%+v", err)
	}
	t.Logf("outDir %s", outDir)
	defer os.RemoveAll(outDir)

	fname := "testdata/basn6a08.png"
	img1, err := stdReadPNG(fname)
	if err != nil {
		t.Fatalf("%+v", err)
	}

	outName := filepath.Join(outDir, "test.png")
	f, err := os.Create(outName)
	if err != nil {
		t.Fatalf("%+v", err)
	}
	defer f.Close()
	if err := NewEncoder(BestSpeed).Encode(f, img1); err != nil {
		t.Fatalf("%+v", err)
	}

	img2, err := stdReadPNG(outName)
	if err != nil {
		t.Fatalf("%+v", err)
	}
	if img1.Rect != img2.Rect {
		t.Fatalf("%+v %+v", img1, img2)
	}
	if img1.Stride != img2.Stride {
		t.Fatalf("%+v %+v", img1, img2)
	}
	if !bytes.Equal(img1.Pix, img2.Pix) {
		t.Fatalf("%+v %+v", img1, img2)
	}
}
