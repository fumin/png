// Package png implements a PNG image decoder and encoder.
// It is a fork of the standard library's png package,
// but with a focus on memory usage and speed on image.NRGBA images.
package png

import (
	"compress/zlib"
	"encoding/binary"
	"fmt"
	"hash"
	"hash/crc32"
	"image"
	"image/color"
	"io"
)

// Color type, as per the PNG spec.
const (
	ctTrueColorAlpha = 6
)

// A cb is a combination of color type and bit depth.
const (
	cbInvalid = iota
	cbTCA8
)

// Filter type, as per the PNG spec.
const (
	ftNone    = 0
	ftSub     = 1
	ftUp      = 2
	ftAverage = 3
	ftPaeth   = 4
	nFilter   = 5
)

// Decoding stage.
// The PNG specification says that the IHDR, PLTE (if present), tRNS (if
// present), IDAT and IEND chunks must appear in that order. There may be
// multiple IDAT chunks, and IDAT chunks must be sequential (i.e. they may not
// have any other chunks between them).
// https://www.w3.org/TR/PNG/#5ChunkOrdering
const (
	dsStart = iota
	dsSeenIHDR
	dsSeenIDAT
	dsSeenIEND
)

const pngHeader = "\x89PNG\r\n\x1a\n"

// A Decoder is a row-by-row decoder for png image.NRGBA images.
// Compared to the standard library, it reduces memory usage by loading only the current row.
type Decoder struct {
	d *decoder

	zlibR         io.ReadCloser
	bytesPerPixel int
	cr            []uint8
	pr            []uint8
	y             int
}

// NewDecoder decodes the metadata of an image data stream.
func NewDecoder(r io.Reader) (*Decoder, error) {
	d := &decoder{
		r:   r,
		crc: crc32.NewIEEE(),
	}
	if err := d.checkHeader(); err != nil {
		if err == io.EOF {
			err = io.ErrUnexpectedEOF
		}
		return nil, err
	}

	var header string
	for header != "IDAT" {
		if _, err := io.ReadFull(d.r, d.tmp[:8]); err != nil {
			return nil, err
		}
		length := binary.BigEndian.Uint32(d.tmp[:4])
		d.crc.Reset()
		d.crc.Write(d.tmp[4:8])

		header = string(d.tmp[4:8])
		switch header {
		case "IHDR":
			if d.stage != dsStart {
				return nil, chunkOrderError
			}
			d.stage = dsSeenIHDR
			if err := d.parseIHDR(length); err != nil {
				if err == io.EOF {
					return nil, io.ErrUnexpectedEOF
				}
				return nil, err
			}
			if d.cb != cbTCA8 {
				return nil, fmt.Errorf("color type and bit depth not cbTCA8 %v", d.cb)
			}
		case "IDAT":
			if d.stage < dsSeenIHDR || d.stage > dsSeenIDAT {
				return nil, chunkOrderError
			}
			d.idatLength = length
			d.stage = dsSeenIDAT
		default:
			if length > 0x7fffffff {
				return nil, FormatError(fmt.Sprintf("Bad chunk length: %d", length))
			}
			// Ignore this chunk (of a known length).
			var ignored [4096]byte
			for length > 0 {
				n, err := io.ReadFull(d.r, ignored[:min(len(ignored), int(length))])
				if err != nil {
					return nil, err
				}
				d.crc.Write(ignored[:n])
				length -= uint32(n)
			}
			if err := d.verifyChecksum(); err != nil {
				return nil, err
			}
		}
	}

	dec := &Decoder{}
	dec.d = d
	var err error
	dec.zlibR, err = zlib.NewReader(d)
	if err != nil {
		return nil, err
	}

	bitsPerPixel := 32
	dec.bytesPerPixel = (bitsPerPixel + 7) / 8

	// The +1 is for the per-row filter type, which is at cr[0].
	rowSize := 1 + (int64(bitsPerPixel)*int64(d.width)+7)/8
	if rowSize != int64(int(rowSize)) {
		return nil, UnsupportedError("dimension overflow")
	}
	// cr and pr are the bytes for the current and previous row.
	dec.cr = make([]uint8, rowSize)
	dec.pr = make([]uint8, rowSize)

	return dec, nil
}

// Bounds returns the bounds of the decoded image.
func (d *Decoder) Bounds() image.Rectangle {
	return image.Rect(0, 0, d.d.width, d.d.height)
}

// DecodeRow decodes the current row.
// Users must take care of not modifying the returned buffer,
// as it is used in the decoding of subsequent rows.
func (d *Decoder) DecodeRow() ([]byte, error) {
	if d.y >= d.d.height {
		return nil, io.EOF
	}

	// Read the decompressed bytes.
	_, err := io.ReadFull(d.zlibR, d.cr)
	if err != nil {
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			return nil, FormatError("not enough pixel data")
		}
		return nil, err
	}

	// Apply the filter.
	cdat := d.cr[1:]
	pdat := d.pr[1:]
	switch d.cr[0] {
	case ftNone:
		// No-op.
	case ftSub:
		for i := d.bytesPerPixel; i < len(cdat); i++ {
			cdat[i] += cdat[i-d.bytesPerPixel]
		}
	case ftUp:
		for i, p := range pdat {
			cdat[i] += p
		}
	case ftAverage:
		// The first column has no column to the left of it, so it is a
		// special case. We know that the first column exists because we
		// check above that width != 0, and so len(cdat) != 0.
		for i := 0; i < d.bytesPerPixel; i++ {
			cdat[i] += pdat[i] / 2
		}
		for i := d.bytesPerPixel; i < len(cdat); i++ {
			cdat[i] += uint8((int(cdat[i-d.bytesPerPixel]) + int(pdat[i])) / 2)
		}
	case ftPaeth:
		filterPaeth(cdat, pdat, d.bytesPerPixel)
	default:
		return nil, FormatError("bad filter type")
	}

	d.pr, d.cr = d.cr, d.pr
	d.y++
	return cdat, nil
}

