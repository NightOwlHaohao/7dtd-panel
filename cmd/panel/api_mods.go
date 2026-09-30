package main

import (
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

type modCatalogResponse struct {
	Mods       []modInfoResponse `json:"mods"`
	Policy     DependencyPolicy  `json:"policy"`
	Locked     bool              `json:"locked"`
	LockReason string            `json:"lockReason,omitempty"`
}

type modInfoResponse struct {
	Key          string                  `json:"key"`
	ID           string                  `json:"id"`
	Name         string                  `json:"name"`
	Version      string                  `json:"version"`
	Author       string                  `json:"author"`
	Description  string                  `json:"description"`
	Enabled      bool                    `json:"enabled"`
	Unknown      bool                    `json:"unknown"`
	Dependencies []modDependencyResponse `json:"dependencies"`
	Problems     []ModProblem            `json:"problems"`
}

type modDependencyResponse struct {
	ID         string `json:"id"`
	Constraint string `json:"constraint"`
}

func (a *App) modCatalog() (ModCatalog, error) {
	policy, err := a.mods.Policy()
	if err != nil {
		return ModCatalog{}, err
	}
	catalog, err := ScanMods(a.paths, policy)
	if err != nil {
		return ModCatalog{}, err
	}
	if state := a.server.Status().State; state != ServerStopped {
		catalog.Locked = true
		catalog.LockReason = "服务器未停止，Mod 更改已锁定。"
	}
	return catalog, nil
}

func (a *App) writeModCatalog(w http.ResponseWriter, status int, catalog ModCatalog) {
	response := modCatalogResponse{Policy: catalog.Policy, Locked: catalog.Locked, LockReason: catalog.LockReason, Mods: make([]modInfoResponse, 0, len(catalog.Mods))}
	for _, mod := range catalog.Mods {
		item := modInfoResponse{Key: filepath.Base(mod.Path), ID: mod.ID, Name: mod.Name, Version: mod.Version, Author: mod.Author, Description: mod.Description, Enabled: mod.Enabled, Unknown: mod.Unknown, Problems: mod.Problems, Dependencies: make([]modDependencyResponse, 0, len(mod.Dependencies))}
		for _, dependency := range mod.Dependencies {
			item.Dependencies = append(item.Dependencies, modDependencyResponse{ID: dependency.ID, Constraint: dependency.Constraint})
		}
		response.Mods = append(response.Mods, item)
	}
	a.json(w, status, response)
}

func (a *App) listMods(w http.ResponseWriter, _ *http.Request) {
	catalog, err := a.modCatalog()
	if err != nil {
		a.modError(w, err, "mods_load_failed")
		return
	}
	a.writeModCatalog(w, http.StatusOK, catalog)
}

func (a *App) uploadMod(w http.ResponseWriter, req *http.Request) {
	release, err := a.mods.lock()
	if err == nil {
		defer release()
		req.Body = http.MaxBytesReader(w, req.Body, 64<<30)
		reader, readerErr := req.MultipartReader()
		err = readerErr
		var part *multipart.Part
		if err == nil {
			part, err = reader.NextPart()
			if err == io.EOF {
				err = errors.New("package is required")
			}
		}
		if err == nil {
			defer part.Close()
			if part.FormName() != "package" || !strings.HasSuffix(strings.ToLower(part.FileName()), ".zip") {
				err = errors.New("package must be a ZIP")
			}
		}
		if err == nil {
			var upload *os.File
			upload, err = os.CreateTemp(a.paths.Root, ".mod-upload-*.zip")
			if err == nil {
				uploadPath := upload.Name()
				defer os.Remove(uploadPath)
				if _, err = io.Copy(upload, part); err == nil {
					err = upload.Sync()
				}
				if closeErr := upload.Close(); err == nil {
					err = closeErr
				}
				if err == nil {
					upload, err = os.Open(uploadPath)
				}
				if err == nil {
					defer upload.Close()
					var info os.FileInfo
					info, err = upload.Stat()
					if err == nil {
						_, err = a.mods.importLockedFile(req.Context(), upload, info.Size(), part.FileName())
					}
				}
			}
		}
	}
	if err != nil {
		a.modError(w, err, "mods_upload_failed")
		return
	}
	catalog, err := a.modCatalog()
	if err != nil {
		a.modError(w, err, "mods_load_failed")
		return
	}
	a.writeModCatalog(w, http.StatusOK, catalog)
}

func (a *App) setModEnabled(w http.ResponseWriter, req *http.Request) {
	var body struct {
		Enabled *bool `json:"enabled"`
	}
	if !a.decodeModRequest(w, req, &body) {
		return
	}
	if body.Enabled == nil {
		a.error(w, http.StatusBadRequest, "invalid_request", "请求内容无效")
		return
	}
	name := req.PathValue("name")
	if !validModRouteName(name, req.URL.EscapedPath()) {
		a.error(w, http.StatusBadRequest, "invalid_mod_name", "Mod 名称无效")
		return
	}
	catalog, err := a.mods.SetEnabled(name, *body.Enabled)
	if err != nil {
		a.modError(w, err, "mods_update_failed")
		return
	}
	a.writeModCatalog(w, http.StatusOK, catalog)
}

func (a *App) setModPolicy(w http.ResponseWriter, req *http.Request) {
	var body struct {
		Policy DependencyPolicy `json:"policy"`
	}
	if !a.decodeModRequest(w, req, &body) {
		return
	}
	if err := a.mods.SetPolicy(body.Policy); err != nil {
		a.modError(w, err, "invalid_mod_policy")
		return
	}
	catalog, err := a.modCatalog()
	if err != nil {
		a.modError(w, err, "mods_load_failed")
		return
	}
	a.writeModCatalog(w, http.StatusOK, catalog)
}

func (a *App) decodeModRequest(w http.ResponseWriter, req *http.Request, body any) bool {
	req.Body = http.MaxBytesReader(w, req.Body, 4096)
	decoder := json.NewDecoder(req.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(body); err != nil {
		a.error(w, http.StatusBadRequest, "invalid_request", "请求内容无效")
		return false
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		a.error(w, http.StatusBadRequest, "invalid_request", "请求内容无效")
		return false
	}
	return true
}

func validModRouteName(name, escapedPath string) bool {
	escapedPath = strings.ToLower(escapedPath)
	return safeModName(name) == nil && !strings.ContainsAny(name, `/\\`) && !strings.Contains(escapedPath, "%2f") && !strings.Contains(escapedPath, "%5c")
}

func (a *App) modError(w http.ResponseWriter, err error, fallback string) {
	switch {
	case errors.Is(err, ErrModsLocked), errors.Is(err, ErrServerRunning), errors.Is(err, ErrServerBusy), errors.Is(err, ErrServerMaintenance):
		a.error(w, http.StatusConflict, "mods_locked", "服务器未停止，Mod 更改已锁定。")
	case errors.Is(err, ErrModRequired):
		a.error(w, http.StatusConflict, "mod_required", err.Error())
	case errors.Is(err, ErrModConflict):
		a.error(w, http.StatusConflict, "mod_conflict", "目标 Mod 已存在。")
	case errors.Is(err, ErrModPathEscape):
		a.error(w, http.StatusBadRequest, "invalid_mod_path", "Mod 路径无效。")
	default:
		a.error(w, http.StatusBadRequest, fallback, "Mod 请求无法完成。")
	}
}
