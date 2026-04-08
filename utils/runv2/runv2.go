package runv2

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/grafov/m3u8"
	"github.com/itouakirai/mp4ff/mp4"

	"encoding/binary"
	"github.com/schollz/progressbar/v3"

	nethttp "github.com/wuuduf/applemusic-telegram-bot/utils/nethttp"
	"github.com/wuuduf/applemusic-telegram-bot/utils/structs"
)

const (
	prefetchKey              = "skd://itunes.apple.com/P000000000/s1/e1"
	defaultIdleTimeoutSec    = 300
	minIdleTimeoutSec        = 30
	runv2IdleTimeoutEnvKey   = "AMDL_RUNV2_IDLE_TIMEOUT_SEC"
	defaultHeaderTimeoutSec  = 45
	defaultDialTimeoutSec    = 10
	defaultHandshakeTimeout  = 10
	defaultIdleConnTimeout   = 90
	defaultExpectContinueSec = 1
)

var ErrTimeout = errors.New("response timed out")

type TimedResponseBody struct {
	timeout   time.Duration
	timer     *time.Timer
	threshold int
	body      io.Reader
}

type ProgressFunc func(phase string, done, total int64)

type progressWriter struct {
	cb    ProgressFunc
	phase string
	total int64
	done  int64
}

func (p *progressWriter) Write(b []byte) (int, error) {
	n := len(b)
	p.done += int64(n)
	if p.cb != nil {
		p.cb(p.phase, p.done, p.total)
	}
	return n, nil
}

func (b *TimedResponseBody) Read(p []byte) (int, error) {
	n, err := b.body.Read(p)
	if err != nil {
		return n, err
	}
	// fmt.Printf("Read %d bytes, buffer size %d bytes", n, len(p))
	if n >= b.threshold {
		b.timer.Reset(b.timeout)
	}
	return n, err
}

func resolveIdleTimeout() time.Duration {
	raw := strings.TrimSpace(os.Getenv(runv2IdleTimeoutEnvKey))
	if raw == "" {
		return time.Duration(defaultIdleTimeoutSec) * time.Second
	}
	sec, err := strconv.Atoi(raw)
	if err != nil {
		return time.Duration(defaultIdleTimeoutSec) * time.Second
	}
	if sec <= 0 {
		return 0
	}
	if sec < minIdleTimeoutSec {
		sec = minIdleTimeoutSec
	}
	return time.Duration(sec) * time.Second
}

func newRunv2StreamClient() *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			DialContext:           (&net.Dialer{Timeout: defaultDialTimeoutSec * time.Second, KeepAlive: 30 * time.Second}).DialContext,
			ForceAttemptHTTP2:     true,
			MaxIdleConns:          32,
			MaxIdleConnsPerHost:   8,
			IdleConnTimeout:       defaultIdleConnTimeout * time.Second,
			TLSHandshakeTimeout:   defaultHandshakeTimeout * time.Second,
			ExpectContinueTimeout: defaultExpectContinueSec * time.Second,
			ResponseHeaderTimeout: defaultHeaderTimeoutSec * time.Second,
		},
	}
}

func Run(adamId string, playlistUrl string, outfile string, Config structs.ConfigSet, progress ProgressFunc) error {
	return RunWithContext(context.Background(), adamId, playlistUrl, outfile, Config, progress)
}

