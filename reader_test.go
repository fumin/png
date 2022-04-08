package png

import (
	"bytes"
	"fmt"
	"image"
	stdpng "image/png"
	"os"
	"testing"
)

func TestReader(t *testing.T) {
	fname := "testdata/basn6a08.png"
	stdImg, err := stdReadPNG(fname)
	if err != nil {
		t.Fatalf("%+v", err)
	}
	img, err := readPNG(fname)
	if err != nil {
		t.Fatalf("%+v", err)
	}

	if stdImg.Bounds() != img.Bounds() {
		t.Fatalf("%+v %+v", stdImg.Bounds(), img.Bounds())
	}
	if !bytes.Equal(stdImg.Pix, img.Pix) {
		t.Fatalf("not equal")
	}
}

func readPNG(fname string) (*image.NRGBA, error) {
	f, err := os.Open(fname)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	d, err := NewDecoder(f)
	if err != nil {
		return nil, err
	}
	w, h := d.Bounds().Dx(), d.Bounds().Dy()

	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		row, err := d.DecodeRow()
		if err != nil {
			return nil, err
		}
		if len(row) != w*4 {
			return nil, fmt.Errorf("%d %d", len(row), w*h)
		}
		offset := img.PixOffset(0, y)
		copy(img.Pix[offset:], row)
	}

	if err := d.Close(); err != nil {
		return nil, err
	}

	return img, nil
}

func stdReadPNG(fname string) (*image.NRGBA, error) {
	f, err := os.Open(fname)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	img, err := stdpng.Decode(f)
	if err != nil {
		return nil, err
	}
	return img.(*image.NRGBA), nil
}
