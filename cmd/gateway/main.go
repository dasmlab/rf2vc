package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/dasmlab/rf2vc/internal/config"
	"github.com/dasmlab/rf2vc/internal/redfish"
	"github.com/dasmlab/rf2vc/internal/vsphere"
)

var buildVersion = "dev"

func main() {
	cfgPath := flag.String("config", "configs/gateway.yaml", "path to gateway config YAML")
	flag.Parse()

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	ctx := context.Background()
	vs, err := vsphere.New(ctx, cfg)
	if err != nil {
		log.Fatalf("vsphere: %v", err)
	}
	defer func() {
		cctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = vs.Close(cctx)
	}()

	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           redfish.NewServer(cfg, vs).Handler(),
		ReadHeaderTimeout: 15 * time.Second,
	}

	go func() {
		log.Printf("rf2vc %s listening on %s", buildVersion, cfg.Listen)
		var err error
		if cfg.TLSCertFile != "" && cfg.TLSKeyFile != "" {
			err = srv.ListenAndServeTLS(cfg.TLSCertFile, cfg.TLSKeyFile)
		} else {
			log.Printf("WARNING: serving plain HTTP (set tlsCertFile/tlsKeyFile for TLS, or terminate TLS at the Route/HAProxy)")
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
