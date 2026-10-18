package mime

import (
	"errors"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/japanese"
	"golang.org/x/text/encoding/korean"
	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/encoding/traditionalchinese"
)

type errMode int

const (
	errStrict errMode = iota
	errReplace
	errSurrogateEscape
)

var (
	errLookup = errors.New("unknown encoding")
	errDecode = errors.New("undecodable bytes")
)

type decodeFunc func(b []byte, mode errMode) ([]rune, error)

type badRange struct {
	out  []rune
	mode errMode
}

func (d *badRange) bad(b []byte) error {
	switch d.mode {
	case errReplace:
		d.out = append(d.out, utf8.RuneError)
	case errSurrogateEscape:
		for _, c := range b {
			if c < 0x80 {
				return errDecode
			}
			d.out = append(d.out, surrogateBase+rune(c))
		}
	default:
		return errDecode
	}
	return nil
}

func decodeUTF8(b []byte, mode errMode) []rune {
	out, _ := decodeUTF8Err(b, mode)
	return out
}

func decodeUTF8Err(b []byte, mode errMode) ([]rune, error) {
	d := badRange{out: make([]rune, 0, len(b)), mode: mode}
	for i := 0; i < len(b); {
		c := b[i]
		if c < 0x80 {
			d.out = append(d.out, rune(c))
			i++
			continue
		}
		n, lo, hi := 0, byte(0x80), byte(0xBF)
		switch {
		case c >= 0xC2 && c <= 0xDF:
			n = 1
		case c == 0xE0:
			n, lo = 2, 0xA0
		case c == 0xED:
			n, hi = 2, 0x9F
		case c >= 0xE1 && c <= 0xEF:
			n = 2
		case c == 0xF0:
			n, lo = 3, 0x90
		case c == 0xF4:
			n, hi = 3, 0x8F
		case c >= 0xF1 && c <= 0xF3:
			n = 3
		}
		valid := 1
		for valid <= n && i+valid < len(b) {
			x := b[i+valid]
			if valid == 1 && (x < lo || x > hi) || valid > 1 && (x < 0x80 || x > 0xBF) {
				break
			}
			valid++
		}
		if n == 0 || valid <= n {
			if err := d.bad(b[i : i+valid]); err != nil {
				return nil, err
			}
			i += valid
			continue
		}
		r, size := utf8.DecodeRune(b[i:])
		d.out = append(d.out, r)
		i += size
	}
	return d.out, nil
}

func decodeASCII(b []byte, mode errMode) ([]rune, error) {
	d := badRange{out: make([]rune, 0, len(b)), mode: mode}
	for i, c := range b {
		if c < 0x80 {
			d.out = append(d.out, rune(c))
		} else if err := d.bad(b[i : i+1]); err != nil {
			return nil, err
		}
	}
	return d.out, nil
}

func decodeLatin1(b []byte, _ errMode) ([]rune, error) {
	out := make([]rune, len(b))
	for i, c := range b {
		out[i] = rune(c)
	}
	return out, nil
}

func charmapDecoder(table string) decodeFunc {
	runes := []rune(table)
	return func(b []byte, mode errMode) ([]rune, error) {
		d := badRange{out: make([]rune, 0, len(b)), mode: mode}
		for i, c := range b {
			if r := runes[c]; r != 0xFFFE {
				d.out = append(d.out, r)
			} else if err := d.bad(b[i : i+1]); err != nil {
				return nil, err
			}
		}
		return d.out, nil
	}
}

func utf8SigDecode(b []byte, mode errMode) ([]rune, error) {
	if len(b) >= 3 && b[0] == 0xEF && b[1] == 0xBB && b[2] == 0xBF {
		b = b[3:]
	}
	return decodeUTF8Err(b, mode)
}

func utf16Decoder(bigEndian, bom bool) decodeFunc {
	return func(b []byte, mode errMode) ([]rune, error) {
		big := bigEndian
		if bom && len(b) >= 2 {
			switch {
			case b[0] == 0xFF && b[1] == 0xFE:
				big, b = false, b[2:]
			case b[0] == 0xFE && b[1] == 0xFF:
				big, b = true, b[2:]
			}
		}
		unit := func(i int) rune {
			if big {
				return rune(b[i])<<8 | rune(b[i+1])
			}
			return rune(b[i+1])<<8 | rune(b[i])
		}
		d := badRange{out: make([]rune, 0, len(b)/2), mode: mode}
		i := 0
		for i+1 < len(b) {
			u := unit(i)
			switch {
			case u < 0xD800 || u > 0xDFFF:
				d.out = append(d.out, u)
				i += 2
			case u >= 0xDC00:
				if err := d.bad(b[i : i+2]); err != nil {
					return nil, err
				}
				i += 2
			case i+3 >= len(b):
				if err := d.bad(b[i:]); err != nil {
					return nil, err
				}
				i = len(b)
			default:
				v := unit(i + 2)
				if v < 0xDC00 || v > 0xDFFF {
					if err := d.bad(b[i : i+2]); err != nil {
						return nil, err
					}
					i += 2
					continue
				}
				d.out = append(d.out, 0x10000+(u-0xD800)<<10+(v-0xDC00))
				i += 4
			}
		}
		if i < len(b) {
			if err := d.bad(b[i:]); err != nil {
				return nil, err
			}
		}
		return d.out, nil
	}
}

