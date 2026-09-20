package main

import (
	"context"
	"flag"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/dasmlab/rf2vc/internal/activity"
	"github.com/dasmlab/rf2vc/internal/api"
	"github.com/dasmlab/rf2vc/internal/config"
	"github.com/dasmlab/rf2vc/internal/redfish"
	"github.com/dasmlab/rf2vc/internal/store"
	"github.com/dasmlab/rf2vc/internal/vsphere"
	"github.com/dasmlab/rf2vc/web"
)

var buildVersion = "dev"

func main() {
	cfgPath := flag.String("config", "configs/gateway.yaml", "path to gateway config YAML")
	flag.Parse()

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	st, err := store.Open(cfg.DataDir)
	if err != nil {
		log.Fatalf("store: %v", err)
	}
	if seeded, err := st.SeedFromGOVC(); err != nil {
		log.Fatalf("govc seed: %v", err)
	} else if seeded {
		log.Printf("seeded vCenter from GOVC_* environment")
	}

	pool := vsphere.NewPool(cfg.ISOCacheDir)
	defer func() {
		cctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		pool.CloseAll(cctx)
	}()

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	api.New(st, pool, buildVersion).Mount(mux)
	redfish.NewServer(cfg, st, pool).Mount(mux)

	staticFS, err := fs.Sub(web.Assets, "static")
	if err != nil {
		log.Fatalf("web static: %v", err)
	}
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.FS(staticFS))))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		b, err := web.Assets.ReadFile("index.html")
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(b)
	})

	handler := basicAuth(cfg.Auth.Username, cfg.Auth.Password, mux)

	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           handler,
		ReadHeaderTimeout: 15 * time.Second,
	}

	go func() {
		vc, mp := st.Stats()
		log.Printf("rf2vc %s listening on %s (dataDir=%s vcenters=%d mappings=%d dryRun=%v)",
			buildVersion, cfg.Listen, cfg.DataDir, vc, mp, st.GetSettings().DryRun)
		activity.Run("startup", "gateway listening", map[string]any{
			"version":  buildVersion,
			"listen":   cfg.Listen,
			"vcenters": vc,
			"mappings": mp,
			"dryRun":   st.GetSettings().DryRun,
		})
		var err error
		if cfg.TLSCertFile != "" && cfg.TLSKeyFile != "" {
			err = srv.ListenAndServeTLS(cfg.TLSCertFile, cfg.TLSKeyFile)
		} else {
			log.Printf("WARNING: serving plain HTTP (TLS at Route/HAProxy)")
			err = srv.ListenAndServe()
		}
		if err != nil && err != http.ErrServerClosed {
			log.Fatalf("listen: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	cctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(cctx)
}

func basicAuth(user, pass string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" || strings.HasPrefix(r.URL.Path, "/healthz?") {
			next.ServeHTTP(w, r)
			return
		}
		u, p, ok := r.BasicAuth()
		if !ok || u != user || p != pass {
			w.Header().Set("WWW-Authenticate", `Basic realm="rf2vc"`)
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}
