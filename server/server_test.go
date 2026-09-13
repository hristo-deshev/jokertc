package server

import (
	"errors"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/go-chi/httplog/v2"
	"jokertc/turn"
)

func testLogger(t *testing.T) *httplog.Logger {
	t.Helper()
	return httplog.NewLogger("test", httplog.Options{LogLevel: slog.LevelError})
}

func TestErrChPropagatesComponentError(t *testing.T) {
	srv, err := New(&HTTPServerConfig{
		ListenAddr: "127.0.0.1:0",
		Log:        testLogger(t),
		TURN: &turn.Config{
			ListenUDPAddr: "127.0.0.1:0",
			ExternalIP:    "127.0.0.1",
			RelayPortMin:  60000,
			RelayPortMax:  60100,
			Auth:          turn.AllowAllAuth{},
		},
	})
	if err != nil {
		t.Fatalf("New(): %v", err)
	}
	srv.RunInBackground()
	defer srv.Shutdown()

	select {
	case err := <-srv.ErrCh():
		t.Fatalf("unexpected early error: %v", err)
	case <-time.After(200 * time.Millisecond):
	}
	if srv.turnSrv == nil {
		t.Fatal("expected TURN server to be composed")
	}
}

func TestShutdownWaitsWithoutError(t *testing.T) {
	srv, err := New(&HTTPServerConfig{ListenAddr: "127.0.0.1:0", Log: testLogger(t)})
	if err != nil {
		t.Fatalf("New(): %v", err)
	}
	srv.RunInBackground()
	srv.Shutdown() // must return; wg.Wait must not deadlock
	select {
	case err := <-srv.ErrCh():
		if !errors.Is(err, http.ErrServerClosed) && err != nil {
			t.Fatalf("unexpected shutdown error: %v", err)
		}
	default:
	}
}
