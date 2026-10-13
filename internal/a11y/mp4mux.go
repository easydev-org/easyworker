package goa11y

import (
	"bytes"
	"encoding/binary"
)

// muxMJPEG writes a minimal progressive MP4 file with a single MJPEG video
// track. Pure Go, no external tools. Layout: ftyp, mdat (all JPEG frames
// concatenated), moov (with a 'mjpa' sample entry pointing into mdat).
func muxMJPEG(frames [][]byte, width, height, fps int) ([]byte, error) {
	if fps <= 0 {
		fps = 10
	}
	var mdatData bytes.Buffer
	offsets := make([]uint32, len(frames))
	sizes := make([]uint32, len(frames))
	for i, fr := range frames {
		sizes[i] = uint32(len(fr))
		mdatData.Write(fr)
	}

	ftyp := makeFtyp()
	// mdat box header is 8 bytes; its payload starts after ftyp + 8.
	mdatPayloadStart := uint32(len(ftyp) + 8)
	for i := range frames {
		if i == 0 {
			offsets[i] = mdatPayloadStart
		} else {
			offsets[i] = offsets[i-1] + sizes[i-1]
		}
	}

	mdat := makeBox("mdat", mdatData.Bytes())
	moov := makeMoov(width, height, fps, sizes, offsets)

	var out bytes.Buffer
	out.Write(ftyp)
	out.Write(mdat)
	out.Write(moov)
	return out.Bytes(), nil
}

func makeFtyp() []byte {
	var b bytes.Buffer
	b.WriteString("isom")
	u32(&b, 512)
	b.WriteString("isom")
	b.WriteString("iso2")
	b.WriteString("mp41")
	return makeBox("ftyp", b.Bytes())
}

func makeMoov(width, height, fps int, sizes, offsets []uint32) []byte {
	n := len(sizes)
	dur := uint32(n) // in mdhd timescale = fps
	mvhd := makeMvhd(uint32(n) * 1000 / uint32(fps))
	trak := makeTrak(width, height, fps, uint32(n), dur, sizes, offsets)
	var b bytes.Buffer
	b.Write(mvhd)
	b.Write(trak)
	return makeBox("moov", b.Bytes())
}

func makeMvhd(duration uint32) []byte {
	var b bytes.Buffer
	u32(&b, 0)             // version+flags
	u32(&b, 0)             // creation_time
	u32(&b, 0)             // modification_time
	u32(&b, 1000)          // timescale
	u32(&b, duration)      // duration
	u32(&b, 0x00010000)    // rate 1.0
	u16(&b, 0x0100)        // volume 1.0
	u16(&b, 0)             // reserved
	u32(&b, 0); u32(&b, 0) // reserved[2]
	// matrix (unity)
	for _, v := range []uint32{0x00010000, 0, 0, 0, 0x00010000, 0, 0, 0, 0x40000000} {
		u32(&b, v)
	}
	for i := 0; i < 6; i++ {
		u32(&b, 0) // pre_defined
	}
	u32(&b, 2) // next_track_ID
	return makeBox("mvhd", b.Bytes())
}

func makeTrak(width, height, fps int, n, dur uint32, sizes, offsets []uint32) []byte {
	tkhd := makeTkhd(uint32(width), uint32(height), dur)
	mdhd := makeMdhd(uint32(fps), n)
	hdlr := makeHdlr()
	minf := makeMinf(width, height, sizes, offsets)
	var mdia bytes.Buffer
	mdia.Write(mdhd)
	mdia.Write(hdlr)
	mdia.Write(minf)
	var b bytes.Buffer
	b.Write(tkhd)
	b.Write(makeBox("mdia", mdia.Bytes()))
	return makeBox("trak", b.Bytes())
}

func makeTkhd(width, height, duration uint32) []byte {
	var b bytes.Buffer
	u32(&b, 0x00000007) // version=0 flags=enabled|in_movie|in_preview
	u32(&b, 0)          // creation_time
	u32(&b, 0)          // modification_time
	u32(&b, 1)          // track_ID
	u32(&b, 0)          // reserved
	u32(&b, duration)   // duration (mvhd timescale 1000)
	u32(&b, 0); u32(&b, 0) // reserved[2]
	u16(&b, 0)          // layer
	u16(&b, 0)          // alternate_group
	u16(&b, 0)          // volume (0 for video)
	u16(&b, 0)          // reserved
	for _, v := range []uint32{0x00010000, 0, 0, 0, 0x00010000, 0, 0, 0, 0x40000000} {
		u32(&b, v)
	}
	u32(&b, uint32(width)<<16)  // width 16.16
	u32(&b, uint32(height)<<16) // height 16.16
	return makeBox("tkhd", b.Bytes())
}

func makeMdhd(timescale, duration uint32) []byte {
	var b bytes.Buffer
	u32(&b, 0)          // version+flags
	u32(&b, 0)          // creation_time
	u32(&b, 0)          // modification_time
	u32(&b, timescale)  // timescale (=fps)
	u32(&b, duration)   // duration (in frames)
	u16(&b, 0x55c4)     // language (und)
	u16(&b, 0)          // pre_defined
	return makeBox("mdhd", b.Bytes())
}

