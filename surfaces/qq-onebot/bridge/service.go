package bridge

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"fairy-surfaces/turnclient"
	"fairy/coreclient"
	"github.com/RomiChan/websocket"
	log "github.com/sirupsen/logrus"
	zero "github.com/wdvxdr1123/ZeroBot"
	"github.com/wdvxdr1123/ZeroBot/driver"
)

func Doctor(ctx context.Context, cfg Config) error {
	core, err := coreclient.New(coreclient.Options{Endpoint: cfg.CoreEndpoint, Token: cfg.CoreToken})
	if err != nil {
		return err
	}
	if _, err := core.Status(ctx); err != nil {
		return errors.New("Core check failed")
	}
	if err := checkOneBot(ctx, cfg); err != nil {
		return errors.New("OneBot check failed")
	}
	return nil
}

func checkOneBot(ctx context.Context, cfg Config) error {
	header := http.Header{"Authorization": []string{"Bearer " + cfg.OneBotToken}}
	conn, response, err := (&websocket.Dialer{}).DialContext(ctx, cfg.OneBotEndpoint, header)
	if err != nil {
		return err
	}
	defer conn.Close()
	if response != nil && response.Body != nil {
		defer response.Body.Close()
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(5 * time.Second)
	}
	if err := conn.SetReadDeadline(deadline); err != nil {
		return err
	}
	var lifecycle struct {
		SelfID int64 `json:"self_id"`
	}
	if err := conn.ReadJSON(&lifecycle); err != nil {
		return err
	}
	selfID, err := strconv.ParseInt(cfg.SelfID, 10, 64)
	if err != nil || lifecycle.SelfID != selfID {
		return errors.New("OneBot self ID mismatch")
	}
	return nil
}

func Serve(ctx context.Context, cfg Config) error {
	return serve(ctx, cfg, func(stage string, _ error) {
		log.WithField("stage", stage).Warn("QQ surface operation failed")
	})
}

func serve(ctx context.Context, cfg Config, report func(string, error)) error {
	log.SetLevel(log.WarnLevel)
	core, err := coreclient.New(coreclient.Options{Endpoint: cfg.CoreEndpoint, Token: cfg.CoreToken})
	if err != nil {
		return err
	}
	turns, err := turnclient.New(core, 15*time.Second)
	if err != nil {
		return err
	}
	plugin, err := NewPlugin(ctx, cfg, &Runner{Core: turns, Sessions: core})
	if err != nil {
		return err
	}
	plugin.report = report
	defer plugin.Close()

	engine := zero.New()
	plugin.Register(engine)
	defer engine.Delete()

	botConfig := &zero.Config{
		Driver:         []zero.Driver{driver.NewWebSocketClient(cfg.OneBotEndpoint, cfg.OneBotToken)},
		MaxProcessTime: 15 * time.Minute,
	}
	go zero.Run(botConfig)
	<-ctx.Done()
	return nil
}
