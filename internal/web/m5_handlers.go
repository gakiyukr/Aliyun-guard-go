// M5 面板路由 handler：加密備份、S3 備份與實例探索（對應 web_panel.py
// do_POST 同名分支）。還原類操作完成後觸發 delayed_restart（202）。
package web

import "net/http"

func (s *PanelServer) handleBackupCreate(w http.ResponseWriter, r *http.Request) {
	data, err := s.readJSON(r)
	if err != nil {
		writeResult(w, nil, err)
		return
	}
	result, err := createEncryptedBackup(s.paths, data)
	writeResult(w, map[string]any{"result": result}, err)
}

func (s *PanelServer) handleBackupPreview(w http.ResponseWriter, r *http.Request) {
	data, err := s.readJSON(r)
	if err != nil {
		writeResult(w, nil, err)
		return
	}
	result, err := previewEncryptedBackup(s.paths, data)
	writeResult(w, map[string]any{"result": result}, err)
}

func (s *PanelServer) handleBackupRestore(w http.ResponseWriter, r *http.Request) {
	data, err := s.readJSON(r)
	if err != nil {
		writeResult(w, nil, err)
		return
	}
	result, err := restoreEncryptedBackup(s.paths, data)
	if err == nil {
		s.delayedRestart()
		writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "result": result})
		return
	}
	writeResult(w, nil, err)
}

func (s *PanelServer) handleS3Settings(w http.ResponseWriter, r *http.Request) {
	data, err := s.readJSON(r)
	if err != nil {
		writeResult(w, nil, err)
		return
	}
	result, err := saveS3BackupSettings(s.paths, data)
	writeResult(w, map[string]any{"s3_backup": result}, err)
}

func (s *PanelServer) handleS3Test(w http.ResponseWriter, r *http.Request) {
	data, err := s.readJSON(r)
	if err != nil {
		writeResult(w, nil, err)
		return
	}
	result, err := testS3BackupSettings(s.paths, data)
	writeResult(w, map[string]any{"result": result}, err)
}

func (s *PanelServer) handleS3List(w http.ResponseWriter, r *http.Request) {
	result, err := listS3Backups(s.paths)
	writeResult(w, map[string]any{"result": result}, err)
}

func (s *PanelServer) handleS3Run(w http.ResponseWriter, r *http.Request) {
	result, err := runS3BackupNow(s.paths)
	writeResult(w, map[string]any{"result": result}, err)
}

func (s *PanelServer) handleS3Preview(w http.ResponseWriter, r *http.Request) {
	data, err := s.readJSON(r)
	if err != nil {
		writeResult(w, nil, err)
		return
	}
	result, err := previewS3Backup(s.paths, toString(data["key"]))
	writeResult(w, map[string]any{"result": result}, err)
}

func (s *PanelServer) handleS3Restore(w http.ResponseWriter, r *http.Request) {
	data, err := s.readJSON(r)
	if err != nil {
		writeResult(w, nil, err)
		return
	}
	includeLogs, err := asBool(data, "include_logs", true)
	if err != nil {
		writeResult(w, nil, err)
		return
	}
	result, err := restoreS3Backup(s.paths, toString(data["key"]), includeLogs)
	if err == nil {
		s.delayedRestart()
		writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "result": result})
		return
	}
	writeResult(w, nil, err)
}

func (s *PanelServer) handleDiscoveryScan(w http.ResponseWriter, r *http.Request) {
	data, err := s.readJSON(r)
	if err != nil {
		writeResult(w, nil, err)
		return
	}
	result, err := discoverInstances(s.paths, data)
	writeResult(w, map[string]any{"result": result}, err)
}

func (s *PanelServer) handleDiscoveryImport(w http.ResponseWriter, r *http.Request) {
	data, err := s.readJSON(r)
	if err != nil {
		writeResult(w, nil, err)
		return
	}
	result, err := importDiscoveredInstances(s.paths, data)
	writeResult(w, map[string]any{"result": result}, err)
}
