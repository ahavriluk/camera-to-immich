package denoise

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// JPEG marker constants used for surgical APP1 (EXIF) splicing.
const (
	jpegMarkerSOI  = 0xFFD8 // Start Of Image
	jpegMarkerAPP0 = 0xFFE0 // JFIF
	jpegMarkerAPP1 = 0xFFE1 // EXIF / XMP
	jpegMarkerSOS  = 0xFFDA // Start Of Scan (after this is entropy-coded data)
	jpegMarkerEOI  = 0xFFD9 // End Of Image
)

var (
	errNotJPEG          = errors.New("not a JPEG (missing SOI marker)")
	errTruncatedJPEG    = errors.New("truncated JPEG (segment runs past end)")
	errNoEXIFSegment    = errors.New("no EXIF (APP1) segment found in source")
	errNoInsertionPoint = errors.New("could not find a place to insert APP1 (corrupt JPEG?)")
)

// extractEXIFSegment locates the first APP1 segment in a JPEG byte stream that
// is identified as EXIF (header "Exif\x00\x00") and returns the entire
// segment INCLUDING the 0xFFE1 marker and its two-byte length field.
// The returned slice is suitable for direct injection into another JPEG via
// injectEXIFSegment.
//
// Returns errNoEXIFSegment if the JPEG has no EXIF data (e.g. a stripped
// re-encoded JPEG) -- callers should treat this as a soft "nothing to copy"
// rather than a hard error.
func extractEXIFSegment(jpegBytes []byte) ([]byte, error) {
	if len(jpegBytes) < 4 || binary.BigEndian.Uint16(jpegBytes[0:2]) != jpegMarkerSOI {
		return nil, errNotJPEG
	}

	i := 2
	for i+4 <= len(jpegBytes) {
		// Every marker starts with 0xFF; skip any 0xFF padding.
		if jpegBytes[i] != 0xFF {
			return nil, fmt.Errorf("malformed JPEG at offset %d: expected 0xFF marker prefix, got 0x%02X", i, jpegBytes[i])
		}
		for i < len(jpegBytes) && jpegBytes[i] == 0xFF {
			i++
		}
		if i >= len(jpegBytes) {
			break
		}
		marker := uint16(0xFF00) | uint16(jpegBytes[i])
		i++ // step past marker byte

		// SOI / EOI / restart markers (0xFFD0..0xFFD7) have no payload.
		if marker == jpegMarkerSOI || marker == jpegMarkerEOI ||
			(marker >= 0xFFD0 && marker <= 0xFFD7) {
			continue
		}
		// Reached compressed data without finding an APP1 EXIF segment.
		if marker == jpegMarkerSOS {
			return nil, errNoEXIFSegment
		}

		if i+2 > len(jpegBytes) {
			return nil, errTruncatedJPEG
		}
		segLen := int(binary.BigEndian.Uint16(jpegBytes[i : i+2])) // includes the 2 length bytes
		if segLen < 2 || i+segLen > len(jpegBytes) {
			return nil, errTruncatedJPEG
		}

		// Is this an APP1 EXIF segment?
		if marker == jpegMarkerAPP1 {
			// Payload starts at i+2 (after length field).
			payloadStart := i + 2
			payloadEnd := i + segLen
			payload := jpegBytes[payloadStart:payloadEnd]
			if len(payload) >= 6 &&
				payload[0] == 'E' && payload[1] == 'x' && payload[2] == 'i' && payload[3] == 'f' &&
				payload[4] == 0x00 && payload[5] == 0x00 {
				// Return the full segment: marker (2 bytes) + length (2 bytes) + payload.
				full := make([]byte, 2+segLen)
				binary.BigEndian.PutUint16(full[0:2], jpegMarkerAPP1)
				copy(full[2:], jpegBytes[i:i+segLen])
				return full, nil
			}
		}

		i += segLen
	}

	return nil, errNoEXIFSegment
}

