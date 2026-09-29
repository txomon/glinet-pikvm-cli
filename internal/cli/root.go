package cli

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/spf13/cobra"
	"github.com/txomon/glinet-pikvm-cli/internal/config"
	"github.com/txomon/glinet-pikvm-cli/internal/kvmd"
)

// waitPollInterval is how often waitFor rechecks its condition.
const waitPollInterval = 250 * time.Millisecond

// globals holds the persistent flag values shared by every subcommand.
type globals struct {
	device     string
	output     string
	configPath string
	timeout    time.Duration
}

// client loads the config file, resolves the device named by --device (or
// default_device when empty), and builds a kvmd client for it. A wide config
// file permission prints config.PermWarning to stderr but is not an error.
func (g *globals) client(stderr io.Writer) (*kvmd.Client, config.Device, error) {
	f, err := config.Load(g.configPath)
	if err != nil {
		return nil, config.Device{}, err
	}
	if w := config.PermWarning(g.configPath); w != "" {
		fmt.Fprintln(stderr, w)
	}
	d, err := f.Device(g.device)
	if err != nil {
		return nil, config.Device{}, err
	}
	return kvmd.New(d, g.timeout), d, nil
}

// waitFor polls cond every interval until it reports true, returns an error,
// or ctx is done. cond is checked immediately before the first wait.
func waitFor(ctx context.Context, interval time.Duration, cond func() (bool, error)) error {
	for {
		ok, err := cond()
		if err != nil {
			return err
		}
		if ok {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(interval):
		}
	}
}

// portLink is the per-port HDMI/USB link summary shared by status and port.
type portLink struct {
	Port int    `json:"port"`
	ID   string `json:"id"`
	HDMI bool   `json:"hdmi"`
	USB  bool   `json:"usb"`
}

// portLinks builds the per-port link summary from a switch state, in port
// order (index 0 is port 1).
func portLinks(sw kvmd.SwitchState) []portLink {
	links := make([]portLink, len(sw.Ports))
	for i, p := range sw.Ports {
		links[i] = portLink{
			Port: i + 1,
			ID:   p.ID,
			HDMI: i < len(sw.VideoLinks) && sw.VideoLinks[i],
			USB:  i < len(sw.USBLinks) && sw.USBLinks[i],
		}
	}
	return links
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
	root.AddCommand(newStatusCmd(g))
	root.AddCommand(newPortCmd(g))
	root.AddCommand(newScreenshotCmd(g))
	root.AddCommand(newEdidCmd(g))

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
