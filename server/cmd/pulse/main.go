// Command pulse is the whole server: phone page, dashboard, WebSockets, APIs,
// detector, storage and alerting, in one process.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	qrcode "github.com/skip2/go-qrcode"

	"github.com/Drivera0/crowd-crush/server/internal/app"
	"github.com/Drivera0/crowd-crush/server/internal/brief"
	"github.com/Drivera0/crowd-crush/server/internal/detect"
	"github.com/Drivera0/crowd-crush/server/internal/protocol"
	"github.com/Drivera0/crowd-crush/server/internal/sign"
	"github.com/Drivera0/crowd-crush/server/internal/store"
	"github.com/Drivera0/crowd-crush/server/internal/voice"
)

func main() {
	loadDotEnv(".env")
	addr := flag.String("addr", envOr("PULSE_ADDR", ":8080"), "listen address")
	webDir := flag.String("web", "web", "directory holding phone/dist and dashboard/dist")
	recDir := flag.String("recordings", "recordings", "where labelled runs and fallback recordings go")
	audioDir := flag.String("audio", "audio", "where generated mp3s go")
	cfgPath := flag.String("config", "", "detector config JSON (defaults built in)")
	dataDir := flag.String("data", "data", "where staff-drawn areas and the venue anchor are saved")
	venueW := flag.Float64("venue-w", envFloat("VENUE_W", 0), "venue width in metres (overrides config; env VENUE_W)")
	venueH := flag.Float64("venue-h", envFloat("VENUE_H", 0), "venue height in metres (overrides config; env VENUE_H)")
	venueLat := flag.Float64("venue-lat", envFloat("VENUE_LAT", math.NaN()), "latitude of the venue map's top-left corner (env VENUE_LAT)")
	venueLon := flag.Float64("venue-lon", envFloat("VENUE_LON", math.NaN()), "longitude of the venue map's top-left corner (env VENUE_LON)")
	venueBearing := flag.Float64("venue-bearing", envFloat("VENUE_BEARING", 0), "compass bearing of the map's up, degrees clockwise from north (env VENUE_BEARING)")
	zoneCols := flag.Int("zone-cols", 0, "default zones across the venue (overrides config)")
	zoneRows := flag.Int("zone-rows", 0, "default zones down the venue (overrides config)")
	escalate := flag.Duration("escalate-after", envDuration("PULSE_ESCALATE_AFTER", app.DefaultEscalateAfter), "re-announce a red alert nobody acknowledged after this long; 0 = never (env PULSE_ESCALATE_AFTER)")
	check := flag.Bool("check", false, "test the services configured in .env and exit")
	preflight := flag.Bool("preflight", false, "go/no-go list for the table demo, against the server already running on -addr, then exit")
	dumpConfig := flag.Bool("dump-config", false, "print the detector config as JSON and exit")
	publicURL := flag.String("public-url", os.Getenv("PUBLIC_URL"), "URL phones should open (the QR code); a link set in the dashboard wins over it; default: a running Cloudflare quick tunnel, else the dashboard's own host")
	flag.Parse()

	cfg := detect.DefaultConfig()
	if *cfgPath != "" {
		var err error
		if cfg, err = detect.LoadConfig(*cfgPath); err != nil {
			log.Fatalf("config: %v", err)
		}
	}
	if *zoneCols > 0 {
		cfg.ZoneCols = *zoneCols
	}
	if *zoneRows > 0 {
		cfg.ZoneRows = *zoneRows
	}
	if *venueW > 0 {
		cfg.VenueW = *venueW
	}
	if *venueH > 0 {
		cfg.VenueH = *venueH
	}
	if err := cfg.Validate(); err != nil {
		log.Fatalf("config: %v", err)
	}
	if *check {
		os.Exit(runCheck(*audioDir))
	}
	if *preflight {
		os.Exit(runPreflight(*addr, *publicURL))
	}
	if *dumpConfig {
		b, _ := json.MarshalIndent(cfg, "", "  ")
		fmt.Println(string(b))
		return
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Every external service fails soft: missing key or no network → log and
	// keep detecting.
	sink, tiger := store.Open(ctx, os.Getenv("TIGER_DATABASE_URL"), *recDir)
	defer sink.Close()
	b := brief.New(os.Getenv("GEMINI_API_KEY"), os.Getenv("GEMINI_MODEL"))
	v := voice.New(os.Getenv("ELEVENLABS_API_KEY"), os.Getenv("ELEVENLABS_VOICE_ID"), *audioDir)
	s := sign.New(os.Getenv("SIGN_URL"))
	logService("gemini", b.Enabled(), "GEMINI_API_KEY not set: template briefings")
	logService("elevenlabs", v.Enabled(), "ELEVENLABS_API_KEY not set: dashboard uses the browser's voice")
	logService("sign", s.Enabled(), "SIGN_URL not set: no Arduino sign")
	if s.Enabled() {
		log.Printf("signs: %s", s.Describe())
	}

	// Initial venue; data/venue.json (saved by PUT /api/venue) wins.
	venue := protocol.Venue{W: cfg.VenueW, H: cfg.VenueH}
	if !math.IsNaN(*venueLat) && !math.IsNaN(*venueLon) {
		venue.Lat, venue.Lon, venue.Bearing, venue.Geo = *venueLat, *venueLon, *venueBearing, true
	}
	a := app.New(app.Options{Detect: cfg, RecordingsDir: *recDir, DataDir: *dataDir, Venue: venue,
		Sink: sink, Tiger: tiger, Brief: b, Voice: v, Sign: s, EscalateAfter: escalateOpt(*escalate),
		PublicURL: *publicURL})
	cfg = a.Config()
	go a.Run(ctx)

	mux := http.NewServeMux()
	a.Routes(mux)
	mux.Handle("GET /audio/", http.StripPrefix("/audio/", http.FileServer(http.Dir(*audioDir))))
	mux.HandleFunc("GET /api/qr.png", func(w http.ResponseWriter, r *http.Request) {
		// The same URL as GET /api/join: staff setting, PUBLIC_URL, quick tunnel, then this request's host.
		u := a.JoinInfo(r).URL
		link := strings.TrimRight(u, "/") + "/"
		// ?at=<key>: a tower's check-in code (the join link with ?at=<key>).
		if at := r.URL.Query().Get("at"); at != "" {
			if t, err := a.Tower(at); err == nil {
				link += "?at=" + url.QueryEscape(t.Key)
			} else {
				http.Error(w, err.Error(), http.StatusNotFound)
				return
			}
		}
		png, err := qrcode.Encode(link, qrcode.Medium, 512)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Cache-Control", "no-store") // the link can change without a restart
		w.Header().Set("X-Phone-URL", u)
		w.Write(png)
	})
	mux.HandleFunc("GET /api/phone-url", func(w http.ResponseWriter, r *http.Request) {
		u := a.JoinInfo(r).URL
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprint(w, strings.TrimRight(u, "/")+"/")
	})
	mux.Handle("/dash/", http.StripPrefix("/dash/", spa(filepath.Join(*webDir, "dashboard", "dist"))))
	mux.HandleFunc("GET /dash", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/dash/", http.StatusFound) })
	// The pitch deck (docs/deck): /deck/ is the scrolling overview, /deck/present.html the full-screen version for a tablet.
	mux.Handle("GET /deck/", http.StripPrefix("/deck/", http.FileServer(http.Dir(filepath.Join("docs", "deck")))))
	mux.HandleFunc("GET /deck", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/deck/present.html", http.StatusFound) })
	mux.Handle("/", spa(filepath.Join(*webDir, "phone", "dist")))

	srv := &http.Server{Addr: *addr, Handler: logRequests(mux), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		srv.Shutdown(sctx)
	}()
	log.Printf("pulse on %s — phones: http://localhost%s/  dashboard: http://localhost%s/dash/  (venue %gx%g m, geo-anchor %v, %d area(s))",
		*addr, port(*addr), port(*addr), cfg.VenueW, cfg.VenueH, a.Venue().Geo, len(a.Areas()))
	for _, ip := range lanIPs() {
		log.Printf("  on the LAN: http://%s%s/ (phones need HTTPS for motion: use the tunnel)", ip, port(*addr))
	}
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

// spa serves a built Vite app, with a helpful page if it hasn't been built.
func spa(dir string) http.Handler {
	fs := http.FileServer(http.Dir(dir))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := os.Stat(filepath.Join(dir, "index.html")); err != nil {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusServiceUnavailable)
			fmt.Fprintf(w, "<h1>Not built</h1><p>%s is missing. Run <code>make web</code> (or <code>cd web && npm install && npm run build</code>).</p>", dir)
			return
		}
		if r.URL.Path == "/" || r.URL.Path == "" || strings.HasSuffix(r.URL.Path, ".html") {
			w.Header().Set("Cache-Control", "no-cache")
		}
		fs.ServeHTTP(w, r)
	})
}

func requestBaseURL(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if p := r.Header.Get("X-Forwarded-Proto"); p != "" {
		scheme = p
	}
	host := r.Host
	if h := r.Header.Get("X-Forwarded-Host"); h != "" {
		host = h
	}
	return scheme + "://" + host
}

func logRequests(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") && r.Method != http.MethodGet {
			log.Printf("%s %s", r.Method, r.URL.Path)
		}
		h.ServeHTTP(w, r)
	})
}

