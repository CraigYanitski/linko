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

	"boot.dev/linko/internal/build"
	"boot.dev/linko/internal/linkoerr"
	"boot.dev/linko/internal/store"
	"github.com/lmittmann/tint"
	"github.com/mattn/go-isatty"
	pkgerr "github.com/pkg/errors"
)

type stackTracer interface {
	error
	StackTrace() pkgerr.StackTrace
}

type multiError interface {
	error
	Unwrap() []error
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
	logName := os.Getenv("LINKO_LOG_FILE")

	var logClose closeFunc
	var logger *slog.Logger
	var infoHandler *slog.JSONHandler
	isaTTY := isatty.IsCygwinTerminal(os.Stderr.Fd()) || isatty.IsTerminal(os.Stderr.Fd())
	options := &tint.Options{Level: slog.LevelDebug, ReplaceAttr: replaceAttr, NoColor: !isaTTY}
	debugHandler := tint.NewHandler(os.Stderr, options)

	if logName == "" {
		logger = slog.New(slog.NewMultiHandler(
			debugHandler,
		))

		logClose = func() error {
			return nil
		}
	} else {
		logFile, err := os.OpenFile(logName, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
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

	// Set information to print with logger
	env := os.Getenv("ENV")
	hostname, _ := os.Hostname()
	logger = logger.With(
		slog.String("git_sha", build.GitSHA),
		slog.String("build_time", build.BuildTime),
		slog.String("env", env),
		slog.String("hostname", hostname),
	)

	return logger, logClose, nil
}

func errorAttrs(err error) []slog.Attr {
	attrs := []slog.Attr{{
		Key:   "message",
		Value: slog.StringValue(err.Error()),
	}}
	attrs = append(attrs, linkoerr.Attrs(err)...)
	if stackErr, ok := errors.AsType[stackTracer](err); ok {
		attrs = append(attrs, slog.Attr{
				Key:   "stack_trace",
				Value: slog.StringValue(fmt.Sprintf("%+v", stackErr.StackTrace())),
		})
	}
	return attrs
}

func replaceAttr(groups []string, a slog.Attr) slog.Attr {
	if a.Key == "error" {
		err, ok := a.Value.Any().(error)
		if !ok {
			return a
		}
		var errKey string
		var errAttrs []slog.Attr
		if multiErr, ok := errors.AsType[multiError](err); ok {
			errKey = "errors"
			for i, err := range multiErr.Unwrap() {
				attrs := errorAttrs(err)
				errAttrs = append(
					errAttrs, 
					slog.GroupAttrs(fmt.Sprintf("error_%d", i+1), attrs...),
				)
			}
		} else {
			errKey = "error"
			errAttrs = errorAttrs(err)
		}
		return slog.GroupAttrs(errKey, errAttrs...)
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

