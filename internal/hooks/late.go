package hooks

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Late decisions. When Jev doesn't answer within the hook's timeout, the
// prompt goes on with the decision in force (or the default tier), and a
// detached copy of the hook asks again with a longer timeout. If no newer
// prompt arrived meanwhile, its decision replaces the fallback: the proxy
// applies it from the turn's next request.

// LateEnv carries "<trigger>:<prompt time in ns>" to the late process.
const LateEnv = "AUTOMODEL_LATE_DECISION"

// LateTimeout bounds the late Jev call.
const LateTimeout = 20 * time.Second

// spawnLate starts the late decision (a package variable: tests replace it).
var spawnLate = func(in *Input, trigger string, at time.Time) {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	body, err := json.Marshal(in)
	if err != nil {
		return
	}
	cmd := exec.Command(exe, os.Args[1:]...)
	cmd.Stdin = bytes.NewReader(body)
	cmd.Env = append(os.Environ(), fmt.Sprintf("%s=%s:%d", LateEnv, trigger, at.UnixNano()))
	detach(cmd)
	if err := cmd.Start(); err != nil {
		log.Printf("late decision %s: %v", in.SessionID, err)
		return
	}
	cmd.Process.Release()
}

// lateMode reports the trigger and prompt time of a late decision process.
func lateMode() (trigger string, at int64, ok bool) {
	v := os.Getenv(LateEnv)
	if v == "" {
		return "", 0, false
	}
	t, ns, found := strings.Cut(v, ":")
	n, err := strconv.ParseInt(ns, 10, 64)
	if !found || err != nil {
		return "", 0, false
	}
	return t, n, true
}