func makeHdlr() []byte {
	var b bytes.Buffer
	u32(&b, 0) // version+flags
	u32(&b, 0) // pre_defined
	b.WriteString("vide")
	u32(&b, 0); u32(&b, 0); u32(&b, 0) // reserved[3]
	b.WriteString("VideoHandler\x00")
	return makeBox("hdlr", b.Bytes())
}

func makeMinf(width, height int, sizes, offsets []uint32) []byte {
	vmhd := makeBox("vmhd", func() []byte {
		var b bytes.Buffer
		u32(&b, 0x00000001) // version=0 flags=1
		u16(&b, 0)          // graphicsmode
		u16(&b, 0); u16(&b, 0); u16(&b, 0) // opcolor[3]
		return b.Bytes()
	}())
	dinf := makeBox("dinf", func() []byte {
		// dref: version+flags, entry_count=1, then a self-contained 'url '
		var dref bytes.Buffer
		u32(&dref, 0) // version+flags
		u32(&dref, 1) // entry_count
		dref.Write(makeBox("url ", []byte{0x00, 0x00, 0x00, 0x01})) // flags=1 self-contained
		return makeBox("dref", dref.Bytes())
	}())
	stbl := makeStbl(width, height, sizes, offsets)
	var b bytes.Buffer
	b.Write(vmhd)
	b.Write(dinf)
	b.Write(stbl)
	return makeBox("minf", b.Bytes())
}

func makeStbl(width, height int, sizes, offsets []uint32) []byte {
	n := uint32(len(sizes))
	// stsd: 1 entry, 'mjpa' VisualSampleEntry
	stsdEntry := makeMJPASampleEntry(width, height)
	var stsdBuf bytes.Buffer
	u32(&stsdBuf, 0) // version+flags
	u32(&stsdBuf, 1) // entry_count
	stsdBuf.Write(stsdEntry)
	stsd := makeBox("stsd", stsdBuf.Bytes())

	// stts: one entry (count=n, delta=1)
	var sttsBuf bytes.Buffer
	u32(&sttsBuf, 0)
	u32(&sttsBuf, 1) // entry_count
	u32(&sttsBuf, n) // sample_count
	u32(&sttsBuf, 1) // sample_delta
	stts := makeBox("stts", sttsBuf.Bytes())

	// stsc: one entry (first_chunk=1, samples_per_chunk=n, desc=1)
	var stscBuf bytes.Buffer
	u32(&stscBuf, 0)
	u32(&stscBuf, 1)
	u32(&stscBuf, 1) // first_chunk
	u32(&stscBuf, n) // samples_per_chunk
	u32(&stscBuf, 1) // sample_description_index
	stsc := makeBox("stsc", stscBuf.Bytes())

	// stsz: sample_size=0, count=n, then sizes
	var stszBuf bytes.Buffer
	u32(&stszBuf, 0)
	u32(&stszBuf, 0) // sample_size
	u32(&stszBuf, n) // sample_count
	for _, s := range sizes {
		u32(&stszBuf, s)
	}
	stsz := makeBox("stsz", stszBuf.Bytes())

	// stco: chunk offsets (one chunk, offset = first frame)
	var stcoBuf bytes.Buffer
	u32(&stcoBuf, 0)
	u32(&stcoBuf, 1) // entry_count
	u32(&stcoBuf, offsets[0])
	stco := makeBox("stco", stcoBuf.Bytes())

	var b bytes.Buffer
	b.Write(stsd)
	b.Write(stts)
	b.Write(stsc)
	b.Write(stsz)
	b.Write(stco)
	return makeBox("stbl", b.Bytes())
}

// makeMJPASampleEntry builds a VisualSampleEntry with format 'mjpa' (MJPEG),
// wrapped as a proper box (size + type).
func makeMJPASampleEntry(width, height int) []byte {
	var b bytes.Buffer
	// VisualSampleEntry payload (after the 8-byte box header):
	for i := 0; i < 6; i++ {
		b.WriteByte(0) // reserved
	}
	u16(&b, 1) // data_reference_index
	u16(&b, 0); u16(&b, 0) // pre_defined, reserved
	u32(&b, 0); u32(&b, 0); u32(&b, 0) // pre_defined[3]
	u16(&b, uint16(width))
	u16(&b, uint16(height))
	u32(&b, 0x00480000) // horizresolution 72dpi
	u32(&b, 0x00480000) // vertresolution 72dpi
	u32(&b, 0)          // reserved
	u16(&b, 1)          // frame_count
	for i := 0; i < 32; i++ {
		b.WriteByte(0) // compressorname
	}
	u16(&b, 0x0018) // depth
	u16(&b, 0xffff) // pre_defined = -1
	return makeBox("mjpa", b.Bytes())
}

// ── box helpers ────────────────────────────────────────────────────

func makeBox(typ string, payload []byte) []byte {
	var b bytes.Buffer
	u32(&b, uint32(len(payload)+8))
	b.WriteString(typ)
	b.Write(payload)
	return b.Bytes()
}

func u32(b *bytes.Buffer, v uint32) { binary.Write(b, binary.BigEndian, v) }
func u16(b *bytes.Buffer, v uint16) { binary.Write(b, binary.BigEndian, v) }
