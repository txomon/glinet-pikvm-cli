package kvmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

// MSDImage is one image in the device's mass-storage image library.
type MSDImage struct {
	Size     int64
	Complete bool
}

// MSDDrive is the virtual drive's current state. Image is the selected
// image's name, or "" when none is selected.
type MSDDrive struct {
	CDROM     bool
	Connected bool
	RW        bool
	Image     string
}

// MSDState is the parsed shape of GET /msd.
type MSDState struct {
	Enabled bool
	Online  bool
	Busy    bool
	Drive   MSDDrive
	Images  map[string]MSDImage
	Free    int64
}

// msdImageResponse is one entry of storage.images in GET /msd.
type msdImageResponse struct {
	Size     int64 `json:"size"`
	Complete bool  `json:"complete"`
}

// msdResponse mirrors the fields of GET /msd this client uses. drive.image
// is JSON null when no image is selected, or an object naming the selected
// image otherwise, so it is decoded separately by decodeDriveImage.
type msdResponse struct {
	Enabled bool `json:"enabled"`
	Online  bool `json:"online"`
	Busy    bool `json:"busy"`
	Drive   struct {
		CDROM     bool            `json:"cdrom"`
		Connected bool            `json:"connected"`
		RW        bool            `json:"rw"`
		Image     json.RawMessage `json:"image"`
	} `json:"drive"`
	Storage struct {
		Images map[string]msdImageResponse `json:"images"`
		Parts  map[string]struct {
			Free int64 `json:"free"`
		} `json:"parts"`
	} `json:"storage"`
}

// decodeDriveImage decodes drive.image from GET /msd: null means no image
// is selected and decodes to "", an object means an image is selected and
// decodes to its name.
func decodeDriveImage(raw json.RawMessage) (string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", nil
	}
	var obj struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(raw, &obj); err != nil {
		return "", fmt.Errorf("kvmd: decode drive.image: %w", err)
	}
	return obj.Name, nil
}

// MSD fetches the current mass-storage state.
func (c *Client) MSD(ctx context.Context) (MSDState, error) {
	var raw msdResponse
	if err := c.getJSON(ctx, "/msd", nil, &raw); err != nil {
		return MSDState{}, err
	}

	image, err := decodeDriveImage(raw.Drive.Image)
	if err != nil {
		return MSDState{}, err
	}

	st := MSDState{
		Enabled: raw.Enabled,
		Online:  raw.Online,
		Busy:    raw.Busy,
		Drive: MSDDrive{
			CDROM:     raw.Drive.CDROM,
			Connected: raw.Drive.Connected,
			RW:        raw.Drive.RW,
			Image:     image,
		},
		Images: make(map[string]MSDImage, len(raw.Storage.Images)),
	}
	for name, img := range raw.Storage.Images {
		st.Images[name] = MSDImage{Size: img.Size, Complete: img.Complete}
	}
	if part, ok := raw.Storage.Parts[""]; ok {
		st.Free = part.Free
	}
	return st, nil
}

// MSDUpload streams r as the named image's content to the device. size must
// be r's exact length: it becomes the request's Content-Length so the body
// is never buffered whole in memory. Uploads can run far longer than a
// normal API call, so this goes through the client's no-timeout HTTP
// client; ctx still bounds it.
func (c *Client) MSDUpload(ctx context.Context, name string, r io.Reader, size int64) error {
	q := url.Values{"image": {name}}
	resp, err := c.doWithClient(ctx, c.uploadHTTP, http.MethodPost, "/msd/write", q, r, "application/octet-stream", size)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return decodeEnvelope("/msd/write", resp, nil)
}

// MSDSetParams selects image as the drive's image and sets its cdrom and rw
// flags. image must name an image already in the device's storage; all
// three query params are always sent, matching what the fake and the real
// device expect. The drive must not be connected.
func (c *Client) MSDSetParams(ctx context.Context, image string, cdrom, rw bool) error {
	q := url.Values{
		"image": {image},
		"cdrom": {msdBool(cdrom)},
		"rw":    {msdBool(rw)},
	}
	return c.post(ctx, "/msd/set_params", q, nil, "", nil)
}

// MSDSetConnected attaches (connected=true) or detaches (connected=false)
// the virtual drive from the host.
func (c *Client) MSDSetConnected(ctx context.Context, connected bool) error {
	q := url.Values{"connected": {msdBool(connected)}}
	return c.post(ctx, "/msd/set_connected", q, nil, "", nil)
}

// MSDRemove deletes image from the device's image library. It fails while
// image is the connected drive's image.
func (c *Client) MSDRemove(ctx context.Context, image string) error {
	q := url.Values{"image": {image}}
	return c.post(ctx, "/msd/remove", q, nil, "", nil)
}

// msdBool renders a bool as the MSD API's 1/0 query value.
func msdBool(b bool) string {
	if b {
		return "1"
	}
	return "0"
}
