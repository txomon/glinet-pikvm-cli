package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/txomon/glinet-pikvm-cli/internal/config"
	"github.com/txomon/glinet-pikvm-cli/internal/kvmd"
)

// --- devices ---

// deviceEntry is one configured device in glkvm devices' result. It never
// includes the password.
type deviceEntry struct {
	Name    string `json:"name"`
	URL     string `json:"url"`
	Default bool   `json:"default"`
}

// devicesFromFile lists f's configured devices, sorted by name, marking
// which one default_device names.
func devicesFromFile(f *config.File) []deviceEntry {
	names := f.Names()
	entries := make([]deviceEntry, 0, len(names))
	for _, name := range names {
		entries = append(entries, deviceEntry{
			Name:    name,
			URL:     f.Devices[name].URL,
			Default: name == f.DefaultDevice,
		})
	}
	return entries
}

func newDevicesCmd(g *globals) *cobra.Command {
	cmd := &cobra.Command{
		Use:           "devices",
		Short:         "List configured devices",
		Args:          noArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		f, err := config.Load(g.configPath)
		if err != nil {
			return err
		}
		if w := config.PermWarning(g.configPath); w != "" {
			fmt.Fprintln(cmd.ErrOrStderr(), w)
		}
		entries := devicesFromFile(f)
		return render(cmd.OutOrStdout(), g.output, entries, func(w io.Writer) {
			for _, e := range entries {
				mark := " "
				if e.Default {
					mark = "*"
				}
				fmt.Fprintf(w, "%s %-16s %s\n", mark, e.Name, e.URL)
			}
		})
	}
	return cmd
}

// --- doctor ---

// doctorCheck is one named check in glkvm doctor's ordered report.
type doctorCheck struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
}

// addCheck appends one doctorCheck to checks. When required is true, an
// ok=false check also adds name to failedNames, which decides doctor's exit
// code. The otg and mouse checks pass required=false: they report device
// state, not a pass/fail condition, so they never affect the exit code even
// when the underlying request itself failed.
func addCheck(checks *[]doctorCheck, failedNames *[]string, name string, ok bool, detail string, required bool) {
	*checks = append(*checks, doctorCheck{Name: name, OK: ok, Detail: detail})
	if required && !ok {
		*failedNames = append(*failedNames, name)
	}
}

// doctorErr builds the error doDoctor returns when failedNames is non-empty,
// or nil when every required check passed.
func doctorErr(failedNames []string) error {
	if len(failedNames) == 0 {
		return nil
	}
	return fmt.Errorf("checks failed: %s", strings.Join(failedNames, ", "))
}

