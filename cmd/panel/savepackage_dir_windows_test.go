//go:build windows

package main

import (
	"bytes"
	"context"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestExportKeepsPackageWriteBelowOpenedBackupsHandle(t *testing.T) {
	p, outside := saveFixture(t, "Navezgane", "game"), t.TempDir()
	beforeSavePackageChild = func(name string) {
		if name != "saves" {
			return
		}
		beforeSavePackageChild = nil
		if err := os.Remove(p.Backups); err != nil {
			t.Fatal(err)
		}
		makeReparseLink(t, outside, p.Backups)
	}
	t.Cleanup(func() { beforeSavePackageChild = nil })

	if _, err := (SaveService{paths: p, server: NewServerManager(p, nil)}).Export(context.Background(), "Navezgane", "game"); err == nil {
		t.Fatal("expected ancestor replacement to abort export")
	}
	if _, err := os.Stat(filepath.Join(outside, "saves")); !os.IsNotExist(err) {
		t.Fatalf("outside save package directory = %v", err)
	}
}

func TestImportKeepsStagingWriteBelowOpenedBackupsHandle(t *testing.T) {
	a, outside := newTestApp(t), t.TempDir()
	beforeSavePackageChild = func(name string) {
		if name != "saves" {
			return
		}
		beforeSavePackageChild = nil
		if err := os.Remove(a.paths.Backups); err != nil {
			t.Fatal(err)
		}
		makeReparseLink(t, outside, a.paths.Backups)
	}
	t.Cleanup(func() { beforeSavePackageChild = nil })

	archive := makePackage(t, SavePackageManifest{Version: 1, Kind: WorldBuiltin, World: "Navezgane", Game: "imported"}, map[string]string{"save/main.ttw": "save"})
	body := new(bytes.Buffer)
	form := multipart.NewWriter(body)
	part, err := form.CreateFormFile("package", "save.zip")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(mustRead(t, archive)); err != nil {
		t.Fatal(err)
	}
	if err := form.Close(); err != nil {
		t.Fatal(err)
	}
	req := saveRequest(a, http.MethodPost, "/api/saves/import", body)
	req.Header.Set("Content-Type", form.FormDataContentType())
	rr := httptest.NewRecorder()
	a.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("POST import = %d: %s", rr.Code, rr.Body.String())
	}
	if _, err := os.Stat(filepath.Join(outside, "saves")); !os.IsNotExist(err) {
		t.Fatalf("outside staging directory = %v", err)
	}
}

func TestImportOpenStagedUploadFailureCleansUp(t *testing.T) {
	a := newTestApp(t)
	beforeSavePackageOpen = func(d *savePackageDir, name string) {
		beforeSavePackageOpen = nil
		if err := d.Remove(name); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { beforeSavePackageOpen = nil })

	archive := makePackage(t, SavePackageManifest{Version: 1, Kind: WorldBuiltin, World: "Navezgane", Game: "imported"}, map[string]string{"save/main.ttw": "save"})
	body := new(bytes.Buffer)
	form := multipart.NewWriter(body)
	part, err := form.CreateFormFile("package", "save.zip")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(mustRead(t, archive)); err != nil {
		t.Fatal(err)
	}
	if err := form.Close(); err != nil {
		t.Fatal(err)
	}
	req := saveRequest(a, http.MethodPost, "/api/saves/import", body)
	req.Header.Set("Content-Type", form.FormDataContentType())
	rr := httptest.NewRecorder()
	a.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest || !bytes.Contains(rr.Body.Bytes(), []byte("save_import_failed")) {
		t.Fatalf("POST import = %d: %s", rr.Code, rr.Body.String())
	}
	entries, err := os.ReadDir(a.paths.SavePackages)
	if err != nil || len(entries) != 0 {
		t.Fatalf("staged uploads = %v, %v", entries, err)
	}
	release, err := a.server.BeginStoppedOperation()
	if err != nil {
		t.Fatalf("import retained lease: %v", err)
	}
	release()
}

func TestDeleteVerifiesExportWithOpenedPackageHandle(t *testing.T) {
	p, outside := saveFixture(t, "Navezgane", "game"), t.TempDir()
	s := SaveService{paths: p, server: NewServerManager(p, nil)}
	s.beforeSavePackageVerification = func(name string) {
		if err := os.RemoveAll(p.Backups); err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(outside, "saves", name), "attacker package")
		makeReparseLink(t, outside, p.Backups)
	}

	if _, err := s.Delete(context.Background(), "Navezgane", "game"); !errors.Is(err, ErrUnsafeSavePath) {
		t.Fatalf("err=%v", err)
	}
	if !exists(filepath.Join(p.Saves, "Navezgane", "game")) {
		t.Fatal("save was deleted")
	}
}
