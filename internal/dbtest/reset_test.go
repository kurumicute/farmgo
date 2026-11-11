package dbtest

import "testing"

func TestReset(t *testing.T) {
	d := Open(t)
	Reset(t, d)
	Reset(t, d)
}
