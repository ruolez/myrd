package app

import (
	"unicode/utf8"

	"github.com/ruolez/remotectl/proto"
)

// validFileRecord checks a transfer or file-operation record reported by an
// agent (session.file) before it is written to the audit log.
func validFileRecord(r proto.FileRecord) bool {
	switch {
	case len(r.Name) > 1024 || !utf8.ValidString(r.Name):
	case len(r.Dir) > 4096 || !utf8.ValidString(r.Dir):
	case len(r.To) > 1024 || !utf8.ValidString(r.To):
	case r.DurationMS < 0 || !isErrorCode(r.Error):
	case r.Direction == "upload" || r.Direction == "download":
		return validTransferRecord(r)
	case r.Direction == "mkdir" || r.Direction == "rename" || r.Direction == "delete":
		return validOpRecord(r)
	}
	return false
}

func validTransferRecord(r proto.FileRecord) bool {
	ok := r.Result == "ok"
	switch {
	case !ok && r.Result != "cancelled" && r.Result != "error":
	case ok && r.Name == "":
	case r.To != "" || r.Kind != "" || r.Replaced && r.Direction != "upload":
	case r.Size < 0 || r.Bytes < 0 || r.Bytes > r.Size:
	case r.SHA256 != "" && !isLowerHex(r.SHA256, 64):
	case ok && (r.SHA256 == "" || r.Error != "" || r.Bytes != r.Size):
	case !ok && (r.Error == "" || r.SHA256 != "" || r.Replaced):
	default:
		return true
	}
	return false
}

func validOpRecord(r proto.FileRecord) bool {
	ok := r.Result == "ok"
	switch {
	case !ok && r.Result != "error":
	case r.Name == "" || r.Size != 0 || r.Bytes != 0 || r.SHA256 != "" || r.Replaced:
	case r.Kind != "" && r.Kind != "dir" && r.Kind != "file" && r.Kind != "other":
	case r.Direction != "rename" && r.To != "":
	case ok && (r.Error != "" || r.Direction == "rename" && r.To == ""):
	case !ok && r.Error == "":
	default:
		return true
	}
	return false
}

func isLowerHex(s string, n int) bool {
	if len(s) != n {
		return false
	}
	for i := 0; i < len(s); i++ {
		if c := s[i]; (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// isErrorCode accepts "" or a short snake_case code like "no_space".
func isErrorCode(s string) bool {
	if len(s) > 32 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if c := s[i]; (c < 'a' || c > 'z') && c != '_' {
			return false
		}
	}
	return true
}

// fileEvent is the audit event of a valid record.
func fileEvent(r proto.FileRecord) string {
	ok := r.Result == "ok"
	switch r.Direction {
	case "upload":
		return pick(ok, "file.uploaded", "file.upload_failed")
	case "mkdir":
		return pick(ok, "file.folder_created", "file.folder_create_failed")
	case "rename":
		return pick(ok, "file.renamed", "file.rename_failed")
	case "delete":
		return pick(ok, "file.deleted", "file.delete_failed")
	}
	return pick(ok, "file.downloaded", "file.download_failed")
}

func pick(ok bool, yes, no string) string {
	if ok {
		return yes
	}
	return no
}

func fileMeta(r proto.FileRecord) map[string]any {
	m := map[string]any{"name": r.Name, "dir": r.Dir, "result": r.Result, "error": r.Error,
		"duration_ms": r.DurationMS, "xfer": r.Xfer}
	switch r.Direction {
	case "upload", "download":
		m["size"], m["bytes"], m["sha256"] = r.Size, r.Bytes, r.SHA256
		if r.Replaced {
			m["replaced"] = true
		}
	case "rename":
		m["to"], m["kind"] = r.To, r.Kind
	default:
		m["kind"] = r.Kind
	}
	return m
}
