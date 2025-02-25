package qtime

import (
	"fmt"
	"testing"
	"time"
)

func TestName(t *testing.T) {
	start := NewDate(time.Now())
	end := NewDate(time.Now().AddDate(0, 1, 12))

	start.ForTo(end, 1, func(curr Date, percent int) {
		fmt.Println(curr, percent)
	})

	start1 := NewDateTime(time.Now())
	end1 := NewDateTime(time.Now().AddDate(0, 1, 12))

	start1.ForTo(end1, 1, func(curr DateTime, percent int) {
		fmt.Println(curr, percent)
	})
}