// Close checks the validity of the decoded image stream at the end.
func (d *Decoder) Close() error {
	if d.d.stage == dsSeenIEND {
		return nil
	}

	if err := d.zlibR.Close(); err != nil {
		return err
	}
	if err := d.d.verifyChecksum(); err != nil {
		return err
	}

	if err := d.d.readIEND(); err != nil {
		return err
	}

	return nil
}

type decoder struct {
	r             io.Reader
	img           image.Image
	crc           hash.Hash32
	width, height int
	depth         int
	palette       color.Palette
	cb            int
	stage         int
	idatLength    uint32
	tmp           [3 * 256]byte
	interlace     int

	// useTransparent and transparent are used for grayscale and truecolor
	// transparency, as opposed to palette transparency.
	useTransparent bool
	transparent    [6]byte
}

// A FormatError reports that the input is not a valid PNG.
type FormatError string

func (e FormatError) Error() string { return "png: invalid format: " + string(e) }

var chunkOrderError = FormatError("chunk out of order")

// An UnsupportedError reports that the input uses a valid but unimplemented PNG feature.
type UnsupportedError string

func (e UnsupportedError) Error() string { return "png: unsupported feature: " + string(e) }

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func (d *decoder) parseIHDR(length uint32) error {
	if length != 13 {
		return FormatError("bad IHDR length")
	}
	if _, err := io.ReadFull(d.r, d.tmp[:13]); err != nil {
		return err
	}
	d.crc.Write(d.tmp[:13])
	if d.tmp[10] != 0 {
		return UnsupportedError("compression method")
	}
	if d.tmp[11] != 0 {
		return UnsupportedError("filter method")
	}

	w := int32(binary.BigEndian.Uint32(d.tmp[0:4]))
	h := int32(binary.BigEndian.Uint32(d.tmp[4:8]))
	if w <= 0 || h <= 0 {
		return FormatError("non-positive dimension")
	}
	nPixels64 := int64(w) * int64(h)
	nPixels := int(nPixels64)
	if nPixels64 != int64(nPixels) {
		return UnsupportedError("dimension overflow")
	}
	// There can be up to 8 bytes per pixel, for 16 bits per channel RGBA.
	if nPixels != (nPixels*8)/8 {
		return UnsupportedError("dimension overflow")
	}

	d.cb = cbInvalid
	d.depth = int(d.tmp[8])
	switch d.depth {
	case 8:
		switch d.tmp[9] {
		case ctTrueColorAlpha:
			d.cb = cbTCA8
		}
	}
	if d.cb == cbInvalid {
		return UnsupportedError(fmt.Sprintf("bit depth %d, color type %d", d.tmp[8], d.tmp[9]))
	}
	d.width, d.height = int(w), int(h)
	return d.verifyChecksum()
}

// Read presents one or more IDAT chunks as one continuous stream (minus the
// intermediate chunk headers and footers). If the PNG data looked like:
//   ... len0 IDAT xxx crc0 len1 IDAT yy crc1 len2 IEND crc2
// then this reader presents xxxyy. For well-formed PNG data, the decoder state
// immediately before the first Read call is that d.r is positioned between the
// first IDAT and xxx, and the decoder state immediately after the last Read
// call is that d.r is positioned between yy and crc1.
func (d *decoder) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	for d.idatLength == 0 {
		// We have exhausted an IDAT chunk. Verify the checksum of that chunk.
		if err := d.verifyChecksum(); err != nil {
			return 0, err
		}
		// Read the length and chunk type of the next chunk, and check that
		// it is an IDAT chunk.
		if _, err := io.ReadFull(d.r, d.tmp[:8]); err != nil {
			return 0, err
		}
		d.idatLength = binary.BigEndian.Uint32(d.tmp[:4])
		if string(d.tmp[4:8]) != "IDAT" {
			return 0, FormatError("not enough pixel data")
		}
		d.crc.Reset()
		d.crc.Write(d.tmp[4:8])
	}
	if int(d.idatLength) < 0 {
		return 0, UnsupportedError("IDAT chunk length overflow")
	}
	n, err := d.r.Read(p[:min(len(p), int(d.idatLength))])
	d.crc.Write(p[:n])
	d.idatLength -= uint32(n)
	return n, err
}

func (d *decoder) readIEND() error {
	// Read the length and chunk type.
	if _, err := io.ReadFull(d.r, d.tmp[:8]); err != nil {
		return err
	}
	length := binary.BigEndian.Uint32(d.tmp[:4])
	d.crc.Reset()
	d.crc.Write(d.tmp[4:8])

	header := string(d.tmp[4:8])
	if header != "IEND" {
		return FormatError("not IEND " + header)
	}

	if length != 0 {
		return FormatError("bad IEND length")
	}
	return d.verifyChecksum()
}

func (d *decoder) verifyChecksum() error {
	if _, err := io.ReadFull(d.r, d.tmp[:4]); err != nil {
		return err
	}
	if binary.BigEndian.Uint32(d.tmp[:4]) != d.crc.Sum32() {
		return FormatError("invalid checksum")
	}
	return nil
}

func (d *decoder) checkHeader() error {
	_, err := io.ReadFull(d.r, d.tmp[:len(pngHeader)])
	if err != nil {
		return err
	}
	if string(d.tmp[:len(pngHeader)]) != pngHeader {
		return FormatError("not a PNG file")
	}
	return nil
}
