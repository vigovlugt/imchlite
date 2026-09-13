package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net"
	"net/http"
	"os/exec"
	"runtime"
	"time"
)

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		log.Printf("write response: %v", err)
	}
}

func newServer(addr string, state *indexerState, assets *assetRepository, libraryLocation string) *http.Server {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	mux.HandleFunc("GET /api/index/status", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, state.status())
	})

	registerAssetRoutes(mux, assets, libraryLocation)

	mux.Handle("GET /", frontendHandler())

	return &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
}

func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}

	if err := cmd.Start(); err != nil {
		log.Printf("open browser: %v", err)
	}
}

func runServer(ctx context.Context, srv *http.Server) error {
	log.Printf("api server listening on %s", srv.Addr)

	listener, err := net.Listen("tcp", srv.Addr)
	if err != nil {
		return err
	}

	go openBrowser("http://" + srv.Addr)

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go func() {
		<-ctx.Done()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			log.Printf("shutdown: %v", err)
		}
	}()

	err = srv.Serve(listener)
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}

	return nil
}
