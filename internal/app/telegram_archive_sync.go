package app

import (
	"fmt"
	"strings"
	"time"
)

const telegramArchiveCaptionPrefix = "#amdlcache"

type archiveAudioMeta struct {
	TrackID    string
	Format     string
	Storefront string
}

func appendArchiveAudioCaption(caption string, trackID string, format string, storefront string) string {
	trackID = strings.TrimSpace(trackID)
	if trackID == "" {
		return strings.TrimSpace(caption)
	}
	format = normalizeTelegramFormat(format)
	if format == "" {
		format = defaultTelegramFormat
	}
	storefront = normalizeSongCommentStorefront(storefront)
	parts := []string{
		telegramArchiveCaptionPrefix,
		"tid=" + trackID,
		"fmt=" + format,
	}
	if storefront != "" {
		parts = append(parts, "sf="+storefront)
	}
	metaLine := strings.Join(parts, " ")
	caption = strings.TrimSpace(caption)
	if caption == "" {
		return metaLine
	}
	return caption + "\n" + metaLine
}

func parseArchiveAudioCaption(caption string) (archiveAudioMeta, bool) {
	lines := strings.Split(strings.TrimSpace(caption), "\n")
	for _, rawLine := range lines {
		line := strings.TrimSpace(rawLine)
		if !strings.HasPrefix(line, telegramArchiveCaptionPrefix) {
			continue
		}
		fields := strings.Fields(line)
		meta := archiveAudioMeta{}
		for _, field := range fields[1:] {
			key, value, ok := strings.Cut(field, "=")
			if !ok {
				continue
			}
			key = strings.TrimSpace(strings.ToLower(key))
			value = strings.TrimSpace(value)
			switch key {
			case "tid":
				meta.TrackID = value
			case "fmt":
				meta.Format = normalizeTelegramFormat(value)
			case "sf":
				meta.Storefront = normalizeSongCommentStorefront(value)
			}
		}
		meta.TrackID = strings.TrimSpace(meta.TrackID)
		if meta.TrackID == "" {
			return archiveAudioMeta{}, false
		}
		if meta.Format == "" {
			meta.Format = defaultTelegramFormat
		}
		return meta, true
	}
	return archiveAudioMeta{}, false
}

func (b *TelegramBot) shouldIngestArchiveMessage(msg *Message) bool {
	if b == nil || msg == nil || b.forwardChatID == 0 {
		return false
	}
	return msg.Chat.ID == b.forwardChatID
}

func (b *TelegramBot) maybeIngestArchiveMessage(msg *Message) bool {
	if !b.shouldIngestArchiveMessage(msg) {
		return false
	}
	if msg.Audio == nil {
		return false
	}
	fileID := strings.TrimSpace(msg.Audio.FileID)
	if fileID == "" {
		return false
	}
	meta, ok := parseArchiveAudioCaption(msg.Caption)
	if !ok {
		return false
	}
	b.storeCachedAudio(meta.TrackID, CachedAudio{
		FileID:     fileID,
		FileSize:   msg.Audio.FileSize,
		Format:     meta.Format,
		Storefront: meta.Storefront,
		CreatedAt:  time.Now(),
	})
	fmt.Printf("telegram archive ingest cached audio chat=%d message=%d track=%s format=%s\n", msg.Chat.ID, msg.MessageID, meta.TrackID, meta.Format)
	return true
}
