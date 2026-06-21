package main

import "testing"

func TestSelfCommentedOnItem(t *testing.T) {
	const itemID = "WI-471"
	tests := []struct {
		name    string
		command string
		itemID  string
		want    bool
	}{
		{
			name:    "documented form with env var",
			command: `ws comment add $WINDSHIFT_ITEM_ID -m "Found the cause and left a note"`,
			itemID:  itemID,
			want:    true,
		},
		{
			name:    "literal item id",
			command: `ws comment add WI-471 -m "done"`,
			itemID:  itemID,
			want:    true,
		},
		{
			name:    "compound command, comment second",
			command: `cd /workspace && ws comment add $WINDSHIFT_ITEM_ID -m "progress"`,
			itemID:  itemID,
			want:    true,
		},
		{
			name:    "quoted env var",
			command: `ws comment add "${WINDSHIFT_ITEM_ID}" -m "hi"`,
			itemID:  itemID,
			want:    true,
		},
		{
			name:    "comment list is not a post",
			command: `ws comment list $WINDSHIFT_ITEM_ID`,
			itemID:  itemID,
			want:    false,
		},
		{
			name:    "unrelated ws command",
			command: `ws task get $WINDSHIFT_ITEM_ID`,
			itemID:  itemID,
			want:    false,
		},
		{
			name:    "add verb without item reference",
			command: `ws comment add WI-999 -m "other item"`,
			itemID:  itemID,
			want:    false,
		},
		{
			name:    "empty item id never matches",
			command: `ws comment add $WINDSHIFT_ITEM_ID -m "hi"`,
			itemID:  "",
			want:    false,
		},
		{
			name:    "git commit mentioning the item key",
			command: `git commit -m "fix(x): handle edge case (WI-471)"`,
			itemID:  itemID,
			want:    false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := selfCommentedOnItem(tt.command, tt.itemID); got != tt.want {
				t.Fatalf("selfCommentedOnItem(%q, %q) = %v, want %v", tt.command, tt.itemID, got, tt.want)
			}
		})
	}
}
