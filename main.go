package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
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

type closeFunc func() error

func initializeLogger() (*slog.Logger, closeFunc, error) {
	env := os.Getenv("LINKO_LOG_FILE")

	var logWriter io.Writer
	var logClose closeFunc

	if env == "" {
		logWriter = os.Stderr
		logClose = func() error {
			return nil
		}
	} else {
		logFile, err := os.OpenFile(env, os.O_WRONLY|os.O_CREATE, 0o644)
		if err != nil {
			return nil, nil, err
		}
		logFileWriter := bufio.NewWriterSize(logFile, 8192)

		logWriter = io.MultiWriter(
			logFileWriter, 
			os.Stderr,
		)

		logClose = func() error {
			return logFileWriter.Flush()
		}
	}

	logger := slog.New(slog.NewTextHandler(logWriter, nil))

	return logger, logClose, nil
}

func run(ctx context.Context, cancel context.CancelFunc, httpPort int, dataDir string) int {
	logger, closer, err := initializeLogger()
	if err != nil {
	}
	defer func() {
		err = closer()
		if err != nil {
			os.Stderr.WriteString(err.Error())
		}
	}()
	if err = closer(); err != nil {
		return 1
	}

	st, err := store.New(dataDir, logger)
	if err != nil {
		logger.Info(fmt.Sprintf("failed to create store: %v", err))
		return 1
	}
	s := newServer(*st, httpPort, cancel, logger)
	var serverErr error
	go func() {
		serverErr = s.start()
	}()

	<-ctx.Done()
	logger.Info("Linko is shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := s.shutdown(shutdownCtx); err != nil {
		logger.Info(fmt.Sprintf("failed to shutdown server: %v", err))
		return 1
	}
	if serverErr != nil {
		logger.Info(fmt.Sprintf("server error: %v", serverErr))
		return 1
	}

	return 0
}

