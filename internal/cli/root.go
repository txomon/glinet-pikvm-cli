package cli

import (
	"fmt"
	"io"
	"time"

	"github.com/spf13/cobra"
	"github.com/txomon/glinet-pikvm-cli/internal/config"
)

// globals holds the persistent flag values shared by every subcommand.
// Later commands read from it (and, once the API client lands, will get a
// (g *globals) client(...) helper) so keep it the single source of truth.
type globals struct {
	device     string
	output     string
	configPath string
	timeout    time.Duration
}

// noArgs rejects any positional argument, as a UsageError.
func noArgs(cmd *cobra.Command, args []string) error {
	if len(args) > 0 {
		return usagef("unknown command %q for %q", args[0], cmd.CommandPath())
	}
	return nil
}

// newRoot builds the root command. Later tasks hang their subcommands off
// the returned command via root.AddCommand.
func newRoot(g *globals, stdout, stderr io.Writer) *cobra.Command {
	root := &cobra.Command{
		Use:           "glkvm",
		Short:         "Drive a GL.iNet Comet X KVM over its HTTP API",
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          noArgs,
	}
	root.SetOut(stdout)
	root.SetErr(stderr)

	root.PersistentFlags().StringVarP(&g.device, "device", "d", "", "device name from the config file (default: default_device)")
	root.PersistentFlags().StringVarP(&g.output, "output", "o", "text", "output format: text|json")
	root.PersistentFlags().StringVar(&g.configPath, "config", config.DefaultPath(), "path to the config file")
	root.PersistentFlags().DurationVar(&g.timeout, "timeout", 15*time.Second, "per-request timeout")

	root.PersistentPreRunE = func(cmd *cobra.Command, args []string) error {
		if g.output != "text" && g.output != "json" {
			return usagef("invalid --output %q: must be text or json", g.output)
		}
		return nil
	}

	root.SetFlagErrorFunc(func(cmd *cobra.Command, err error) error {
		return UsageError{Msg: err.Error()}
	})

	// A root with no RunE is "not runnable": cobra short-circuits straight
	// to printing help and returning nil, before Args ever runs, so a typo
	// subcommand ("glkvm frobnicate") would silently exit 0. Giving it a
	// trivial RunE keeps it runnable, so noArgs still gets a chance to
	// reject stray args as a UsageError; called with no args it just prints
	// help, same as before.
	root.RunE = func(cmd *cobra.Command, args []string) error {
		return cmd.Help()
	}

	root.AddCommand(newVersionCmd(g))

	return root
}

func newVersionCmd(g *globals) *cobra.Command {
	cmd := &cobra.Command{
		Use:           "version",
		Short:         "Print the glkvm version",
		Args:          noArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		return render(cmd.OutOrStdout(), g.output, map[string]string{"version": "dev"}, func(w io.Writer) {
			fmt.Fprintln(w, "glkvm dev")
		})
	}
	return cmd
}

// Execute runs the CLI for args (excluding the program name) and returns the
// process exit code. JSON errors are written to stdout, text errors to
// stderr, matching where their corresponding success output goes.
func Execute(args []string, stdout, stderr io.Writer) int {
	g := &globals{}
	root := newRoot(g, stdout, stderr)
	root.SetArgs(args)

	err := root.Execute()
	if err == nil {
		return ExitOK
	}

	w := stderr
	if g.output == "json" {
		w = stdout
	}
	return renderError(w, g.output, err)
}
