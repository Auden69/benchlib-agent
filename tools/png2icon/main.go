package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"os"
)

func main() {
	// Lire le PNG
	data, err := os.ReadFile("cmd/tray/icon.png")
	if err != nil {
		fmt.Fprintln(os.Stderr, "Erreur lecture icon.png :", err)
		os.Exit(1)
	}

	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		fmt.Fprintln(os.Stderr, "Erreur décodage PNG :", err)
		os.Exit(1)
	}

	// Convertir en RGBA 32x32
	bounds := img.Bounds()
	rgba := image.NewRGBA(bounds)
	draw.Draw(rgba, bounds, img, bounds.Min, draw.Src)

	w := bounds.Dx()
	h := bounds.Dy()

	// Construire le BMP (BITMAPINFOHEADER + pixels BGRA + masque AND)
	var bmp bytes.Buffer

	// BITMAPINFOHEADER (40 bytes)
	binary.Write(&bmp, binary.LittleEndian, uint32(40))       // biSize
	binary.Write(&bmp, binary.LittleEndian, int32(w))         // biWidth
	binary.Write(&bmp, binary.LittleEndian, int32(h*2))       // biHeight (×2 pour ICO)
	binary.Write(&bmp, binary.LittleEndian, uint16(1))        // biPlanes
	binary.Write(&bmp, binary.LittleEndian, uint16(32))       // biBitCount
	binary.Write(&bmp, binary.LittleEndian, uint32(0))        // biCompression
	binary.Write(&bmp, binary.LittleEndian, uint32(w*h*4))    // biSizeImage
	binary.Write(&bmp, binary.LittleEndian, int32(0))         // biXPelsPerMeter
	binary.Write(&bmp, binary.LittleEndian, int32(0))         // biYPelsPerMeter
	binary.Write(&bmp, binary.LittleEndian, uint32(0))        // biClrUsed
	binary.Write(&bmp, binary.LittleEndian, uint32(0))        // biClrImportant

	// Pixels BGRA — de bas en haut (format ICO)
	for y := h - 1; y >= 0; y-- {
		for x := 0; x < w; x++ {
			r, g, b, a := rgba.At(x, y).RGBA()
			bmp.WriteByte(byte(b >> 8))
			bmp.WriteByte(byte(g >> 8))
			bmp.WriteByte(byte(r >> 8))
			bmp.WriteByte(byte(a >> 8))
		}
	}

	// Masque AND (tous transparents = 0x00), aligné sur 4 bytes par ligne
	rowSize := ((w + 31) / 32) * 4
	for y := 0; y < h; y++ {
		for x := 0; x < rowSize; x++ {
			bmp.WriteByte(0x00)
		}
	}

	bmpData := bmp.Bytes()

	// Construire le fichier ICO
	var ico bytes.Buffer

	// Header ICO (6 bytes)
	binary.Write(&ico, binary.LittleEndian, uint16(0)) // reserved
	binary.Write(&ico, binary.LittleEndian, uint16(1)) // type = ICO
	binary.Write(&ico, binary.LittleEndian, uint16(1)) // count = 1 image

	// Directory entry (16 bytes)
	ico.WriteByte(byte(w))           // width
	ico.WriteByte(byte(h))           // height
	ico.WriteByte(0)                 // colorCount
	ico.WriteByte(0)                 // reserved
	binary.Write(&ico, binary.LittleEndian, uint16(1))               // planes
	binary.Write(&ico, binary.LittleEndian, uint16(32))              // bitCount
	binary.Write(&ico, binary.LittleEndian, uint32(len(bmpData)))    // sizeInBytes
	binary.Write(&ico, binary.LittleEndian, uint32(6+16))            // offset (header + dir)

	ico.Write(bmpData)

	icoBytes := ico.Bytes()

	// Générer icon.go avec les bytes ICO
	var sb bytes.Buffer
	sb.WriteString("//go:build windows\n\n")
	sb.WriteString("package main\n\n")
	sb.WriteString("var iconData = []byte{\n")

	for i, b := range icoBytes {
		if i%16 == 0 {
			sb.WriteString("\t")
		}
		fmt.Fprintf(&sb, "0x%02x, ", b)
		if (i+1)%16 == 0 {
			sb.WriteString("\n")
		}
	}
	sb.WriteString("\n}\n")

	err = os.WriteFile("cmd/tray/icon.go", sb.Bytes(), 0644)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Erreur écriture icon.go :", err)
		os.Exit(1)
	}

	fmt.Printf("icon.go généré avec succès (%d bytes ICO depuis %dx%d PNG)\n", len(icoBytes), w, h)
}