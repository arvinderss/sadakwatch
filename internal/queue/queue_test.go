package queue

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestFIFOAndAck(t *testing.T) {
	q, err := Open(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if _, err := q.Enqueue(map[string]int{"n": i}); err != nil {
			t.Fatal(err)
		}
	}
	items, err := q.Peek(3)
	if err != nil || len(items) != 3 {
		t.Fatalf("peek: %d %v", len(items), err)
	}
	for i, it := range items {
		var v map[string]int
		json.Unmarshal(it.Payload, &v)
		if v["n"] != i {
			t.Fatalf("order broken: got %d at %d", v["n"], i)
		}
	}
	q.Ack(items[0].Key)
	if q.Len() != 4 {
		t.Fatalf("len %d", q.Len())
	}
}

func TestSurvivesReopenAndQuarantinesCorruption(t *testing.T) {
	dir := t.TempDir()
	q, _ := Open(dir, 0)
	q.Enqueue("a")
	q.Enqueue("b")
	os.WriteFile(filepath.Join(dir, "00000000000000000001-000001.json"), []byte("{torn"), 0o644)
	os.WriteFile(filepath.Join(dir, ".tmp-123"), []byte("partial"), 0o644)

	q2, _ := Open(dir, 0)
	items, err := q2.Peek(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 good items after reopen, got %d", len(items))
	}
	if _, err := os.Stat(filepath.Join(dir, ".tmp-123")); !os.IsNotExist(err) {
		t.Fatal("temp file should be cleaned on open")
	}
	if _, err := os.Stat(filepath.Join(dir, "00000000000000000001-000001.json.bad")); err != nil {
		t.Fatal("corrupt item should be quarantined")
	}
}

func TestEvictionAndAttachments(t *testing.T) {
	dir := t.TempDir()
	q, _ := Open(dir, 2)
	att := filepath.Join(dir, "frame.jpg")
	os.WriteFile(att, []byte("jpg"), 0o644)
	q.Enqueue("oldest", att)
	q.Enqueue("b")
	q.Enqueue("c")
	if q.Len() != 2 {
		t.Fatalf("expected eviction to 2, got %d", q.Len())
	}
	if _, err := os.Stat(att); !os.IsNotExist(err) {
		t.Fatal("evicted item's attachment should be deleted")
	}
}

func TestUpdate(t *testing.T) {
	q, _ := Open(t.TempDir(), 0)
	q.Enqueue("x")
	items, _ := q.Peek(1)
	items[0].Attempts = 3
	if err := q.Update(items[0]); err != nil {
		t.Fatal(err)
	}
	again, _ := q.Peek(1)
	if again[0].Attempts != 3 {
		t.Fatal("update not persisted")
	}
}
