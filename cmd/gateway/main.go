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
	"github.com/dasmlab/rf2vc/internal/kubeauth"
	"github.com/dasmlab/rf2vc/internal/redfish"
	"github.com/dasmlab/rf2vc/internal/store"
	"github.com/dasmlab/rf2vc/internal/vsphere"
	"github.com/dasmlab/rf2vc/web"
)

var buildVersion = "dev"

func main() {
	cfgPath := flag.String("config", "configs/gateway.yaml", "path to gateway config YAML")
	htpasswdOut := flag.String("write-htpasswd", "", "write a bcrypt htpasswd for RF2VC_AUTH_USERNAME/PASSWORD to this path and exit")
	templatesOut := flag.String("write-oauth-templates", "", "write oauth-proxy sign-in templates (sign_in.html, error.html) to this directory and exit")
	flag.Parse()

	if *htpasswdOut != "" || *templatesOut != "" {
		if *htpasswdOut != "" {
			if err := writeHtpasswd(*htpasswdOut, os.Getenv("RF2VC_AUTH_USERNAME"), os.Getenv("RF2VC_AUTH_PASSWORD")); err != nil {
				log.Fatalf("write-htpasswd: %v", err)
			}
			log.Printf("wrote %s", *htpasswdOut)
		}
		if *templatesOut != "" {
			if err := writeOAuthTemplates(*templatesOut); err != nil {
				log.Fatalf("write-oauth-templates: %v", err)
			}
			log.Printf("wrote oauth-proxy templates to %s", *templatesOut)
		}
		return
	}

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

	healthz := func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}
	apiSrv := api.New(st, pool, buildVersion)
	mountAPI := func(mux *http.ServeMux, mode string) {
		apiSrv.Mount(mux)
		mux.HandleFunc("/api/v1/whoami", whoami(mode))
	}
	mountUI := func(mux *http.ServeMux, mode string) {
		mountAPI(mux, mode)
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
	}

	creds := newCredentials(cfg.Auth.Username, cfg.Auth.Password, cfg.Auth.RedfishClientsDir, !cfg.Auth.DisableSharedRedfish)
	if cfg.Auth.RedfishClientsDir != "" {
		creds.clientsNow()
		log.Printf("redfish clients from %s (shared account on /redfish: %v)",
			cfg.Auth.RedfishClientsDir, !cfg.Auth.DisableSharedRedfish)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", healthz)
	redfish.NewServer(cfg, st, pool).Mount(mux)
	if cfg.APIListen != "" {
		rev, err := kubeauth.InCluster(kubeauth.ResourceAttributes{
			Verb: "get", Resource: "services", Name: cfg.Auth.APIService,
		})
		if err != nil {
			log.Fatalf("apiListen: %v", err)
		}
		apiMux := http.NewServeMux()
		apiMux.HandleFunc("/healthz", healthz)
		mountAPI(apiMux, "token")
		apiListener := &http.Server{
			Addr:              cfg.APIListen,
			Handler:           tokenAuth(rev, apiMux),
			ReadHeaderTimeout: 15 * time.Second,
		}
		go func() {
			var err error
			if cfg.APITLSCertFile != "" && cfg.APITLSKeyFile != "" {
				log.Printf("/api on %s (TLS): Kubernetes bearer tokens that may %s", cfg.APIListen, rev.Attributes())
				err = apiListener.ListenAndServeTLS(cfg.APITLSCertFile, cfg.APITLSKeyFile)
			} else {
				log.Printf("WARNING: /api on %s is plain HTTP; bearer tokens travel unencrypted", cfg.APIListen)
				err = apiListener.ListenAndServe()
			}
			if err != nil && err != http.ErrServerClosed {
				log.Fatalf("api listen: %v", err)
			}
		}()
		defer func() { _ = apiListener.Close() }()
	}

	handler := basicAuth(creds, mux)
	if cfg.UIListen == "" {
		mountUI(mux, "basic")
	} else {
		warnIfNotLoopback(cfg.UIListen)
		uiMux := http.NewServeMux()
		uiMux.HandleFunc("/healthz", healthz)
		mountUI(uiMux, "oauth")
		uiSrv := &http.Server{
			Addr:              cfg.UIListen,
			Handler:           proxyAuth(uiMux),
			ReadHeaderTimeout: 15 * time.Second,
		}
		go func() {
			log.Printf("dashboard + API on %s (identity from oauth-proxy)", cfg.UIListen)
			if err := uiSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				log.Fatalf("ui listen: %v", err)
			}
		}()
		defer func() { _ = uiSrv.Close() }()
	}

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

// isPublicRedfishRoot matches the Redfish-mandated unauthenticated discovery paths.
func isPublicRedfishRoot(path string) bool {
	switch strings.TrimSuffix(path, "/") {
	case "/redfish", "/redfish/v1":
		return true
	default:
		return false
	}
}