func logService(name string, ok bool, why string) {
	if ok {
		log.Printf("%s: enabled", name)
	} else {
		log.Printf("%s: %s", name, why)
	}
}

func envFloat(k string, def float64) float64 {
	if v, err := strconv.ParseFloat(strings.TrimSpace(os.Getenv(k)), 64); err == nil {
		return v
	}
	return def
}

func envDuration(k string, def time.Duration) time.Duration {
	if v, err := time.ParseDuration(strings.TrimSpace(os.Getenv(k))); err == nil {
		return v
	}
	return def
}

// escalateOpt maps the flag (0 = never) to app.Options (0 = default, < 0 = never).
func escalateOpt(d time.Duration) time.Duration {
	if d <= 0 {
		return -1
	}
	return d
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func port(addr string) string {
	if i := strings.LastIndex(addr, ":"); i >= 0 {
		return addr[i:]
	}
	return ""
}

func lanIPs() []string {
	var out []string
	addrs, _ := net.InterfaceAddrs()
	for _, a := range addrs {
		if ipn, ok := a.(*net.IPNet); ok && !ipn.IP.IsLoopback() && ipn.IP.To4() != nil {
			out = append(out, ipn.IP.String())
		}
	}
	return out
}

// loadDotEnv sets variables from a .env file without overriding the real
// environment. Lines are KEY=value; # starts a comment.
func loadDotEnv(path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(strings.TrimPrefix(k, "export "))
		v = strings.Trim(strings.TrimSpace(v), `"'`)
		if _, set := os.LookupEnv(k); !set && v != "" {
			os.Setenv(k, v)
		}
	}
}
