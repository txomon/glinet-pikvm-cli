package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/spf13/cobra"
	"github.com/txomon/glinet-pikvm-cli/internal/kvmd"
)

// otgApplyTimeout bounds how long ensureStartCDROM waits for the device to
// finish applying an OTG start_cdrom change.
const otgApplyTimeout = 15 * time.Second

// msdConfirmTimeout bounds how long msd upload/remove wait for the device's
// storage listing to catch up after a write or removal: observed live, kvmd
// can keep reporting a just-removed image (or omit a just-written one) for a
// few seconds after the call that changed it already succeeded. A package
// variable so a test can shorten it to exercise the timeout error quickly.
var msdConfirmTimeout = 10 * time.Second

// msdDriveResult is the drive stanza of glkvm msd's result.
type msdDriveResult struct {
	Image     string `json:"image"`
	Connected bool   `json:"connected"`
	CDROM     bool   `json:"cdrom"`
	RW        bool   `json:"rw"`
}

// msdImageResult is one entry of glkvm msd's images list.
type msdImageResult struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
}

// msdUSBResult is glkvm msd's "usb" stanza: the OTG gadget state relevant to
// the virtual drive. On the Comet X, kvmd's selected image is only served to
// the host while start_cdrom is on.
type msdUSBResult struct {
	StartCDROM bool `json:"start_cdrom"`
	Ready      bool `json:"ready"`
}

// msdResult is the result of glkvm msd, and of attach/detach after they act.
type msdResult struct {
	Enabled bool             `json:"enabled"`
	Online  bool             `json:"online"`
	Drive   msdDriveResult   `json:"drive"`
	Images  []msdImageResult `json:"images"`
	Free    int64            `json:"free"`
	USB     msdUSBResult     `json:"usb"`
}

// msdUploadResult is the result of glkvm msd upload.
type msdUploadResult struct {
	Name     string `json:"name"`
	Size     int64  `json:"size"`
	Replaced bool   `json:"replaced"`
}

// msdAttachResult is the result of glkvm msd attach. Changed is false when
// the requested image was already connected with the same cdrom/rw flags,
// in which case attach touched nothing on the device.
type msdAttachResult struct {
	msdResult
	Changed bool `json:"changed"`
}

// msdRemoveResult is the result of glkvm msd remove.
type msdRemoveResult struct {
	Removed string `json:"removed"`
}

// buildMSDResult reads the current MSD and OTG state into one msdResult. It
// is a plain function, not a cobra RunE, so other callers (like a future
// batch runner) can use it without cobra.
func buildMSDResult(ctx context.Context, c *kvmd.Client) (msdResult, error) {
	st, err := c.MSD(ctx)
	if err != nil {
		return msdResult{}, err
	}
	otg, err := c.OTGFunctions(ctx)
	if err != nil {
		return msdResult{}, err
	}

	images := make([]msdImageResult, 0, len(st.Images))
	for name, img := range st.Images {
		images = append(images, msdImageResult{Name: name, Size: img.Size})
	}
	sort.Slice(images, func(i, j int) bool { return images[i].Name < images[j].Name })

	return msdResult{
		Enabled: st.Enabled,
		Online:  st.Online,
		Drive: msdDriveResult{
			Image:     st.Drive.Image,
			Connected: st.Drive.Connected,
			CDROM:     st.Drive.CDROM,
			RW:        st.Drive.RW,
		},
		Images: images,
		Free:   st.Free,
		USB: msdUSBResult{
			StartCDROM: otg.StartCDROM,
			Ready:      otg.Ready,
		},
	}, nil
}

// doMSDStatus is buildMSDResult under the name the other "glkvm <noun>"
// status commands use (doStatus, doPort, doEdidShow).
func doMSDStatus(ctx context.Context, c *kvmd.Client) (msdResult, error) {
	return buildMSDResult(ctx, c)
}

