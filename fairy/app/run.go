package app

import (
	"fmt"
	"io/fs"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"fairy/character"
	"fairy/companion"
	"fairy/config"
	"fairy/desktop"
	"fairy/logx"
	"fairy/memory"
	"fairy/model"
	"fairy/notify"
	"fairy/profile"
	"fairy/search"
	"fairy/secret"
	"fairy/speech"
	"fairy/visual"
	"github.com/wailsapp/wails/v3/pkg/application"
	"go.uber.org/zap"
)

// Options contains process-level dependencies owned by package main.
type Options struct {
	EmbeddedAssets   fs.FS
	AssetsDir        string
	AppIcon          []byte
	TrayTemplateIcon []byte
	ConfigRoot       string
	Logger           *zap.Logger
}

// Run builds and starts the Wails desktop shell.
func Run(options Options) error {
	logger := options.Logger
	if logger == nil {
		logger = logx.New()
		defer func() { _ = logger.Sync() }()
	}

	assetsDir := strings.TrimSpace(options.AssetsDir)
	if assetsDir == "" {
		assetsDir = "assets/dist"
	}
	assets, err := fs.Sub(options.EmbeddedAssets, assetsDir)
	if err != nil {
		return fmt.Errorf("embed assets: %w", err)
	}

	configRoot := options.ConfigRoot
	if configRoot == "" {
		configRoot = os.Getenv("FAIRY_CONFIG_ROOT")
	}
	if configRoot == "" {
		configRoot = filepath.Join(os.Getenv("HOME"), "Library", "Application Support", "dev.rinai.fairy", "harness", "v1")
	}

	bootstrap := desktop.NewBootstrapService(desktop.BootstrapOptions{
		AppName:                "FAIRY",
		MigrationStage:         "wails3-only",
		WailsVersion:           "v3.0.0-alpha2.117",
		RespondRuntimeMigrated: true,
	})
	memoryPath, err := memory.DatabasePath(configRoot)
	if err != nil {
		return fmt.Errorf("resolve memory path: %w", err)
	}
	memoryStore, err := memory.OpenOrCreate(memoryPath)
	if err != nil {
		return fmt.Errorf("open memory store: %w", err)
	}
	secretPath, err := secret.DatabasePath(configRoot)
	if err != nil {
		return fmt.Errorf("resolve secret path: %w", err)
	}
	secretStore := secret.NewStore(secretPath)
	webSearch := search.NewService(configRoot)
	modelService := model.NewModelService(configRoot, secretStore)
	desktopService := desktop.NewDesktopService()
	companionService := companion.NewCompanionServiceWithRuntime(configRoot, memoryStore, modelService, webSearch)
	defer companionService.Close()

	characterService := character.NewCharacterService(configRoot)
	configService := config.NewConfigService(configRoot, secretStore)
	speechService := speech.NewSpeechService(configRoot, secretStore)
	profileService := profile.NewProfileService(configRoot)
	assetHandler := visual.NewAssetHandler(configRoot)
	configReader := config.NewReader(configRoot)

	// Dependency injection from the composition root (project Attach* idiom):
	// no package globals; each service receives its long-lived handles here.
	companion.AttachLogger(companionService, logger.Named("companion"))
	companion.AttachCharacterStore(companionService, characterService.CatalogStore())
	companion.AttachProfileStore(companionService, profileService.ProfileStore())
	companion.AttachConfigReader(companionService, configReader)
	companion.AttachSpeechSynthesizer(companionService, companionSpeechAdapter{service: speechService})
	character.AttachLogger(characterService, logger.Named("character"))
	visual.AttachLogger(assetHandler, logger.Named("visual"))
	search.AttachLogger(webSearch, logger.Named("openserp"))

	// shuttingDown gates teardown-only log filtering (see WarningHandler).
	var shuttingDown atomic.Bool

	wailsApp := application.New(application.Options{
		Name:        "FAIRY",
		Description: "Desktop companion app with a Go/Wails migration runtime.",
		Icon:        options.AppIcon,
		// Route Wails system logs through the same zap backend for uniform output.
		Logger: logx.NewSlog(logger.Named("wails")),
		// During teardown Wails dispatches late window events after the window
		// map is cleared, emitting benign "Window #N not found" warnings. Drop
		// those once shutdown has begun; surface every other warning so real
		// issues during normal operation stay visible.
		WarningHandler: func(msg string) {
			if shuttingDown.Load() && strings.Contains(msg, "not found") {
				return
			}
			logger.Warn("wails warning", zap.String("msg", msg))
		},
		// OnShutdown runs on the native app-terminate path (tray Quit / Cmd+Q),
		// cancelling in-flight turns and extraction timers and stopping the
		// openserp sidecar rather than orphaning them. SIGINT/SIGTERM are NOT
		// covered here (Wails v3 alpha2.117 sets up a signal handler but never
		// starts it), so Run installs its own handler below. Close is idempotent.
		OnShutdown: func() {
			shuttingDown.Store(true)
			if err := companionService.Close(); err != nil {
				logger.Error("companion shutdown", zap.Error(err))
			}
		},
		Services: []application.Service{
			application.NewService(bootstrap),
			application.NewService(characterService),
			application.NewService(configService),
			application.NewService(desktopService),
			application.NewService(modelService),
			application.NewService(companionService),
			application.NewService(memory.NewMemoryServiceWithStore(configRoot, memoryStore)),
			application.NewService(profileService),
			application.NewService(speechService),
			application.NewService(visual.NewVisualService(configRoot)),
			application.NewServiceWithOptions(assetHandler, application.ServiceOptions{
				Name:  "CharacterAssetHandler",
				Route: "/fairy-character",
			}),
		},
		Assets: application.AssetOptions{
			Handler: application.AssetFileServerFS(assets),
		},
		Mac: application.MacOptions{
			ApplicationShouldTerminateAfterLastWindowClosed: false,
		},
	})

	companion.AttachEventEmitter(companionService, func(event companion.HarnessEvent) {
		wailsApp.Event.Emit("companion-harness-event", event)
	})
	desktop.AttachStateEmitter(desktopService, func(state desktop.DesktopState) {
		wailsApp.Event.Emit("desktop-state-changed", state)
	})
	emitConfig := func(change notify.ConfigurationChange) {
		wailsApp.Event.Emit("companion-configuration-changed", change)
	}
	character.AttachConfigEmitter(characterService, emitConfig)
	config.AttachConfigEmitter(configService, emitConfig)
	profile.AttachConfigEmitter(profileService, emitConfig)

	attachProductWindows(wailsApp, desktopService, logger)
	setupSystemTray(wailsApp, desktopService, logger, options.TrayTemplateIcon)

	// Wails v3 alpha2.117 creates a SIGINT/SIGTERM handler but never starts it,
	// so without this an interrupt kills the process immediately, skipping every
	// defer and OnShutdown and orphaning the openserp sidecar. Run business
	// cleanup ourselves, then Quit for native teardown; a second signal or a
	// stalled teardown forces exit. Close is idempotent with OnShutdown.
	signals := make(chan os.Signal, 2)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	go func() {
		sig := <-signals
		logger.Info("received signal, shutting down", zap.String("signal", sig.String()))
		shuttingDown.Store(true)
		if err := companionService.Close(); err != nil {
			logger.Error("companion shutdown", zap.Error(err))
		}
		wailsApp.Quit()
		time.AfterFunc(5*time.Second, func() {
			logger.Warn("shutdown timed out, forcing exit")
			os.Exit(1)
		})
		<-signals
		os.Exit(1)
	}()

	if err := wailsApp.Run(); err != nil {
		return fmt.Errorf("app run: %w", err)
	}
	return nil
}
