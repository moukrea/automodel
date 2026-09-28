package hooks

import (
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

// InputFileEnv names the file holding a detached hook's input (read, then
// removed, in place of stdin).
const InputFileEnv = "AUTOMODEL_HOOK_INPUT"

// LateEnv carries "<trigger>:<prompt time in ns>" to the late process.
const LateEnv = "AUTOMODEL_LATE_DECISION"

// LateTimeout bounds the late Jev call.
const LateTimeout = 20 * time.Second

// spawnLate starts the late decision (a package variable: tests replace it).
var spawnLate = func(in *Input, trigger string, at time.Time) {
	spawnSelf(in, fmt.Sprintf("%s=%s:%d", LateEnv, trigger, at.UnixNano()))
}

// spawnSelf runs this hook again, detached, with in on stdin and one more
// environment variable.
func spawnSelf(in *Input, envVar string) {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	body, err := json.Marshal(in)
	if err != nil {
		return
	}
	// The input goes through a file: a pipe fed by this process would be
	// cut when it exits, which is right away.
	f, err := os.CreateTemp("", "automodel-hook-*.json")
	if err != nil {
		return
	}
	_, werr := f.Write(body)
	f.Close()
	if werr != nil {
		os.Remove(f.Name())
		return
	}
	cmd := exec.Command(exe, os.Args[1:]...)
	cmd.Env = append(os.Environ(), envVar, InputFileEnv+"="+f.Name())
	detach(cmd)
	if err := cmd.Start(); err != nil {
		log.Printf("detached hook %s: %v", in.SessionID, err)
		return
	}
	cmd.Process.Release()
}

// CompactEnv carries the compaction time (ns) to the detached compaction decision.
const CompactEnv = "AUTOMODEL_COMPACT_DECISION"

// compactWait bounds the wait for the compaction summary in the transcript.
var compactWait = 10 * time.Second

// spawnCompact starts the decision on the compaction summary (a package
// variable: tests replace it).
var spawnCompact = func(in *Input, at time.Time) {
	spawnSelf(in, fmt.Sprintf("%s=%d", CompactEnv, at.UnixNano()))
}

func compactMode() (time.Time, bool) {
	n, err := strconv.ParseInt(os.Getenv(CompactEnv), 10, 64)
	if err != nil {
		return time.Time{}, false
	}
	return time.Unix(0, n), true
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
