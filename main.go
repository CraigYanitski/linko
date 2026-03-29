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

	"boot.dev/linko/internal/store"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)

	httpPort := flag.Int("port", 8899, "port to listen on")
	dataDir := flag.String("data", "./data", "directory to store data")
	flag.Parse()

	status := run(ctx, cancel, *httpPort, *dataDir)
	cancel()
	os.Exit(status)
}

func requestLogger(logger *log.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r)
			logger.Printf("Served request: %s %s\n", r.Method, r.URL.Path)
		})
	}
}

func run(ctx context.Context, cancel context.CancelFunc, httpPort int, dataDir string) int {
	logFile, err := os.Create("linko.access.log")
	if err != nil {
		log.Println(err)
		os.Exit(1)
	}

	var accessLogger = log.New(logFile, "INFO: ", log.LstdFlags)
	var stdLogger = log.New(os.Stderr, "DEBUG: ", log.LstdFlags)

	st, err := store.New(dataDir, stdLogger)
	if err != nil {
		log.Printf("failed to create store: %v\n", err)
		return 1
	}
	s := newServer(*st, httpPort, cancel, accessLogger)
	var serverErr error
	go func() {
		serverErr = s.start()
	}()

	<-ctx.Done()
	stdLogger.Println("Linko is shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := s.shutdown(shutdownCtx); err != nil {
		log.Printf("failed to shutdown server: %v\n", err)
		return 1
	}
	if serverErr != nil {
		log.Printf("server error: %v\n", serverErr)
		return 1
	}
	return 0
}
