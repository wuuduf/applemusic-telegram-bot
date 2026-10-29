package app

import (
	"sort"
	"strings"
	"time"
)

func setSearchMeta(trackID string, title string, performer string) {
	trackID = strings.TrimSpace(trackID)
	if trackID == "" {
		return
	}
	meta := AudioMeta{
		TrackID:   trackID,
		Title:     strings.TrimSpace(title),
		Performer: strings.TrimSpace(performer),
	}
	if meta.Title == "" && meta.Performer == "" {
		return
	}
	searchMetaMu.Lock()
	storeSearchMetaLocked(trackID, meta, time.Now())
	searchMetaMu.Unlock()
}

// storeSearchMetaLocked records meta and prunes expired or excess entries.
// Caller holds searchMetaMu.
func storeSearchMetaLocked(trackID string, meta AudioMeta, now time.Time) {
	searchMetaByID[trackID] = meta
	searchMetaAt[trackID] = now
	if len(searchMetaByID) <= searchMetaMaxEntries && now.Sub(searchMetaLastPrune) < searchMetaPruneEvery {
		return
	}
	searchMetaLastPrune = now
	for id := range searchMetaByID {
		if now.Sub(searchMetaAt[id]) > searchMetaTTL {
			delete(searchMetaByID, id)
			delete(searchMetaAt, id)
		}
	}
	if excess := len(searchMetaByID) - searchMetaMaxEntries; excess > 0 {
		ids := make([]string, 0, len(searchMetaByID))
		for id := range searchMetaByID {
			ids = append(ids, id)
		}
		sort.Slice(ids, func(i, j int) bool { return searchMetaAt[ids[i]].Before(searchMetaAt[ids[j]]) })
		for _, id := range ids[:excess] {
			delete(searchMetaByID, id)
			delete(searchMetaAt, id)
		}
	}
}

func popSearchMeta(trackID string) (AudioMeta, bool) {
	searchMetaMu.Lock()
	defer searchMetaMu.Unlock()
	meta, ok := searchMetaByID[trackID]
	if ok {
		delete(searchMetaByID, trackID)
		delete(searchMetaAt, trackID)
	}
	return meta, ok
}
