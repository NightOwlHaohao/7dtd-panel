package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
)

type saveTargetRequest struct {
	World string `json:"world"`
	Game  string `json:"game"`
}

type saveSwitchRequest struct {
	saveTargetRequest
	Hash string `json:"hash"`
}

type saveDeleteRequest struct {
	Confirm bool `json:"confirm"`
}

func (a *App) listSaves(w http.ResponseWriter, _ *http.Request) {
	status := a.server.Status()
	catalog, err := a.saves.Catalog(status.World, status.GameName)
	if err != nil {
		a.error(w, http.StatusInternalServerError, "save_catalog_failed", err.Error())
		return
	}
	a.json(w, http.StatusOK, catalog)
}

func (a *App) switchSave(w http.ResponseWriter, req *http.Request) {
	var body saveSwitchRequest
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		a.error(w, http.StatusBadRequest, "invalid_request", "request body is invalid")
		return
	}
	if !a.saveAttempt(w, "save switch") {
		return
	}
	a.saveEvent("switch started", nil)
	hash, err := a.saves.Switch(body.World, body.Game, body.Hash)
	if auditErr := a.auditResult("save switch", err); auditErr != nil {
		a.saveEvent("switch failed", nil)
		a.auditFailure(w, true, auditErr)
		return
	}
	if err != nil {
		a.saveEvent("switch failed", nil)
		a.saveError(w, "save_switch_failed", err)
		return
	}
	a.saveEvent("switch completed", map[string]string{"world": body.World, "game": body.Game})
	a.json(w, http.StatusOK, map[string]string{"hash": hash})
}

func (a *App) exportSave(w http.ResponseWriter, req *http.Request) {
	var body saveTargetRequest
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		a.error(w, http.StatusBadRequest, "invalid_request", "request body is invalid")
		return
	}
	if !a.saveAttempt(w, "save export") {
		return
	}
	a.saveEvent("export started", nil)
	backup, err := a.saves.Export(req.Context(), body.World, body.Game)
	if auditErr := a.auditResult("save export", err); auditErr != nil {
		a.saveEvent("export failed", nil)
		a.auditFailure(w, true, auditErr)
		return
	}
	if err != nil {
		a.saveEvent("export failed", nil)
		a.saveError(w, "save_export_failed", err)
		return
	}
	a.saveEvent("export completed", backup)
	a.json(w, http.StatusOK, backup)
}

func (a *App) importSave(w http.ResponseWriter, req *http.Request) {
	if !a.saveAttempt(w, "save import") {
		return
	}
	a.saveEvent("import started", nil)
	release, err := a.saves.acquireStoppedOperation()
	if err == nil {
		defer release()
		req.Body = http.MaxBytesReader(w, req.Body, 64<<30)
		reader, readerErr := req.MultipartReader()
		err = readerErr
		if err == nil {
			part, partErr := reader.NextPart()
			if partErr == io.EOF {
				err = errors.New("package is required")
			} else {
				err = partErr
			}
			if err == nil {
				defer part.Close()
				if part.FormName() != "package" {
					err = errors.New("package is required")
				} else {
					packageDir, dirErr := openSavePackageDir(a.paths)
					err = dirErr
					if err == nil {
						defer packageDir.Close()
						upload, uploadBase, createErr := packageDir.CreateTemp("upload-*.zip")
						err = createErr
						if err == nil {
							defer packageDir.Remove(uploadBase)
							_, err = io.Copy(upload, part)
							if err == nil {
								err = upload.Sync()
							}
							if closeErr := upload.Close(); err == nil {
								err = closeErr
							}
							if err == nil {
								upload, err = packageDir.Open(uploadBase)
							}
							var uploadSize int64
							if err == nil {
								var info os.FileInfo
								info, err = upload.Stat()
								if err == nil {
									uploadSize = info.Size()
								}
							}
							if err == nil {
								extra, nextErr := reader.NextPart()
								if extra != nil {
									extra.Close()
								}
								if nextErr != io.EOF {
									if nextErr != nil {
										err = nextErr
									} else {
										err = errors.New("only one package is allowed")
									}
								}
							}
							if err == nil {
								err = a.saves.importLockedFile(req.Context(), upload, uploadSize)
							}
							if upload != nil {
								if closeErr := upload.Close(); err == nil {
									err = closeErr
								}
							}
						}
					}
				}
			}
		}
	}
	if auditErr := a.auditResult("save import", err); auditErr != nil {
		a.saveEvent("import failed", nil)
		a.auditFailure(w, err == nil, auditErr)
		return
	}
	if err != nil {
		a.saveEvent("import failed", nil)
		a.saveError(w, "save_import_failed", err)
		return
	}
	a.saveEvent("import completed", nil)
	a.json(w, http.StatusOK, map[string]string{"status": "imported"})
}

