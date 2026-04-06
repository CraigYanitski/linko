package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"boot.dev/linko/internal/linkoerr"
	"boot.dev/linko/internal/store"
	pkgerr "github.com/pkg/errors"
)

type stackTracer interface {
	error
	StackTrace() pkgerr.StackTrace
}

type closeFunc func() error

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)

	httpPort := flag.Int("port", 8899, "port to listen on")
	dataDir := flag.String("data", "./data", "directory to store data")
	flag.Parse()

	status := run(ctx, cancel, *httpPort, *dataDir)
	cancel()
	os.Exit(status)
}

func initializeLogger() (*slog.Logger, closeFunc, error) {
	env := os.Getenv("LINKO_LOG_FILE")

	var logClose closeFunc
	var logger *slog.Logger
	var infoHandler *slog.JSONHandler
	debugHandler := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelDebug,
		ReplaceAttr: replaceAttr,
	})

	if env == "" {
		logger = slog.New(slog.NewMultiHandler(
			debugHandler,
		))

		logClose = func() error {
			return nil
		}
	} else {
		logFile, err := os.OpenFile(env, os.O_WRONLY|os.O_CREATE, 0o644)
		if err != nil {
			return nil, nil, err
		}
		logFileWriter := bufio.NewWriterSize(logFile, 8192)
		
		infoHandler = slog.NewJSONHandler(logFileWriter, &slog.HandlerOptions{
			Level: slog.LevelInfo,
			ReplaceAttr: replaceAttr,
		})

		logger = slog.New(slog.NewMultiHandler(
			debugHandler,
			infoHandler,
		))

		logClose = func() error {
			return logFileWriter.Flush()
		}
	}

	return logger, logClose, nil
}

func replaceAttr(groups []string, a slog.Attr) slog.Attr {
	if a.Key == "error" {
		err, ok := a.Value.Any().(error)
		if !ok {
			return a
		}
		attrs := []slog.Attr{
			{
				Key:   "message",
				Value: slog.StringValue(err.Error()),
			},
		}
		attrs = append(attrs, linkoerr.Attrs(err)...)
		if stackErr, ok := errors.AsType[stackTracer](err); ok {
			attrs = append(attrs, slog.Attr{
					Key:   "stack_trace",
					Value: slog.StringValue(fmt.Sprintf("%+v", stackErr.StackTrace())),
			})
		}
		return slog.GroupAttrs("error", attrs...)
	}
	return a
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
		logger.Error("failed to create store", "error", err)
		return 1
	}
	s := newServer(*st, httpPort, cancel, logger)
	var serverErr error
	go func() {
		serverErr = s.start()
	}()

	<-ctx.Done()
	logger.Debug("Linko is shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := s.shutdown(shutdownCtx); err != nil {
		logger.Error("failed to shutdown server", "error", err)
		return 1
	}
	if serverErr != nil {
		logger.Error("server error", "error", serverErr)
		return 1
	}

	return 0
}

