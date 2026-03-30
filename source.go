package sloggetalert

import "runtime"

func runtimeFrame(pc uintptr) runtime.Frame {
	frames := runtime.CallersFrames([]uintptr{pc})
	f, _ := frames.Next()
	return f
}
