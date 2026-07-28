package main

import (
	"context"
	"io"
	"log"
	"os"
	"os/signal"
	"syscall"

	app "portlistener2dns/client/internal/client"
)

func main() {
	cfg, err := app.ConfigFromEnv()
	if err != nil {
		os.Exit(2)
	}

	output := io.Writer(io.Discard)
	if cfg.Verbose {
		output = os.Stderr
	}
	logger := log.New(output, "portlistener2dns: ", log.Ldate|log.Ltime|log.LUTC|log.Lmsgprefix)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := app.Run(ctx, cfg, logger); err != nil {
		logger.Printf("运行失败: %v", err)
		os.Exit(1)
	}
}
