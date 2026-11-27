package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"github.com/lagrangian-dev/lagrangian/internal/cli"
	"github.com/lagrangian-dev/lagrangian/internal/core"
	"github.com/lagrangian-dev/lagrangian/internal/platform/paths"
	"github.com/lagrangian-dev/lagrangian/internal/platform/secrets"
	"github.com/lagrangian-dev/lagrangian/internal/transport/httpapi"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "init" {
		runInitCommand(os.Args[2:])
		return
	}
	runServeCommand(os.Args[1:])
}

func runInitCommand(args []string) {
	fs := flag.NewFlagSet("init", flag.ExitOnError)
	homeFlag := fs.String("home", "", "Override Lagrangian home directory (or set LAGRANGIAN_HOME)")
	configFlag := fs.String("config", "", "Explicit path to config.yaml")
	forceFlag := fs.Bool("force", false, "Overwrite existing config.yaml")
	_ = fs.Parse(args)

	svc, cleanup := openService(*homeFlag, *configFlag)
	defer cleanup()

	if _, err := cli.RunInitWizard(svc, os.Stdin, os.Stdout, *forceFlag); err != nil {
		if errors.Is(err, cli.ErrCanceled) {
			fmt.Fprintln(os.Stdout, "已取消初始化。")
			return
		}
		log.Fatalf("初始化失败: %v", err)
	}
}

func runServeCommand(args []string) {
	fs := flag.NewFlagSet("lagrangian-server", flag.ExitOnError)
	homeFlag := fs.String("home", "", "Override Lagrangian home directory (or set LAGRANGIAN_HOME)")
	configFlag := fs.String("config", "", "Explicit path to config.yaml")
	hostFlag := fs.String("host", "", "Override listen host")
	portFlag := fs.Int("port", 0, "Override listen port")
	devFlag := fs.Bool("dev", false, "Enable development logging")
	_ = fs.Parse(args)

	svc, cleanup := openService(*homeFlag, *configFlag)
	defer cleanup()

	if !svc.IsInitialized() {
		if !cli.IsInteractive(os.Stdin) {
			log.Fatalf("未检测到配置文件 (%s)。请先在终端运行 `lagrangian-server init` 完成分步初始化，或通过 --config 指定已有配置。", svc.SetupStatus().ConfigPath)
		}
		fmt.Fprintln(os.Stdout, "首次启动未检测到配置文件，即将进入终端分步初始化向导。")
		if _, err := cli.RunInitWizard(svc, os.Stdin, os.Stdout, false); err != nil {
			if errors.Is(err, cli.ErrCanceled) {
				fmt.Fprintln(os.Stdout, "已取消初始化，服务未启动。")
				return
			}
			log.Fatalf("初始化失败: %v", err)
		}
		if !cli.PromptStartNow(os.Stdin, os.Stdout) {
			fmt.Fprintln(os.Stdout, "配置已保存。后续可直接运行 `lagrangian-server` 启动服务。")
			return
		}
	}

	cfg := svc.Config()
	host := cfg.Server.EffectiveHost()
	if *hostFlag != "" {
		host = *hostFlag
	}
	port := cfg.Server.Port
	if *portFlag > 0 {
		port = *portFlag
	}
	addr := fmt.Sprintf("%s:%d", host, port)

	apiServer := httpapi.NewServer(svc)
	httpSrv := &http.Server{
		Addr:              addr,
		Handler:           apiServer,
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		log.Printf("Lagrangian server listening on http://%s (mode=%s, os=%s/%s, auth=%v, config=%s, dev=%v)",
			addr, cfg.Server.Mode, runtime.GOOS, runtime.GOARCH, cfg.Server.AuthEnabled, svc.SetupStatus().ConfigPath, *devFlag)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("http server error: %v", err)
		}
	}()

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	log.Println("Shutting down Lagrangian server...")
	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		log.Printf("graceful shutdown error: %v", err)
	}
}

func openService(homeFlag, configFlag string) (*core.Service, func()) {
	layout, err := paths.Resolve(homeFlag)
	if err != nil {
		log.Fatalf("resolve paths: %v", err)
	}
	configPath := layout.ConfigFile
	if configFlag != "" {
		configPath = configFlag
	}
	secretStore, err := secrets.Open(layout.SecretsDir)
	if err != nil {
		log.Fatalf("open secret store: %v", err)
	}
	svc, err := core.NewService(layout, configPath, secretStore)
	if err != nil {
		log.Fatalf("initialize core service: %v", err)
	}
	return svc, func() { _ = svc.Close() }
}
