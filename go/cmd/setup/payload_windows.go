//go:build windows && setup

package main

import _ "embed"

//go:embed payload/yanhekt.exe.gz
var embeddedExeGz []byte