func RunWithContext(ctx context.Context, adamId string, playlistUrl string, outfile string, Config structs.ConfigSet, progress ProgressFunc) error {
	var err error
	if ctx == nil {
		ctx = context.Background()
	}
	idleTimeout := resolveIdleTimeout()
	header := make(http.Header)

	// request media playlist
	req, err := http.NewRequestWithContext(ctx, "GET", playlistUrl, nil)
	if err != nil {
		return err
	}
	req.Header = header
	do, err := nethttp.Do(req)
	if err != nil {
		return err
	}
	defer do.Body.Close()
	if do.StatusCode != http.StatusOK {
		return fmt.Errorf("request media playlist failed: %s", do.Status)
	}

	// parse m3u8
	segments, err := parseMediaPlaylist(do.Body)
	if err != nil {
		return err
	}
	segment := segments[0]
	if segment == nil {
		return errors.New("no segments extracted from playlist")
	}
	if segment.Limit <= 0 {
		return errors.New("non-byterange playlists are currently unsupported")
	}

	// get URL to the actual file
	parsedUrl, err := url.Parse(playlistUrl)
	if err != nil {
		return err
	}
	fileUrl, err := parsedUrl.Parse(segment.URI)
	if err != nil {
		return err
	}

	// request mp4
	runCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	req, err = http.NewRequestWithContext(runCtx, "GET", fileUrl.String(), nil)
	if err != nil {
		return err
	}
	req.Header = header

	var body io.Reader
	client := newRunv2StreamClient()
	if idleTimeout > 0 {
		// Idle watchdog: cancel the request only when no bytes are received for N seconds.
		timer := time.AfterFunc(idleTimeout, func() { cancel(ErrTimeout) })
		defer timer.Stop()
		do, err = client.Do(req)
		if err != nil {
			return err
		}
		defer do.Body.Close()
		if do.StatusCode != http.StatusOK {
			return fmt.Errorf("request media stream failed: %s", do.Status)
		}
		body = &TimedResponseBody{
			timeout:   idleTimeout,
			timer:     timer,
			threshold: 256,
			body:      do.Body,
		}
	} else {
		do, err = client.Do(req)
		if err != nil {
			return err
		}
		defer do.Body.Close()
		if do.StatusCode != http.StatusOK {
			return fmt.Errorf("request media stream failed: %s", do.Status)
		}
		body = do.Body
	}

	var totalLen int64
	totalLen = do.ContentLength
	// connect to decryptor
	//addr := fmt.Sprintf("127.0.0.1:10020")
	addr := Config.DecryptM3u8Port
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		return err
	}
	//fmt.Print("Decrypting...\n")
	defer Close(conn)

	err = downloadAndDecryptFile(conn, body, outfile, adamId, segments, totalLen, Config, progress, "Downloading")
	if err != nil {
		return err
	}
	fmt.Print("Decrypted\n")
	return nil
}

