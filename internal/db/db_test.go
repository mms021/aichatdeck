package db

import "testing"

func TestRebind(t *testing.T) {
	for in, want := range map[string]string{
		`SELECT 1`:                               `SELECT 1`,
		`a=? AND b IN (?,?)`:                     `a=$1 AND b IN ($2,$3)`,
		`x='what?' AND y=?`:                      `x='what?' AND y=$1`,
		`"odd?col"=? AND z=?::jsonb`:             `"odd?col"=$1 AND z=$2::jsonb`,
		`jsonb_set(c,'{k}',?::jsonb) WHERE id=?`: `jsonb_set(c,'{k}',$1::jsonb) WHERE id=$2`,
	} {
		if got := Rebind(in); got != want {
			t.Errorf("Rebind(%q) = %q, want %q", in, got, want)
		}
	}
}