// ensureStartCDROM makes the device's start_cdrom OTG function match on,
// issuing the change only when needed (so a caller already in the desired
// state, with no stale error, triggers no POST), then polls every
// waitPollInterval until the device reports start_cdrom equal to on, ready
// and not mid-apply: ready-and-not-applying alone is not enough, since a
// transient poll can report both while start_cdrom itself has not yet
// caught up to the requested value. It errors when the apply reports a
// non-empty apply_error, or the device does not become ready within
// otgApplyTimeout. Toggling this rebuilds the device's USB gadget, which
// briefly drops the host's keyboard and mouse.
//
// A POST is issued both when the current state does not already match on
// and when apply_error is non-empty even though it does: otherwise, once an
// apply fails, start_cdrom is left sitting at the requested value with its
// error still attached, so a caller that already matches on would read that
// stale error back forever, without ever retrying.
func ensureStartCDROM(ctx context.Context, c *kvmd.Client, on bool) error {
	cur, err := c.OTGFunctions(ctx)
	if err != nil {
		return err
	}
	if cur.StartCDROM == on && cur.Ready && !cur.Applying && cur.ApplyError == "" {
		return nil
	}

	if cur.StartCDROM != on || cur.ApplyError != "" {
		if err := c.SetOTGStartCDROM(ctx, on); err != nil {
			return err
		}
	}

	applyCtx, cancel := context.WithTimeout(ctx, otgApplyTimeout)
	defer cancel()

	var last kvmd.OTGFunctions
	waitErr := waitFor(applyCtx, waitPollInterval, func() (bool, error) {
		st, err := c.OTGFunctions(applyCtx)
		if err != nil {
			return false, err
		}
		last = st
		return st.StartCDROM == on && st.Ready && !st.Applying, nil
	})
	if waitErr != nil {
		return fmt.Errorf("otg start_cdrom did not become ready within %s: %w", otgApplyTimeout, waitErr)
	}
	if last.ApplyError != "" {
		return fmt.Errorf("otg apply failed: %s", last.ApplyError)
	}
	return nil
}

// doMSDAttach selects name as the drive's image (cdrom by default, flash
// with --flash, read-write only with --flash --rw) and connects it, after
// making sure start_cdrom is on: without it the host sees no disk at all,
// regardless of drive.connected. It reads the MSD state first and refuses,
// before touching anything, when name is not in storage or not yet
// complete: attaching an unknown or incomplete image otherwise disconnects
// whatever was already attached and toggles start_cdrom before
// set_params's own rejection ever arrives, which is disruptive for a call
// that was always going to fail. When name is already connected with the
// same cdrom/rw flags requested, it reports Changed: false and does not
// touch the drive or the USB gadget at all. Otherwise it disconnects first
// when the drive is already connected (set_params rejects a connected
// drive), ensures start_cdrom, applies the new params, connects, then reads
// back the drive and msd.online and errors if either does not match what
// was requested. It is a plain function, not a cobra RunE, so other callers
// can use it without cobra.
func doMSDAttach(ctx context.Context, c *kvmd.Client, name string, flash, rw bool) (msdAttachResult, error) {
	if rw && !flash {
		return msdAttachResult{}, usagef("--rw requires --flash")
	}

	st, err := c.MSD(ctx)
	if err != nil {
		return msdAttachResult{}, err
	}

	img, exists := st.Images[name]
	if !exists || !img.Complete {
		return msdAttachResult{}, usagef("image %q is not a complete image in the device's storage", name)
	}

	cdrom := !flash
	if st.Drive.Connected && st.Drive.Image == name && st.Drive.CDROM == cdrom && st.Drive.RW == rw {
		result, err := buildMSDResult(ctx, c)
		if err != nil {
			return msdAttachResult{}, err
		}
		return msdAttachResult{msdResult: result, Changed: false}, nil
	}

	if st.Drive.Connected {
		if err := c.MSDSetConnected(ctx, false); err != nil {
			return msdAttachResult{}, err
		}
	}

	if err := ensureStartCDROM(ctx, c, true); err != nil {
		return msdAttachResult{}, err
	}

	if err := c.MSDSetParams(ctx, name, cdrom, rw); err != nil {
		return msdAttachResult{}, err
	}
	if err := c.MSDSetConnected(ctx, true); err != nil {
		return msdAttachResult{}, err
	}

	result, err := buildMSDResult(ctx, c)
	if err != nil {
		return msdAttachResult{}, err
	}
	if !result.Drive.Connected || result.Drive.Image != name || result.Drive.CDROM != cdrom || result.Drive.RW != rw {
		return msdAttachResult{}, fmt.Errorf("drive state after attach does not match: %+v", result.Drive)
	}
	if !result.Online {
		return msdAttachResult{}, fmt.Errorf("msd reports online=false after attach")
	}

	return msdAttachResult{msdResult: result, Changed: true}, nil
}

