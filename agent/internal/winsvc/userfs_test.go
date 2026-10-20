package winsvc

import (
	"strings"
	"testing"
)

func TestDriveRoots(t *testing.T) {
	types := map[string]uint32{`A:\`: 1, `C:\`: driveFixed, `D:\`: driveCDROM, `E:\`: driveRemovable, `Z:\`: driveRemote, `Q:\`: 0}
	mask := uint32(1<<0 | 1<<2 | 1<<3 | 1<<4 | 1<<16 | 1<<25)
	var got []string
	for _, r := range driveRoots(mask, func(r string) uint32 { return types[r] }) {
		got = append(got, r.Name+"="+r.Path+"/"+r.Kind)
	}
	if want := `C:=C:\/drive D:=D:\/drive E:=E:\/drive Z:=Z:\/drive`; strings.Join(got, " ") != want {
		t.Fatalf("drive roots %v, want %s", got, want)
	}
}
