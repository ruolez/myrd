package files

import "os"

func openRead(p string) (*os.File, error) { return os.Open(p) }
