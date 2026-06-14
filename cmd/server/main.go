// Command server runs the RetroArch↔RomM WebDAV sync bridge.
package main

import (
	"context"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jleight/retroarch-romm-bridge/internal/bridge"
	"github.com/jleight/retroarch-romm-bridge/internal/config"
	"github.com/jleight/retroarch-romm-bridge/internal/store"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: logLevel()})))

	cfg, err := config.Load()
	if err != nil {
		slog.Error("load config", "err", err)
		os.Exit(1)
	}

	st, err := store.Open(cfg.Store.Path)
	if err != nil {
		slog.Error("open pairing store", "path", cfg.Store.Path, "err", err)
		os.Exit(1)
	}

	svc, err := bridge.NewService(cfg, st)
	if err != nil {
		slog.Error("init service", "err", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	svc.Start(ctx)

	mux := http.NewServeMux()
	mux.Handle("/", bridge.NewHandler(svc))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("/ping", pingHandler)

	srv := &http.Server{
		Addr:              cfg.Server.Listen,
		Handler:           accessLog(mux),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		<-ctx.Done()
		slog.Info("shutting down")
		shutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
	}()

	slog.Info("listening", "addr", cfg.Server.Listen, "romm", cfg.RomM.BaseURL, "store", cfg.Store.Path)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Error("server error", "err", err)
		os.Exit(1)
	}
}

// pingHandler is an unauthenticated browser-friendly connectivity check. Hitting
// https://<host>/ping from the device proves DNS, TLS trust, and the
// Caddy->bridge path all work end to end.
func pingHandler(w http.ResponseWriter, r *http.Request) {
	clientIP := r.Header.Get("X-Forwarded-For")
	if clientIP == "" {
		clientIP = r.RemoteAddr
	}
	proto := r.Header.Get("X-Forwarded-Proto")
	if proto == "" {
		if r.TLS != nil {
			proto = "https"
		} else {
			proto = "http"
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<!doctype html>
<title>retroarch-romm-bridge</title>
<body style="font-family:sans-serif;max-width:40em;margin:3em auto">
<h1>&#9989; Connection successful</h1>
<p>You reached the <b>retroarch-romm-bridge</b>. The WebDAV endpoint is ready.</p>
<ul>
<li>Host you used: <code>%s</code></li>
<li>Your IP (as seen here): <code>%s</code></li>
<li>Proto: <code>%s</code> &middot; HTTP <code>%s</code></li>
<li>Server time (UTC): <code>%s</code></li>
</ul>
<p>In RetroArch, set the WebDAV URL to <code>%s://%s/</code> (with trailing slash).</p>
</body>`,
		htmlEscape(r.Host), htmlEscape(clientIP), htmlEscape(proto), htmlEscape(r.Proto),
		time.Now().UTC().Format(time.RFC3339), proto, htmlEscape(r.Host))
	slog.Info("ping", "host", r.Host, "client", clientIP, "ua", r.UserAgent())
}

func htmlEscape(s string) string { return html.EscapeString(s) }

// accessLog logs one line per request (method, path, status, client, UA) so it
// is obvious whether requests are reaching the bridge at all.
func accessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		client := r.Header.Get("X-Forwarded-For")
		if client == "" {
			client = r.RemoteAddr
		}
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		slog.Info("request",
			"method", r.Method, "path", r.URL.Path, "status", rec.status,
			"client", client, "ua", r.UserAgent())
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func logLevel() slog.Level {
	switch os.Getenv("LOG_LEVEL") {
	case "debug", "DEBUG":
		return slog.LevelDebug
	case "warn", "WARN":
		return slog.LevelWarn
	case "error", "ERROR":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
