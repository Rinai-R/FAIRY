package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"fairy/coreclient"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

func newStatusCmd(v *viper.Viper, deps Dependencies) *cobra.Command {
	return &cobra.Command{
		Use: "status", Short: "Read Session Core status", Args: cobra.NoArgs, GroupID: "debug",
		RunE: func(command *cobra.Command, args []string) error {
			client, config, err := newClient(v, deps)
			if err != nil {
				return err
			}
			status, err := client.Status(command.Context())
			if err != nil {
				return err
			}
			return writeOutput(command.OutOrStdout(), config.Output, status)
		},
	}
}

type doctorCheck struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
}

type doctorReport struct {
	Endpoint string        `json:"endpoint"`
	Checks   []doctorCheck `json:"checks"`
}

func newDoctorCmd(v *viper.Viper, deps Dependencies) *cobra.Command {
	return &cobra.Command{
		Use: "doctor", Short: "Check Core connectivity and configuration", Args: cobra.NoArgs, GroupID: "debug",
		RunE: func(command *cobra.Command, args []string) error {
			client, config, err := newClient(v, deps)
			if err != nil {
				return err
			}
			report, checkErr := runDoctor(command.Context(), client, config.Endpoint)
			if err := writeOutput(command.OutOrStdout(), config.Output, report); err != nil {
				return err
			}
			return checkErr
		},
	}
}

func runDoctor(ctx context.Context, client APIClient, endpoint string) (doctorReport, error) {
	report := doctorReport{Endpoint: endpoint, Checks: make([]doctorCheck, 0, 10)}
	status, err := client.Status(ctx)
	if err != nil {
		report.Checks = append(report.Checks, doctorCheck{Name: "core", Status: "fail", Detail: err.Error()})
		return report, fmt.Errorf("doctor: connect to %s: %w", endpoint, err)
	}
	report.Checks = append(report.Checks,
		doctorCheck{Name: "core", Status: "pass"},
		doctorCheck{Name: "bootstrap", Status: "pass"},
	)
	var failures []error
	for _, dependency := range []struct {
		name   string
		status coreclient.DependencyStatus
	}{
		{name: "database", status: status.Database},
		{name: "qdrant", status: status.Qdrant},
		{name: "secret-key", status: status.SecretKey},
	} {
		check := doctorCheck{Name: dependency.name, Status: "pass"}
		if dependency.status.Mode != "production" || !dependency.status.Ready {
			check.Status = "fail"
			check.Detail = dependency.status.Error
			if check.Detail == "" {
				check.Detail = "required production dependency is not ready"
			}
			failures = append(failures, fmt.Errorf("%s: %s", dependency.name, check.Detail))
		}
		report.Checks = append(report.Checks, check)
	}
	checkConfig := func(section string) {
		raw, err := client.GetConfig(ctx, section)
		if err != nil {
			report.Checks = append(report.Checks, doctorCheck{Name: section, Status: "fail", Detail: err.Error()})
			failures = append(failures, err)
			return
		}
		state := configuredState(raw)
		report.Checks = append(report.Checks, doctorCheck{Name: section, Status: state})
		if state == "fail" {
			failures = append(failures, fmt.Errorf("%s status response is missing required fields", section))
		}
	}
	checkConfig("model")
	checkConfig("speech")
	checkConfig("web-search")
	checkConfig("semantic-embedding")
	catalog, err := client.ListCharacters(ctx)
	if err != nil {
		report.Checks = append(report.Checks, doctorCheck{Name: "character", Status: "fail", Detail: err.Error()})
		failures = append(failures, err)
	} else if catalog.Active == nil {
		report.Checks = append(report.Checks, doctorCheck{Name: "character", Status: "unconfigured"})
	} else {
		report.Checks = append(report.Checks, doctorCheck{Name: "character", Status: "pass"})
	}
	if _, err := client.GetProfile(ctx); err != nil {
		report.Checks = append(report.Checks, doctorCheck{Name: "profile", Status: "fail", Detail: err.Error()})
		failures = append(failures, err)
	} else {
		report.Checks = append(report.Checks, doctorCheck{Name: "profile", Status: "pass"})
	}
	if _, err := client.Logs(ctx, coreclient.LogQuery{Limit: 1}); err != nil {
		report.Checks = append(report.Checks, doctorCheck{Name: "logs", Status: "fail", Detail: err.Error()})
		failures = append(failures, err)
	} else {
		report.Checks = append(report.Checks, doctorCheck{Name: "logs", Status: "pass"})
	}
	if _, err := client.Metrics(ctx); err != nil {
		report.Checks = append(report.Checks, doctorCheck{Name: "metrics", Status: "fail", Detail: err.Error()})
		failures = append(failures, err)
	} else {
		report.Checks = append(report.Checks, doctorCheck{Name: "metrics", Status: "pass"})
	}
	if len(failures) > 0 {
		return report, errors.Join(failures...)
	}
	return report, nil
}

func configuredState(raw []byte) string {
	var state struct {
		Configured *bool `json:"configured"`
		Enabled    *bool `json:"enabled"`
	}
	if json.Unmarshal(raw, &state) != nil {
		return "fail"
	}
	if state.Configured != nil && !*state.Configured {
		return "unconfigured"
	}
	if state.Enabled != nil && !*state.Enabled {
		return "unconfigured"
	}
	return "pass"
}

func terminalError(state string) error {
	return fmt.Errorf("turn ended in terminal state %s", strings.ToLower(state))
}
