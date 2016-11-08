package main

// #cgo pkg-config: libpng
// #include <png.h>
import "C"
import (
	"encoding/binary"
	"fmt"
	"github.com/go-gl/gl/v2.1/gl"
	"io"
	"os"
	"runtime"
	"unsafe"
)

type Texture uint32

func textureFinalizer(t *Texture) {
	if *t != 0 {
		gl.DeleteTextures(1, (*uint32)(t))
	}
}
func NewTexture() (t *Texture) {
	t = new(Texture)
	gl.GenTextures(1, (*uint32)(t))
	runtime.SetFinalizer(t, textureFinalizer)
	return
}

type PalFX struct{}

func (fx *PalFX) GetFcPalFx(trans int32) (neg bool, color float32,
	add, mul [3]float32) {
	neg = false
	color = 1
	for i := range add {
		add[i] = 0
	}
	for i := range mul {
		mul[i] = 1
	}
	return
}

type PalleteList struct {
	palletes   [][]uint32
	palleteMap []int
	PalTable   map[[2]int16]int
}

func (pl *PalleteList) init() {
	pl.palletes = nil
	pl.palleteMap = nil
	pl.PalTable = make(map[[2]int16]int)
}
func (pl *PalleteList) SetSource(i int, p []uint32) {
	if i < len(pl.palleteMap) {
		pl.palleteMap[i] = i
	} else {
		for i > len(pl.palleteMap) {
			AppendI(&pl.palleteMap, len(pl.palleteMap))
		}
		AppendI(&pl.palleteMap, i)
	}
	if i < len(pl.palletes) {
		pl.palletes[i] = p
	} else {
		for i > len(pl.palletes) {
			AppendPal(&pl.palletes, nil)
		}
		AppendPal(&pl.palletes, p)
	}
}
func (pl *PalleteList) NewPal() (i int, p []uint32) {
	i = len(pl.palletes)
	p = make([]uint32, 256)
	pl.SetSource(i, p)
	return
}
func (pl *PalleteList) Get(i int) []uint32 {
	return pl.palletes[pl.palleteMap[i]]
}
func (pl *PalleteList) Remap(source int, destination int) {
	pl.palleteMap[source] = destination
}
func (pl *PalleteList) ResetRemap() {
	for i := range pl.palleteMap {
		pl.palleteMap[i] = i
	}
}
func (pl *PalleteList) GetPalMap() []int {
	pm := make([]int, len(pl.palleteMap))
	copy(pm, pl.palleteMap)
	return pm
}
func (pl *PalleteList) SwapPalMap(palMap *[]int) bool {
	if len(*palMap) != len(pl.palleteMap) {
		return false
	}
	*palMap, pl.palleteMap = pl.palleteMap, *palMap
	return true
}

type SffHeader struct {
	Ver0, Ver1, Ver2, Ver3   byte
	FirstSpriteHeaderOffset  uint32
	FirstPaletteHeaderOffset uint32
	NumberOfSprites          uint32
	NumberOfPalettes         uint32
}

func (sh *SffHeader) Read(r io.Reader, lofs *uint32, tofs *uint32) error {
	buf := make([]byte, 12)
	n, err := r.Read(buf)
	if err != nil {
		return err
	}
	if string(buf[:n]) != "ElecbyteSpr\x00" {
		return Error("ElecbyteSprではありません")
	}
	read := func(x interface{}) error {
		return binary.Read(r, binary.LittleEndian, x)
	}
	if err := read(&sh.Ver3); err != nil {
		return err
	}
	if err := read(&sh.Ver2); err != nil {
		return err
	}
	if err := read(&sh.Ver1); err != nil {
		return err
	}
	if err := read(&sh.Ver0); err != nil {
		return err
	}
	var dummy uint32
	if err := read(&dummy); err != nil {
		return err
	}
	switch sh.Ver0 {
	case 1:
		sh.FirstPaletteHeaderOffset, sh.NumberOfPalettes = 0, 0
		if err := read(&sh.NumberOfSprites); err != nil {
			return err
		}
		if err := read(&sh.FirstSpriteHeaderOffset); err != nil {
			return err
		}
		if err := read(&dummy); err != nil {
			return err
		}
	case 2:
		for i := 0; i < 4; i++ {
			if err := read(&dummy); err != nil {
				return err
			}
		}
		if err := read(&sh.FirstSpriteHeaderOffset); err != nil {
			return err
		}
		if err := read(&sh.NumberOfSprites); err != nil {
			return err
		}
		if err := read(&sh.FirstPaletteHeaderOffset); err != nil {
			return err
		}
		if err := read(&sh.NumberOfPalettes); err != nil {
			return err
		}
		if err := read(lofs); err != nil {
			return err
		}
		if err := read(&dummy); err != nil {
			return err
		}
		if err := read(tofs); err != nil {
			return err
		}
	default:
		return Error("バージョンが不正です")
	}
	return nil
}

