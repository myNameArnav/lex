package store

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"runtime"
	"slices"
	"strconv"
	"strings"
)

// Config is the server-wide settings document, editable from the admin UI.
type Config struct {
	ServerName string `json:"serverName"`

	// Metadata
	TMDBKey          string `json:"tmdbKey"`
	MetadataLanguage string `json:"metadataLanguage"`
	EnableTMDB       bool   `json:"enableTmdb"`
	EnableTVmaze     bool   `json:"enableTvmaze"`
	RadarrURL        string `json:"radarrUrl"`
	RadarrKey        string `json:"radarrKey"`
	RadarrDetectDone bool   `json:"radarrDetectDone"`
	HWAutoDone       bool   `json:"hwAutoDone"`

	// Subtitle downloads (OpenSubtitles.com)
	OpenSubtitlesKey  string `json:"openSubtitlesKey"`
	OpenSubtitlesUser string `json:"openSubtitlesUser"`
	OpenSubtitlesPass string `json:"openSubtitlesPass"`
	// SubtitleDownloads is who may search and download: "everyone" or
	// "admins". Downloads share one account's small daily allowance.
	SubtitleDownloads string `json:"subtitleDownloads"`

	// Seek-bar previews
	TrickplayEnabled  bool `json:"trickplayEnabled"`
	TrickplayInterval int  `json:"trickplayInterval"` // seconds between thumbnails
	TrickplayWidth    int  `json:"trickplayWidth"`
	UseLocalMetadata  bool `json:"useLocalMetadata"`
	GenerateThumbs    bool `json:"generateThumbs"`

	// Scanning
	ScanIntervalMin   int  `json:"scanIntervalMin"`
	ScanOnStartup     bool `json:"scanOnStartup"`
	ProbeWorkers      int  `json:"probeWorkers"`
	ExtractSubsOnScan bool `json:"extractSubsOnScan"`

	// Playback / transcoding
	EnableDirectPlay   bool   `json:"enableDirectPlay"`
	EnableRemux        bool   `json:"enableRemux"`
	EnableTranscode    bool   `json:"enableTranscode"`
	MaxTranscodes      int    `json:"maxTranscodes"`
	VideoEncoder       string `json:"videoEncoder"` // libx264 | h264_v4l2m2m
	HWDecode           bool   `json:"hwDecode"`
	X264Preset         string `json:"x264Preset"`
	X264CRF            int    `json:"x264Crf"`
	TranscodeThreads   int    `json:"transcodeThreads"`
	FFmpegNice         int    `json:"ffmpegNice"`
	AudioChannels      int    `json:"audioChannels"` // 2 or 6
	AudioBitrate       int    `json:"audioBitrate"`  // kbps for stereo; scaled for 5.1
	ClientAudio        bool   `json:"clientAudio"`   // browsers convert AC-3/E-AC-3/DTS/TrueHD themselves
	ClientRemux        bool   `json:"clientRemux"`   // browsers remux MKV/WebM themselves (Direct Stream without ffmpeg)
	Tonemap            bool   `json:"tonemap"`
	FragmentMs         int    `json:"fragmentMs"`
	KeyframeSec        int    `json:"keyframeSec"`
	MaxTranscodeHeight int    `json:"maxTranscodeHeight"`

	// Network
	RemoteMaxBitrate int    `json:"remoteMaxBitrate"` // kbps, 0 = unlimited
	LocalNetworks    string `json:"localNetworks"`
	TrustProxy       bool   `json:"trustProxy"`
	TrustedProxies   string `json:"trustedProxies"` // comma-separated proxy peer CIDRs
	StreamBufferKB   int    `json:"streamBufferKb"`

	WebhookToken string `json:"webhookToken"`
	// Sonarr is only used to set up the rescan webhook (Radarr's address
	// above serves both that and metadata).
	SonarrURL        string `json:"sonarrUrl"`
	SonarrKey        string `json:"sonarrKey"`
	SonarrDetectDone bool   `json:"sonarrDetectDone"`

	// LiveTV turns on the Live TV channels (off by default). TVChannels are
	// the admin's own. TVOrder is the lineup, by channel id ("custom-<id>",
	// "all", "lib-1", "genre-Drama"): channels it doesn't list follow it, the
	// admin's first. TVHidden are the channels turned off; they keep their
	// number. All of these are edited through /api/admin/tv, not the config.
	LiveTV     bool        `json:"liveTv"`
	TVChannels []TVChannel `json:"tvChannels"`
	TVOrder    []string    `json:"tvOrder"`
	TVHidden   []string    `json:"tvHidden"`
	// TVAutomatic are the automatic channels the admin added to the lineup
	// ("all", "lib-1", "genre-Drama"): none are on it until added.
	TVAutomatic []string `json:"tvAutomatic"`
	// TVStreams are live channels from the internet (IPTV), "stream-<id>"
	// in the lineup; TVSources are the playlists (M3U) they can be picked
	// from, with a guide (XMLTV) for them.
	TVStreams []TVStream `json:"tvStreams"`
	TVSources []TVSource `json:"tvSources"`

	// SSD cache for media files living on a slower disk.
	CacheEnabled   bool   `json:"cacheEnabled"`
	CacheDir       string `json:"cacheDir"` // empty = <data>/cache
	CacheMaxGB     int    `json:"cacheMaxGb"`
	CacheMinFreeGB int    `json:"cacheMinFreeGb"`
	CacheOnPlay    bool   `json:"cacheOnPlay"`
	CachePrefetch  int    `json:"cachePrefetch"` // next N episodes
	CacheSpeedMBs  int    `json:"cacheSpeedMbs"` // copy rate limit, 0 = unlimited
	CacheMaxFileGB int    `json:"cacheMaxFileGb"`

	// Intro detection
	IntroDetect   bool `json:"introDetect"`
	IntroScanSecs int  `json:"introScanSecs"`
	IntroMinSecs  int  `json:"introMinSecs"`
	IntroMaxSecs  int  `json:"introMaxSecs"`

	// End-credits detection (same fingerprinting, on the end of episodes)
	CreditsDetect   bool `json:"creditsDetect"`
	CreditsScanSecs int  `json:"creditsScanSecs"`
	CreditsMinSecs  int  `json:"creditsMinSecs"`

	// HistoryRetentionDays is how long playback history is kept; older plays
	// are pruned in the background. 0 keeps it forever.
	HistoryRetentionDays int `json:"historyRetentionDays"`

	// Database backups. BackupDir empty = <data>/backups (otherwise a
	// "lex-backups" folder inside it). BackupKeep is how many of each kind
	// (daily, manual, pre-migration, pre-restore) are kept.
	BackupDaily bool   `json:"backupDaily"`
	BackupDir   string `json:"backupDir"`
	BackupKeep  int    `json:"backupKeep"`
}

