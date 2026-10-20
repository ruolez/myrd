//go:build linux

package tray

func run(h Hooks, ready func(update func(State))) {
	done := make(chan struct{})
	go ready(func(State) {})
	<-done
}