type Sprite struct {
	Pal           []uint32
	Tex           *Texture
	Group, Number int16
	Size          [2]uint16
	Offset        [2]int16
	palidx, link  int
	rle           int
}

func NewSprite() *Sprite {
	return &Sprite{palidx: -1, link: -1}
}
func LoadFromSff(filename string, g int16, n int16) (*Sprite, error) {
	s := NewSprite()
	f, err := os.Open(filename)
	if err != nil {
		return nil, err
	}
	defer func() { chk(f.Close()) }()
	h := &SffHeader{}
	var lofs, tofs uint32
	if err := h.Read(f, &lofs, &tofs); err != nil {
		return nil, err
	}
	var shofs, xofs, size uint32 = h.FirstSpriteHeaderOffset, 0, 0
	var indexOfPrevious uint16
	pl := &PalleteList{}
	pl.init()
	foo := func() error {
		switch h.Ver0 {
		case 1:
			if err := s.readHeader(f, &xofs, &size, &indexOfPrevious); err != nil {
				return err
			}
		case 2:
			if err := s.readHeaderV2(f, &xofs, &size,
				lofs, tofs, &indexOfPrevious); err != nil {
				return err
			}
		}
		return nil
	}
	var palletSame bool
	var dummy *Sprite
	var newSubHeaderOffset []uint32
	AppendU32(&newSubHeaderOffset, shofs)
	i := 0
	for ; i < int(h.NumberOfSprites); i++ {
		AppendU32(&newSubHeaderOffset, shofs)
		f.Seek(int64(shofs), 0)
		if err := foo(); err != nil {
			return nil, err
		}
		if s.palidx < 0 || s.Group == g && s.Number == n {
			ip := len(newSubHeaderOffset)
			for size == 0 {
				if int(indexOfPrevious) >= ip {
					return nil, Error("linkが不正です")
				}
				ip = int(indexOfPrevious)
				if h.Ver0 == 1 {
					shofs = newSubHeaderOffset[ip]
				} else {
					shofs = h.FirstSpriteHeaderOffset + uint32(ip)*28
				}
				f.Seek(int64(shofs), 0)
				if err := foo(); err != nil {
					return nil, err
				}
			}
			switch h.Ver0 {
			case 1:
				if err := s.read(f, h, int64(shofs+32), size, xofs, dummy,
					&palletSame, pl, false); err != nil {
					return nil, err
				}
			case 2:
				if err := s.readV2(f, int64(xofs), size); err != nil {
					return nil, err
				}
			}
			if s.Group == g && s.Number == n {
				break
			}
			dummy = &Sprite{palidx: s.palidx}
		}
		if h.Ver0 == 1 {
			shofs = xofs
		} else {
			shofs += 28
		}
	}
	if i == int(h.NumberOfSprites) {
		return nil, Error(fmt.Sprintf("%d, %d のスプライトが見つかりません", g, n))
	}
	if h.Ver0 == 1 {
		s.Pal = pl.Get(s.palidx)
		s.palidx = -1
		return s, nil
	}
	read := func(x interface{}) error {
		return binary.Read(f, binary.LittleEndian, x)
	}
	size = 0
	indexOfPrevious = uint16(s.palidx)
	ip := indexOfPrevious + 1
	for size == 0 && ip != indexOfPrevious {
		ip = indexOfPrevious
		shofs = h.FirstPaletteHeaderOffset + uint32(ip)*16
		f.Seek(int64(shofs)+6, 0)
		if err := read(&indexOfPrevious); err != nil {
			return nil, err
		}
		if err := read(&xofs); err != nil {
			return nil, err
		}
		if err := read(&size); err != nil {
			return nil, err
		}
	}
	if s.rle != -12 {
		f.Seek(int64(lofs+xofs), 0)
		s.Pal = make([]uint32, 256)
		var rgba [4]byte
		for i := 0; i < int(size)/4 && i < len(s.Pal); i++ {
			if err := read(rgba[:]); err != nil {
				return nil, err
			}
			s.Pal[i] = uint32(rgba[2])<<16 | uint32(rgba[1])<<8 | uint32(rgba[0])
		}
	}
	s.palidx = -1
	return s, nil
}
func (s *Sprite) shareCopy(src *Sprite) {
	s.Pal = src.Pal
	s.Tex = src.Tex
	s.Size = src.Size
	s.palidx = src.palidx
}
func (s *Sprite) GetPal(pl *PalleteList) []uint32 {
	if s.Pal != nil || s.rle == -12 {
		return s.Pal
	}
	return pl.Get(int(s.palidx))
}
func (s *Sprite) SetPxl(px []byte) {
	if int64(len(px)) != int64(s.Size[0])*int64(s.Size[1]) {
		return
	}
	s.Tex = NewTexture()
	gl.BindTexture(gl.TEXTURE_2D, uint32(*s.Tex))
	gl.PixelStorei(gl.UNPACK_ALIGNMENT, 1)
	gl.TexImage2D(gl.TEXTURE_2D, 0, gl.LUMINANCE,
		int32(s.Size[0]), int32(s.Size[1]),
		0, gl.LUMINANCE, gl.UNSIGNED_BYTE, unsafe.Pointer(&px[0]))
	gl.TexParameteri(gl.TEXTURE_2D, gl.TEXTURE_MAG_FILTER, gl.NEAREST)
	gl.TexParameteri(gl.TEXTURE_2D, gl.TEXTURE_MIN_FILTER, gl.NEAREST)
	gl.TexParameteri(gl.TEXTURE_2D, gl.TEXTURE_WRAP_S, gl.CLAMP)
	gl.TexParameteri(gl.TEXTURE_2D, gl.TEXTURE_WRAP_T, gl.CLAMP)
}
func (s *Sprite) readHeader(r io.Reader, ofs *uint32, size *uint32,
	link *uint16) error {
	read := func(x interface{}) error {
		return binary.Read(r, binary.LittleEndian, x)
	}
	if err := read(ofs); err != nil {
		return err
	}
	if err := read(size); err != nil {
		return err
	}
	if err := read(s.Offset[:]); err != nil {
		return err
	}
	if err := read(&s.Group); err != nil {
		return err
	}
	if err := read(&s.Number); err != nil {
		return err
	}
	if err := read(link); err != nil {
		return err
	}
	return nil
}
func (s *Sprite) readPcxHeader(f *os.File, offset int64) error {
	f.Seek(offset, 0)
	read := func(x interface{}) error {
		return binary.Read(f, binary.LittleEndian, x)
	}
	var dummy uint16
	if err := read(&dummy); err != nil {
		return err
	}
	var encoding, bpp byte
	if err := read(&encoding); err != nil {
		return err
	}
	if err := read(&bpp); err != nil {
		return err
	}
	if bpp != 8 {
		return Error("256色でありません")
	}
	var rect [4]uint16
	if err := read(rect[:]); err != nil {
		return err
	}
	f.Seek(offset+66, 0)
	var bpl uint16
	if err := read(&bpl); err != nil {
		return err
	}
	s.Size[0] = rect[2] - rect[0] + 1
	s.Size[1] = rect[3] - rect[1] + 1
	if encoding == 1 {
		s.rle = int(bpl)
	} else {
		s.rle = 0
	}
	return nil
}
func (s *Sprite) RlePcxDecode(rle []byte) (p []byte) {
	if len(rle) == 0 || s.rle <= 0 {
		return rle
	}
	p = make([]byte, int(s.Size[0])*int(s.Size[1]))
	i, j, k, w := 0, 0, 0, int(s.Size[0])
	for j < len(p) {
		n, d := 1, rle[i]
		if i < len(rle)-1 {
			i++
		}
		if d >= 0xc0 {
			n = int(d & 0x3f)
			d = rle[i]
			if i < len(rle)-1 {
				i++
			}
		}
		for ; n > 0; n-- {
			if k < w && j < len(p) {
				p[j] = d
				j++
			}
			k++
			if k == s.rle {
				k = 0
				n = 1
			}
		}
	}
	s.rle = 0
	return
}
func (s *Sprite) read(f *os.File, sh *SffHeader, offset int64,
	datasize uint32, nextSubheader uint32, prev *Sprite,
	palletSame *bool, pl *PalleteList, c00 bool) error {
	if int64(nextSubheader) > offset {
		// 最後以外datasizeを無視
		datasize = nextSubheader - uint32(offset)
	}
	read := func(x interface{}) error {
		return binary.Read(f, binary.LittleEndian, x)
	}
	var ps byte
	if err := read(&ps); err != nil {
		return err
	}
	*palletSame = ps != 0 && prev != nil
	if err := s.readPcxHeader(f, offset); err != nil {
		return err
	}
	f.Seek(offset+128, 0)
	var palSize int
	if c00 || *palletSame {
		palSize = 0
	} else {
		palSize = 768
	}
	px := make([]byte, int(datasize)-(128+palSize))
	if err := read(px); err != nil {
		return err
	}
	if *palletSame {
		if prev != nil {
			s.palidx = prev.palidx
		}
		if s.palidx < 0 {
			s.palidx, _ = pl.NewPal()
		}
	} else {
		var pal []uint32
		s.palidx, pal = pl.NewPal()
		if c00 {
			f.Seek(offset+int64(datasize)-768, 0)
		}
		var rgb [3]byte
		for i := range pal {
			if err := read(rgb[:]); err != nil {
				return err
			}
			pal[i] = uint32(rgb[2])<<16 | uint32(rgb[1])<<8 | uint32(rgb[0])
		}
	}
	s.SetPxl(s.RlePcxDecode(px))
	return nil
}
func (s *Sprite) readHeaderV2(r io.Reader, ofs *uint32, size *uint32,
	lofs uint32, tofs uint32, link *uint16) error {
	read := func(x interface{}) error {
		return binary.Read(r, binary.LittleEndian, x)
	}
	if err := read(&s.Group); err != nil {
		return err
	}
	if err := read(&s.Number); err != nil {
		return err
	}
	if err := read(s.Size[:]); err != nil {
		return err
	}
	if err := read(s.Offset[:]); err != nil {
		return err
	}
	if err := read(link); err != nil {
		return err
	}
	var format byte
	if err := read(&format); err != nil {
		return err
	}
	s.rle = -int(format)
	var dummy byte
	if err := read(&dummy); err != nil {
		return err
	}
	if err := read(ofs); err != nil {
		return err
	}
	if err := read(size); err != nil {
		return err
	}
	var tmp uint16
	if err := read(&tmp); err != nil {
		return err
	}
	s.palidx = int(tmp)
	if err := read(&tmp); err != nil {
		return err
	}
	if tmp&1 == 0 {
		*ofs += lofs
	} else {
		*ofs += tofs
	}
	return nil
}
func (s *Sprite) Rle8Decode(rle []byte) (p []byte) {
	if len(rle) == 0 {
		return rle
	}
	p = make([]byte, int(s.Size[0])*int(s.Size[1]))
	i, j := 0, 0
	for j < len(p) {
		n, d := 1, rle[i]
		if i < len(rle)-1 {
			i++
		}
		if d&0xc0 == 0x40 {
			n = int(d & 0x3f)
			d = rle[i]
			if i < len(rle)-1 {
				i++
			}
		}
		for ; n > 0; n-- {
			if j < len(p) {
				p[j] = d
				j++
			}
		}
	}
	return
}
func (s *Sprite) Rle5Decode(rle []byte) (p []byte) {
	if len(rle) == 0 {
		return rle
	}
	p = make([]byte, int(s.Size[0])*int(s.Size[1]))
	i, j := 0, 0
	for j < len(p) {
		rl := int(rle[i])
		if i < len(rle)-1 {
			i++
		}
		dl := int(rle[i] & 0x7f)
		c := byte(0)
		if rle[i]>>7 != 0 {
			if i < len(rle)-1 {
				i++
			}
			c = rle[i]
		}
		if i < len(rle)-1 {
			i++
		}
		for {
			if j < len(p) {
				p[j] = c
				j++
			}
			rl--
			if rl < 0 {
				dl--
				if dl < 0 {
					break
				}
				c = rle[i] & 0x1f
				rl = int(rle[i] >> 5)
				if i < len(rle)-1 {
					i++
				}
			}
		}
	}
	return
}
func (s *Sprite) Lz5Decode(rle []byte) (p []byte) {
	if len(rle) == 0 {
		return rle
	}
	p = make([]byte, int(s.Size[0])*int(s.Size[1]))
	i, j, n := 0, 0, 0
	ct, cts, rb, rbc := rle[i], uint(0), byte(0), uint(0)
	if i < len(rle)-1 {
		i++
	}
	for j < len(p) {
		d := int(rle[i])
		if i < len(rle)-1 {
			i++
		}
		if ct&byte(1<<cts) != 0 {
			if d&0x3f == 0 {
				d = (d<<2 | int(rle[i])) + 1
				if i < len(rle)-1 {
					i++
				}
				n = int(rle[i]) + 2
				if i < len(rle)-1 {
					i++
				}
			} else {
				rb |= byte(d & 0xc0 >> rbc)
				rbc += 2
				n = int(d & 0x3f)
				if rbc < 8 {
					d = int(rle[i]) + 1
					if i < len(rle)-1 {
						i++
					}
				} else {
					d = int(rb) + 1
					rb, rbc = 0, 0
				}
			}
			for {
				if j < len(p) {
					p[j] = p[j-d]
					j++
				}
				n--
				if n < 0 {
					break
				}
			}
		} else {
			if d&0xe0 == 0 {
				n = int(rle[i]) + 8
				if i < len(rle)-1 {
					i++
				}
			} else {
				n = d >> 5
				d &= 0x1f
			}
			for ; n > 0; n-- {
				if j < len(p) {
					p[j] = byte(d)
					j++
				}
			}
		}
		cts++
		if cts >= 8 {
			ct, cts = rle[i], 0
			if i < len(rle)-1 {
				i++
			}
		}
	}
	return
}
func (s *Sprite) readV2(f *os.File, offset int64, datasize uint32) error {
	f.Seek(offset+4, 0)
	if s.rle < 0 {
		format := -s.rle
		var px []byte
		if 2 <= format && format <= 4 {
			px = make([]byte, datasize)
			if err := binary.Read(f, binary.LittleEndian, px); err != nil {
				return err
			}
		}
		getIHDR := func() (png_ptr C.png_structp, info_ptr C.png_infop,
			width, height C.png_uint_32, bit_depth, color_type C.int,
			ok bool, err error) {
			png_sig := make([]C.png_byte, 8)
			if err = binary.Read(f, binary.LittleEndian, png_sig); err != nil {
				return
			}
			if C.png_sig_cmp(&png_sig[0], 0, 8) != 0 {
				err = Error("png_sig_cmp failed")
				return
			}
			png_ptr = C.png_create_read_struct(C.CString(C.PNG_LIBPNG_VER_STRING),
				nil, nil, nil)
			if png_ptr == nil {
				err = Error("png_create_read_struct failed")
				return
			}
			info_ptr = C.png_create_info_struct(png_ptr)
			if info_ptr == nil {
				C.png_destroy_read_struct(&png_ptr, nil, nil)
				err = Error("png_create_info_struct failed")
				return
			}
			C.png_init_io(png_ptr, C.fdopen(C.int(f.Fd()), C.CString("rb")))
			C.png_set_sig_bytes(png_ptr, 8)
			C.png_read_info(png_ptr, info_ptr)
			ok = C.png_get_IHDR(png_ptr, info_ptr, &width, &height,
				&bit_depth, &color_type, nil, nil, nil) != 0
			return
		}
		switch format {
		case 2:
			px = s.Rle8Decode(px)
		case 3:
			px = s.Rle5Decode(px)
		case 4:
			px = s.Lz5Decode(px)
		case 10:
			png_ptr, info_ptr, width, height, bit_depth, color_type, ok, err :=
				getIHDR()
			if err != nil {
				return err
			}
			if ok && color_type == C.PNG_COLOR_TYPE_PALETTE && bit_depth <= 8 {
				px = make([]byte, int(width*height))
				pp := make([]*C.png_byte, int(height))
				for i := range pp {
					pp[i] = (*C.png_byte)(&px[i*int(width)])
				}
				C.png_read_image(png_ptr, &pp[0])
				switch bit_depth {
				case 1:
					for y := range pp {
						for i := width - 1; i >= 0; i-- {
							p := (*[1 << 30]byte)(unsafe.Pointer(pp[y]))[:width:width]
							p[i] = p[i>>3] & (1 << uint(i&7)) >> uint(i&7)
						}
					}
				case 2:
					for y := range pp {
						for i := width - 1; i >= 0; i-- {
							p := (*[1 << 30]byte)(unsafe.Pointer(pp[y]))[:width:width]
							p[i] = p[i>>2] & (3 << uint(i&3*2)) >> uint(i&3*2)
						}
					}
				case 4:
					for y := range pp {
						for i := width - 1; i >= 0; i-- {
							p := (*[1 << 30]byte)(unsafe.Pointer(pp[y]))[:width:width]
							p[i] = p[i>>1] & (15 << uint(i&1*4)) >> uint(i&1*4)
						}
					}
				}
			}
			C.png_destroy_read_struct(&png_ptr, &info_ptr, nil)
		case 11, 12:
			s.rle = -12
			png_ptr, info_ptr, width, height, bit_depth, color_type, ok, err :=
				getIHDR()
			if err != nil {
				return err
			}
			if ok {
				if bit_depth > 8 {
					C.png_set_strip_16(png_ptr)
				}
				C.png_set_expand(png_ptr)
				if color_type&C.PNG_COLOR_MASK_ALPHA == 0 {
					C.png_set_add_alpha(png_ptr, 0xFF, C.PNG_FILLER_AFTER)
				}
				px = make([]byte, int(width*height*4))
				pp := make([]*C.png_byte, int(height))
				for i := range pp {
					pp[i] = (*C.png_byte)(&px[i*int(width)*4])
				}
				C.png_read_image(png_ptr, &pp[0])
				s.Tex = NewTexture()
				gl.BindTexture(gl.TEXTURE_2D, uint32(*s.Tex))
				gl.PixelStorei(gl.UNPACK_ALIGNMENT, 1)
				gl.TexImage2D(gl.TEXTURE_2D, 0, gl.RGBA, int32(width), int32(height),
					0, gl.RGBA, gl.UNSIGNED_BYTE, unsafe.Pointer(&px[0]))
				gl.TexParameteri(gl.TEXTURE_2D, gl.TEXTURE_MAG_FILTER, gl.LINEAR)
				gl.TexParameteri(gl.TEXTURE_2D, gl.TEXTURE_MIN_FILTER, gl.LINEAR)
				gl.TexParameteri(gl.TEXTURE_2D, gl.TEXTURE_WRAP_S, gl.CLAMP)
				gl.TexParameteri(gl.TEXTURE_2D, gl.TEXTURE_WRAP_T, gl.CLAMP)
			}
			C.png_destroy_read_struct(&png_ptr, &info_ptr, nil)
			return nil
		default:
			return Error("不明な形式です")
		}
		s.SetPxl(px)
	}
	return nil
}
func (s *Sprite) glDraw(pal []uint32, mask int32, x, y float32, tile *[4]int32,
	xts, xbs, ys, rxadd, agl float32, trans int32, window *[4]int32,
	rcx, rcy float32, fx *PalFX) {
	if s.Tex == nil {
		return
	}
	if s.rle == -12 {
		neg, color, padd, pmul := fx.GetFcPalFx(trans)
		RenderMugenFc(*s.Tex, s.Size, x, y, tile, xts, xbs, ys, 1, rxadd, agl,
			trans, window, rcx, rcy, neg, color, &padd, &pmul)
	} else {
		RenderMugen(*s.Tex, pal, mask, s.Size, x, y, tile, xts, xbs, ys, 1,
			rxadd, agl, trans, window, rcx, rcy)
	}
}
func (s *Sprite) Draw(x, y, xscale, yscale float32, pal []uint32) {
	x -= xscale*float32(s.Offset[0]) + float32(GameWidth-320)/2
	y -= yscale*float32(s.Offset[1]) + float32(GameHeight-240)
	if xscale < 0 {
		x *= -1
	}
	if yscale < 0 {
		y *= -1
	}
	s.glDraw(pal, 0, -x*WidthScale, -y*HeightScale, &notiling,
		xscale*WidthScale, xscale*WidthScale, yscale*HeightScale, 0, 0, 255,
		&scrrect, 0, 0, nil)
}