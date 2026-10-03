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
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	qrcode "github.com/skip2/go-qrcode"

	"github.com/Drivera0/crowd-crush/server/internal/app"
	"github.com/Drivera0/crowd-crush/server/internal/brief"
	"github.com/Drivera0/crowd-crush/server/internal/detect"
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
	rows := flag.Int("rows", 0, "grid rows (overrides config)")
	cols := flag.Int("cols", 0, "grid cols (overrides config)")
	zoneCols := flag.Int("zone-cols", 0, "grid cols per zone (overrides config)")
	zoneRows := flag.Int("zone-rows", 0, "grid rows per zone (overrides config)")
	dumpConfig := flag.Bool("dump-config", false, "print the detector config as JSON and exit")
	publicURL := flag.String("public-url", os.Getenv("PUBLIC_URL"), "URL phones should open (for the QR code); default: the dashboard's own host")
	flag.Parse()

	cfg := detect.DefaultConfig()
	if *cfgPath != "" {
		var err error
		if cfg, err = detect.LoadConfig(*cfgPath); err != nil {
			log.Fatalf("config: %v", err)
		}
	}
	for _, o := range []struct {
		v   int
		dst *int
	}{{*rows, &cfg.Rows}, {*cols, &cfg.Cols}, {*zoneCols, &cfg.ZoneCols}, {*zoneRows, &cfg.ZoneRows}} {
		if o.v > 0 {
			*o.dst = o.v
		}
	}
	if err := cfg.Validate(); err != nil {
		log.Fatalf("config: %v", err)
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

	a := app.New(app.Options{Detect: cfg, RecordingsDir: *recDir, Sink: sink, Tiger: tiger, Brief: b, Voice: v, Sign: s})
	go a.Run(ctx)

	mux := http.NewServeMux()
	a.Routes(mux)
	mux.Handle("GET /audio/", http.StripPrefix("/audio/", http.FileServer(http.Dir(*audioDir))))
	mux.HandleFunc("GET /api/qr.png", func(w http.ResponseWriter, r *http.Request) {
		u := *publicURL
		if u == "" {
			u = requestBaseURL(r)
		}
		png, err := qrcode.Encode(strings.TrimRight(u, "/")+"/", qrcode.Medium, 512)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("X-Phone-URL", u)
		w.Write(png)
	})
	mux.HandleFunc("GET /api/phone-url", func(w http.ResponseWriter, r *http.Request) {
		u := *publicURL
		if u == "" {
			u = requestBaseURL(r)
		}
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprint(w, strings.TrimRight(u, "/")+"/")
	})
	mux.Handle("/dash/", http.StripPrefix("/dash/", spa(filepath.Join(*webDir, "dashboard", "dist"))))
	mux.HandleFunc("GET /dash", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/dash/", http.StatusFound) })
	mux.Handle("/", spa(filepath.Join(*webDir, "phone", "dist")))

	srv := &http.Server{Addr: *addr, Handler: logRequests(mux), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		srv.Shutdown(sctx)
	}()
	log.Printf("pulse on %s — phones: http://localhost%s/  dashboard: http://localhost%s/dash/  (grid %dx%d, zones %dx%d)",
		*addr, port(*addr), port(*addr), cfg.Rows, cfg.Cols, cfg.ZoneRows, cfg.ZoneCols)
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