func (a *App) deleteSave(w http.ResponseWriter, req *http.Request) {
	var body saveDeleteRequest
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		a.error(w, http.StatusBadRequest, "invalid_request", "request body is invalid")
		return
	}
	if !a.saveAttempt(w, "save delete") {
		return
	}
	a.saveEvent("delete started", nil)
	world, game := req.PathValue("world"), req.PathValue("game")
	var backup BackupInfo
	var err error
	if !body.Confirm {
		err = errors.New("delete confirmation is required")
	} else {
		backup, err = a.saves.Delete(req.Context(), world, game)
	}
	if auditErr := a.auditResult("save delete", err); auditErr != nil {
		a.saveEvent("delete failed", nil)
		a.auditFailure(w, err == nil, auditErr)
		return
	}
	if err != nil {
		a.saveEvent("delete failed", nil)
		a.saveError(w, "save_delete_failed", err)
		return
	}
	a.saveEvent("delete completed", backup)
	a.json(w, http.StatusOK, backup)
}

func (a *App) saveAttempt(w http.ResponseWriter, action string) bool {
	return a.auditAttempt(w, action)
}

func (a *App) saveEvent(message string, data any) {
	if a.events != nil {
		a.events.Publish(Event{Type: "save", Source: "save", Message: message, Data: data})
	}
}

func (a *App) saveError(w http.ResponseWriter, code string, err error) {
	status := http.StatusBadRequest
	var maxBytes *http.MaxBytesError
	if errors.As(err, &maxBytes) {
		status = http.StatusRequestEntityTooLarge
	} else if errors.Is(err, ErrServerMustBeStopped) || errors.Is(err, ErrServerRunning) || errors.Is(err, ErrServerBusy) || errors.Is(err, ErrServerMaintenance) || errors.Is(err, ErrConfigChanged) {
		status = http.StatusConflict
	}
	a.error(w, status, code, err.Error())
}

func (a *App) listBackups(w http.ResponseWriter, _ *http.Request) {
	backups, err := a.backups.List()
	if err != nil {
		a.error(w, http.StatusInternalServerError, "backup_list_failed", err.Error())
		return
	}
	a.json(w, http.StatusOK, backups)
}

func (a *App) createBackup(w http.ResponseWriter, req *http.Request) {
	a.events.Publish(Event{Type: "backup", Source: "backup", Message: "开始创建备份"})
	backup, err := a.backups.Create(req.Context(), false, a.saveWorld)
	if err != nil {
		a.events.Publish(Event{Type: "backup", Source: "backup", Message: "备份失败"})
		a.backupError(w, "backup_create_failed", err)
		return
	}
	a.events.Publish(Event{Type: "backup", Source: "backup", Message: "备份完成", Data: backup})
	a.json(w, http.StatusOK, backup)
}

// saveWorld asks the running game to write the world to disk.
func (a *App) saveWorld(ctx context.Context) error {
	_, err := a.runTelnetCommand(ctx, "saveworld")
	return err
}

func (a *App) deleteBackup(w http.ResponseWriter, req *http.Request) {
	name := req.PathValue("name")
	if !a.auditAttempt(w, "backup delete "+name) {
		return
	}
	err := a.backups.Delete(name)
	if auditErr := a.auditResult("backup delete "+name, err); auditErr != nil {
		a.auditFailure(w, true, auditErr)
		return
	}
	if err != nil {
		a.backupError(w, "backup_delete_failed", err)
		return
	}
	a.json(w, http.StatusOK, map[string]string{"deleted": name})
}

func (a *App) restoreBackup(w http.ResponseWriter, req *http.Request) {
	name := req.PathValue("name")
	var body struct {
		Confirm bool `json:"confirm"`
	}
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil || !body.Confirm {
		a.error(w, http.StatusBadRequest, "invalid_request", "restore confirmation is required")
		return
	}
	if !a.auditAttempt(w, "backup restore "+name) {
		return
	}
	a.events.Publish(Event{Type: "backup", Source: "backup", Message: "开始恢复备份 " + name})
	result, err := a.backups.Restore(req.Context(), name)
	if auditErr := a.auditResult("backup restore "+name, err); auditErr != nil {
		a.auditFailure(w, true, auditErr)
		return
	}
	if err != nil {
		a.events.Publish(Event{Type: "backup", Source: "backup", Message: "恢复备份失败"})
		a.backupError(w, "backup_restore_failed", err)
		return
	}
	a.events.Publish(Event{Type: "backup", Source: "backup", Message: "恢复备份完成", Data: result})
	a.json(w, http.StatusOK, result)
}

func (a *App) backupError(w http.ResponseWriter, code string, err error) {
	status := http.StatusBadRequest
	switch {
	case errors.Is(err, ErrBackupNotFound):
		status = http.StatusNotFound
	case errors.Is(err, ErrServerMustBeStopped), errors.Is(err, ErrServerBusy), errors.Is(err, ErrServerMaintenance), errors.Is(err, ErrServerRunning):
		status = http.StatusConflict
	}
	a.error(w, status, code, err.Error())
}

func (a *App) openBackupFolder(w http.ResponseWriter, _ *http.Request) {
	if err := os.MkdirAll(a.paths.UserDataBackups, 0700); err != nil {
		a.error(w, http.StatusInternalServerError, "backup_open_failed", err.Error())
		return
	}
	if err := exec.Command("explorer.exe", a.paths.UserDataBackups).Start(); err != nil {
		a.error(w, http.StatusInternalServerError, "backup_open_failed", err.Error())
		return
	}
	a.json(w, http.StatusOK, map[string]string{"folder": a.paths.UserDataBackups})
}
