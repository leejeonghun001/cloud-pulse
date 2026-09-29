package notify

import "testing"

func TestMessage_HasImage(t *testing.T) {
	t.Parallel()

	if (Message{}).HasImage() {
		t.Errorf("HasImage() on zero-value Message = true, want false")
	}
	if !(Message{Image: []byte{1}}).HasImage() {
		t.Errorf("HasImage() with non-empty Image = false, want true")
	}
	if (Message{Image: []byte{}}).HasImage() {
		t.Errorf("HasImage() with empty-but-non-nil Image = true, want false")
	}
}
