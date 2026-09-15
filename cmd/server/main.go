package main

import (
	"fmt"
	"log"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"
	"uuid"

	"github.com/urfave/cli/v2" // imports as package "cli"

	"jokertc/common"
	"jokertc/server"
	"jokertc/signaling"
	"jokertc/turn"
)

var flags []cli.Flag = []cli.Flag{
	&cli.StringFlag{
		Name:  "listen-addr",
		Value: "127.0.0.1:9000",
		Usage: "address to listen on for API",
	},
	&cli.StringFlag{
		Name:  "metrics-addr",
		Value: "127.0.0.1:8090",
		Usage: "address to listen on for Prometheus metrics",
	},
	&cli.BoolFlag{
		Name:  "log-json",
		Value: false,
		Usage: "log in JSON format",
	},
	&cli.BoolFlag{
		Name:  "log-debug",
		Value: false,
		Usage: "log debug messages",
	},
	&cli.BoolFlag{
		Name:  "log-uid",
		Value: false,
		Usage: "generate a uuid and add to all log messages",
	},
	&cli.StringFlag{
		Name:  "log-service",
		Value: "jokertc",
		Usage: "add 'service' tag to logs",
	},
	&cli.BoolFlag{
		Name:  "pprof",
		Value: false,
		Usage: "enable pprof debug endpoint",
	},
	&cli.Int64Flag{
		Name:  "drain-seconds",
		Value: 45,
		Usage: "seconds to wait in drain HTTP request",
	},
	&cli.StringFlag{
		Name:  "turn-listen-addr",
		Value: ":3478",
		Usage: "STUN/TURN listen address (UDP and TCP); empty disables TURN",
	},
	&cli.StringFlag{
		Name:  "turn-external-ip",
		Value: "",
		Usage: "external IP advertised in TURN relayed addresses; auto-detected when empty",
	},
	&cli.BoolFlag{
		Name:  "signaling",
		Value: true,
		Usage: "enable the /ws WebRTC signaling endpoint",
	},
	&cli.StringFlag{
		Name:  "signaling-stun-url",
		Value: "",
		Usage: "STUN URL advertised to signaling peers, e.g. stun:host:3478",
	},
	&cli.StringFlag{
		Name:  "signaling-turn-url",
		Value: "",
		Usage: "TURN URL advertised to signaling peers, e.g. turn:host:3478; empty sends no credential",
	},
	&cli.Int64Flag{
		Name:  "signaling-no-receiver-seconds",
		Value: 20,
		Usage: "seconds a device may wait alone before it is sent bye; 0 disables",
	},
	&cli.Int64Flag{
		Name:  "signaling-ping-seconds",
		Value: 30,
		Usage: "keepalive ping interval on idle signaling sockets; 0 disables",
	},
	&cli.BoolFlag{
		Name:  "turn-disable-stun",
		Value: false,
		Usage: "drop STUN binding requests so peers can only use a TURN relay (testing)",
	},
	&cli.BoolFlag{
		Name:  "turn-allow-any-credential",
		Value: false,
		Usage: "accept any TURN username/password combination, making an open relay (testing)",
	},
	&cli.StringFlag{
		Name:  "turn-relay-port-range",
		Value: "49152-49352",
		Usage: "UDP port range for TURN relay allocations (min-max)",
	},
}

func parsePortRange(s string) (min, max int, err error) {
	parts := strings.SplitN(s, "-", 2)
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("invalid port range %q: want min-max", s)
	}
	min, err = strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil {
		return 0, 0, fmt.Errorf("invalid port range %q: %w", s, err)
	}
	max, err = strconv.Atoi(strings.TrimSpace(parts[1]))
	if err != nil {
		return 0, 0, fmt.Errorf("invalid port range %q: %w", s, err)
	}
	if min <= 0 || max < min || max > 65535 {
		return 0, 0, fmt.Errorf("invalid port range %d-%d", min, max)
	}
	return min, max, nil
}

func main() {
	app := &cli.App{
		Name:        "server",
		Usage:       "Serve API, and metrics",
		Version:     common.Version,
		HideVersion: false,
		Flags:       flags,
		Action: func(cCtx *cli.Context) error {
			listenAddr := cCtx.String("listen-addr")
			metricsAddr := cCtx.String("metrics-addr")
			logJSON := cCtx.Bool("log-json")
			logDebug := cCtx.Bool("log-debug")
			logUID := cCtx.Bool("log-uid")
			logService := cCtx.String("log-service")
			enablePprof := cCtx.Bool("pprof")
			drainDuration := time.Duration(cCtx.Int64("drain-seconds")) * time.Second
			turnListenAddr := cCtx.String("turn-listen-addr")
			turnExternalIP := cCtx.String("turn-external-ip")
			turnRelayRange := cCtx.String("turn-relay-port-range")
			turnDisableSTUN := cCtx.Bool("turn-disable-stun")
			turnAllowAnyCredential := cCtx.Bool("turn-allow-any-credential")
			enableSignaling := cCtx.Bool("signaling")
			signalingStunURL := cCtx.String("signaling-stun-url")
			signalingTurnURL := cCtx.String("signaling-turn-url")
			signalingNoReceiver := time.Duration(cCtx.Int64("signaling-no-receiver-seconds")) * time.Second
			signalingPing := time.Duration(cCtx.Int64("signaling-ping-seconds")) * time.Second

			uid := ""
			if logUID {
				uid = uuid.New().String()
			}

			log := common.SetupLogger(&common.LoggingOpts{
				Debug:   logDebug,
				JSON:    logJSON,
				Service: logService,
				Version: common.Version,
				UID:     uid,
			})

			cfg := &server.HTTPServerConfig{
				ListenAddr:  listenAddr,
				MetricsAddr: metricsAddr,
				Log:         log,
				EnablePprof: enablePprof,

				DrainDuration:            drainDuration,
				GracefulShutdownDuration: 30 * time.Second,
				ReadTimeout:              60 * time.Second,
				WriteTimeout:             30 * time.Second,
			}

			if enableSignaling {
				cfg.Signaling = &signaling.Config{
					StunURL:           signalingStunURL,
					TurnURL:           signalingTurnURL,
					NoReceiverTimeout: signalingNoReceiver,
					PingInterval:      signalingPing,
					Log:               log,
				}
			}

			relayMin, relayMax, err := parsePortRange(turnRelayRange)
			if err != nil {
				return err
			}
			if turnListenAddr != "" {
				turnCfg := &turn.Config{
					ListenUDPAddr:      turnListenAddr,
					ListenTCPAddr:      turnListenAddr,
					RelayPortMin:       relayMin,
					RelayPortMax:       relayMax,
					Auth:               turn.AllowAllAuth{},
					Log:                log,
					DisableSTUN:        turnDisableSTUN,
					AllowAnyCredential: turnAllowAnyCredential,
				}
				if turnExternalIP != "" {
					turnCfg.ExternalIP = turnExternalIP
				}
				cfg.TURN = turnCfg
			}

			srv, err := server.New(cfg)
			if err != nil {
				cfg.Log.Error("failed to create server", "err", err)
				return err
			}

			exit := make(chan os.Signal, 1)
			signal.Notify(exit, os.Interrupt, syscall.SIGTERM)
			srv.RunInBackground()

			select {
			case <-exit:
				cfg.Log.Info("shutting down")
				srv.Shutdown()
			case err := <-srv.ErrCh():
				cfg.Log.Error("server component failed, exiting", "err", err)
				srv.Shutdown()
				return err
			}
			return nil
		},
	}

	if err := app.Run(os.Args); err != nil {
		log.Fatal(err)
	}
}
