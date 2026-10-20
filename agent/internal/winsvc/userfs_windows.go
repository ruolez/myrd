package winsvc

import (
	"errors"
	"fmt"
	"runtime"

	"golang.org/x/sys/windows"

	"github.com/ruolez/remotectl/agent/internal/files"
)

// UserFS is the files.Host of the Windows helper. The helper runs as
// SYSTEM; file access must instead carry the identity of the user signed in
// to its session, so ACLs apply and uploads are owned by that user.
type UserFS struct {
	SessionID uint32
}

var _ files.Host = UserFS{}

// userToken is the session user's primary token (needs SeTcbPrivilege).
func (u UserFS) userToken() (windows.Token, error) {
	var tok windows.Token
	if err := windows.WTSQueryUserToken(u.SessionID, &tok); err != nil {
		if errors.Is(err, windows.ERROR_NO_TOKEN) {
			return 0, files.ErrNoUser
		}
		return 0, fmt.Errorf("WTSQueryUserToken: %w", err)
	}
	return tok, nil
}

// Do runs fn impersonating the session user on a dedicated OS thread. The
// goroutine exits without UnlockOSThread, so the runtime discards the
// thread: the user's token can never leak to other goroutines, even if
// RevertToSelf fails.
func (u UserFS) Do(fn func() error) error {
	primary, err := u.userToken()
	if err != nil {
		return err
	}
	defer primary.Close()
	var imp windows.Token
	if err := windows.DuplicateTokenEx(primary, windows.TOKEN_IMPERSONATE|windows.TOKEN_QUERY, nil,
		windows.SecurityImpersonation, windows.TokenImpersonation, &imp); err != nil {
		return fmt.Errorf("DuplicateTokenEx: %w", err)
	}
	defer imp.Close()
	done := make(chan error, 1)
	go func() {
		runtime.LockOSThread()
		if err := windows.SetThreadToken(nil, imp); err != nil {
			done <- fmt.Errorf("SetThreadToken: %w", err)
			return
		}
		err := fn()
		_ = windows.RevertToSelf()
		done <- err
	}()
	return <-done
}

// Roots lists the user's profile and known folders (wherever they are
// redirected, e.g. to OneDrive) and the drives the user sees, including
// their mapped network drives.
func (u UserFS) Roots() ([]files.Root, string, error) {
	tok, err := u.userToken()
	if err != nil {
		return nil, "", err
	}
	defer tok.Close()
	home, err := tok.GetUserProfileDirectory()
	if err != nil {
		return nil, "", fmt.Errorf("user profile: %w", err)
	}
	roots := []files.Root{{Name: "Home", Path: home, Kind: "home"}}
	def := home
	for _, f := range []struct {
		id   *windows.KNOWNFOLDERID
		name string
		kind string
	}{
		{windows.FOLDERID_Desktop, "Desktop", "desktop"},
		{windows.FOLDERID_Documents, "Documents", "documents"},
		{windows.FOLDERID_Downloads, "Downloads", "downloads"},
	} {
		p, err := tok.KnownFolderPath(f.id, windows.KF_FLAG_DEFAULT)
		if err != nil || p == "" {
			continue
		}
		roots = append(roots, files.Root{Name: f.name, Path: p, Kind: f.kind})
		if f.kind == "downloads" {
			def = p
		}
	}
	err = u.Do(func() error {
		mask, err := windows.GetLogicalDrives()
		if err != nil {
			return err
		}
		roots = append(roots, driveRoots(mask, func(root string) uint32 {
			p, _ := windows.UTF16PtrFromString(root)
			return windows.GetDriveType(p)
		})...)
		return nil
	})
	if err != nil {
		return nil, "", err
	}
	return roots, def, nil
}
