package store

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"runtime"
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
}

func DefaultConfig() Config {
	return Config{
		ServerName:         "Lex",
		MetadataLanguage:   "en-US",
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
	if c.TrickplayInterval < 2 || c.TrickplayInterval > 60 {
		c.TrickplayInterval = d.TrickplayInterval
	}
	if c.TrickplayWidth < 120 || c.TrickplayWidth > 480 {
		c.TrickplayWidth = d.TrickplayWidth
	}
	if c.WebhookToken == "" {
		c.WebhookToken = RandomToken(12)
	}
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