// doMSDDetach disconnects the drive, reads back to confirm it took, then
// turns start_cdrom off (unless keepUSB) so the host stops seeing a disk at
// all. It reads the MSD state first and only calls set_connected=0 when the
// drive is actually connected: the device rejects that call with
// MsdDisconnectedError when it is already disconnected (verified live), and
// without this check that error would also skip the start_cdrom turn-off
// below. It is a plain function, not a cobra RunE, so other callers can use
// it without cobra.
func doMSDDetach(ctx context.Context, c *kvmd.Client, keepUSB bool) (msdResult, error) {
	st, err := c.MSD(ctx)
	if err != nil {
		return msdResult{}, err
	}

	if st.Drive.Connected {
		if err := c.MSDSetConnected(ctx, false); err != nil {
			return msdResult{}, err
		}

		st, err = c.MSD(ctx)
		if err != nil {
			return msdResult{}, err
		}
		if st.Drive.Connected {
			return msdResult{}, fmt.Errorf("drive still reports connected after detach")
		}
	}

	if !keepUSB {
		if err := ensureStartCDROM(ctx, c, false); err != nil {
			return msdResult{}, err
		}
	}

	return buildMSDResult(ctx, c)
}

// progressReport is called with the cumulative percent complete each time it
// crosses a new 5% step.
type progressReport func(percent int)

// progressReader wraps r, calling report every time cumulative bytes read
// crosses a new 5% step of total.
type progressReader struct {
	r        io.Reader
	total    int64
	read     int64
	lastStep int
	report   progressReport
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	p.read += int64(n)
	if p.total > 0 {
		step := int(p.read * 100 / p.total / 5)
		if step > p.lastStep {
			p.lastStep = step
			p.report(step * 5)
		}
	}
	return n, err
}

