//go:build darwin

package update

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/user"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/ruolez/remotectl/proto"
)

const (
	systemApp         = "/Applications/RemoteAgent.app"
	systemAgentPlist  = "/Library/LaunchAgents/com.remotectl.agent.plist"
	systemDaemonPlist = "/Library/LaunchDaemons/com.remotectl.updater.plist"

	// SystemDaemonLog is where `remote-agent update-daemon` logs.
	SystemDaemonLog = SystemUpdaterDir + "/logs/daemon.log"
)

// RunSystemDaemon is `remote-agent update-daemon`, run by launchd as root
// (com.remotectl.updater). It never touches the device key or the tray.
func RunSystemDaemon(ctx context.Context, log *slog.Logger) error {
	if os.Geteuid() != 0 {
		return errors.New("update-daemon runs as root under launchd (com.remotectl.updater)")
	}
	r := ExecRunner{}
	d := &Daemon{
		Root: SystemUpdaterDir, App: systemApp, AgentPlist: systemAgentPlist, DaemonPlist: systemDaemonPlist,
		PubKey: proto.ReleasePublicKey, Runner: r, Console: consoleUser, UserHome: homeOf, Now: time.Now, Log: log,
		// The server URL comes from the root-owned LaunchAgent plist, not from any user.
		ServerUp: func(ctx context.Context) bool {
			out, err := r.Run(ctx, "plutil", "-extract", "EnvironmentVariables.RC_SERVER_URL", "raw", systemAgentPlist)
			url := strings.TrimSpace(string(out))
			return err == nil && url != "" && ProbeServer(ctx, url)
		},
	}
	return d.Run(ctx)
}

func consoleUser() (string, uint32, bool) {
	fi, err := os.Stat("/dev/console")
	if err != nil {
		return "", 0, false
	}
	uid := fi.Sys().(*syscall.Stat_t).Uid
	u, err := user.LookupId(strconv.FormatUint(uint64(uid), 10))
	if err != nil {
		return "", uid, false
	}
	return u.Username, uid, true
}

func homeOf(uid uint32) string {
	u, err := user.LookupId(strconv.FormatUint(uint64(uid), 10))
	if err != nil {
		return ""
	}
	return u.HomeDir
}
