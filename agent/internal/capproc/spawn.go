package capproc

import (
	"bufio"
	"log/slog"
	"os"
	"os/exec"
	"sync"
)

// Command spawns exe with args as a capture worker. The worker's stderr (its
// log and the native libraries') is relayed to log line by line.
func Command(exe string, args []string, log *slog.Logger) func() (*Process, error) {
	return func() (*Process, error) {
		inR, inW, err := os.Pipe()
		if err != nil {
			return nil, err
		}
		outR, outW, err := os.Pipe()
		if err != nil {
			closeAll(inR, inW)
			return nil, err
		}
		errR, errW, err := os.Pipe()
		if err != nil {
			closeAll(inR, inW, outR, outW)
			return nil, err
		}
		// Plain files, not exec's pipes: Wait must not close the ends this
		// process still reads.
		cmd := exec.Command(exe, args...)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = inR, outW, errW
		cmd.SysProcAttr = sysProcAttr()
		if err := cmd.Start(); err != nil {
			closeAll(inR, inW, outR, outW, errR, errW)
			return nil, err
		}
		closeAll(inR, outW, errW)
		pid := cmd.Process.Pid
		go func() {
			sc := bufio.NewScanner(errR)
			sc.Buffer(make([]byte, 4096), 64<<10)
			for sc.Scan() {
				log.Info("capture worker: "+sc.Text(), "pid", pid)
			}
			errR.Close()
		}()
		var once sync.Once
		var werr error
		wait := func() error {
			once.Do(func() {
				werr = cmd.Wait()
				outR.Close()
			})
			return werr
		}
		return &Process{R: outR, W: inW, Wait: wait, Kill: func() { _ = cmd.Process.Kill() }}, nil
	}
}

func closeAll(fs ...*os.File) {
	for _, f := range fs {
		f.Close()
	}
}
