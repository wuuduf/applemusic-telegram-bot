package runv3

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/go-resty/resty/v2"
	"github.com/grafov/m3u8"
	"github.com/itouakirai/mp4ff/mp4"
	"github.com/schollz/progressbar/v3"
	"google.golang.org/protobuf/proto"

	"github.com/wuuduf/applemusic-telegram-bot/utils/cmdrunner"
	nethttp "github.com/wuuduf/applemusic-telegram-bot/utils/nethttp"
	cdm "github.com/wuuduf/applemusic-telegram-bot/utils/runv3/cdm"
	key "github.com/wuuduf/applemusic-telegram-bot/utils/runv3/key"
)

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

type PlaybackLicense struct {
	ErrorCode  int    `json:"errorCode"`
	License    string `json:"license"`
	RenewAfter int    `json:"renew-after"`
	Status     int    `json:"status"`
}

func getPSSH(contentId string, kidBase64 string) (string, error) {
	kidBytes, err := base64.StdEncoding.DecodeString(kidBase64)
	if err != nil {
		return "", fmt.Errorf("failed to decode base64 KID: %v", err)
	}
	contentIdEncoded := base64.StdEncoding.EncodeToString([]byte(contentId))
	algo := cdm.WidevineCencHeader_AESCTR
	widevineCencHeader := &cdm.WidevineCencHeader{
		KeyId:     [][]byte{kidBytes},
		Algorithm: &algo,
		Provider:  new(string),
		ContentId: []byte(contentIdEncoded),
		Policy:    new(string),
	}
	widevineCenc, err := proto.Marshal(widevineCencHeader)
	if err != nil {
		return "", fmt.Errorf("failed to marshal WidevineCencHeader: %v", err)
	}
	//最前面添加32字节
	widevineCenc = append([]byte("0123456789abcdef0123456789abcdef"), widevineCenc...)
	pssh := base64.StdEncoding.EncodeToString(widevineCenc)
	return pssh, nil
}

func BeforeRequest(cl *resty.Client, ctx context.Context, url string, body []byte) (*resty.Response, error) {
	jsondata := map[string]interface{}{
		"challenge":      base64.StdEncoding.EncodeToString(body), // 'body' is passed in directly
		"key-system":     "com.widevine.alpha",
		"uri":            ctx.Value("uriPrefix").(string) + "," + ctx.Value("pssh").(string),
		"adamId":         ctx.Value("adamId").(string),
		"isLibrary":      false,
		"user-initiated": true,
	}

	resp, err := cl.R().
		SetContext(ctx).
		SetBody(jsondata).
		Post(url)

	if err != nil {
		fmt.Println(err)
	}

	return resp, err
}

func AfterRequest(response *resty.Response) ([]byte, error) {
	var responseData PlaybackLicense

	err := json.Unmarshal(response.Body(), &responseData)
	if err != nil {
		return nil, fmt.Errorf("failed to parse response JSON: %v", err)
	}

	if responseData.ErrorCode != 0 || responseData.Status != 0 {
		return nil, fmt.Errorf("error in license response, code: %d, status: %d", responseData.ErrorCode, responseData.Status)
	}

	license, err := base64.StdEncoding.DecodeString(responseData.License)
	if err != nil {
		return nil, fmt.Errorf("failed to decode license: %v", err)
	}

	return license, nil
}

func GetWebplayback(adamId string, authtoken string, mutoken string, mvmode bool) (string, string, string, error) {
	return GetWebplaybackWithContext(context.Background(), adamId, authtoken, mutoken, mvmode)
}

