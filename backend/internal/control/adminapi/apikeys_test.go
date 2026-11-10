package adminapi

import (
	"slices"
	"testing"
)

func TestUniqueScopes(t *testing.T) {
	for _, tc := range []struct{ in, want []string }{
		{[]string{"invoke", "dry_run:int_evt_brandA", "invoke"}, []string{"invoke", "dry_run:int_evt_brandA"}},
		{[]string{"b", "a", "b", "a"}, []string{"b", "a"}},
		{[]string{"invoke"}, []string{"invoke"}},
		{nil, []string{}},
	} {
		if got := uniqueScopes(tc.in); !slices.Equal(got, tc.want) {
			t.Errorf("uniqueScopes(%v) = %v, want %v", tc.in, got, tc.want)
		}
	}
}
