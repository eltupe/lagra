package notify

import (
	"context"
	"testing"
	"time"
)

func TestNotifyPassesTitleAndBodyAsArgs(t *testing.T) {
	got := make(chan []string, 1)
	o := OSA{Run: func(ctx context.Context, name string, args ...string) error {
		got <- append([]string{name}, args...)
		return nil
	}}
	o.Notify(`lagra: "x"`, "it's broken")
	select {
	case a := <-got:
		if a[0] != "/usr/bin/osascript" || a[len(a)-2] != `lagra: "x"` || a[len(a)-1] != "it's broken" {
			t.Errorf("args = %v", a)
		}
	case <-time.After(time.Second):
		t.Fatal("not called")
	}
}
