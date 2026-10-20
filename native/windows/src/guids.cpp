// Instantiates the Media Foundation / codec API GUIDs (selectany) so the
// library links without wmcodecdspuuid / strmiids on every toolchain.
#include "rc_win.h"
#include <initguid.h>
#include <codecapi.h>
#include <mfapi.h>
#include <mftransform.h>