// injectEXIFSegment inserts the given pre-formed APP1 EXIF segment into a
// JPEG byte stream, replacing any existing APP1 EXIF segment if present.
// The new segment is placed immediately after SOI (and after any leading
// APP0/JFIF marker, which by convention precedes APP1).
//
// `exifSeg` must be a full segment as returned by extractEXIFSegment --
// i.e. it must begin with 0xFFE1 followed by a 2-byte length.
func injectEXIFSegment(jpegBytes, exifSeg []byte) ([]byte, error) {
	if len(jpegBytes) < 4 || binary.BigEndian.Uint16(jpegBytes[0:2]) != jpegMarkerSOI {
		return nil, errNotJPEG
	}
	if len(exifSeg) < 4 || binary.BigEndian.Uint16(exifSeg[0:2]) != jpegMarkerAPP1 {
		return nil, fmt.Errorf("injectEXIFSegment: exifSeg does not start with APP1 marker")
	}

	// Walk markers in jpegBytes. Identify:
	//   - insertion point (right after SOI, or right after the optional
	//     leading JFIF/APP0 segment)
	//   - any existing APP1-EXIF segment to remove
	insertAt := 2 // right after SOI by default
	removeStart, removeEnd := -1, -1

	i := 2
	sawAPP0 := false
	for i+4 <= len(jpegBytes) {
		if jpegBytes[i] != 0xFF {
			return nil, fmt.Errorf("malformed JPEG at offset %d (no 0xFF marker prefix)", i)
		}
		for i < len(jpegBytes) && jpegBytes[i] == 0xFF {
			i++
		}
		if i >= len(jpegBytes) {
			break
		}
		marker := uint16(0xFF00) | uint16(jpegBytes[i])
		markerStart := i - 1 // index of the 0xFF byte
		i++

		if marker == jpegMarkerSOI || marker == jpegMarkerEOI ||
			(marker >= 0xFFD0 && marker <= 0xFFD7) {
			continue
		}
		if marker == jpegMarkerSOS {
			break // entropy-coded data; stop scanning
		}
		if i+2 > len(jpegBytes) {
			return nil, errTruncatedJPEG
		}
		segLen := int(binary.BigEndian.Uint16(jpegBytes[i : i+2]))
		if segLen < 2 || i+segLen > len(jpegBytes) {
			return nil, errTruncatedJPEG
		}

		if marker == jpegMarkerAPP0 && !sawAPP0 {
			sawAPP0 = true
			// Insert AFTER the JFIF segment.
			insertAt = markerStart + 2 + segLen
		} else if marker == jpegMarkerAPP1 {
			payloadStart := i + 2
			payloadEnd := i + segLen
			payload := jpegBytes[payloadStart:payloadEnd]
			if len(payload) >= 6 &&
				payload[0] == 'E' && payload[1] == 'x' && payload[2] == 'i' && payload[3] == 'f' &&
				payload[4] == 0x00 && payload[5] == 0x00 {
				removeStart = markerStart
				removeEnd = markerStart + 2 + segLen
			}
		}

		i += segLen
	}

	if insertAt < 2 || insertAt > len(jpegBytes) {
		return nil, errNoInsertionPoint
	}

	// Build the result: [prefix][exif][middle][suffix], optionally removing
	// the old APP1-EXIF segment if present.
	var out []byte
	if removeStart < 0 {
		// Simple insertion.
		out = make([]byte, 0, len(jpegBytes)+len(exifSeg))
		out = append(out, jpegBytes[:insertAt]...)
		out = append(out, exifSeg...)
		out = append(out, jpegBytes[insertAt:]...)
		return out, nil
	}

	// Existing APP1-EXIF: remove it and insert the new one at insertAt.
	// Handle both orderings of insertAt vs removeStart correctly.
	out = make([]byte, 0, len(jpegBytes)-(removeEnd-removeStart)+len(exifSeg))
	if insertAt <= removeStart {
		out = append(out, jpegBytes[:insertAt]...)
		out = append(out, exifSeg...)
		out = append(out, jpegBytes[insertAt:removeStart]...)
		out = append(out, jpegBytes[removeEnd:]...)
	} else {
		// insertAt is past the removed region (e.g. existing APP1 came
		// before the desired insertion point). Splice carefully.
		out = append(out, jpegBytes[:removeStart]...)
		out = append(out, jpegBytes[removeEnd:insertAt]...)
		out = append(out, exifSeg...)
		out = append(out, jpegBytes[insertAt:]...)
	}
	return out, nil
}