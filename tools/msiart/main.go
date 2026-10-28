// Command msiart draws the pictures of the agent installer from the
// WatchZit icon: dialog.bmp (493×312, the welcome and finish pages) and
// banner.bmp (493×58, the top of the other pages), as 24-bit BMP files
// the Windows Installer shows.
//
//	go run ./tools/msiart brand/watchzit-icon-256.png packaging/windows
package main

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
)

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: msiart ICON.png OUTDIR")
		os.Exit(2)
	}
	f, err := os.Open(os.Args[1]) // #nosec G304 G703 -- input path from the command line of this developer tool
	if err != nil {
		panic(err)
	}
	icon, err := png.Decode(f)
	_ = f.Close()
	if err != nil {
		panic(err)
	}
	// dialog: a dark brand panel on the left with the icon, white on the right
	dlg := image.NewRGBA(image.Rect(0, 0, 493, 312))
	for y := 0; y < 312; y++ {
		for x := 0; x < 493; x++ {
			c := color.RGBA{255, 255, 255, 255}
			if x < 164 {
				t := float64(y) / 312
				c = color.RGBA{uint8(15 + 20*t), uint8(23 + 10*t), uint8(42 + 50*t), 255}
			}
			dlg.Set(x, y, c)
		}
	}
	draw(dlg, icon, 32, 40, 100)
	write(filepath.Join(os.Args[2], "dialog.bmp"), dlg)
	// banner: white with a small icon on the right and a line below
	ban := image.NewRGBA(image.Rect(0, 0, 493, 58))
	for y := 0; y < 58; y++ {
		for x := 0; x < 493; x++ {
			c := color.RGBA{255, 255, 255, 255}
			if y == 57 {
				c = color.RGBA{214, 220, 232, 255}
			}
			ban.Set(x, y, c)
		}
	}
	draw(ban, icon, 493-52, 7, 44)
	write(filepath.Join(os.Args[2], "banner.bmp"), ban)
}

// draw scales src into a size×size square at x,y (box filter) and blends
// it over dst.
func draw(dst *image.RGBA, src image.Image, x0, y0, size int) {
	b := src.Bounds()
	sw, sh := b.Dx(), b.Dy()
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			var r, g, bl, a, n uint32
			for sy := y * sh / size; sy < (y+1)*sh/size; sy++ {
				for sx := x * sw / size; sx < (x+1)*sw/size; sx++ {
					cr, cg, cb, ca := src.At(b.Min.X+sx, b.Min.Y+sy).RGBA()
					r, g, bl, a, n = r+cr, g+cg, bl+cb, a+ca, n+1
				}
			}
			if n == 0 {
				continue
			}
			r, g, bl, a = r/n, g/n, bl/n, a/n
			under := dst.RGBAAt(x0+x, y0+y)
			mix := func(top uint32, bot uint8) uint8 { // premultiplied over
				return uint8((top + uint32(bot)*257*(0xffff-a)/0xffff) >> 8) // #nosec G115 -- 16-bit to 8-bit
			}
			dst.SetRGBA(x0+x, y0+y, color.RGBA{mix(r, under.R), mix(g, under.G), mix(bl, under.B), 255})
		}
	}
}

// write saves a 24-bit bottom-up BMP.
func write(path string, img *image.RGBA) {
	w, h := img.Bounds().Dx(), img.Bounds().Dy()
	row := (w*3 + 3) &^ 3
	f, err := os.Create(path) // #nosec G304 G703 -- output path from the command line of this developer tool
	if err != nil {
		panic(err)
	}
	defer f.Close()
	bw := bufio.NewWriter(f)
	le := binary.LittleEndian
	hdr := make([]byte, 54)
	copy(hdr, "BM")
	le.PutUint32(hdr[2:], uint32(54+row*h)) // #nosec G115 -- small image
	le.PutUint32(hdr[10:], 54)
	le.PutUint32(hdr[14:], 40)
	le.PutUint32(hdr[18:], uint32(w)) // #nosec G115 -- small image
	le.PutUint32(hdr[22:], uint32(h)) // #nosec G115 -- small image
	le.PutUint16(hdr[26:], 1)
	le.PutUint16(hdr[28:], 24)
	le.PutUint32(hdr[34:], uint32(row*h)) // #nosec G115 -- small image
	_, _ = bw.Write(hdr)
	line := make([]byte, row)
	for y := h - 1; y >= 0; y-- {
		for x := 0; x < w; x++ {
			c := img.RGBAAt(x, y)
			line[x*3], line[x*3+1], line[x*3+2] = c.B, c.G, c.R
		}
		_, _ = bw.Write(line)
	}
	if err := bw.Flush(); err != nil {
		panic(err)
	}
}
