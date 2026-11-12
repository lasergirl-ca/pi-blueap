package event

import "testing"

func TestSeqAndRecent(t *testing.T) {
	b := New()
	ch, unsub := b.Subscribe()
	defer unsub()
	for i := 0; i < historySize+5; i++ {
		b.Emit("tick", "", "")
	}
	if e := <-ch; e.Seq != 1 {
		t.Errorf("first seq %d", e.Seq)
	}
	all := b.Recent(0, 0)
	if len(all) != historySize || all[0].Seq != 6 || all[len(all)-1].Seq != historySize+5 {
		t.Errorf("history: %d events, %d..%d", len(all), all[0].Seq, all[len(all)-1].Seq)
	}
	if got := b.Recent(0, 3); len(got) != 3 || got[2].Seq != historySize+5 {
		t.Errorf("last 3: %v", got)
	}
	if got := b.Recent(historySize+3, 0); len(got) != 2 || got[0].Seq != historySize+4 {
		t.Errorf("since: %v", got)
	}
	if got := b.Recent(historySize+5, 0); len(got) != 0 {
		t.Errorf("nothing new: %v", got)
	}
}