func downloadAndDecryptFile(conn io.ReadWriter, in io.Reader, outfile string,
	adamId string, playlistSegments []*m3u8.MediaSegment, totalLen int64, Config structs.ConfigSet, progress ProgressFunc, phase string) error {
	inBuf := bufio.NewReader(in)
	ofh, err := os.Create(outfile)
	if err != nil {
		return err
	}
	defer ofh.Close()
	outBuf := bufio.NewWriter(ofh)
	init, offset, err := ReadInitSegment(inBuf)
	if err != nil {
		return err
	}
	if init == nil {
		return errors.New("no init segment found")
	}

	tracks, err := TransformInit(init)
	if err != nil {
		return err
	}
	err = sanitizeInit(init)
	if err != nil {
		// errors returned by sanitizeInit are non-fatal
		fmt.Printf("Warning: unable to sanitize init completely: %s\n", err)
	}
	err = init.Encode(outBuf)
	if err != nil {
		return err
	}

	// 'segment' in m3u8 == 'fragment' in mp4ff
	//fmt.Println("Starting decryption...")
	var bar *progressbar.ProgressBar
	if phase == "" {
		phase = "Downloading"
	}
	if progress == nil {
		bar = progressbar.NewOptions64(totalLen,
			progressbar.OptionClearOnFinish(),
			progressbar.OptionSetElapsedTime(false),
			progressbar.OptionSetPredictTime(false),
			progressbar.OptionShowElapsedTimeOnFinish(),
			progressbar.OptionShowCount(),
			progressbar.OptionEnableColorCodes(true),
			progressbar.OptionShowBytes(true),
			progressbar.OptionSetDescription("Decrypting..."),
			progressbar.OptionSetTheme(progressbar.Theme{
				Saucer:        "",
				SaucerHead:    "",
				SaucerPadding: "",
				BarStart:      "",
				BarEnd:        "",
			}),
		)
		bar.Add64(int64(offset))
	} else {
		progress(phase, int64(offset), totalLen)
	}
	rw := bufio.NewReadWriter(bufio.NewReader(conn), bufio.NewWriter(conn))
	for i := 0; ; i++ {
		var frag *mp4.Fragment
		rawoffset := offset
		frag, offset, err = ReadNextFragment(inBuf, offset)
		rawoffset = offset - rawoffset
		if err != nil {
			return err
		}
		if frag == nil {
			// check offset against Content-Length?
			break
		}
		// print progress

		// if totalLen > 0 {
		// 	fmt.Printf("%.2f%% of %d bytes\n", 100*float32(offset)/float32(totalLen), totalLen)
		// }
		segment := playlistSegments[i]
		if segment == nil {
			return errors.New("segment number out of sync")
		}
		key := segment.Key
		if key != nil {
			if i != 0 {
				SwitchKeys(rw)
			}
			if key.URI == prefetchKey {
				SendString(rw, "0")
			} else {
				SendString(rw, adamId)
			}
			SendString(rw, key.URI)
		}
		// flushes the buffer
		err = DecryptFragment(frag, tracks, rw)
		if err != nil {
			return fmt.Errorf("decryptFragment: %w", err)
		}
		err = frag.Encode(outBuf)
		if err != nil {
			return err
		}
		if progress != nil {
			progress(phase, int64(offset), totalLen)
		} else {
			bar.Add64(int64(rawoffset))
		}
	}
	err = outBuf.Flush()
	if err != nil {
		return err
	}
	return nil
}

// Remove boxes in the init segment that are known to cause compatibility issues
func sanitizeInit(init *mp4.InitSegment) error {
	if init == nil {
		return errors.New("init segment is nil")
	}
	if init.Moov == nil {
		return errors.New("moov box is missing")
	}
	traks := init.Moov.Traks
	if len(traks) == 0 {
		return errors.New("no track found")
	}
	if len(traks) > 1 {
		return errors.New("more than 1 track found")
	}
	// Remove duplicate ec-3 or alac boxes in stsd since some programs (e.g. cuetools) don't
	// like it when there's more than 1 entry in stsd.
	// Every audio track contains two of these boxes because two IVs are needed to decrypt the
	// track. The two boxes become identical after removing encryption info.
	if traks[0].Mdia == nil || traks[0].Mdia.Minf == nil || traks[0].Mdia.Minf.Stbl == nil || traks[0].Mdia.Minf.Stbl.Stsd == nil {
		return errors.New("stsd box is missing")
	}
	stsd := traks[0].Mdia.Minf.Stbl.Stsd
	if stsd.SampleCount == 1 {
		return nil
	}
	if stsd.SampleCount > 2 {
		return fmt.Errorf("expected only 1 or 2 entries in stsd, got %d", stsd.SampleCount)
	}
	children := stsd.Children
	if len(children) < 2 {
		return fmt.Errorf("invalid stsd entries: expected >=2, got %d", len(children))
	}
	if children[0].Type() != children[1].Type() {
		return errors.New("children in stsd are not of the same type")
	}
	stsd.Children = children[:1]
	stsd.SampleCount = 1
	return nil
}

