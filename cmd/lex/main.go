// Command lex is a lightweight self-hosted media server.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"syscall"
	"time"

	"lex/internal/api"
	"lex/internal/cache"
	"lex/internal/intro"
	"lex/internal/library"
	"lex/internal/logx"
	"lex/internal/meta"
	"lex/internal/store"
	"lex/internal/stream"
	"lex/internal/sysstats"
	"lex/internal/trickplay"
	ver "lex/internal/version"
	"lex/web"
)

var version = ver.String()

func env(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

func main() {
	addr := flag.String("addr", env("LEX_ADDR", "127.0.0.1:8420"), "listen address")
	dataDir := flag.String("data", env("LEX_DATA", "./data"), "data directory (database, artwork, subtitle cache)")
	ffmpeg := flag.String("ffmpeg", env("LEX_FFMPEG", "ffmpeg"), "ffmpeg binary")
	ffprobe := flag.String("ffprobe", env("LEX_FFPROBE", "ffprobe"), "ffprobe binary")
	debugLog := flag.Bool("debug", os.Getenv("LEX_DEBUG") == "1", "verbose logging")
	showVersion := flag.Bool("version", false, "print version and exit")
	healthcheck := flag.Bool("healthcheck", false, "check that the server on -addr answers, then exit (for container health checks)")
	flag.Parse()
	if *showVersion {
		fmt.Println("lex", version)
		return
	}
	if *healthcheck {
		os.Exit(health(*addr))
	}

	// Keep the heap small on low-memory boards unless the user overrides it.
	if os.Getenv("GOMEMLIMIT") == "" {
		debug.SetMemoryLimit(128 << 20)
	}
	if os.Getenv("GOGC") == "" {
		debug.SetGCPercent(50)
	}

	log := logx.New(1000, *debugLog)
	abs, _ := filepath.Abs(*dataDir)
	st, err := store.Open(abs)
	if err != nil {
		log.Errorf("open database: %v", err)
		os.Exit(1)
	}
	defer st.Close()

	for _, bin := range []*string{ffmpeg, ffprobe} {
		if p, err := exec.LookPath(*bin); err == nil {
			*bin = p
		} else {
			log.Warnf("%s not found in PATH: probing/remuxing/transcoding will not work until it's installed", *bin)
		}
	}
	ff := stream.DetectFFmpeg(*ffmpeg, *ffprobe)
	log.Infof("lex %s starting; data=%s ffmpeg=%s encoders=%v", version, abs, ff.Version, ff.Encoders)

	// Turn on hardware video the first time it's detected (the user can
	// still switch it off in Settings → Transcoding).
	if c := st.Config(); !c.HWAutoDone && (ff.HEVCHWDecode || ff.V4L2Device) {
		if ff.HEVCHWDecode {
			c.HWDecode = true
		}
		if ff.V4L2Device {
			c.VideoEncoder = "h264_v4l2m2m"
		}
		c.HWAutoDone = true
		st.SaveConfig(c)
		log.Infof("hardware video detected: HEVC decode=%v, H.264 encode=%v; enabled", ff.HEVCHWDecode, ff.V4L2Device)
	}
	log.Infof("hardware: HEVC decode available=%v (enabled=%v), H.264 encoder available=%v (using %s)", ff.HEVCHWDecode, st.Config().HWDecode, ff.V4L2Device, st.Config().VideoEncoder)

	images := meta.NewImageCache(filepath.Join(abs, "images"))
	agent := meta.NewAgent(st, images, log)
	scanner := library.NewScanner(st, *ffprobe, log)
	sessions := stream.NewManager(st, log)
	subs := stream.NewSubs(filepath.Join(abs, "subs"), *ffmpeg, log)
	mediaCache := cache.New(st, log, abs)
	subs.Resolve = mediaCache.Resolve
	intros := intro.New(st, log, *ffmpeg)
	intros.Resolve = mediaCache.Resolve
	hls := stream.NewHLS(filepath.Join(abs, "hls"), *ffmpeg, log)
	sessions.OnEnd = hls.Stop
	tricks := trickplay.New(st, log, *ffmpeg, filepath.Join(abs, "trickplay"))
	tricks.Resolve = mediaCache.Peek
	tricks.Busy = func() bool { return sessions.Active() > 0 }
	tricks.HWDecode = func() bool { return st.Config().HWDecode && ff.HEVCHWDecode }
	scanner.OnDone = func() {
		agent.Trigger()
		intros.Trigger()
		tricks.Trigger()
	}
	sampler := sysstats.New(3*time.Second, time.Hour, func() []string {
		paths := []string{abs, mediaCache.Dir()}
		if libs, err := st.Libraries(); err == nil {
			for _, l := range libs {
				paths = append(paths, l.Paths...)
			}
		}
		return paths
	}, func() (float64, int) {
		return sessions.TotalRate(), len(sessions.Snapshot())
	})

	srv := &api.Server{
		Version: version, DataDir: abs, St: st, Log: log, Scanner: scanner, Agent: agent, Images: images,
		Subs: subs, Sess: sessions, Stats: sampler, FF: ff, Web: web.FS(), Cache: mediaCache, Intro: intros, Trick: tricks, HLS: hls,
	}
	handler, err := srv.Handler()
	if err != nil {
		log.Errorf("init: %v", err)
		os.Exit(1)
	}
	httpSrv := &http.Server{
		Addr:              *addr,
		Handler:           handler,
		ReadHeaderTimeout: 15 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    64 << 10,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Zero-config metadata: use a local Radarr as a TMDB source if present.
	if c := st.Config(); c.RadarrURL == "" && !c.RadarrDetectDone {
		dctx, dcancel := context.WithTimeout(context.Background(), 5*time.Second)
		if u, k := meta.DetectRadarr(dctx); u != "" {
			c.RadarrURL, c.RadarrKey = u, k
			log.Infof("found local Radarr at %s; using it for movie metadata", u)
			st.DB().Exec(`UPDATE items SET meta_status=0 WHERE kind='movie' AND meta_status IN (2,3)`)
		}
		dcancel()
		c.RadarrDetectDone = true
		st.SaveConfig(c)
	}
	if st.Config().ScanOnStartup {
		scanner.Trigger(0)
	} else {
		agent.Trigger()
	}
	go func() {
		last := time.Now()
		t := time.NewTicker(time.Minute)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				iv := st.Config().ScanIntervalMin
				if iv > 0 && time.Since(last) >= time.Duration(iv)*time.Minute {
					last = time.Now()
					scanner.Trigger(0)
				}
			}
		}
	}()

	listener, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Errorf("listen: %v", err)
		os.Exit(1)
	}
	go func() {
		log.Infof("listening on %s", *addr)
		if err := httpSrv.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Errorf("http: %v", err)
			stop()
		}
	}()
	<-ctx.Done()
	log.Infof("shutting down")
	shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	httpSrv.Shutdown(shutCtx)
}

// health probes the local server; the exit code is the health status.
func health(addr string) int {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return 1
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	c := http.Client{Timeout: 5 * time.Second}
	resp, err := c.Get("http://" + net.JoinHostPort(host, port) + "/api/public/info")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		return 1
	}
	return 0
}
