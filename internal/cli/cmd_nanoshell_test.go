package cli

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/YoooClaw/cli/internal/clictx"
	"github.com/YoooClaw/cli/internal/nanoshell"
	"github.com/YoooClaw/cli/internal/paths"
)

func TestNanoshellPublishCommand(t *testing.T) {
	home := t.TempDir()
	t.Setenv("YOOOCLAW_HOME", home)
	os.WriteFile(filepath.Join(home, "credentials.json"), []byte(`{"apiKeys":[{"label":"phone-a","key":"test-key","default":true}]}`), 0600)
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	w, _ := z.Create("game.nsp/manifest.json")
	w.Write([]byte(`{"id":"com.game","name":"Game","version":1,"entry":"app.wasm"}`))
	w, _ = z.Create("game.nsp/app.wasm")
	w.Write([]byte{0, 97, 115, 109, 1, 0, 0, 0})
	z.Close()
	p := filepath.Join(t.TempDir(), "game.zip")
	os.WriteFile(p, b.Bytes(), 0600)
	cmd, _, e := newNanoshellCmd().Find([]string{"publish"})
	if e != nil {
		t.Fatal(e)
	}
	cmd.Flags().Set("package", p)
	cmd.Flags().Set("client", "missing")
	ctx := &clictx.Context{Profile: "test", Paths: paths.For("test")}
	if _, e = nanoshellPublish(ctx, cmd, nil); e == nil {
		t.Fatal("unknown client accepted")
	}
	cmd.Flags().Set("client", "phone-a")
	value, e := nanoshellPublish(ctx, cmd, nil)
	if e != nil {
		t.Fatal(e)
	}
	item := value.(nanoshell.PublishResult)
	if item.AppID != "com.game" || item.Duplicated {
		t.Fatal(item)
	}
	if filepath.Dir(ctx.Paths.Nanoshell) != filepath.Dir(ctx.Paths.Notifications) || filepath.Dir(ctx.Paths.Nanoshell) != filepath.Dir(ctx.Paths.Recordings) {
		t.Fatal("storage must be sibling")
	}
	other, e := (nanoshell.Store{Root: paths.For("other").Nanoshell}).List("phone-a", 20, "")
	if e != nil || len(other.Items) != 0 {
		t.Fatal("profile isolation", other, e)
	}
	if _, e = (nanoshell.Store{Root: ctx.Paths.Nanoshell}).Download("phone-a", item.PackageID); e != nil {
		t.Fatal(e)
	}
}
