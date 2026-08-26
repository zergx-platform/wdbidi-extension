// Package main is wdbidi-extension: the Go rewrite of webdriver-extension.
// It drives a Selenium standalone node over WebDriver BiDi and exposes the 25
// browser tools to the agent over NATS (abep protocol, id "webdriver").
package main

import (
	"context"
	_ "embed"
	"log/slog"
	"os"

	abep "abep.dev/sdk"
	natsbus "abep.dev/sdk/nats"
)

//go:embed manifest.yaml
var manifestYaml []byte

//go:embed ariaSnapshot.js
var ariaBundle string

func main() {
	log := slog.Default().With("svc", "wdbidi-extension")
	natsURL := envOr("NATS_URL", "nats://nats.develop.svc.cluster.local:4222")

	s := newServer()

	nbus, err := natsbus.Connect(natsURL)
	if err != nil {
		log.Error("nats connect failed", "err", err)
		os.Exit(1)
	}

	manifest, err := abep.ParseManifest(manifestYaml)
	if err != nil {
		log.Error("load manifest failed", "err", err)
		os.Exit(1)
	}

	if err := abep.Serve(
		nbus,
		manifest.Config(s.handlers(), nil, nil),
		abep.ServeOptions{
			Run: func(runCtx context.Context, ext *abep.Extension) {
				s.ext = ext
				log.Info("listening", "port", envOr("RUCODER_PORT", "8080"), "nats", natsURL, "selenium", s.seleniumURL)
			},
		},
	); err != nil {
		log.Error("serve failed", "err", err)
		os.Exit(1)
	}
}

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
