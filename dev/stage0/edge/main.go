package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "stage0-edge:", err)
		os.Exit(1)
	}
}

func run() error {
	if len(os.Args) > 1 && os.Args[1] == "homelab-profile" {
		return homelabProfile(os.Args[2:])
	}
	cfgPath := flag.String("config", "", "the edge's configuration file (JSON)")
	debug := flag.Bool("debug", false, "also log each admitted flow request")
	check := flag.Bool("check", false, "check the configuration and the files it names, then exit")
	flag.Parse()
	if *cfgPath == "" {
		return fmt.Errorf("usage: stage0-edge -config FILE, or stage0-edge homelab-profile -base FILE -hosts FILE -out FILE")
	}
	level := slog.LevelInfo
	if *debug {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
	cfg, err := LoadConfig(*cfgPath)
	if err != nil {
		return err
	}
	e, err := New(cfg, log)
	if err != nil {
		return err
	}
	if *check {
		for _, w := range cfg.Workers {
			fmt.Printf("%s 127.0.0.1:%d\n", w.Name, w.Port)
		}
		return nil
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	bound, err := e.Listen(ctx)
	if err != nil {
		return err
	}
	for _, b := range bound {
		log.Info("worker port", slog.String("worker", b.Worker), slog.String("addr", b.Listener.Addr().String()))
	}
	return e.Serve(ctx, bound)
}
