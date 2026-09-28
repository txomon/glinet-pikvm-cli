package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"testing"

	"github.com/txomon/glinet-pikvm-cli/internal/config"
)

func TestRenderJSON(t *testing.T) {
	var b bytes.Buffer
	if err := render(&b, "json", map[string]int{"port": 4}, nil); err != nil {
		t.Fatal(err)
	}
	var env struct {
		OK     bool           `json:"ok"`
		Result map[string]int `json:"result"`
	}
	if err := json.Unmarshal(b.Bytes(), &env); err != nil || !env.OK || env.Result["port"] != 4 {
		t.Fatalf("got %s", b.String())
	}
}

func TestRenderText(t *testing.T) {
	var b bytes.Buffer
	_ = render(&b, "text", nil, func(w io.Writer) { fmt.Fprint(w, "port 4\n") })
	if b.String() != "port 4\n" {
		t.Fatalf("got %q", b.String())
	}
}

func TestRenderErrorCodes(t *testing.T) {
	cases := []struct {
		err  error
		code int
		kind string
	}{
		{UsageError{Msg: "bad"}, ExitUsage, "usage"},
		{fmt.Errorf("%w: x", config.ErrConfig), ExitConfig, "config"},
		{errors.New("boom"), ExitDevice, "device"},
	}
	for _, c := range cases {
		var b bytes.Buffer
		code := renderError(&b, "json", c.err)
		var env struct {
			OK    bool `json:"ok"`
			Error struct {
				Kind    string `json:"kind"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(b.Bytes(), &env); err != nil {
			t.Fatal(err, b.String())
		}
		if code != c.code || env.OK || env.Error.Kind != c.kind || env.Error.Message == "" {
			t.Fatalf("%v: code %d env %+v", c.err, code, env)
		}
	}
}
