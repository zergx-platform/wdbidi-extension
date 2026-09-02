// Package main is wdbidi-extension: the Go rewrite of webdriver-extension.
// It drives a Selenium standalone node over WebDriver BiDi and exposes the 25
// browser tools to the agent over NATS (abc protocol, id "browser").
package main

import (
	"context"
	_ "embed"
	"forgejo.develop.10.199.64.20.nip.io/zergx/wdbidi-extension/internal/env"
	"log/slog"
	"os"

	"github.com/abcp-sdk/abc-protocol-go/extension"
	"github.com/abcp-sdk/abc-protocol-go/manifest"
	natsbus "github.com/abcp-sdk/abc-protocol-go/transport/nats"
)

//go:embed manifest.yaml
var manifestYaml []byte

//go:embed ariaSnapshot.js
var ariaBundle string

func main() {
	log := slog.Default().With("svc", "wdbidi-extension")
	natsURL := env.Or("NATS_URL", "nats://nats.zergx.svc.cluster.local:4222")

	s := newServer()

	nbus, err := natsbus.Connect(natsURL)
	if err != nil {
		log.Error("nats connect failed", "err", err)
		os.Exit(1)
	}

	m, err := manifest.ParseManifest(manifestYaml)
	if err != nil {
		log.Error("load manifest failed", "err", err)
		os.Exit(1)
	}

	if err := extension.Serve(
		extension.New(nbus, m.BuildConfig(manifest.Bindings{Handlers: s.handlers()})),
		extension.ServeOptions{
			Run: func(runCtx context.Context, ext *extension.Extension) {
				s.ext = ext
				log.Info("listening", "port", env.Or("ZERGX_PORT", "8080"), "nats", natsURL, "selenium", s.seleniumURL)
			},
		},
	); err != nil {
		log.Error("serve failed", "err", err)
		os.Exit(1)
	}
}