func GetWebplaybackWithContext(ctx context.Context, adamId string, authtoken string, mutoken string, mvmode bool) (string, string, string, error) {
	url := "https://play.music.apple.com/WebObjects/MZPlay.woa/wa/webPlayback"
	postData := map[string]string{
		"salableAdamId": adamId,
	}
	jsonData, err := json.Marshal(postData)
	if err != nil {
		fmt.Println("Error encoding JSON:", err)
		return "", "", "", err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer([]byte(jsonData)))
	if err != nil {
		fmt.Println("Error creating request:", err)
		return "", "", "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "https://music.apple.com")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/91.0.4472.124 Safari/537.36")
	req.Header.Set("Referer", "https://music.apple.com/")
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", authtoken))
	req.Header.Set("x-apple-music-user-token", mutoken)
	// 创建 HTTP 客户端
	//client := &http.Client{}
	resp, err := nethttp.Do(req)
	// 发送请求
	//resp, err := client.Do(req)
	if err != nil {
		fmt.Println("Error sending request:", err)
		return "", "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", "", "", fmt.Errorf("webPlayback request failed: %s", resp.Status)
	}
	//fmt.Println("Response Status:", resp.Status)
	obj := new(Songlist)
	err = json.NewDecoder(resp.Body).Decode(&obj)
	if err != nil {
		fmt.Println("json err:", err)
		return "", "", "", err
	}
	if len(obj.List) > 0 {
		if mvmode {
			return obj.List[0].HlsPlaylistUrl, "", "", nil
		}
		// 遍历 Assets
		for i := range obj.List[0].Assets {
			if obj.List[0].Assets[i].Flavor == "28:ctrp256" {
				kidBase64, fileurl, uriPrefix, err := extractKidBase64WithContext(ctx, obj.List[0].Assets[i].URL, false)
				if err != nil {
					return "", "", "", err
				}
				return fileurl, kidBase64, uriPrefix, nil
			}
			continue
		}
	}
	return "", "", "", errors.New("Unavailable")
}

type Songlist struct {
	List []struct {
		Hlsurl         string `json:"hls-key-cert-url"`
		HlsPlaylistUrl string `json:"hls-playlist-url"`
		Assets         []struct {
			Flavor string `json:"flavor"`
			URL    string `json:"URL"`
		} `json:"assets"`
	} `json:"songList"`
	Status int `json:"status"`
}

func extractKidBase64(b string, mvmode bool) (string, string, string, error) {
	return extractKidBase64WithContext(context.Background(), b, mvmode)
}

func extractKidBase64WithContext(ctx context.Context, b string, mvmode bool) (string, string, string, error) {
	resp, err := nethttp.GetWithContext(ctx, b)
	if err != nil {
		return "", "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", "", "", errors.New(resp.Status)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", "", "", err
	}
	masterString := string(body)
	from, listType, err := m3u8.DecodeFrom(strings.NewReader(masterString), true)
	if err != nil {
		return "", "", "", err
	}
	var kidbase64 string
	var uriPrefix string
	var urlBuilder strings.Builder
	if listType == m3u8.MEDIA {
		mediaPlaylist := from.(*m3u8.MediaPlaylist)
		if mediaPlaylist.Key == nil {
			return "", "", "", errors.New("no key information found")
		}
		keyURI := strings.TrimSpace(mediaPlaylist.Key.URI)
		parts := strings.SplitN(keyURI, ",", 2)
		if len(parts) != 2 {
			return "", "", "", fmt.Errorf("invalid key uri: %s", keyURI)
		}
		uriPrefix = strings.TrimSpace(parts[0])
		kidbase64 = strings.TrimSpace(parts[1])
		if uriPrefix == "" || kidbase64 == "" {
			return "", "", "", fmt.Errorf("invalid key uri: %s", keyURI)
		}
		if mediaPlaylist.Map == nil || strings.TrimSpace(mediaPlaylist.Map.URI) == "" {
			return "", "", "", errors.New("init segment uri is missing")
		}
		lastSlashIndex := strings.LastIndex(b, "/")
		if lastSlashIndex < 0 {
			return "", "", "", fmt.Errorf("invalid playlist url: %s", b)
		}
		baseURL := b[:lastSlashIndex]
		urlBuilder.WriteString(baseURL)
		urlBuilder.WriteString("/")
		urlBuilder.WriteString(mediaPlaylist.Map.URI)
		if mvmode {
			for _, segment := range mediaPlaylist.Segments {
				if segment == nil || strings.TrimSpace(segment.URI) == "" {
					continue
				}
				urlBuilder.WriteString(";")
				urlBuilder.WriteString(baseURL)
				urlBuilder.WriteString("/")
				urlBuilder.WriteString(segment.URI)
			}
		}
	} else {
		return "", "", "", errors.New("not a media playlist")
	}
	if urlBuilder.Len() == 0 {
		return "", "", "", errors.New("media playlist did not contain downloadable uris")
	}
	return kidbase64, urlBuilder.String(), uriPrefix, nil
}
func extsong(b string, progress ProgressFunc) bytes.Buffer {
	return extsongWithContext(context.Background(), b, progress)
}

func extsongWithContext(ctx context.Context, b string, progress ProgressFunc) bytes.Buffer {
	resp, err := nethttp.GetWithContext(ctx, b)
	var buffer bytes.Buffer
	if err != nil {
		fmt.Printf("下载文件失败: %v\n", err)
		return buffer
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fmt.Printf("下载文件失败: %s\n", resp.Status)
		return buffer
	}
	if progress != nil {
		pw := &progressWriter{
			cb:    progress,
			phase: "Downloading",
			total: resp.ContentLength,
		}
		if _, err := io.Copy(io.MultiWriter(&buffer, pw), resp.Body); err != nil {
			fmt.Printf("读取下载流失败: %v\n", err)
			return bytes.Buffer{}
		}
	} else {
		bar := progressbar.NewOptions64(
			resp.ContentLength,
			progressbar.OptionClearOnFinish(),
			progressbar.OptionSetElapsedTime(false),
			progressbar.OptionSetPredictTime(false),
			progressbar.OptionShowElapsedTimeOnFinish(),
			progressbar.OptionShowCount(),
			progressbar.OptionEnableColorCodes(true),
			progressbar.OptionShowBytes(true),
			progressbar.OptionSetDescription("Downloading..."),
			progressbar.OptionSetTheme(progressbar.Theme{
				Saucer:        "",
				SaucerHead:    "",
				SaucerPadding: "",
				BarStart:      "",
				BarEnd:        "",
			}),
		)
		if _, err := io.Copy(io.MultiWriter(&buffer, bar), resp.Body); err != nil {
			fmt.Printf("读取下载流失败: %v\n", err)
			return bytes.Buffer{}
		}
	}
	return buffer
}
func Run(adamId string, trackpath string, authtoken string, mutoken string, mvmode bool, serverUrl string, progress ProgressFunc) (string, error) {
	return RunWithContext(context.Background(), adamId, trackpath, authtoken, mutoken, mvmode, serverUrl, progress)
}

func RunWithContext(ctx context.Context, adamId string, trackpath string, authtoken string, mutoken string, mvmode bool, serverUrl string, progress ProgressFunc) (string, error) {
	var keystr string //for mv key
	var fileurl string
	var kidBase64 string
	var uriPrefix string
	var err error
	if ctx == nil {
		ctx = context.Background()
	}
	if mvmode {
		kidBase64, fileurl, uriPrefix, err = extractKidBase64WithContext(ctx, trackpath, true)
		if err != nil {
			return "", err
		}
	} else {
		fileurl, kidBase64, uriPrefix, err = GetWebplaybackWithContext(ctx, adamId, authtoken, mutoken, false)
		if err != nil {
			return "", err
		}
	}
	ctx = context.WithValue(ctx, "pssh", kidBase64)
	ctx = context.WithValue(ctx, "adamId", adamId)
	ctx = context.WithValue(ctx, "uriPrefix", uriPrefix)
	pssh, err := getPSSH("", kidBase64)
	//fmt.Println(pssh)
	if err != nil {
		fmt.Println(err)
		return "", err
	}
	headers := map[string]string{
		"authorization":            "Bearer " + authtoken,
		"x-apple-music-user-token": mutoken,
	}
	client := resty.New()
	client.SetHeaders(headers)
	key := key.Key{
		ReqCli:        client,
		BeforeRequest: BeforeRequest,
		AfterRequest:  AfterRequest,
	}
	key.CdmInit()
	var keybt []byte
	if serverUrl != "" {
		keystr, keybt, err = key.GetKey(ctx, serverUrl, pssh, nil)
		if err != nil {
			fmt.Println(err)
			return "", err
		}
	} else {
		keystr, keybt, err = key.GetKey(ctx, "https://play.itunes.apple.com/WebObjects/MZPlay.woa/wa/acquireWebPlaybackLicense", pssh, nil)
		if err != nil {
			fmt.Println(err)
			return "", err
		}
	}
	if mvmode {
		keyAndUrls := "1:" + keystr + ";" + fileurl
		return keyAndUrls, nil
	}
	body := extsongWithContext(ctx, fileurl, progress)
	fmt.Print("Downloaded\n")

	if progress != nil {
		progress("Decrypting", 0, 0)
	}
	// create output file
	ofh, err := os.Create(trackpath)
	if err != nil {
		fmt.Printf("创建文件失败: %v\n", err)
		return "", err
	}
	defer ofh.Close()
	err = DecryptMP4(&body, keybt, ofh)
	if err != nil {
		fmt.Print("Decryption failed\n")
		return "", err
	}
	fmt.Print("Decrypted\n")
	if progress != nil {
		progress("Decrypting", 1, 1)
	}
	return "", nil
}

// Segment 结构体用于在 Channel 中传递分段数据
type Segment struct {
	Index int
	Data  []byte
}

type segmentStats struct {
	mu      sync.Mutex
	success int
	failed  map[int]string
}

func newSegmentStats(total int) *segmentStats {
	return &segmentStats{failed: make(map[int]string, total)}
}

func (s *segmentStats) markSuccess() {
	s.mu.Lock()
	s.success++
	s.mu.Unlock()
}

func (s *segmentStats) markFailure(index int, reason string) {
	s.mu.Lock()
	s.failed[index] = reason
	s.mu.Unlock()
}

func (s *segmentStats) snapshotFailed() (success int, failed map[int]string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cloned := make(map[int]string, len(s.failed))
	for k, v := range s.failed {
		cloned[k] = v
	}
	return s.success, cloned
}

func segmentRetryDelay(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	// 0.6s, 1.2s, 2.4s, 4.8s ...
	delay := 600 * time.Millisecond
	for i := 1; i < attempt; i++ {
		delay *= 2
		if delay > 6*time.Second {
			return 6 * time.Second
		}
	}
	return delay
}

func isRetryableSegmentStatus(code int) bool {
	return code == http.StatusRequestTimeout ||
		code == http.StatusTooManyRequests ||
		(code >= 500 && code <= 599)
}

func buildSegmentDownloadErrorSummary(total, success int, failed map[int]string) string {
	if len(failed) == 0 {
		return fmt.Sprintf("分段下载不完整: 成功 %d/%d", success, total)
	}
	indexes := make([]int, 0, len(failed))
	for idx := range failed {
		indexes = append(indexes, idx)
	}
	sort.Ints(indexes)
	preview := make([]string, 0, 5)
	for i, idx := range indexes {
		if i >= 5 {
			break
		}
		preview = append(preview, fmt.Sprintf("%d(%s)", idx, failed[idx]))
	}
	return fmt.Sprintf("分段下载失败: 成功 %d/%d, 失败分段=%s", success, total, strings.Join(preview, ", "))
}

func downloadSegment(
	ctx context.Context,
	url string,
	index int,
	wg *sync.WaitGroup,
	segmentsChan chan<- Segment,
	client *http.Client,
	limiter chan struct{},
	stats *segmentStats,
) {
	// 函数退出时，从 limiter 中接收一个值，释放一个并发槽位
	defer func() {
		<-limiter
		wg.Done()
	}()

	const maxRetries = 5
	var lastErr string

	for attempt := 1; attempt <= maxRetries; attempt++ {
		reqCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
		req, err := http.NewRequestWithContext(reqCtx, "GET", url, nil)
		if err != nil {
			cancel()
			lastErr = fmt.Sprintf("创建请求失败: %v", err)
			break
		}

		resp, err := client.Do(req)
		if err != nil {
			cancel()
			lastErr = fmt.Sprintf("下载失败: %v", err)
			if attempt < maxRetries {
				time.Sleep(segmentRetryDelay(attempt))
				continue
			}
			break
		}

		if resp.StatusCode != http.StatusOK {
			lastErr = fmt.Sprintf("服务器返回状态码 %d", resp.StatusCode)
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			cancel()
			if attempt < maxRetries && isRetryableSegmentStatus(resp.StatusCode) {
				time.Sleep(segmentRetryDelay(attempt))
				continue
			}
			break
		}

		data, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		cancel()
		if err != nil {
			lastErr = fmt.Sprintf("读取数据失败: %v", err)
			if attempt < maxRetries {
				time.Sleep(segmentRetryDelay(attempt))
				continue
			}
			break
		}

		// 将下载好的分段（包含序号和数据）发送到 Channel
		segmentsChan <- Segment{Index: index, Data: data}
		stats.markSuccess()
		return
	}

	if lastErr == "" {
		lastErr = "未知错误"
	}
	stats.markFailure(index, lastErr)
	fmt.Printf("错误(分段 %d): %s\n", index, lastErr)
}

// fileWriter 从 Channel 接收分段并按顺序写入文件
func fileWriter(wg *sync.WaitGroup, segmentsChan <-chan Segment, outputFile io.Writer, totalSegments int, errCh chan<- error) {
	defer wg.Done()

	// 缓冲区，用于存放乱序到达的分段
	// key 是分段序号，value 是分段数据
	segmentBuffer := make(map[int][]byte)
	nextIndex := 0 // 期望写入的下一个分段的序号
	var writeErr error

	recordWriteErr := func(index int, err error) {
		if err == nil {
			return
		}
		if writeErr == nil {
			writeErr = fmt.Errorf("写入分段 %d 失败: %w", index, err)
		}
	}

	for segment := range segmentsChan {
		// 检查收到的分段是否是当前期望的
		if segment.Index == nextIndex {
			//fmt.Printf("写入分段 %d\n", segment.Index)
			_, err := outputFile.Write(segment.Data)
			recordWriteErr(segment.Index, err)
			nextIndex++

			// 检查缓冲区中是否有下一个连续的分段
			for {
				data, ok := segmentBuffer[nextIndex]
				if !ok {
					break // 缓冲区里没有下一个，跳出循环，等待下一个分段到达
				}

				//fmt.Printf("从缓冲区写入分段 %d\n", nextIndex)
				_, err := outputFile.Write(data)
				recordWriteErr(nextIndex, err)
				// 从缓冲区删除已写入的分段，释放内存
				delete(segmentBuffer, nextIndex)
				nextIndex++
			}
		} else {
			// 如果不是期望的分段，先存入缓冲区
			//fmt.Printf("缓冲分段 %d (等待 %d)\n", segment.Index, nextIndex)
			segmentBuffer[segment.Index] = segment.Data
		}
	}

	// 确保所有分段都已写入
	if nextIndex != totalSegments && writeErr == nil {
		writeErr = fmt.Errorf("写入完成但分段不完整: 期望 %d, 实际 %d", totalSegments, nextIndex)
	}
	errCh <- writeErr
}

func ExtMvData(keyAndUrls string, savePath string) error {
	return ExtMvDataWithContext(context.Background(), keyAndUrls, savePath)
}

func ExtMvDataWithContext(ctx context.Context, keyAndUrls string, savePath string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	segments := strings.Split(keyAndUrls, ";")
	key := segments[0]
	//fmt.Println(key)
	urls := segments[1:]
	tempFile, err := os.CreateTemp("", "enc_mv_data-*.mp4")
	if err != nil {
		fmt.Printf("创建文件失败：%v\n", err)
		return err
	}
	defer os.Remove(tempFile.Name())
	defer tempFile.Close()

	var downloadWg, writerWg sync.WaitGroup
	segmentsChan := make(chan Segment, len(urls))
	writerErrCh := make(chan error, 1)
	// --- 新增代码: 定义最大并发数 ---
	const maxConcurrency = 10
	// --- 新增代码: 创建带缓冲的 Channel 作为信号量 ---
	limiter := make(chan struct{}, maxConcurrency)
	client := &http.Client{
		Transport: &http.Transport{
			Proxy:               http.ProxyFromEnvironment,
			MaxIdleConns:        32,
			MaxIdleConnsPerHost: 16,
			IdleConnTimeout:     30 * time.Second,
			// Apple CDN 在高并发下偶发 H2 stream reset，强制 H1 更稳。
			ForceAttemptHTTP2: false,
		},
	}
	defer client.CloseIdleConnections()
	stats := newSegmentStats(len(urls))

	// 初始化进度条
	bar := progressbar.DefaultBytes(-1, "Downloading...")
	barWriter := io.MultiWriter(tempFile, bar)

	// 启动写入 Goroutine
	writerWg.Add(1)
	go fileWriter(&writerWg, segmentsChan, barWriter, len(urls), writerErrCh)

	// 启动下载 Goroutines
	for i, url := range urls {
		// 在启动 Goroutine 前，向 limiter 发送一个值来“获取”一个槽位
		// 如果 limiter 已满 (达到10个)，这里会阻塞，直到有其他任务完成并释放槽位
		//fmt.Printf("请求启动任务 %d...\n", i)
		limiter <- struct{}{}
		//fmt.Printf("...任务 %d 已启动\n", i)

		downloadWg.Add(1)
		// 将 limiter 传递给下载函数
		go downloadSegment(ctx, url, i, &downloadWg, segmentsChan, client, limiter, stats)
	}

	// 等待所有下载任务完成
	downloadWg.Wait()
	// 下载完成后，关闭 Channel。写入 Goroutine 会在处理完 Channel 中所有数据后退出。
	close(segmentsChan)

	// 等待写入 Goroutine 完成所有写入和缓冲处理
	writerWg.Wait()
	if writerErr := <-writerErrCh; writerErr != nil {
		return writerErr
	}

	success, failed := stats.snapshotFailed()
	if success != len(urls) {
		return fmt.Errorf(buildSegmentDownloadErrorSummary(len(urls), success, failed))
	}

	// 显式关闭文件（defer会再次调用，但重复关闭是安全的）
	if err := tempFile.Close(); err != nil {
		fmt.Printf("关闭临时文件失败: %v\n", err)
		return err
	}
	fmt.Println("\nDownloaded.")

	result, err := cmdrunner.RunWithOptions(ctx, "mp4decrypt", []string{"--key", key, tempFile.Name(), filepath.Base(savePath)}, cmdrunner.RunOptions{
		Dir: filepath.Dir(savePath), // 设置 mp4decrypt 的工作目录以解决中文路径错误
	})
	if err != nil {
		fmt.Printf("Decrypt failed: %v\n", err)
		fmt.Printf("Output:\n%s\n", result.Combined)
		return err
	} else {
		fmt.Println("Decrypted.")
	}
	return nil
}

// DecryptMP4 decrypts a fragmented MP4 file with keys from widevice license. Supports CENC and CBCS schemes.
func DecryptMP4(r io.Reader, key []byte, w io.Writer) error {
	// Initialization
	inMp4, err := mp4.DecodeFile(r)
	if err != nil {
		return fmt.Errorf("failed to decode file: %w", err)
	}
	if !inMp4.IsFragmented() {
		return errors.New("file is not fragmented")
	}
	// Handle init segment
	if inMp4.Init == nil {
		return errors.New("no init part of file")
	}
	decryptInfo, err := mp4.DecryptInit(inMp4.Init)
	if err != nil {
		return fmt.Errorf("failed to decrypt init: %w", err)
	}
	if err = inMp4.Init.Encode(w); err != nil {
		return fmt.Errorf("failed to write init: %w", err)
	}
	// Decode segments
	for _, seg := range inMp4.Segments {
		if err = mp4.DecryptSegment(seg, decryptInfo, key); err != nil {
			if err.Error() == "no senc box in traf" {
				// No SENC box, skip decryption for this segment as samples can have
				// unencrypted segments followed by encrypted segments. See:
				// https://github.com/iyear/gowidevine/pull/26#issuecomment-2385960551
				err = nil
			} else {
				return fmt.Errorf("failed to decrypt segment: %w", err)
			}
		}
		if err = seg.Encode(w); err != nil {
			return fmt.Errorf("failed to encode segment: %w", err)
		}
	}
	return nil
}