// TVChannel is a Live TV channel an admin made. Kind "picked" airs just
// its Titles (movies and shows, by item id). Otherwise it airs what matches
// every filter: any of its libraries, any of its genres, its kind ("movie",
// "show", or "" for both), where an empty filter doesn't limit, and leaves
// out the titles in Exclude. A picked channel keeps Exclude (and the
// filters) for when it's switched back, but doesn't apply it: picking a
// title is explicit.
type TVChannel struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Libraries []int64  `json:"libraries"`
	Genres    []string `json:"genres"`
	Kind      string   `json:"kind"`
	Titles    []int64  `json:"titles"`
	Exclude   []int64  `json:"exclude"`
}

// TVStream is a live channel from the internet: an HLS (or MPEG-TS) URL
// Lex fetches with the stream's own User-Agent and Referer, which many
// providers require and a browser can't send. GuideID is its tvg-id in
// the source's guide; Source the playlist it was picked from, if any.
type TVStream struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	URL       string `json:"url"`
	UserAgent string `json:"userAgent"`
	Referrer  string `json:"referrer"`
	Logo      string `json:"logo"`
	GuideID   string `json:"guideId"`
	Source    string `json:"source"`
	// Headers are any other HTTP headers the provider wants (a cookie, a
	// token, an Origin), sent with every request for the stream.
	Headers []TVHeader `json:"headers"`
}

// TVHeader is an HTTP header sent when fetching a live channel.
type TVHeader struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// maxHeaders is how many headers a stream or playlist may send.
const maxHeaders = 20

