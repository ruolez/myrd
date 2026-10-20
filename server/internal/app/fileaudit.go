package app

import (
	"unicode/utf8"

	"github.com/ruolez/remotectl/proto"
)

// validFileRecord checks a transfer record reported by an agent
// (session.file) before it is written to the audit log.
func validFileRecord(r proto.FileRecord) bool {
	ok := r.Result == "ok"
	switch {
	case r.Direction != "upload" && r.Direction != "download":
	case !ok && r.Result != "cancelled" && r.Result != "error":
	case len(r.Name) > 1024 || !utf8.ValidString(r.Name) || ok && r.Name == "":
	case len(r.Dir) > 4096 || !utf8.ValidString(r.Dir):
	case r.Size < 0 || r.Bytes < 0 || r.Bytes > r.Size || r.DurationMS < 0:
	case r.SHA256 != "" && !isLowerHex(r.SHA256, 64):
	case ok && (r.SHA256 == "" || r.Error != "" || r.Bytes != r.Size):
	case !ok && (r.Error == "" || r.SHA256 != ""):
	case !isErrorCode(r.Error):
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
	switch {
	case r.Direction == "upload" && r.Result == "ok":
		return "file.uploaded"
	case r.Direction == "upload":
		return "file.upload_failed"
	case r.Result == "ok":
		return "file.downloaded"
	}
	return "file.download_failed"
}

func fileMeta(r proto.FileRecord) map[string]any {
	return map[string]any{"name": r.Name, "dir": r.Dir, "size": r.Size, "bytes": r.Bytes, "sha256": r.SHA256,
		"result": r.Result, "error": r.Error, "duration_ms": r.DurationMS, "xfer": r.Xfer}
}
