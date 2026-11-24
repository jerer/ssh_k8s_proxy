//go:build !windows

package agent

import (
	"reflect"
	"testing"
)

func TestMergeEnvOverridesLastValue(t *testing.T) {
	got := mergeEnv([]string{"A=1", "B=2", "A=3", "C=4"})
	want := []string{"A=3", "B=2", "C=4"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("mergeEnv = %#v, want %#v", got, want)
	}
}

func TestSignalByName(t *testing.T) {
	for _, name := range []string{"TERM", "SIGTERM", "term", "SIGINT"} {
		if _, ok := signalByName(name); !ok {
			t.Errorf("signalByName(%q) not found", name)
		}
	}
	if _, ok := signalByName("NOTASIGNAL"); ok {
		t.Error("unknown signal was resolved")
	}
}
