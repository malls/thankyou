package render

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// woffToTTF decompresses a WOFF (version 1) font file into the equivalent
// SFNT (TTF/OTF) byte stream.
//
// resvg's font database (fontdb) accepts TTF/OTF/TTC but not WOFF — the
// project ships a Helvetica Black .woff at the repo root, so we decompress
// it on the fly at server boot to feed it into the renderer. This avoids
// adding a build-step font conversion (no extra tools, no extra files in
// the repo) and keeps the binary self-contained.
//
// Format reference: https://www.w3.org/TR/WOFF/
//
// WOFF v1 layout:
//
//   - 44-byte header: magic "wOFF", flavor (sfnt scaler), length, numTables,
//     reserved, totalSfntSize, majorVersion, minorVersion, metaOffset/Length,
//     metaOrigLength, privOffset/Length.
//   - numTables * 20-byte directory entries: tag, offset, compLength,
//     origLength, origChecksum.
//   - Each table follows at `offset`, stored either raw (if compLength ==
//     origLength) or zlib-compressed.
//
// SFNT layout:
//
//   - 12-byte offset table: scaler, numTables, searchRange, entrySelector,
//     rangeShift.
//   - numTables * 16-byte table records: tag, checksum, offset, length.
//   - Each table at `offset`, padded to 4-byte boundaries.
func woffToTTF(woff []byte) ([]byte, error) {
	if len(woff) < 44 {
		return nil, errors.New("woff: file too short for header")
	}
	if string(woff[:4]) != "wOFF" {
		return nil, fmt.Errorf("woff: bad magic %q (want wOFF)", string(woff[:4]))
	}
	flavor := binary.BigEndian.Uint32(woff[4:8])
	numTables := binary.BigEndian.Uint16(woff[12:14])
	totalSfntSize := binary.BigEndian.Uint32(woff[16:20])

	if numTables == 0 {
		return nil, errors.New("woff: zero tables")
	}

	dirStart := 44
	if len(woff) < dirStart+int(numTables)*20 {
		return nil, errors.New("woff: file too short for table directory")
	}

	type entry struct {
		tag        uint32
		woffOffset uint32
		compLen    uint32
		origLen    uint32
		checksum   uint32
	}
	entries := make([]entry, numTables)
	for i := 0; i < int(numTables); i++ {
		base := dirStart + i*20
		entries[i] = entry{
			tag:        binary.BigEndian.Uint32(woff[base : base+4]),
			woffOffset: binary.BigEndian.Uint32(woff[base+4 : base+8]),
			compLen:    binary.BigEndian.Uint32(woff[base+8 : base+12]),
			origLen:    binary.BigEndian.Uint32(woff[base+12 : base+16]),
			checksum:   binary.BigEndian.Uint32(woff[base+16 : base+20]),
		}
	}

	// Compute the search range / entry selector / range shift values that
	// the SFNT offset table requires. They're a binary-search hint:
	// searchRange = (largest power of 2 <= numTables) * 16.
	searchRange := uint16(1)
	entrySelector := uint16(0)
	for searchRange*2 <= numTables {
		searchRange *= 2
		entrySelector++
	}
	searchRange *= 16
	rangeShift := numTables*16 - searchRange

	// Build the SFNT in a single buffer. We pre-size to the WOFF's stated
	// totalSfntSize when sane, otherwise grow as needed.
	out := bytes.NewBuffer(make([]byte, 0, totalSfntSize))
	// Offset table.
	_ = binary.Write(out, binary.BigEndian, flavor)
	_ = binary.Write(out, binary.BigEndian, numTables)
	_ = binary.Write(out, binary.BigEndian, searchRange)
	_ = binary.Write(out, binary.BigEndian, entrySelector)
	_ = binary.Write(out, binary.BigEndian, rangeShift)

	// Reserve space for table records; we'll fill in offsets/checksums
	// after we know where each decompressed table lands.
	recordsStart := out.Len()
	out.Write(make([]byte, int(numTables)*16))

	// Decompress (or copy) each table and write it, padded to 4 bytes.
	type record struct {
		tag      uint32
		checksum uint32
		offset   uint32
		length   uint32
	}
	records := make([]record, numTables)
	for i, e := range entries {
		end := uint64(e.woffOffset) + uint64(e.compLen)
		if end > uint64(len(woff)) {
			return nil, fmt.Errorf("woff: table %d extends past file", i)
		}
		raw := woff[e.woffOffset : e.woffOffset+e.compLen]

		var data []byte
		if e.compLen == e.origLen {
			// Stored raw.
			data = raw
		} else {
			zr, err := zlib.NewReader(bytes.NewReader(raw))
			if err != nil {
				return nil, fmt.Errorf("woff: table %d zlib: %w", i, err)
			}
			data, err = io.ReadAll(zr)
			_ = zr.Close()
			if err != nil {
				return nil, fmt.Errorf("woff: table %d decompress: %w", i, err)
			}
			if uint32(len(data)) != e.origLen {
				return nil, fmt.Errorf("woff: table %d length mismatch: got %d want %d", i, len(data), e.origLen)
			}
		}

		records[i] = record{
			tag:      e.tag,
			checksum: e.checksum,
			offset:   uint32(out.Len()),
			length:   e.origLen,
		}
		out.Write(data)
		// Pad to 4-byte boundary with zeros — required by the SFNT spec
		// even though the table record's `length` field is the unpadded size.
		for out.Len()%4 != 0 {
			out.WriteByte(0)
		}
	}

	// Backfill the table records with the offsets/lengths we now know.
	body := out.Bytes()
	for i, r := range records {
		base := recordsStart + i*16
		binary.BigEndian.PutUint32(body[base:base+4], r.tag)
		binary.BigEndian.PutUint32(body[base+4:base+8], r.checksum)
		binary.BigEndian.PutUint32(body[base+8:base+12], r.offset)
		binary.BigEndian.PutUint32(body[base+12:base+16], r.length)
	}

	return body, nil
}