// doDoctor runs every doctor check in order, always running all of them even
// after an earlier one fails, so one report shows the full state. It returns
// the checks alongside an error naming which required checks failed, or a
// nil error when all of them passed. It is a plain function, not a cobra
// RunE, so other callers can use it without cobra.
func doDoctor(ctx context.Context, configPath, deviceName string, timeout time.Duration) ([]doctorCheck, error) {
	var checks []doctorCheck
	var failedNames []string

	configOK := true
	var detail strings.Builder
	appendDetail := func(s string) {
		if detail.Len() > 0 {
			detail.WriteString("; ")
		}
		detail.WriteString(s)
	}

	st, statErr := os.Stat(configPath)
	switch {
	case statErr != nil:
		configOK = false
		appendDetail(statErr.Error())
	case st.Mode().Perm()&0o077 != 0:
		configOK = false
		appendDetail(fmt.Sprintf("%s is mode %o, want 0600", configPath, st.Mode().Perm()))
	default:
		appendDetail(fmt.Sprintf("%s is mode %o", configPath, st.Mode().Perm()))
	}

	f, loadErr := config.Load(configPath)
	if loadErr != nil {
		configOK = false
		appendDetail(loadErr.Error())
	}

	var d config.Device
	haveDevice := false
	if f != nil {
		dev, devErr := f.Device(deviceName)
		if devErr != nil {
			configOK = false
			appendDetail(devErr.Error())
		} else {
			d = dev
			haveDevice = true
		}
	}

	addCheck(&checks, &failedNames, "config", configOK, detail.String(), true)

	if !haveDevice {
		for _, name := range []string{"reach", "auth", "version", "switch", "capture", "hid", "msd"} {
			addCheck(&checks, &failedNames, name, false, "skipped: no usable device config", true)
		}
		return checks, doctorErr(failedNames)
	}

	c := kvmd.New(d, timeout)

	if reachErr := c.Reach(ctx); reachErr != nil {
		addCheck(&checks, &failedNames, "reach", false, reachErr.Error(), true)
	} else {
		addCheck(&checks, &failedNames, "reach", true, fmt.Sprintf("%s answered over HTTP", d.URL), true)
	}

	if _, authErr := c.Info(ctx); authErr != nil {
		addCheck(&checks, &failedNames, "auth", false, authErr.Error(), true)
	} else {
		addCheck(&checks, &failedNames, "auth", true, fmt.Sprintf("authenticated as %s", d.User), true)
	}

	ver, verErr := c.Version(ctx)
	if verErr != nil {
		addCheck(&checks, &failedNames, "version", false, verErr.Error(), true)
	} else {
		addCheck(&checks, &failedNames, "version", true, fmt.Sprintf("%s %s", ver.Model, ver.Version), true)
	}

	sw, swErr := c.Switch(ctx)
	switch {
	case swErr != nil:
		addCheck(&checks, &failedNames, "switch", false, swErr.Error(), true)
	case len(sw.Ports) != 4:
		addCheck(&checks, &failedNames, "switch", false, fmt.Sprintf("reports %d ports, want 4", len(sw.Ports)), true)
	default:
		addCheck(&checks, &failedNames, "switch", true, fmt.Sprintf("4 ports, active %s", sw.ActiveID), true)
	}

	streamer, streamerErr := c.Streamer(ctx)
	switch {
	case streamerErr != nil:
		addCheck(&checks, &failedNames, "capture", false, streamerErr.Error(), true)
	default:
		if verr := streamer.Valid(); verr != nil {
			addCheck(&checks, &failedNames, "capture", false, verr.Error(), true)
		} else {
			addCheck(&checks, &failedNames, "capture", true, streamer.RealResolution, true)
		}
	}

	hid, hidErr := c.HID(ctx)
	switch {
	case hidErr != nil:
		addCheck(&checks, &failedNames, "hid", false, hidErr.Error(), true)
	case !hid.Online:
		addCheck(&checks, &failedNames, "hid", false, "hid reports online=false", true)
	default:
		addCheck(&checks, &failedNames, "hid", true, "online", true)
	}

	msd, msdErr := c.MSD(ctx)
	switch {
	case msdErr != nil:
		addCheck(&checks, &failedNames, "msd", false, msdErr.Error(), true)
	case !msd.Enabled:
		addCheck(&checks, &failedNames, "msd", false, "msd reports enabled=false", true)
	default:
		addCheck(&checks, &failedNames, "msd", true, "enabled", true)
	}

	// Informational only, from here down: state, not a pass/fail condition.
	// msd attach turns start_cdrom on by itself, and mouse nudge only works
	// with the mouse output in relative mode.
	otg, otgErr := c.OTGFunctions(ctx)
	switch {
	case otgErr != nil:
		addCheck(&checks, &failedNames, "otg", false, otgErr.Error(), false)
	case otg.StartCDROM:
		addCheck(&checks, &failedNames, "otg", true, "start_cdrom on: msd images are visible to the host", false)
	default:
		addCheck(&checks, &failedNames, "otg", true, "start_cdrom off: msd attach turns this on", false)
	}

	switch {
	case hidErr != nil:
		addCheck(&checks, &failedNames, "mouse", false, hidErr.Error(), false)
	case hid.MouseAbsolute:
		addCheck(&checks, &failedNames, "mouse", true, "absolute: mouse nudge refuses, use mouse move or switch the mouse output to usb_rel/usb_hybrid", false)
	default:
		addCheck(&checks, &failedNames, "mouse", true, "relative: mouse nudge available", false)
	}

	return checks, doctorErr(failedNames)
}

// writeDoctorChecks renders checks as one line per check, in order.
func writeDoctorChecks(w io.Writer, checks []doctorCheck) {
	for _, c := range checks {
		status := "ok"
		if !c.OK {
			status = "FAIL"
		}
		fmt.Fprintf(w, "%-8s %-4s %s\n", c.Name, status, c.Detail)
	}
}

func newDoctorCmd(g *globals) *cobra.Command {
	cmd := &cobra.Command{
		Use:           "doctor",
		Short:         "Check config, reachability, auth and device capabilities",
		Args:          noArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		checks, err := doDoctor(cmd.Context(), g.configPath, g.device, g.timeout)
		if err != nil {
			if g.output == "text" {
				writeDoctorChecks(cmd.OutOrStdout(), checks)
			}
			return &batchError{result: checks, err: err}
		}
		return render(cmd.OutOrStdout(), g.output, checks, func(w io.Writer) {
			writeDoctorChecks(w, checks)
		})
	}
	return cmd
}