// doMSDUpload uploads the file at path as name (default: path's base name)
// into the device's image storage. It refuses a name collision unless
// replace is set. The local file is stat'd, and free space checked, before
// anything on the device is touched: with --replace, the space check counts
// the old image's own size as space that will be freed (size <= st.Free +
// oldImage.Size), since the old image is not removed until after that check
// passes. Without this order, a typo'd local path used to delete the
// device's old image via removeAndConfirm before os.Stat ever ran, and a
// replacement that only fits once the old image is gone used to be refused
// against the device's pre-removal free space. Only once the file is
// confirmed to exist and fit is the old image (when replacing) removed
// through removeAndConfirm (itself refused while that image is the
// connected one). The removal must be confirmed, not just requested, before
// the write: live, uploading over an existing name right after asking the
// device to remove it failed with MsdImageExistsError, because kvmd's
// storage view still listed the old image for about a second after the
// remove call had already succeeded. report, when non-nil, is called with
// the cumulative percent complete every 5%. After the upload completes, it
// polls the device's storage listing every waitPollInterval until the image
// is listed complete with the uploaded size, erroring if that never happens
// within msdConfirmTimeout: kvmd's storage view has been observed to lag a
// write by a few seconds. It is a plain function, not a cobra RunE, so
// other callers can use it without cobra.
func doMSDUpload(ctx context.Context, c *kvmd.Client, path, name string, replace bool, report progressReport) (msdUploadResult, error) {
	if name == "" {
		name = filepath.Base(path)
	}

	st, err := c.MSD(ctx)
	if err != nil {
		return msdUploadResult{}, err
	}

	replacing := false
	var oldImage kvmd.MSDImage
	if img, exists := st.Images[name]; exists {
		if !replace {
			return msdUploadResult{}, usagef("image %q already exists, pass --replace to replace it", name)
		}
		if st.Drive.Connected && st.Drive.Image == name {
			return msdUploadResult{}, usagef("cannot replace %q: it is the connected image, detach first", name)
		}
		replacing = true
		oldImage = img
	}

	info, err := os.Stat(path)
	if err != nil {
		return msdUploadResult{}, usagef("stat %s: %v", path, err)
	}
	size := info.Size()

	freeBudget := st.Free
	if replacing {
		freeBudget += oldImage.Size
	}
	if size > freeBudget {
		if replacing {
			return msdUploadResult{}, usagef("not enough free space: %s is %d bytes, device has %d free plus %d from the replaced image (%d total)", path, size, st.Free, oldImage.Size, freeBudget)
		}
		return msdUploadResult{}, usagef("not enough free space: %s is %d bytes, device has %d free", path, size, st.Free)
	}

	replaced := false
	if replacing {
		if err := removeAndConfirm(ctx, c, name); err != nil {
			return msdUploadResult{}, err
		}
		replaced = true
	}

	f, err := os.Open(path)
	if err != nil {
		return msdUploadResult{}, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	var r io.Reader = f
	if report != nil {
		r = &progressReader{r: f, total: size, report: report}
	}

	if err := c.MSDUpload(ctx, name, r, size); err != nil {
		return msdUploadResult{}, err
	}

	confirmCtx, cancel := context.WithTimeout(ctx, msdConfirmTimeout)
	defer cancel()
	waitErr := waitFor(confirmCtx, waitPollInterval, func() (bool, error) {
		st, err := c.MSD(confirmCtx)
		if err != nil {
			return false, err
		}
		img, ok := st.Images[name]
		return ok && img.Complete && img.Size == size, nil
	})
	if waitErr != nil {
		return msdUploadResult{}, fmt.Errorf("uploaded image %q did not appear complete in storage within %s: %w", name, msdConfirmTimeout, waitErr)
	}

	return msdUploadResult{Name: name, Size: size, Replaced: replaced}, nil
}

// removeAndConfirm deletes name from the device's image storage, then polls
// the storage listing every waitPollInterval until the image is gone,
// erroring if it never disappears within msdConfirmTimeout: kvmd's storage
// listing has been observed to keep reporting a just-removed image for a
// few seconds after the remove call already succeeded. Any caller-side
// usage checks (such as refusing to remove the connected image) are the
// caller's job; this only wraps the device call and the confirm-wait shared
// by doMSDRemove and doMSDUpload's --replace path (which must confirm the
// old image is really gone before writing the new one under the same name,
// or the write can fail live with MsdImageExistsError against the same lag).
func removeAndConfirm(ctx context.Context, c *kvmd.Client, name string) error {
	if err := c.MSDRemove(ctx, name); err != nil {
		return err
	}

	confirmCtx, cancel := context.WithTimeout(ctx, msdConfirmTimeout)
	defer cancel()
	waitErr := waitFor(confirmCtx, waitPollInterval, func() (bool, error) {
		st, err := c.MSD(confirmCtx)
		if err != nil {
			return false, err
		}
		_, present := st.Images[name]
		return !present, nil
	})
	if waitErr != nil {
		return fmt.Errorf("image %q still listed in storage %s after removal: %w", name, msdConfirmTimeout, waitErr)
	}
	return nil
}

// doMSDRemove deletes name from the device's image storage, refusing when
// it is the connected image, then confirms the removal landed (see
// removeAndConfirm). It is a plain function, not a cobra RunE, so other
// callers can use it without cobra.
func doMSDRemove(ctx context.Context, c *kvmd.Client, name string) (msdRemoveResult, error) {
	st, err := c.MSD(ctx)
	if err != nil {
		return msdRemoveResult{}, err
	}
	if st.Drive.Connected && st.Drive.Image == name {
		return msdRemoveResult{}, usagef("%q is the connected image; detach first", name)
	}

	if err := removeAndConfirm(ctx, c, name); err != nil {
		return msdRemoveResult{}, err
	}

	return msdRemoveResult{Removed: name}, nil
}

func newMSDCmd(g *globals) *cobra.Command {
	cmd := &cobra.Command{
		Use:           "msd",
		Short:         "Show or manage the virtual mass-storage drive",
		Args:          noArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		c, _, err := g.client(cmd.ErrOrStderr())
		if err != nil {
			return err
		}
		result, err := doMSDStatus(cmd.Context(), c)
		if err != nil {
			return err
		}
		return render(cmd.OutOrStdout(), g.output, result, func(w io.Writer) {
			fmt.Fprintf(w, "enabled: %v online: %v\n", result.Enabled, result.Online)
			fmt.Fprintf(w, "drive: image=%q connected=%v cdrom=%v rw=%v\n", result.Drive.Image, result.Drive.Connected, result.Drive.CDROM, result.Drive.RW)
			fmt.Fprintf(w, "usb: start_cdrom=%v ready=%v\n", result.USB.StartCDROM, result.USB.Ready)
			fmt.Fprintf(w, "free: %d bytes\n", result.Free)
			fmt.Fprintln(w, "images:")
			for _, img := range result.Images {
				fmt.Fprintf(w, "  %s (%d bytes)\n", img.Name, img.Size)
			}
		})
	}

	cmd.AddCommand(newMSDUploadCmd(g))
	cmd.AddCommand(newMSDAttachCmd(g))
	cmd.AddCommand(newMSDDetachCmd(g))
	cmd.AddCommand(newMSDRemoveCmd(g))
	return cmd
}

func newMSDUploadCmd(g *globals) *cobra.Command {
	var name string
	var replace bool
	cmd := &cobra.Command{
		Use:           "upload PATH",
		Short:         "Upload a disk image into the device's MSD storage",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	cmd.Flags().StringVar(&name, "name", "", "image name in storage (default: PATH's base name)")
	cmd.Flags().BoolVar(&replace, "replace", false, "remove an existing image with the same name first")
	cmd.Args = func(cmd *cobra.Command, args []string) error {
		if len(args) != 1 {
			return usagef("msd upload requires exactly one file path, got %d", len(args))
		}
		return nil
	}
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		c, _, err := g.client(cmd.ErrOrStderr())
		if err != nil {
			return err
		}
		path := args[0]

		var report progressReport
		stderr := cmd.ErrOrStderr()
		if g.output == "text" && isTerminal(stderr) {
			report = func(percent int) {
				fmt.Fprintf(stderr, "upload %s: %d%%\n", path, percent)
			}
		}

		result, err := doMSDUpload(cmd.Context(), c, path, name, replace, report)
		if err != nil {
			return err
		}
		return render(cmd.OutOrStdout(), g.output, result, func(w io.Writer) {
			fmt.Fprintf(w, "uploaded %s as %q (%d bytes)\n", path, result.Name, result.Size)
		})
	}
	return cmd
}

func newMSDAttachCmd(g *globals) *cobra.Command {
	var flash, rw bool
	cmd := &cobra.Command{
		Use:           "attach NAME",
		Short:         "Select and connect an image as the virtual drive",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	cmd.Flags().BoolVar(&flash, "flash", false, "attach as a writable flash drive instead of a read-only CD-ROM")
	cmd.Flags().BoolVar(&rw, "rw", false, "attach the flash drive read-write (requires --flash)")
	cmd.Args = func(cmd *cobra.Command, args []string) error {
		if len(args) != 1 {
			return usagef("msd attach requires exactly one image name, got %d", len(args))
		}
		return nil
	}
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		c, _, err := g.client(cmd.ErrOrStderr())
		if err != nil {
			return err
		}
		result, err := doMSDAttach(cmd.Context(), c, args[0], flash, rw)
		if err != nil {
			return err
		}
		return render(cmd.OutOrStdout(), g.output, result, func(w io.Writer) {
			verb := "attached"
			if !result.Changed {
				verb = "already attached"
			}
			fmt.Fprintf(w, "%s %s (cdrom=%v rw=%v)\n", verb, result.Drive.Image, result.Drive.CDROM, result.Drive.RW)
		})
	}
	return cmd
}

func newMSDDetachCmd(g *globals) *cobra.Command {
	var keepUSB bool
	cmd := &cobra.Command{
		Use:           "detach",
		Short:         "Disconnect the virtual drive",
		Args:          noArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	cmd.Flags().BoolVar(&keepUSB, "keep-usb", false, "leave the start_cdrom USB function on after detaching")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		c, _, err := g.client(cmd.ErrOrStderr())
		if err != nil {
			return err
		}
		result, err := doMSDDetach(cmd.Context(), c, keepUSB)
		if err != nil {
			return err
		}
		return render(cmd.OutOrStdout(), g.output, result, func(w io.Writer) {
			fmt.Fprintf(w, "detached (usb start_cdrom=%v)\n", result.USB.StartCDROM)
		})
	}
	return cmd
}

func newMSDRemoveCmd(g *globals) *cobra.Command {
	cmd := &cobra.Command{
		Use:           "remove NAME",
		Short:         "Delete an image from the device's MSD storage",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	cmd.Args = func(cmd *cobra.Command, args []string) error {
		if len(args) != 1 {
			return usagef("msd remove requires exactly one image name, got %d", len(args))
		}
		return nil
	}
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		c, _, err := g.client(cmd.ErrOrStderr())
		if err != nil {
			return err
		}
		result, err := doMSDRemove(cmd.Context(), c, args[0])
		if err != nil {
			return err
		}
		return render(cmd.OutOrStdout(), g.output, result, func(w io.Writer) {
			fmt.Fprintf(w, "removed %s\n", result.Removed)
		})
	}
	return cmd
}