// Workaround for m3u8 not supporting multiple keys - remove
// PlayReady and Widevine
func filterResponse(f io.Reader) (*bytes.Buffer, error) {
	buf := &bytes.Buffer{}
	scanner := bufio.NewScanner(f)

	prefix := []byte("#EXT-X-KEY:")
	keyFormat := []byte("streamingkeydelivery")
	for scanner.Scan() {
		lineBytes := scanner.Bytes()
		if bytes.HasPrefix(lineBytes, prefix) && !bytes.Contains(lineBytes, keyFormat) {
			continue
		}
		_, err := buf.Write(lineBytes)
		if err != nil {
			return nil, err
		}
		_, err = buf.WriteString("\n")
		if err != nil {
			return nil, err
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return buf, nil
}

func parseMediaPlaylist(r io.ReadCloser) ([]*m3u8.MediaSegment, error) {
	defer r.Close()
	playlistBuf, err := filterResponse(r)
	if err != nil {
		return nil, err
	}

	playlist, listType, err := m3u8.Decode(*playlistBuf, true)
	if err != nil {
		return nil, err
	}

	if listType != m3u8.MEDIA {
		return nil, errors.New("m3u8 not of media type")
	}

	mediaPlaylist := playlist.(*m3u8.MediaPlaylist)
	return mediaPlaylist.Segments, nil
}

// pasing
func ReadInitSegment(r io.Reader) (*mp4.InitSegment, uint64, error) {
	var offset uint64 = 0
	init := mp4.NewMP4Init()
	for i := 0; i < 2; i++ {
		box, err := mp4.DecodeBox(offset, r)
		if err != nil {
			return nil, offset, err
		}
		boxType := box.Type()
		if boxType != "ftyp" && boxType != "moov" {
			return nil, offset, fmt.Errorf("unexpected box type %s, should be ftyp or moov", boxType)
		}
		init.AddChild(box)
		offset += box.Size()
	}
	return init, offset, nil
}

// Get the next fragment. Returns nil and no error on EOF
func ReadNextFragment(r io.Reader, offset uint64) (*mp4.Fragment, uint64, error) {
	frag := mp4.NewFragment()
	for {
		box, err := mp4.DecodeBox(offset, r)
		if err == io.EOF {
			return nil, offset, nil
		}
		if err != nil {
			return nil, offset, err
		}
		boxType := box.Type()
		// fmt.Printf("processing %s, box starts @ offset %d\n", boxType, offset)
		offset += box.Size()
		if boxType == "moof" || boxType == "emsg" || boxType == "prft" {
			frag.AddChild(box)
			continue
		}
		if boxType == "mdat" {
			frag.AddChild(box)
			break
		}
		fmt.Printf("ignoring a %s box found mid-stream", boxType)
	}
	// only 1 mdat box in fragment, meaning that the box doesn't have a preceding moof box
	if frag.Moof == nil {
		return nil, offset, fmt.Errorf("more than one mdat box in fragment (box ends @ offset %d)", offset)
	}
	return frag, offset, nil
}

// Return a new slice of boxes with encryption-related sbgp and sgpd removed,
// and the total number of bytes removed.
// Non-encryption-related ones such as 'roll' are left untouched.
func FilterSbgpSgpd(children []mp4.Box) ([]mp4.Box, uint64) {
	var bytesRemoved uint64 = 0
	remainingChildren := make([]mp4.Box, 0, len(children))
	for _, child := range children {
		switch box := child.(type) {
		case *mp4.SbgpBox:
			if box.GroupingType == "seam" || box.GroupingType == "seig" {
				bytesRemoved += child.Size()
				continue
			}
		case *mp4.SgpdBox:
			if box.GroupingType == "seam" || box.GroupingType == "seig" {
				bytesRemoved += child.Size()
				continue
			}
		}
		remainingChildren = append(remainingChildren, child)
	}
	return remainingChildren, bytesRemoved
}

// Get decryption info for tracks from init segment and remove encryption-related boxes
func TransformInit(init *mp4.InitSegment) (map[uint32]mp4.DecryptTrackInfo, error) {
	if init == nil {
		return nil, errors.New("init segment is nil")
	}
	if init.Moov == nil {
		return nil, errors.New("moov box is missing")
	}
	if len(init.Moov.Traks) == 0 {
		return nil, errors.New("no track found")
	}
	di, err := mp4.DecryptInit(init)
	if err != nil {
		return nil, err
	}
	tracks := make(map[uint32]mp4.DecryptTrackInfo, len(di.TrackInfos))
	for _, ti := range di.TrackInfos {
		tracks[ti.TrackID] = ti
	}
	// remove encryption-related sbgp and sgpd
	for _, trak := range init.Moov.Traks {
		if trak.Mdia == nil || trak.Mdia.Minf == nil || trak.Mdia.Minf.Stbl == nil {
			continue
		}
		stbl := trak.Mdia.Minf.Stbl
		stbl.Children, _ = FilterSbgpSgpd(stbl.Children)
	}
	return tracks, nil
}

// remote
// Reset the loops on the script's end and close the connection
func Close(conn io.WriteCloser) error {
	defer conn.Close()
	_, err := conn.Write([]byte{0, 0, 0, 0, 0})
	return err
}

func SwitchKeys(conn io.Writer) error {
	_, err := conn.Write([]byte{0, 0, 0, 0})
	return err
}

// Send id or keyUri
func SendString(conn io.Writer, uri string) error {
	_, err := conn.Write([]byte{byte(len(uri))})
	if err != nil {
		return err
	}
	_, err = io.WriteString(conn, uri)
	return err
}

func cbcsFullSubsampleDecrypt(data []byte, conn *bufio.ReadWriter) error {
	// Drops 4 last bits -> multiple of 16
	// It wouldn't hurt to send the remaining bytes also because the decryption
	// function would just return them as-is, but we're truncating the data here
	// for clarity and interoperability
	truncatedLen := len(data) & ^0xf
	// send the whole chunk at once
	err := binary.Write(conn, binary.LittleEndian, uint32(truncatedLen))
	if err != nil {
		return err
	}
	_, err = conn.Write(data[:truncatedLen])
	if err != nil {
		return err
	}
	err = conn.Flush()
	if err != nil {
		return err
	}
	_, err = io.ReadFull(conn, data[:truncatedLen])
	return err
}

func cbcsStripeDecrypt(data []byte, conn *bufio.ReadWriter, decryptBlockLen, skipBlockLen int) error {
	size := len(data)

	// block too small, ignore
	if size < decryptBlockLen {
		return nil
	}

	// number of encrypted blocks in this sample
	count := ((size - decryptBlockLen) / (decryptBlockLen + skipBlockLen)) + 1
	totalLen := count * decryptBlockLen

	err := binary.Write(conn, binary.LittleEndian, uint32(totalLen))
	if err != nil {
		return err
	}

	pos := 0
	for {
		if size-pos < decryptBlockLen { // Leave the rest
			break
		}
		_, err = conn.Write(data[pos : pos+decryptBlockLen])
		if err != nil {
			return err
		}
		pos += decryptBlockLen
		if size-pos < skipBlockLen {
			break
		}
		pos += skipBlockLen
	}
	err = conn.Flush()
	if err != nil {
		return err
	}

	pos = 0
	for {
		if size-pos < decryptBlockLen {
			break
		}
		_, err = io.ReadFull(conn, data[pos:pos+decryptBlockLen])
		if err != nil {
			return err
		}
		pos += decryptBlockLen
		if size-pos < skipBlockLen {
			break
		}
		pos += skipBlockLen
	}
	return nil
}

// Decryption function dispatcher
func cbcsDecryptRaw(data []byte, conn *bufio.ReadWriter, decryptBlockLen, skipBlockLen int) error {
	if skipBlockLen == 0 {
		// Full encryption of subsamples
		// e.g. Apple Music ALAC
		return cbcsFullSubsampleDecrypt(data, conn)
	} else {
		// Pattern (stripe) encryption of subsamples
		// e.g. most AVC and HEVC applications
		return cbcsStripeDecrypt(data, conn, decryptBlockLen, skipBlockLen)
	}
}

// Decrypt a cbcs-encrypted sample in-place
func cbcsDecryptSample(sample []byte, conn *bufio.ReadWriter,
	subSamplePatterns []mp4.SubSamplePattern, tenc *mp4.TencBox) error {

	decryptBlockLen := int(tenc.DefaultCryptByteBlock) * 16
	skipBlockLen := int(tenc.DefaultSkipByteBlock) * 16
	var pos uint32 = 0

	// Full sample encryption
	if len(subSamplePatterns) == 0 {
		return cbcsDecryptRaw(sample, conn, decryptBlockLen, skipBlockLen)
	}

	// Has subsamples
	for j := 0; j < len(subSamplePatterns); j++ {
		ss := subSamplePatterns[j]
		pos += uint32(ss.BytesOfClearData)

		// Nothing to decrypt!
		if ss.BytesOfProtectedData <= 0 {
			continue
		}

		err := cbcsDecryptRaw(sample[pos:pos+ss.BytesOfProtectedData],
			conn, decryptBlockLen, skipBlockLen)
		if err != nil {
			return err
		}
		pos += ss.BytesOfProtectedData
	}

	return nil
}

// Decrypt an array of cbcs-encrypted samples in-place
func cbcsDecryptSamples(samples []mp4.FullSample, conn *bufio.ReadWriter,
	tenc *mp4.TencBox, senc *mp4.SencBox) error {

	for i := range samples {
		var subSamplePatterns []mp4.SubSamplePattern
		if len(senc.SubSamples) != 0 {
			subSamplePatterns = senc.SubSamples[i]
		}
		err := cbcsDecryptSample(samples[i].Data, conn, subSamplePatterns, tenc)
		if err != nil {
			return err
		}
	}
	return nil
}

func DecryptFragment(frag *mp4.Fragment, tracks map[uint32]mp4.DecryptTrackInfo, conn *bufio.ReadWriter) error {
	moof := frag.Moof
	var bytesRemoved uint64 = 0

	for _, traf := range moof.Trafs {
		ti, ok := tracks[traf.Tfhd.TrackID]
		if !ok {
			return fmt.Errorf("could not find decryption info for track %d", traf.Tfhd.TrackID)
		}
		if ti.Sinf == nil {
			// unencrypted track
			continue
		}

		schemeType := ti.Sinf.Schm.SchemeType
		if schemeType != "cbcs" {
			return fmt.Errorf("scheme type %s not supported", schemeType)
		}
		hasSenc, isParsed := traf.ContainsSencBox()
		if !hasSenc {
			return fmt.Errorf("no senc box in traf")
		}

		var senc *mp4.SencBox
		if traf.Senc != nil {
			senc = traf.Senc
		} else {
			senc = traf.UUIDSenc.Senc
		}

		if !isParsed {
			// simply ignore sbgp and sgpd
			// "Sample To Group Box ('sbgp') and Sample Group Description Box ('sgpd')
			// of type 'seig' are used to indicate the KID applied to each sample, and changes
			// to KIDs over time (i.e. 'key rotation')"
			// (ref: https://dashif.org/docs/DASH-IF-IOP-v3.2.pdf)
			err := senc.ParseReadBox(ti.Sinf.Schi.Tenc.DefaultPerSampleIVSize, traf.Saiz)
			if err != nil {
				return err
			}
		}

		samples, err := frag.GetFullSamples(ti.Trex)
		if err != nil {
			return err
		}

		err = cbcsDecryptSamples(samples, conn, ti.Sinf.Schi.Tenc, senc)
		if err != nil {
			return err
		}

		bytesRemoved += traf.RemoveEncryptionBoxes()
	}
	_, psshBytesRemoved := moof.RemovePsshs()
	bytesRemoved += psshBytesRemoved
	for _, traf := range moof.Trafs {
		for _, trun := range traf.Truns {
			trun.DataOffset -= int32(bytesRemoved)
		}
	}

	return nil
}
