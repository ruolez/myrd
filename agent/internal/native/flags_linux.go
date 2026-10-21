//go:build cgo && linux

package native

/*
#cgo LDFLAGS: -L${SRCDIR}/../../../native/linux/build -lrcnative -lX11 -lXext -lXfixes -lXtst -lXdamage -lXrandr -lx264 -lvpx -lswscale -lavutil -lpthread -lm
*/
import "C"
