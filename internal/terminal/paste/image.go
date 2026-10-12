package paste

import (
	"bytes"
	"encoding/binary"
)

// sniffClipboardImage recognizes the accepted formats and reports whether their
// framing contains a complete image. It intentionally does not decode pixels or verify checksums.
func sniffClipboardImage(data []byte) (string, bool) {
	switch {
	case len(data) >= 8 && bytes.Equal(data[:8], []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}):
		return "image/png", completePNG(data)
	case len(data) >= 6 && (string(data[:6]) == "GIF87a" || string(data[:6]) == "GIF89a"):
		return "image/gif", completeGIF(data)
	case len(data) >= 3 && data[0] == 0xff && data[1] == 0xd8 && data[2] == 0xff:
		return "image/jpeg", completeJPEG(data)
	case len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP":
		return "image/webp", completeWebP(data)
	default:
		return "", false
	}
}

func completePNG(data []byte) bool {
	offset := 8
	sawIHDR, sawIDAT := false, false
	for offset <= len(data) {
		remaining := len(data) - offset
		if remaining < 12 {
			return false
		}
		chunkLength := uint64(binary.BigEndian.Uint32(data[offset : offset+4]))
		if chunkLength > uint64(remaining-12) {
			return false
		}
		chunkType := data[offset+4 : offset+8]
		if !sawIHDR {
			if string(chunkType) != "IHDR" || chunkLength != 13 {
				return false
			}
			sawIHDR = true
		} else if string(chunkType) == "IDAT" {
			sawIDAT = true
		} else if string(chunkType) == "IEND" {
			return chunkLength == 0 && sawIDAT
		}
		offset += 12 + int(chunkLength)
	}
	return false
}

func completeJPEG(data []byte) bool {
	offset := 2
	sawScan := false
	for offset < len(data) {
		if data[offset] != 0xff {
			return false
		}
		offset++
		for offset < len(data) && data[offset] == 0xff {
			offset++
		}
		if offset >= len(data) {
			return false
		}
		marker := data[offset]
		offset++
		if marker == 0x00 {
			return false
		}
		if marker == 0xd9 {
			return sawScan
		}
		if marker == 0x01 || marker >= 0xd0 && marker <= 0xd7 {
			continue
		}
		if len(data)-offset < 2 {
			return false
		}
		segmentLength := int(binary.BigEndian.Uint16(data[offset : offset+2]))
		if segmentLength < 2 || segmentLength > len(data)-offset {
			return false
		}
		offset += segmentLength
		if marker != 0xda {
			continue
		}
		sawScan = true
		for offset+1 < len(data) {
			if data[offset] != 0xff {
				offset++
				continue
			}
			escaped := data[offset+1]
			if escaped == 0x00 || escaped >= 0xd0 && escaped <= 0xd7 {
				offset += 2
				continue
			}
			if escaped == 0xff {
				offset++
				continue
			}
			break
		}
	}
	return false
}

func completeGIF(data []byte) bool {
	if len(data) < 13 {
		return false
	}
	offset := 13
	if data[10]&0x80 != 0 {
		colorTableLength := 3 << ((data[10] & 0x07) + 1)
		if int(colorTableLength) > len(data)-offset {
			return false
		}
		offset += int(colorTableLength)
	}
	sawImage := false
	for offset < len(data) {
		switch data[offset] {
		case 0x3b:
			return sawImage && offset+1 == len(data)
		case 0x21:
			if len(data)-offset < 2 {
				return false
			}
			offset += 2
			if !skipGIFSubBlocks(data, &offset, nil) {
				return false
			}
		case 0x2c:
			if len(data)-offset < 10 {
				return false
			}
			flags := data[offset+9]
			offset += 10
			if flags&0x80 != 0 {
				colorTableLength := 3 << ((flags & 0x07) + 1)
				if int(colorTableLength) > len(data)-offset {
					return false
				}
				offset += int(colorTableLength)
			}
			if offset >= len(data) {
				return false
			}
			offset++ // LZW minimum code size
			sawCompressedData := false
			if !skipGIFSubBlocks(data, &offset, &sawCompressedData) || !sawCompressedData {
				return false
			}
			sawImage = true
		default:
			return false
		}
	}
	return false
}

func skipGIFSubBlocks(data []byte, offset *int, sawData *bool) bool {
	for *offset < len(data) {
		blockLength := int(data[*offset])
		*offset++
		if blockLength == 0 {
			return true
		}
		if blockLength > len(data)-*offset {
			return false
		}
		if sawData != nil {
			*sawData = true
		}
		*offset += blockLength
	}
	return false
}

func completeWebP(data []byte) bool {
	riffLength := uint64(binary.LittleEndian.Uint32(data[4:8]))
	if riffLength < 4 || riffLength > uint64(len(data)-8) {
		return false
	}
	chunksLength := int(riffLength - 4)
	offset := 0
	sawImageData := false
	for offset < chunksLength {
		chunkType, chunkData, chunkLength, next, ok := nextWebPChunk(data[12:12+chunksLength], offset)
		if !ok {
			return false
		}
		switch chunkType {
		case "VP8 ":
			if !validWebPLossy(chunkData) {
				return false
			}
			sawImageData = true
		case "VP8L":
			if !validWebPLossless(chunkData) {
				return false
			}
			sawImageData = true
		case "ANMF":
			if !completeWebPFrame(chunkData) {
				return false
			}
			sawImageData = true
		}
		_ = chunkLength
		offset = next
	}
	return sawImageData
}

func completeWebPFrame(data []byte) bool {
	if len(data) < 16 {
		return false
	}
	offset, sawImageData := 16, false
	for offset < len(data) {
		chunkType, chunkData, _, next, ok := nextWebPChunk(data, offset)
		if !ok {
			return false
		}
		if chunkType == "VP8 " {
			if !validWebPLossy(chunkData) {
				return false
			}
			sawImageData = true
		} else if chunkType == "VP8L" {
			if !validWebPLossless(chunkData) {
				return false
			}
			sawImageData = true
		}
		offset = next
	}
	return sawImageData
}

func nextWebPChunk(data []byte, offset int) (string, []byte, int, int, bool) {
	if offset < 0 || offset > len(data) || len(data)-offset < 8 {
		return "", nil, 0, 0, false
	}
	length := uint64(binary.LittleEndian.Uint32(data[offset+4 : offset+8]))
	if length > uint64(len(data)-offset-8) {
		return "", nil, 0, 0, false
	}
	end := offset + 8 + int(length)
	if length&1 != 0 {
		if end >= len(data) {
			return "", nil, 0, 0, false
		}
		end++
	}
	return string(data[offset : offset+4]), data[offset+8 : offset+8+int(length)], int(length), end, true
}

func validWebPLossy(data []byte) bool {
	return len(data) >= 10 && data[3] == 0x9d && data[4] == 0x01 && data[5] == 0x2a
}

func validWebPLossless(data []byte) bool { return len(data) >= 5 && data[0] == 0x2f }
