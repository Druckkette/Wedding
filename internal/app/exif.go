package app

import (
	"encoding/binary"
	"io"
	"math"
	"os"
	"strings"
	"time"
)

// readImageMetadata only reads the file. It never rewrites or normalizes the
// original. JPEG/TIFF capture time, GPS and dimensions plus PNG dimensions are
// extracted; unsupported containers safely fall back to filesystem time.
func readImageMetadata(path string) cachedImageMetadata {
	file, err := os.Open(path)
	if err != nil {
		return cachedImageMetadata{}
	}
	defer file.Close()
	header := make([]byte, 32)
	n, _ := io.ReadFull(file, header)
	header = header[:n]
	if len(header) >= 24 && string(header[1:4]) == "PNG" {
		return cachedImageMetadata{Width: int(binary.BigEndian.Uint32(header[16:20])), Height: int(binary.BigEndian.Uint32(header[20:24]))}
	}
	if len(header) < 2 || header[0] != 0xff || header[1] != 0xd8 {
		return cachedImageMetadata{}
	}
	_, _ = file.Seek(2, io.SeekStart)
	metadata := cachedImageMetadata{}
	for {
		var marker [2]byte
		if _, err := io.ReadFull(file, marker[:]); err != nil {
			break
		}
		if marker[0] != 0xff {
			break
		}
		for marker[1] == 0xff {
			if _, err := io.ReadFull(file, marker[1:]); err != nil {
				return metadata
			}
		}
		if marker[1] == 0xda || marker[1] == 0xd9 {
			break
		}
		var lengthBytes [2]byte
		if _, err := io.ReadFull(file, lengthBytes[:]); err != nil {
			break
		}
		length := int(binary.BigEndian.Uint16(lengthBytes[:])) - 2
		if length < 0 || length > 16<<20 {
			break
		}
		data := make([]byte, length)
		if _, err := io.ReadFull(file, data); err != nil {
			break
		}
		if marker[1] == 0xe1 && len(data) > 6 && string(data[:6]) == "Exif\x00\x00" {
			readTIFFMetadata(data[6:], &metadata)
		}
		if isJPEGSOF(marker[1]) && len(data) >= 5 {
			metadata.Height = int(binary.BigEndian.Uint16(data[1:3]))
			metadata.Width = int(binary.BigEndian.Uint16(data[3:5]))
		}
	}
	return metadata
}

func isJPEGSOF(marker byte) bool {
	return marker >= 0xc0 && marker <= 0xcf && marker != 0xc4 && marker != 0xc8 && marker != 0xcc
}

type tiffReader struct {
	data  []byte
	order binary.ByteOrder
}

func readTIFFMetadata(data []byte, result *cachedImageMetadata) {
	if len(data) < 8 {
		return
	}
	var order binary.ByteOrder
	switch string(data[:2]) {
	case "II":
		order = binary.LittleEndian
	case "MM":
		order = binary.BigEndian
	default:
		return
	}
	reader := tiffReader{data: data, order: order}
	root := reader.u32(4)
	exifOffset, gpsOffset := reader.readIFD(root, result)
	if exifOffset > 0 {
		reader.readIFD(exifOffset, result)
	}
	if gpsOffset > 0 {
		reader.readGPS(gpsOffset, result)
	}
}

func (r tiffReader) u16(offset uint32) uint16 {
	if uint64(offset)+2 > uint64(len(r.data)) {
		return 0
	}
	return r.order.Uint16(r.data[offset : offset+2])
}

func (r tiffReader) u32(offset uint32) uint32 {
	if uint64(offset)+4 > uint64(len(r.data)) {
		return 0
	}
	return r.order.Uint32(r.data[offset : offset+4])
}

func (r tiffReader) readIFD(offset uint32, result *cachedImageMetadata) (uint32, uint32) {
	count := uint32(r.u16(offset))
	if count > 512 || uint64(offset)+2+uint64(count)*12 > uint64(len(r.data)) {
		return 0, 0
	}
	var exifOffset, gpsOffset uint32
	for index := uint32(0); index < count; index++ {
		entry := offset + 2 + index*12
		tag, typ, amount := r.u16(entry), r.u16(entry+2), r.u32(entry+4)
		value := r.u32(entry + 8)
		switch tag {
		case 0x8769:
			exifOffset = value
		case 0x8825:
			gpsOffset = value
		case 0x9003, 0x9004, 0x0132:
			priority := 1
			if tag == 0x9004 {
				priority = 2
			}
			if tag == 0x9003 {
				priority = 3
			}
			if priority > result.capturePriority && typ == 2 && amount >= 19 {
				if text := r.ascii(entry, amount); text != "" {
					if parsed, err := time.Parse("2006:01:02 15:04:05", strings.TrimSpace(text)); err == nil {
						result.CapturedAt = parsed.Format("2006-01-02T15:04:05")
						result.capturePriority = priority
					}
				}
			}
		case 0xa002:
			result.Width = int(value)
		case 0xa003:
			result.Height = int(value)
		}
	}
	return exifOffset, gpsOffset
}

func (r tiffReader) ascii(entry, count uint32) string {
	var start uint32
	if count <= 4 {
		start = entry + 8
	} else {
		start = r.u32(entry + 8)
	}
	if uint64(start)+uint64(count) > uint64(len(r.data)) {
		return ""
	}
	return strings.TrimRight(string(r.data[start:start+count]), "\x00")
}

func (r tiffReader) readGPS(offset uint32, result *cachedImageMetadata) {
	count := uint32(r.u16(offset))
	if count > 128 || uint64(offset)+2+uint64(count)*12 > uint64(len(r.data)) {
		return
	}
	var latRef, lonRef string
	var lat, lon []float64
	for index := uint32(0); index < count; index++ {
		entry := offset + 2 + index*12
		tag, amount := r.u16(entry), r.u32(entry+4)
		switch tag {
		case 1:
			latRef = r.ascii(entry, amount)
		case 2:
			lat = r.rationals(r.u32(entry+8), amount)
		case 3:
			lonRef = r.ascii(entry, amount)
		case 4:
			lon = r.rationals(r.u32(entry+8), amount)
		}
	}
	if len(lat) == 3 {
		value := lat[0] + lat[1]/60 + lat[2]/3600
		if strings.EqualFold(latRef, "S") {
			value = -value
		}
		result.Latitude = &value
	}
	if len(lon) == 3 {
		value := lon[0] + lon[1]/60 + lon[2]/3600
		if strings.EqualFold(lonRef, "W") {
			value = -value
		}
		result.Longitude = &value
	}
}

func (r tiffReader) rationals(offset, count uint32) []float64 {
	if count > 16 || uint64(offset)+uint64(count)*8 > uint64(len(r.data)) {
		return nil
	}
	values := make([]float64, 0, count)
	for index := uint32(0); index < count; index++ {
		numerator := r.u32(offset + index*8)
		denominator := r.u32(offset + index*8 + 4)
		if denominator == 0 {
			return nil
		}
		value := float64(numerator) / float64(denominator)
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return nil
		}
		values = append(values, value)
	}
	return values
}
