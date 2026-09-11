package main

import (
	"context"
	"log"

	"go.uber.org/fx"
)

func main() {
	fx.New(
		fx.Invoke(registerLifecycle),
	).Run()
}

func registerLifecycle(lc fx.Lifecycle) {
	lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			log.Println("starting")
			return nil
		},
		OnStop: func(ctx context.Context) error {
			log.Println("stopping")
			return nil
		},
	})
}
