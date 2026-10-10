package nanoshell

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func testZIP(t *testing.T, id string, version int, extra string) []byte {
	t.Helper()
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	m, _ := json.Marshal(Manifest{id, "Game" + extra, version, "app.wasm"})
	for _, entry := range []struct {
		name string
		data []byte
	}{{"game.nsp/manifest.json", m}, {"game.nsp/app.wasm", []byte{0, 97, 115, 109, 1, 0, 0, 0}}} {
		w, e := z.Create(entry.name)
		if e != nil {
			t.Fatal(e)
		}
		w.Write(entry.data)
	}
	if e := z.Close(); e != nil {
		t.Fatal(e)
	}
	return b.Bytes()
}
func put(t *testing.T, b []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "game.zip")
	if e := os.WriteFile(p, b, 0600); e != nil {
		t.Fatal(e)
	}
	return p
}
func TestPublishListDownload(t *testing.T) {
	s := Store{filepath.Join(t.TempDir(), "nanoshell")}
	empty, e := s.List("phone-a", 20, "")
	if e != nil || empty.Items == nil || len(empty.Items) != 0 {
		t.Fatal(empty, e)
	}
	b := testZIP(t, "com.game", 1, "")
	p := put(t, b)
	first, e := s.Publish(p, "phone-a")
	if e != nil {
		t.Fatal(e)
	}
	again, e := s.Publish(p, "phone-a")
	if e != nil || !again.Duplicated || first.Item != again.Item {
		t.Fatal(again, e)
	}
	if _, e = s.Publish(put(t, testZIP(t, "com.game", 1, "changed")), "phone-a"); Code(e) != "VERSION_CONFLICT" {
		t.Fatal(e)
	}
	if _, e = s.Publish(put(t, testZIP(t, "com.game", 2, "")), "phone-a"); e != nil {
		t.Fatal(e)
	}
	list, e := s.List("phone-a", 20, "")
	if e != nil || len(list.Items) != 1 || list.Items[0].Version != 2 {
		t.Fatal(list, e)
	}
	d, e := s.Download("phone-a", first.PackageID)
	if e != nil {
		t.Fatal(e)
	}
	got, e := base64.StdEncoding.DecodeString(d.Data)
	if e != nil || !bytes.Equal(got, b) || digest(got) != d.PackageID || d.Size != len(b) {
		t.Fatal(d, e)
	}
	for _, scope := range []string{"phone-b", "default", "legacy"} {
		list, e = s.List(scope, 20, "")
		if e != nil || len(list.Items) != 0 {
			t.Fatal(list, e)
		}
		if _, e = s.Download(scope, first.PackageID); Code(e) != "PACKAGE_NOT_FOUND" {
			t.Fatal(e)
		}
	}
	if _, e = s.Publish(p, "phone-b"); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Download("phone-b", first.PackageID); e != nil {
		t.Fatal(e)
	}
	target := filepath.Join(s.Root, recordID("phone-a", "com.game", 1), "package.zip")
	os.WriteFile(target, []byte("corrupt"), 0600)
	if _, e = s.Download("phone-a", first.PackageID); Code(e) != "PACKAGE_CORRUPTED" {
		t.Fatal(e)
	}
}
func TestPaginationAndConcurrentPublish(t *testing.T) {
	s := Store{t.TempDir()}
	p := put(t, testZIP(t, "com.game", 1, ""))
	var wg sync.WaitGroup
	errs := make(chan error, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, e := s.Publish(p, "a"); errs <- e }()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	for i := 0; i < 4; i++ {
		if _, e := s.Publish(put(t, testZIP(t, fmt.Sprintf("com.game%d", i), 1, "")), "a"); e != nil {
			t.Fatal(e)
		}
	}
	token := ""
	seen := map[string]bool{}
	for {
		r, e := s.List("a", 2, token)
		if e != nil {
			t.Fatal(e)
		}
		for _, v := range r.Items {
			if seen[v.AppID] {
				t.Fatal("duplicate page item")
			}
			seen[v.AppID] = true
		}
		token = r.NextCursor
		if token == "" {
			break
		}
		if _, e := s.List("b", 2, token); Code(e) != "INVALID_CURSOR" {
			t.Fatal(e)
		}
	}
	if len(seen) != 5 {
		t.Fatal(seen)
	}
	if _, e := s.List("a", 0, ""); Code(e) != "INVALID_PARAMS" {
		t.Fatal(e)
	}
	if _, e := s.List("a", 20, "bad"); Code(e) != "INVALID_CURSOR" {
		t.Fatal(e)
	}
	if _, e := s.Download("a", "../../x"); Code(e) != "INVALID_PARAMS" {
		t.Fatal(e)
	}
}
func TestPackageValidation(t *testing.T) {
	for _, name := range []string{"../outside", "game.nsp/../x", "/game.nsp/app.wasm", "game.nsp\\app.wasm"} {
		var b bytes.Buffer
		z := zip.NewWriter(&b)
		for _, n := range []string{name, "game.nsp/manifest.json"} {
			w, _ := z.Create(n)
			w.Write([]byte("{}"))
		}
		z.Close()
		if _, _, e := Validate(b.Bytes()); Code(e) != "PACKAGE_CORRUPTED" {
			t.Fatal(name, e)
		}
	}
	if _, _, e := Validate(make([]byte, MaxPackageBytes+1)); Code(e) != "PACKAGE_TOO_LARGE" {
		t.Fatal(e)
	}
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	w, _ := z.Create("game.nsp/manifest.json")
	w.Write(bytes.Repeat([]byte("x"), MaxExpandedBytes+1))
	w, _ = z.Create("game.nsp/app.wasm")
	w.Write([]byte("x"))
	z.Close()
	if _, _, e := Validate(b.Bytes()); Code(e) != "PACKAGE_CORRUPTED" {
		t.Fatal(e)
	}
}
