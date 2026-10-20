package update

import (
	"context"
	"errors"

	"github.com/ruolez/remotectl/agent/internal/api"
)

type unsupportedInstaller struct{}

func (unsupportedInstaller) Install(context.Context, string, api.Manifest) error {
	return errors.New("auto-update is not supported on the Linux demo device; rebuild the image")
}

func newDefaultInstaller(Options) Installer { return unsupportedInstaller{} }
