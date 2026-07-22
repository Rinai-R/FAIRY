package cmd

import (
	"fairy/interaction"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

func newIdentityCmd(v *viper.Viper, deps Dependencies) *cobra.Command {
	command := &cobra.Command{Use: "identity", Short: "Manage Core owner identity bindings", GroupID: "admin"}
	var namespace, subject string
	bind := &cobra.Command{Use: "bind", Args: cobra.NoArgs, RunE: func(command *cobra.Command, _ []string) error {
		if err := validateIdentityFlags(namespace, subject); err != nil {
			return err
		}
		client, config, err := newClient(v, deps)
		if err != nil {
			return err
		}
		result, err := client.BindOwnerIdentity(command.Context(), namespace, subject)
		if err != nil {
			return err
		}
		return writeOutput(command.OutOrStdout(), config.Output, result)
	}}
	bind.Flags().StringVar(&namespace, "namespace", "", "principal namespace")
	bind.Flags().StringVar(&subject, "subject", "", "principal subject; sent only over the authenticated admin API")
	_ = bind.MarkFlagRequired("namespace")
	_ = bind.MarkFlagRequired("subject")

	list := &cobra.Command{Use: "list", Args: cobra.NoArgs, RunE: func(command *cobra.Command, _ []string) error {
		client, config, err := newClient(v, deps)
		if err != nil {
			return err
		}
		result, err := client.ListOwnerIdentities(command.Context())
		if err != nil {
			return err
		}
		return writeOutput(command.OutOrStdout(), config.Output, result)
	}}

	unbind := &cobra.Command{Use: "unbind", Args: cobra.NoArgs, RunE: func(command *cobra.Command, _ []string) error {
		if err := validateIdentityFlags(namespace, subject); err != nil {
			return err
		}
		client, config, err := newClient(v, deps)
		if err != nil {
			return err
		}
		if err := client.UnbindOwnerIdentity(command.Context(), namespace, subject); err != nil {
			return err
		}
		return writeOutput(command.OutOrStdout(), config.Output, map[string]bool{"ok": true})
	}}
	unbind.Flags().StringVar(&namespace, "namespace", "", "principal namespace")
	unbind.Flags().StringVar(&subject, "subject", "", "principal subject")
	_ = unbind.MarkFlagRequired("namespace")
	_ = unbind.MarkFlagRequired("subject")
	command.AddCommand(bind, list, unbind)
	return command
}

func validateIdentityFlags(namespace, subject string) error {
	return (&interaction.PrincipalRef{Namespace: namespace, Subject: subject}).Validate()
}
