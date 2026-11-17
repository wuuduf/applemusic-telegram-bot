package app

// botRipSong and botRipAlbum are how the Telegram bot starts song and album
// downloads. End-to-end tests replace them to stand in for Apple Music and
// the decrypt wrapper; everything around them (queueing, status messages,
// uploads, caching, persistence) runs as in production. The CLI calls
// ripSong and ripAlbum directly.
var (
	botRipSong  = ripSong
	botRipAlbum = ripAlbum
)
