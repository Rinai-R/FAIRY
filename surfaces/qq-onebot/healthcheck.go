package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"fairy/transport/session"
)

const (
	readinessTimeout           = 12 * time.Second
	readinessRequestTimeout    = 5 * time.Second
	maxOneBotReadinessResponse = 64 << 10
)

type readinessPolicy struct {
	totalTimeout   time.Duration
	requestTimeout time.Duration
}

func defaultReadinessPolicy() readinessPolicy {
	return readinessPolicy{
		totalTimeout:   readinessTimeout,
		requestTimeout: readinessRequestTimeout,
	}
}

type readinessError string

const (
	errCoreUnavailable    readinessError = "core_unavailable"
	errOneBotUnavailable  readinessError = "onebot_unavailable"
	errWebhookUnavailable readinessError = "webhook_unavailable"
)

func (err readinessError) Error() string { return string(err) }

func runReadinessCheck(ctx context.Context, cfg Config) error {
	return runReadinessCheckWithPolicy(ctx, cfg, defaultReadinessPolicy())
}

func runReadinessCheckWithPolicy(ctx context.Context, cfg Config, policy readinessPolicy) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	if policy.totalTimeout <= 0 || policy.requestTimeout <= 0 || policy.totalTimeout < policy.requestTimeout {
		return errors.New("readiness timeout policy is invalid")
	}
	checkCtx, cancel := context.WithTimeout(ctx, policy.totalTimeout)
	defer cancel()
	if err := checkCoreReadiness(checkCtx, cfg, policy.requestTimeout); err != nil {
		return errCoreUnavailable
	}
	if err := checkOneBotReadiness(checkCtx, cfg, policy.requestTimeout); err != nil {
		return errOneBotUnavailable
	}
	if err := checkWebhookReadiness(checkCtx, cfg, policy.requestTimeout); err != nil {
		return errWebhookUnavailable
	}
	return nil
}

func checkCoreReadiness(ctx context.Context, cfg Config, requestTimeout time.Duration) error {
	client, err := session.New(session.Options{
		Endpoint: cfg.CoreEndpoint,
		Token:    cfg.CoreToken,
		Timeout:  requestTimeout,
	})
	if err != nil {
		return err
	}
	status, err := client.Status(ctx)
	if err != nil {
		return err
	}
	if !status.Database.Ready || !status.SecretKey.Ready {
		return errors.New("Core dependencies are not ready")
	}
	return nil
}

func checkOneBotReadiness(ctx context.Context, cfg Config, requestTimeout time.Duration) error {
	requestCtx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	endpoint := strings.TrimRight(cfg.OneBotAPIEndpoint, "/") + "/get_login_info"
	request, err := http.NewRequestWithContext(requestCtx, http.MethodPost, endpoint, bytes.NewReader([]byte("{}")))
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+cfg.OneBotToken)
	request.Header.Set("Content-Type", "application/json")
	response, err := (&http.Client{Timeout: requestTimeout}).Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return errors.New("OneBot action returned non-success status")
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxOneBotReadinessResponse+1))
	if err != nil {
		return err
	}
	if len(raw) > maxOneBotReadinessResponse {
		return errors.New("OneBot action response exceeded limit")
	}
	var result struct {
		Status  string `json:"status"`
		RetCode int64  `json:"retcode"`
		Data    struct {
			UserID int64 `json:"user_id"`
		} `json:"data"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := decoder.Decode(&result); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("OneBot action response contains trailing data")
	}
	if result.Status != "ok" || result.RetCode != 0 || result.Data.UserID <= 0 {
		return errors.New("OneBot login is not ready")
	}
	return nil
}

func checkWebhookReadiness(ctx context.Context, cfg Config, requestTimeout time.Duration) error {
	parsed, err := url.Parse(cfg.OneBotWebhookEndpoint)
	if err != nil {
		return err
	}
	host := parsed.Hostname()
	if host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	port := parsed.Port()
	if port == "" {
		port = "80"
	}
	dialCtx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	connection, err := (&net.Dialer{Timeout: requestTimeout}).DialContext(dialCtx, "tcp", net.JoinHostPort(host, port))
	if err != nil {
		return err
	}
	return connection.Close()
}