// CleanHeaders keeps the headers that can be sent: a name of letters,
// digits and dashes, a value without line breaks, none that HTTP sets
// itself. A User-Agent or Referer among them moves to its own field
// (returned, empty when there's none).
func CleanHeaders(in []TVHeader) (out []TVHeader, ua, ref string) {
	out = []TVHeader{}
	for _, hd := range in {
		name := strings.TrimSpace(hd.Name)
		value := strings.Join(strings.Fields(hd.Value), " ")
		if name == "" || !headerName(name) {
			continue
		}
		switch strings.ToLower(name) {
		case "user-agent":
			ua = value
			continue
		case "referer", "referrer":
			ref = value
			continue
		case "host", "content-length", "connection", "transfer-encoding", "te", "upgrade", "expect":
			continue
		}
		if len(out) < maxHeaders {
			out = append(out, TVHeader{Name: name, Value: value})
		}
	}
	return out, ua, ref
}

func headerName(s string) bool {
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return len(s) <= 64
}

// TVSource is an IPTV playlist (M3U) to pick channels from, and a guide
// (XMLTV) for the channels picked. UserAgent is sent for both, and is what
// its channels use unless the playlist names one.
type TVSource struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	URL       string `json:"url"`
	GuideURL  string `json:"guideUrl"`
	UserAgent string `json:"userAgent"`
	// PlaylistFile and GuideFile are uploaded files (in the data folder's
	// livetv folder), used instead of URL and GuideURL.
	PlaylistFile string `json:"playlistFile"`
	GuideFile    string `json:"guideFile"`
	// Headers are sent when reading the playlist and guide, and with its
	// channels unless the playlist gives them their own.
	Headers []TVHeader `json:"headers"`
}

