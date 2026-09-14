package server

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/httplog/v2"
	"jokertc/api"
	"jokertc/healthcheck"
	"jokertc/metrics"
	"jokertc/signaling"
	"jokertc/turn"
)

//go:embed static/webrtc-test.html
var webrtcTestHTML []byte

type HTTPServerConfig struct {
	ListenAddr  string
	MetricsAddr string
	EnablePprof bool
	Log         *httplog.Logger

	DrainDuration            time.Duration
	GracefulShutdownDuration time.Duration
	ReadTimeout              time.Duration
	WriteTimeout             time.Duration

	TURN      *turn.Config      // nil disables the embedded STUN/TURN server
	Signaling *signaling.Config // nil disables the /ws signaling endpoint
}

type Server struct {
	cfg         *HTTPServerConfig
	healthcheck *healthcheck.Healthcheck
	log         *httplog.Logger

	srv        *http.Server
	metricsSrv *http.Server
	turnSrv    *turn.Server
	signaling  *signaling.Hub

	errCh      chan error
	wg         sync.WaitGroup
	turnCancel context.CancelFunc
}

func New(cfg *HTTPServerConfig) (srv *Server, err error) {
	srv = &Server{
		cfg:         cfg,
		log:         cfg.Log,
		healthcheck: healthcheck.New(&healthcheck.Opts{Log: cfg.Log, DrainDuration: cfg.DrainDuration}),
		srv:         nil,
	}

	if cfg.MetricsAddr != "" {
		srv.metricsSrv = &http.Server{
			Addr:         cfg.MetricsAddr,
			Handler:      metrics.Routes(),
			ReadTimeout:  cfg.ReadTimeout,
			WriteTimeout: cfg.WriteTimeout,
		}
	}

	if cfg.TURN != nil {
		turnSrv, err := turn.New(cfg.TURN)
		if err != nil {
			return nil, fmt.Errorf("TURN server: %w", err)
		}
		srv.turnSrv = turnSrv
	}

	if cfg.Signaling != nil {
		if cfg.Signaling.Log == nil {
			cfg.Signaling.Log = cfg.Log
		}
		hub, err := signaling.New(cfg.Signaling)
		if err != nil {
			return nil, fmt.Errorf("signaling server: %w", err)
		}
		srv.signaling = hub
	}

	srv.srv = &http.Server{
		Addr:         cfg.ListenAddr,
		Handler:      srv.getRouter(),
		ReadTimeout:  cfg.ReadTimeout,
		WriteTimeout: cfg.WriteTimeout,
	}

	return srv, nil
}

func (srv *Server) getRouter() http.Handler {
	mux := chi.NewRouter()

	mux.Use(httplog.RequestLogger(srv.log))
	mux.Use(middleware.Recoverer)
	mux.Use(metrics.Middleware)

	api.RegisterRoutes(mux)
	srv.healthcheck.RegisterRoutes(mux)
	if srv.signaling != nil {
		srv.signaling.RegisterRoutes(mux)
	}

	mux.Get("/ui/manual", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(webrtcTestHTML)
	})

	if srv.cfg.EnablePprof {
		srv.log.Info("pprof API enabled")
		mux.Mount("/debug", middleware.Profiler())
	}
	return mux
}

// RunInBackground starts all configured components in goroutines. The first
// fatal error from any component is delivered on ErrCh.
func (srv *Server) RunInBackground() {
	srv.errCh = make(chan error, 4) // api, metrics, turn — buffered so senders never block

	if srv.turnSrv != nil {
		ctx, cancel := context.WithCancel(context.Background())
		srv.turnCancel = cancel
		srv.wg.Go(func() {
			srv.log.Info("Starting TURN server", "listenUDP", srv.cfg.TURN.ListenUDPAddr, "listenTCP", srv.cfg.TURN.ListenTCPAddr)
			if err := srv.turnSrv.Run(ctx); err != nil {
				srv.log.Error("TURN server failed", "err", err)
				srv.errCh <- err
			}
		})
	}

	// metrics
	if srv.cfg.MetricsAddr != "" {
		srv.wg.Go(func() {
			srv.log.With("metricsAddress", srv.cfg.MetricsAddr).Info("Starting metrics server")
			if err := srv.metricsSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				srv.log.Error("Metrics server failed", "err", err)
				srv.errCh <- err
			}
		})
	}

	// api
	srv.wg.Go(func() {
		srv.log.Info("Starting HTTP server", "listenAddress", srv.cfg.ListenAddr)
		if err := srv.srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			srv.log.Error("HTTP server failed", "err", err)
			srv.errCh <- err
		}
	})
}

// ErrCh delivers the first fatal component error. Blocks until one arrives;
// use in a select alongside the shutdown signal channel in main.
func (srv *Server) ErrCh() <-chan error { return srv.errCh }

func (srv *Server) Shutdown() {
	// signaling: http.Server.Shutdown neither waits for nor closes hijacked
	// connections, so live WebSockets must be ended here first.
	if srv.signaling != nil {
		srv.signaling.Close()
	}

	// api
	ctx, cancel := context.WithTimeout(context.Background(), srv.cfg.GracefulShutdownDuration)
	defer cancel()
	if err := srv.srv.Shutdown(ctx); err != nil {
		srv.log.Error("Graceful HTTP server shutdown failed", "err", err)
	} else {
		srv.log.Info("HTTP server gracefully stopped")
	}

	// metrics
	if len(srv.cfg.MetricsAddr) != 0 {
		ctx, cancel := context.WithTimeout(context.Background(), srv.cfg.GracefulShutdownDuration)
		defer cancel()

		if err := srv.metricsSrv.Shutdown(ctx); err != nil {
			srv.log.Error("Graceful metrics server shutdown failed", "err", err)
		} else {
			srv.log.Info("Metrics server gracefully stopped")
		}
	}

	// embedded STUN/TURN
	if srv.turnSrv != nil {
		srv.turnCancel() // unblocks turnSrv.Run, which closes the server
	}

	srv.wg.Wait()
}
