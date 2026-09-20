package handler

import "testing"

func TestCountLogLines(t *testing.T) {
	cases := []struct {
		name string
		text string
		want int
	}{
		{"空", "", 0},
		{"一行不带换行", "only", 1},
		{"一行带换行", "only\n", 1},
		{"三行", "a\nb\nc\n", 3},
		{"末尾无换行", "a\nb\nc", 3},
		{"中间空行也算", "a\n\nc\n", 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := countLogLines(tc.text); got != tc.want {
				t.Fatalf("应为 %d 行，实际 %d", tc.want, got)
			}
		})
	}
}