// UploadName reports whether name is a file Lex named for an upload: hex
// and an extension, nothing that leaves its folder.
func UploadName(name string) bool {
	stem, ext, ok := strings.Cut(name, ".")
	if !ok || len(stem) < 8 || (ext != "m3u" && ext != "xmltv") {
		return false
	}
	for _, c := range stem {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// WebURL reports whether u is an http(s) URL with a host: the only kind
// Lex fetches for Live TV (ffmpeg would also open local files).
func WebURL(u string) bool {
	p, err := url.Parse(u)
	return err == nil && (p.Scheme == "http" || p.Scheme == "https") && p.Host != ""
}

// Clean trims a stream's fields; a header value can't hold a line break.
func (st TVStream) Clean() TVStream {
	one := func(v string) string { return strings.Join(strings.Fields(v), " ") }
	st.ID, st.Name, st.URL, st.Logo, st.GuideID, st.Source = strings.TrimSpace(st.ID), one(st.Name), strings.TrimSpace(st.URL), strings.TrimSpace(st.Logo), strings.TrimSpace(st.GuideID), strings.TrimSpace(st.Source)
	st.UserAgent, st.Referrer = one(st.UserAgent), one(st.Referrer)
	var ua, ref string
	st.Headers, ua, ref = CleanHeaders(st.Headers)
	if ua != "" && st.UserAgent == "" {
		st.UserAgent = ua
	}
	if ref != "" && st.Referrer == "" {
		st.Referrer = ref
	}
	if st.Logo != "" && !WebURL(st.Logo) {
		st.Logo = ""
	}
	return st
}

// Clean trims a source's fields.
func (src TVSource) Clean() TVSource {
	src.ID, src.Name, src.URL, src.GuideURL = strings.TrimSpace(src.ID), strings.Join(strings.Fields(src.Name), " "), strings.TrimSpace(src.URL), strings.TrimSpace(src.GuideURL)
	src.UserAgent = strings.Join(strings.Fields(src.UserAgent), " ")
	if !UploadName(src.PlaylistFile) {
		src.PlaylistFile = ""
	}
	if !UploadName(src.GuideFile) {
		src.GuideFile = ""
	}
	if src.PlaylistFile != "" {
		src.URL = ""
	}
	if src.GuideFile != "" {
		src.GuideURL = ""
	}
	var ua string
	src.Headers, ua, _ = CleanHeaders(src.Headers)
	if ua != "" && src.UserAgent == "" {
		src.UserAgent = ua
	}
	return src
}

func DefaultConfig() Config {
	return Config{
		ServerName:         "lex",
		MetadataLanguage:   "en-US",
		SubtitleDownloads:  "everyone",
		EnableTMDB:         true,
		EnableTVmaze:       true,
		UseLocalMetadata:   true,
		GenerateThumbs:     true,
		ScanIntervalMin:    30,
		ScanOnStartup:      true,
		ProbeWorkers:       1,
		EnableDirectPlay:   true,
		EnableRemux:        true,
		EnableTranscode:    true,
		MaxTranscodes:      1,
		VideoEncoder:       "libx264",
		X264Preset:         lowPowerPreset(),
		X264CRF:            23,
		TranscodeThreads:   0,
		FFmpegNice:         5,
		AudioChannels:      2,
		AudioBitrate:       192,
		ClientAudio:        true,
		ClientRemux:        true,
		FragmentMs:         1000,
		KeyframeSec:        2,
		MaxTranscodeHeight: lowPowerHeight(),
		RemoteMaxBitrate:   0,
		LocalNetworks:      "127.0.0.0/8, 10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16, ::1/128, fc00::/7, fe80::/10",
		TrustProxy:         false,
		TrustedProxies:     "127.0.0.0/8, ::1/128",
		StreamBufferKB:     256,
		CacheMaxGB:         50,
		CacheMinFreeGB:     20,
		CacheOnPlay:        true,
		CachePrefetch:      2,
		CacheSpeedMBs:      40,
		CacheMaxFileGB:     25,
		IntroDetect:        true,
		TrickplayEnabled:   true,
		TrickplayInterval:  10,
		TrickplayWidth:     240,
		IntroScanSecs:      600,
		IntroMinSecs:       12,
		IntroMaxSecs:       130,
		CreditsDetect:      true,
		CreditsScanSecs:    420,
		CreditsMinSecs:     15,

		HistoryRetentionDays: 365,

		BackupDaily: true,
		BackupKeep:  7,
	}
}

// ARM boards (Raspberry Pi etc.) can't afford slower x264 presets: a Pi 4
// manages ~1.8x realtime for 1080p H.264 -> 720p only with ultrafast.
func lowPowerPreset() string {
	if runtime.GOARCH == "arm64" || runtime.GOARCH == "arm" {
		return "ultrafast"
	}
	return "veryfast"
}

func lowPowerHeight() int {
	if runtime.GOARCH == "arm64" || runtime.GOARCH == "arm" {
		return 720
	}
	return 1080
}

func (c *Config) normalize() {
	d := DefaultConfig()
	if c.ServerName == "" {
		c.ServerName = d.ServerName
	}
	if c.MetadataLanguage == "" {
		c.MetadataLanguage = d.MetadataLanguage
	}
	if c.SubtitleDownloads != "admins" {
		c.SubtitleDownloads = "everyone"
	}
	if c.ProbeWorkers < 1 {
		c.ProbeWorkers = 1
	}
	if c.ProbeWorkers > 8 {
		c.ProbeWorkers = 8
	}
	if c.MaxTranscodes < 0 {
		c.MaxTranscodes = 0
	}
	if c.VideoEncoder == "" {
		c.VideoEncoder = d.VideoEncoder
	}
	if c.X264Preset == "" {
		c.X264Preset = d.X264Preset
	}
	if c.X264CRF <= 0 || c.X264CRF > 51 {
		c.X264CRF = d.X264CRF
	}
	if c.AudioChannels != 6 {
		c.AudioChannels = 2
	}
	if c.AudioBitrate < 64 || c.AudioBitrate > 640 {
		c.AudioBitrate = d.AudioBitrate
	}
	if c.FragmentMs < 200 || c.FragmentMs > 10000 {
		c.FragmentMs = d.FragmentMs
	}
	if c.KeyframeSec < 1 || c.KeyframeSec > 10 {
		c.KeyframeSec = d.KeyframeSec
	}
	if c.MaxTranscodeHeight <= 0 {
		c.MaxTranscodeHeight = d.MaxTranscodeHeight
	}
	if c.FFmpegNice < 0 || c.FFmpegNice > 19 {
		c.FFmpegNice = d.FFmpegNice
	}
	if c.StreamBufferKB < 32 || c.StreamBufferKB > 4096 {
		c.StreamBufferKB = d.StreamBufferKB
	}
	if c.ScanIntervalMin < 0 {
		c.ScanIntervalMin = 0
	}
	if c.CacheMaxGB < 1 {
		c.CacheMaxGB = 1
	}
	if c.CacheMinFreeGB < 0 {
		c.CacheMinFreeGB = 0
	}
	if c.CachePrefetch < 0 || c.CachePrefetch > 10 {
		c.CachePrefetch = d.CachePrefetch
	}
	if c.CacheSpeedMBs < 0 {
		c.CacheSpeedMBs = 0
	}
	if c.CacheMaxFileGB < 1 {
		c.CacheMaxFileGB = d.CacheMaxFileGB
	}
	if c.IntroScanSecs < 120 || c.IntroScanSecs > 1800 {
		c.IntroScanSecs = d.IntroScanSecs
	}
	if c.IntroMinSecs < 5 || c.IntroMinSecs > 60 {
		c.IntroMinSecs = d.IntroMinSecs
	}
	if c.IntroMaxSecs < c.IntroMinSecs+10 || c.IntroMaxSecs > 300 {
		c.IntroMaxSecs = d.IntroMaxSecs
	}
	if c.CreditsScanSecs < 120 || c.CreditsScanSecs > 1200 {
		c.CreditsScanSecs = d.CreditsScanSecs
	}
	if c.CreditsMinSecs < 5 || c.CreditsMinSecs > 120 {
		c.CreditsMinSecs = d.CreditsMinSecs
	}
	if c.TrickplayInterval < 2 || c.TrickplayInterval > 60 {
		c.TrickplayInterval = d.TrickplayInterval
	}
	if c.TrickplayWidth < 120 || c.TrickplayWidth > 480 {
		c.TrickplayWidth = d.TrickplayWidth
	}
	if c.HistoryRetentionDays < 0 {
		c.HistoryRetentionDays = 0
	}
	if c.HistoryRetentionDays > 36500 {
		c.HistoryRetentionDays = 36500
	}
	if c.BackupKeep < 1 || c.BackupKeep > 100 {
		c.BackupKeep = d.BackupKeep
	}
	c.BackupDir = strings.TrimSpace(c.BackupDir)
	if c.WebhookToken == "" {
		c.WebhookToken = RandomToken(12)
	}
	c.normalizeTVChannels()
}

// normalizeTVChannels gives every channel a unique id (the UI makes one,
// but a hand-edited config may not), a name and a known kind, and uses
// empty lists rather than null so the UI can compare drafts.
func (c *Config) normalizeTVChannels() {
	chs := make([]TVChannel, 0, len(c.TVChannels))
	seen := map[string]bool{}
	for i, ch := range c.TVChannels {
		ch = ch.Clean()
		for ch.ID == "" || seen[ch.ID] {
			ch.ID = RandomToken(4)
		}
		seen[ch.ID] = true
		if ch.Name == "" {
			ch.Name = "Channel " + strconv.Itoa(i+1)
		}
		chs = append(chs, ch)
	}
	c.TVChannels = chs
	streams := make([]TVStream, 0, len(c.TVStreams))
	seen = map[string]bool{}
	for i, st := range c.TVStreams {
		st = st.Clean()
		if !WebURL(st.URL) {
			continue
		}
		for st.ID == "" || seen[st.ID] {
			st.ID = RandomToken(4)
		}
		seen[st.ID] = true
		if st.Name == "" {
			st.Name = "Stream " + strconv.Itoa(i+1)
		}
		streams = append(streams, st)
	}
	c.TVStreams = streams
	sources := make([]TVSource, 0, len(c.TVSources))
	seen = map[string]bool{}
	for _, src := range c.TVSources {
		src = src.Clean()
		if !WebURL(src.URL) && src.PlaylistFile == "" || (src.GuideURL != "" && !WebURL(src.GuideURL)) {
			continue
		}
		for src.ID == "" || seen[src.ID] {
			src.ID = RandomToken(4)
		}
		seen[src.ID] = true
		if src.Name == "" {
			src.Name = src.URL
			if src.Name == "" {
				src.Name = "Playlist"
			}
		}
		sources = append(sources, src)
	}
	c.TVSources = sources
	c.TVOrder, c.TVHidden = c.channelIDs(c.TVOrder), c.channelIDs(c.TVHidden)
	c.TVAutomatic = slices.DeleteFunc(c.channelIDs(c.TVAutomatic), func(id string) bool {
		return strings.HasPrefix(id, "custom-") || strings.HasPrefix(id, "stream-")
	})
}

// Clean trims the channel's id and name, drops a kind it doesn't know,
// non-ids and repeats from its lists, and uses empty lists rather than null
// so the UI can compare drafts.
func (ch TVChannel) Clean() TVChannel {
	ch.ID, ch.Name = strings.TrimSpace(ch.ID), strings.TrimSpace(ch.Name)
	if ch.Kind != "movie" && ch.Kind != "show" && ch.Kind != "picked" {
		ch.Kind = ""
	}
	ch.Libraries, ch.Titles, ch.Exclude = ids(ch.Libraries), ids(ch.Titles), ids(ch.Exclude)
	genres := []string{}
	for _, g := range ch.Genres {
		if g = strings.TrimSpace(g); g != "" && !slices.Contains(genres, g) {
			genres = append(genres, g)
		}
	}
	ch.Genres = genres
	return ch
}

// channelIDs drops blanks, repeats and the ids of the admin's channels that
// no longer exist, keeping the order. Automatic channels the library
// doesn't fill now are kept: they may come back.
func (c *Config) channelIDs(in []string) []string {
	out := []string{}
	for _, id := range in {
		id = strings.TrimSpace(id)
		if id == "" || slices.Contains(out, id) {
			continue
		}
		if custom, ok := strings.CutPrefix(id, "custom-"); ok && !slices.ContainsFunc(c.TVChannels, func(ch TVChannel) bool { return ch.ID == custom }) {
			continue
		}
		if stream, ok := strings.CutPrefix(id, "stream-"); ok && !slices.ContainsFunc(c.TVStreams, func(st TVStream) bool { return st.ID == stream }) {
			continue
		}
		out = append(out, id)
	}
	return out
}

// ids drops non-ids and repeats, keeping the order.
func ids(in []int64) []int64 {
	out := []int64{}
	for _, id := range in {
		if id > 0 && !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	return out
}

func (s *Store) loadConfig() error {
	c := DefaultConfig()
	raw, err := s.getKV("config")
	if err != nil && !errors.Is(err, ErrNotFound) {
		return err
	}
	if raw != "" {
		// Unmarshal over defaults so new fields keep sensible values.
		if err := json.Unmarshal([]byte(raw), &c); err != nil {
			return err
		}
	}
	c.normalize()
	s.cfgMu.Lock()
	s.cfg = c
	s.cfgMu.Unlock()
	if raw == "" {
		return s.setKV("config", jsonString(c))
	}
	return nil
}

func (s *Store) Config() Config {
	s.cfgMu.RLock()
	defer s.cfgMu.RUnlock()
	return s.cfg
}

func (s *Store) SaveConfig(c Config) (Config, error) {
	s.cfgSaveMu.Lock()
	defer s.cfgSaveMu.Unlock()
	return s.saveConfig(c)
}

// UpdateConfig changes the config in place: edit gets the current one, and
// no other save can come in between. An error from edit saves nothing.
func (s *Store) UpdateConfig(edit func(*Config) error) (Config, error) {
	s.cfgSaveMu.Lock()
	defer s.cfgSaveMu.Unlock()
	c := s.Config()
	// Lists are shared with the cached config: edit copies.
	c.TVChannels, c.TVOrder, c.TVHidden = slices.Clone(c.TVChannels), slices.Clone(c.TVOrder), slices.Clone(c.TVHidden)
	c.TVStreams, c.TVSources, c.TVAutomatic = slices.Clone(c.TVStreams), slices.Clone(c.TVSources), slices.Clone(c.TVAutomatic)
	if err := edit(&c); err != nil {
		return s.Config(), err
	}
	return s.saveConfig(c)
}

func (s *Store) saveConfig(c Config) (Config, error) {
	c.normalize()
	if err := s.setKV("config", jsonString(c)); err != nil {
		return c, err
	}
	s.cfgMu.Lock()
	s.cfg = c
	s.cfgMu.Unlock()
	return c, nil
}

func RandomToken(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