func utf32Decoder(bigEndian, bom bool) decodeFunc {
	return func(b []byte, mode errMode) ([]rune, error) {
		big := bigEndian
		if bom && len(b) >= 4 {
			switch {
			case b[0] == 0xFF && b[1] == 0xFE && b[2] == 0 && b[3] == 0:
				big, b = false, b[4:]
			case b[0] == 0 && b[1] == 0 && b[2] == 0xFE && b[3] == 0xFF:
				big, b = true, b[4:]
			}
		}
		d := badRange{out: make([]rune, 0, len(b)/4), mode: mode}
		i := 0
		for ; i+3 < len(b); i += 4 {
			var u uint32
			if big {
				u = uint32(b[i])<<24 | uint32(b[i+1])<<16 | uint32(b[i+2])<<8 | uint32(b[i+3])
			} else {
				u = uint32(b[i+3])<<24 | uint32(b[i+2])<<16 | uint32(b[i+1])<<8 | uint32(b[i])
			}
			if u > 0x10FFFF || u >= 0xD800 && u <= 0xDFFF {
				if err := d.bad(b[i : i+4]); err != nil {
					return nil, err
				}
				continue
			}
			d.out = append(d.out, rune(u))
		}
		if i < len(b) {
			if err := d.bad(b[i:]); err != nil {
				return nil, err
			}
		}
		return d.out, nil
	}
}

func xtextDecoder(enc encoding.Encoding) decodeFunc {
	return func(b []byte, mode errMode) ([]rune, error) {
		out, err := enc.NewDecoder().Bytes(b)
		if err != nil {
			return nil, errDecode
		}
		if mode == errStrict && strings.ContainsRune(string(out), utf8.RuneError) {
			return nil, errDecode
		}
		return []rune(string(out)), nil
	}
}

var codecs = map[string]decodeFunc{
	"ascii":        decodeASCII,
	"latin_1":      decodeLatin1,
	"utf_8":        decodeUTF8Err,
	"utf_8_sig":    utf8SigDecode,
	"utf_16":       utf16Decoder(false, true),
	"utf_16_le":    utf16Decoder(false, false),
	"utf_16_be":    utf16Decoder(true, false),
	"utf_32":       utf32Decoder(false, true),
	"utf_32_le":    utf32Decoder(false, false),
	"utf_32_be":    utf32Decoder(true, false),
	"shift_jis":    xtextDecoder(japanese.ShiftJIS),
	"cp932":        xtextDecoder(japanese.ShiftJIS),
	"euc_jp":       xtextDecoder(japanese.EUCJP),
	"iso2022_jp":   xtextDecoder(japanese.ISO2022JP),
	"gb2312":       xtextDecoder(simplifiedchinese.GBK),
	"gbk":          xtextDecoder(simplifiedchinese.GBK),
	"gb18030":      xtextDecoder(simplifiedchinese.GB18030),
	"hz":           xtextDecoder(simplifiedchinese.HZGB2312),
	"big5":         xtextDecoder(traditionalchinese.Big5),
	"cp950":        xtextDecoder(traditionalchinese.Big5),
	"big5hkscs":    xtextDecoder(traditionalchinese.Big5),
	"euc_kr":       xtextDecoder(korean.EUCKR),
	"cp949":        xtextDecoder(korean.EUCKR),
	"iso2022_jp_1": xtextDecoder(japanese.ISO2022JP),
}

func init() {
	for name, table := range pyCharmaps {
		codecs[name] = charmapDecoder(table)
	}
}

func normalizeEncoding(name string) string {
	var b strings.Builder
	punct := false
	for i := range len(name) {
		c := name[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		if c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '.' {
			if punct && b.Len() > 0 {
				b.WriteByte('_')
			}
			b.WriteByte(c)
			punct = false
		} else {
			punct = true
		}
	}
	return b.String()
}

func lookupCodec(name string) (decodeFunc, error) {
	if strings.IndexByte(name, 0) >= 0 {
		return nil, errLookup
	}
	norm := normalizeEncoding(name)
	var candidates []string
	if alias, ok := pyCodecAliases[norm]; ok {
		candidates = append(candidates, alias, norm)
	} else if alias, ok := pyCodecAliases[strings.ReplaceAll(norm, ".", "_")]; ok {
		candidates = append(candidates, alias, norm)
	} else {
		candidates = append(candidates, norm)
	}
	for _, mod := range candidates {
		if mod == "" || strings.Contains(mod, ".") || !pyCodecModules[mod] {
			continue
		}
		if f, ok := codecs[mod]; ok {
			return f, nil
		}
		return nil, errLookup
	}
	return nil, errLookup
}

func decodeBytes(b []byte, charset string, mode errMode) ([]rune, error) {
	f, err := lookupCodec(charset)
	if err != nil {
		return nil, err
	}
	return f(b, mode)
}
